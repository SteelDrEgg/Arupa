package plugin

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"arupa/internal/auth"
	"arupa/internal/conf"
	grpcpb "arupa/pluginsdk/grpc/proto"
	wasmpb "arupa/pluginsdk/wasm/proto"

	goplugin "github.com/SteelDrEgg/go-plugin"
	"google.golang.org/grpc"
)

// handshake is shared with gRPC plugins. Plugins must use the same values.
var handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "ARUPA_PLUGIN",
	MagicCookieValue: "arupa",
}

// defaultRegisterTimeout bounds the host control-plane wait for plugin
// registration.
const defaultRegisterTimeout = 15 * time.Second

type pluginLoaderOptions struct {
	TempDir      string
	API          *HostAPI
	HostGRPC     *grpcHostServer
	HostGRPCAddr string
	// RegisterTimeout bounds Register calls while loading plugins. A zero value
	// uses defaultRegisterTimeout; a negative value disables the timeout.
	RegisterTimeout time.Duration
}

type pluginLoader struct {
	inner           *goplugin.Manager
	tempDir         string
	api             *HostAPI
	hostGRPC        *grpcHostServer
	hostGRPCAddr    string
	registerTimeout time.Duration
}

type pluginLoadResult struct {
	loaded       *loadedPlugin
	registration *RegisterResult
	rootPath     string
	runAsUser    string
}

type loadedPlugin struct {
	loader    *goplugin.Manager
	handle    *goplugin.Handle
	conn      pluginConn
	record    *PluginRecord
	grpcToken string
	accessMu  sync.RWMutex
	access    auth.AccessPolicy
	// lifecycle is canceled when the host stops or replaces this loaded plugin.
	lifecycle context.Context
	cancel    context.CancelFunc
}

func newPluginLoader(opts pluginLoaderOptions) (*pluginLoader, error) {
	registerTimeout := opts.RegisterTimeout
	if registerTimeout == 0 {
		registerTimeout = defaultRegisterTimeout
	}
	l := &pluginLoader{
		tempDir:         opts.TempDir,
		api:             opts.API,
		hostGRPC:        opts.HostGRPC,
		hostGRPCAddr:    opts.HostGRPCAddr,
		registerTimeout: registerTimeout,
	}
	inner, err := l.newInner("")
	if err != nil {
		return nil, err
	}
	l.inner = inner
	return l, nil
}

func (l *pluginLoader) load(scanned DiscoveredPlugin, cfg conf.Plugin) (*pluginLoadResult, error) {
	params, err := cfg.ResolveParams(os.LookupEnv)
	if err != nil {
		return nil, fmt.Errorf("resolve params for plugin %q: %w", scanned.Name, err)
	}
	cfg = cfg.Clone()
	cfg.Params = params

	loader := l.inner
	runAsUser := ""
	if scanned.Type == "grpc" {
		runAsUser = strings.TrimSpace(cfg.RunAsUser)
	}
	if runAsUser != "" {
		var err error
		loader, err = l.newInner(runAsUser)
		if err != nil {
			return nil, err
		}
	}

	handle, err := loader.Load(scanned.PackagePath)
	if err != nil {
		return nil, err
	}

	info := handle.Info()
	conn, err := l.connFor(info.Type, handle.Client())
	if err != nil {
		_ = loader.Unload(handle)
		return nil, err
	}

	instanceID := info.Name
	if instanceID != scanned.Name {
		_ = loader.Unload(handle)
		return nil, fmt.Errorf("plugin package name changed from %q to %q while loading", scanned.Name, instanceID)
	}

	req := RegisterRequest{InstanceID: instanceID, Params: cfg.Params}
	var grpcToken string
	if info.Type == "grpc" {
		if l.hostGRPC == nil {
			_ = loader.Unload(handle)
			return nil, fmt.Errorf("host callback server is not configured")
		}
		token, err := l.hostGRPC.issueToken(instanceID)
		if err != nil {
			_ = loader.Unload(handle)
			return nil, fmt.Errorf("issue host callback token: %w", err)
		}
		grpcToken = token
		req.HostCallbackAddr = l.hostGRPCAddr
		req.HostCallbackToken = token
	}

	registerCtx, cancelRegister := l.registerContext()
	reg, err := conn.Register(registerCtx, req)
	cancelRegister()
	if err != nil {
		if grpcToken != "" {
			l.hostGRPC.revokeToken(grpcToken)
		}
		_ = loader.Unload(handle)
		return nil, fmt.Errorf("register plugin %q: %w", instanceID, err)
	}
	if err := validateRegisterResultIdentity(info, reg); err != nil {
		if grpcToken != "" {
			l.hostGRPC.revokeToken(grpcToken)
		}
		_ = loader.Unload(handle)
		return nil, err
	}

	record := &PluginRecord{
		InstanceID: instanceID,
		Name:       reg.Name,
		Version:    reg.Version,
		Type:       info.Type,
		Path:       scanned.PackagePath,
		Routes:     reg.Routes,
		Static:     reg.Static,
		Namespaces: reg.Namespaces,
	}

	lifecycle, cancelLifecycle := context.WithCancel(context.Background())
	lp := &loadedPlugin{
		loader:    loader,
		handle:    handle,
		conn:      conn,
		record:    record,
		grpcToken: grpcToken,
		access:    auth.AccessPolicy{Groups: append([]string(nil), cfg.Allow...)},
		lifecycle: lifecycle,
		cancel:    cancelLifecycle,
	}
	return &pluginLoadResult{
		loaded:       lp,
		registration: reg,
		rootPath:     handle.RootPath(),
		runAsUser:    runAsUser,
	}, nil
}

func (lp *loadedPlugin) accessPolicy() auth.AccessPolicy {
	lp.accessMu.RLock()
	defer lp.accessMu.RUnlock()
	return auth.AccessPolicy{
		Groups: append([]string(nil), lp.access.Groups...),
	}
}

func (lp *loadedPlugin) updateAccessGroups(groups []string) {
	lp.accessMu.Lock()
	lp.access.Groups = append([]string(nil), groups...)
	lp.accessMu.Unlock()
}

type unfaithfulPluginError struct {
	reason string
}

func (e *unfaithfulPluginError) Error() string {
	return e.reason
}

func validateRegisterResultIdentity(info goplugin.Info, reg *RegisterResult) error {
	if reg == nil {
		return &unfaithfulPluginError{reason: "RegisterReply is nil"}
	}

	var mismatches []string
	if reg.Name != info.Name {
		mismatches = append(mismatches, fmt.Sprintf("Name info.yaml=%q RegisterReply=%q", info.Name, reg.Name))
	}
	if reg.Version != info.Version {
		mismatches = append(mismatches, fmt.Sprintf("Version info.yaml=%q RegisterReply=%q", info.Version, reg.Version))
	}
	if len(mismatches) == 0 {
		return nil
	}
	return &unfaithfulPluginError{
		reason: "info.yaml and RegisterReply mismatch: " + strings.Join(mismatches, ", "),
	}
}

// registerContext returns the host control-plane context used for Register.
func (l *pluginLoader) registerContext() (context.Context, context.CancelFunc) {
	if l.registerTimeout <= 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), l.registerTimeout)
}

func (l *pluginLoader) newInner(runAsUser string) (*goplugin.Manager, error) {
	return goplugin.NewManager(goplugin.Config{
		TempDir: l.tempDir,
		GRPC: &goplugin.GRPCConfig{
			HandshakeConfig:  handshake,
			RunAsUser:        strings.TrimSpace(runAsUser),
			SkipHostEnv:      true,
			AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
			SyncStderr:       os.Stderr,
			LoaderWithBroker: func(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
				return grpcpb.NewPluginClient(c), nil
			},
		},
		WASM: &goplugin.WASMConfig{
			Loader: l.wasmLoader,
			ClientConfigOverride: func(cfg *goplugin.WASMClientConfig) {
				cfg.ModuleConfig = cfg.ModuleConfig.WithSysWalltime()
			},
		},
	})
}

func (l *pluginLoader) wasmLoader(ctx context.Context, modulePath string, info goplugin.Info, clientConfig *goplugin.WASMClientConfig) (any, func(context.Context) error, error) {
	loader, err := wasmpb.NewPluginPlugin(
		ctx,
		wasmpb.WazeroRuntime(clientConfig.NewRuntime),
		wasmpb.WazeroModuleConfig(clientConfig.ModuleConfig),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("new wasm loader: %w", err)
	}
	client, err := loader.Load(ctx, modulePath, wasmHostFns{api: l.api, source: info.Name})
	if err != nil {
		return nil, nil, fmt.Errorf("load wasm module: %w", err)
	}
	return client, func(ctx context.Context) error { return client.Close(ctx) }, nil
}

func (l *pluginLoader) connFor(pluginType string, client any) (pluginConn, error) {
	switch pluginType {
	case "wasm":
		pc, ok := client.(wasmpb.Plugin)
		if !ok {
			return nil, fmt.Errorf("unexpected wasm plugin client type %T", client)
		}
		return newWASMConn(pc), nil
	case "grpc":
		pc, ok := client.(grpcpb.PluginClient)
		if !ok {
			return nil, fmt.Errorf("unexpected grpc plugin client type %T", client)
		}
		return grpcConn{client: pc}, nil
	default:
		return nil, fmt.Errorf("unsupported plugin type %q", pluginType)
	}
}

func (l *pluginLoader) revoke(lp *loadedPlugin) {
	if lp != nil && lp.grpcToken != "" && l.hostGRPC != nil {
		l.hostGRPC.revokeToken(lp.grpcToken)
	}
}

func (l *pluginLoader) unload(lp *loadedPlugin) error {
	if lp == nil || lp.loader == nil || lp.handle == nil {
		return nil
	}
	return lp.loader.Unload(lp.handle)
}

// callContext returns a call context tied to this loaded plugin's lifetime.
func (lp *loadedPlugin) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if lp == nil {
		return mergePluginContext(ctx, nil)
	}
	return mergePluginContext(ctx, lp.lifecycle)
}

// eventContext returns the host-side context for plugin events that do not have a
// natural parent request context.
func (lp *loadedPlugin) eventContext() (context.Context, context.CancelFunc) {
	return lp.callContext(context.Background())
}

// cancelLifecycle cancels in-flight and future host calls associated with this
// loaded plugin.
func (lp *loadedPlugin) cancelLifecycle() {
	if lp != nil && lp.cancel != nil {
		lp.cancel()
	}
}
