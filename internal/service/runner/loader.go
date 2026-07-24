// Package runner starts service runtimes and owns backend-specific resources.
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"arupa/internal/auth"
	"arupa/internal/conf"
	adaptergrpc "arupa/internal/service/adapter/grpc"
	adapterwasm "arupa/internal/service/adapter/wasm"
	"arupa/internal/service/binding"
	"arupa/internal/service/catalog"
	"arupa/internal/service/host"
	"arupa/internal/service/instance"
	"arupa/internal/service/spec"
	grpcpb "arupa/servicesdk/grpc/proto"
	wasmpb "arupa/servicesdk/wasm/proto"

	goservice "github.com/SteelDrEgg/go-plugin"
	"google.golang.org/grpc"
)

// handshake is shared with gRPC services. Services must use the same values.
var handshake = goservice.HandshakeConfig{
	ProtocolVersion:  spec.ContractVersion,
	MagicCookieKey:   "ARUPA_SERVICE",
	MagicCookieValue: "arupa-service-v2",
}

// defaultRegisterTimeout bounds the host control-plane wait for service
// registration.
const defaultRegisterTimeout = 15 * time.Second

type Options struct {
	API      *host.API
	Bindings *binding.Controller
	// RegisterTimeout bounds Register calls while loading services. A zero value
	// uses defaultRegisterTimeout; a negative value disables the timeout.
	RegisterTimeout time.Duration
}

type Loader struct {
	api             *host.API
	bindings        *binding.Controller
	registerTimeout time.Duration
}

type LoadResult struct {
	Loaded    *instance.Instance
	RootPath  string
	RunAsUser string
}

func New(opts Options) (*Loader, error) {
	registerTimeout := opts.RegisterTimeout
	if registerTimeout == 0 {
		registerTimeout = defaultRegisterTimeout
	}
	l := &Loader{
		api:             opts.API,
		bindings:        opts.Bindings,
		registerTimeout: registerTimeout,
	}
	if l.bindings == nil {
		return nil, fmt.Errorf("binding controller is required")
	}
	if l.api == nil {
		return nil, fmt.Errorf("host API is required")
	}
	return l, nil
}

func (l *Loader) Load(
	scanned catalog.DiscoveredService,
	cfg conf.Service,
	tempDir string,
	access func() auth.AccessPolicy,
) (*LoadResult, error) {
	tempDir = strings.TrimSpace(tempDir)
	if tempDir == "" {
		return nil, fmt.Errorf("service temp directory is required")
	}
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	if scanned.Type == "static" {
		return l.loadStatic(scanned, cfg, tempDir, access)
	}
	if err := verifyPackageChecksum(scanned.PackagePath, cfg); err != nil {
		return nil, fmt.Errorf("verify service %q package checksum: %w", scanned.Name, err)
	}

	params, err := cfg.ResolveParams(os.LookupEnv)
	if err != nil {
		return nil, fmt.Errorf("resolve params for service %q: %w", scanned.Name, err)
	}
	cfg = cfg.Clone()
	cfg.Params = params

	runAsUser := ""
	if scanned.Type == "grpc" {
		runAsUser = strings.TrimSpace(cfg.RunAsUser)
	}
	var inherited inheritedProxy
	var prepareErr error
	loader, err := l.newInner(tempDir, runAsUser, func(client *goservice.ClientConfig) {
		if scanned.Type != "grpc" {
			return
		}
		prepareErr = inherited.prepare(client)
	})
	if err != nil {
		return nil, err
	}

	handle, err := loader.Load(scanned.PackagePath)
	inherited.closeParent()
	if err != nil {
		inherited.cleanup()
		return nil, err
	}
	if prepareErr != nil {
		_ = loader.Unload(handle)
		inherited.cleanup()
		return nil, fmt.Errorf("prepare inherited proxy listener: %w", prepareErr)
	}

	info := handle.Info()
	conn, err := l.connFor(info.Type, handle.Client())
	if err != nil {
		_ = loader.Unload(handle)
		inherited.cleanup()
		return nil, err
	}

	instanceID := info.Name
	if instanceID != scanned.Name {
		_ = loader.Unload(handle)
		inherited.cleanup()
		return nil, fmt.Errorf("service package name changed from %q to %q while loading", scanned.Name, instanceID)
	}

	record := &spec.ServiceRecord{
		InstanceID: instanceID, Name: info.Name, Version: info.Version,
		Type: info.Type, Path: scanned.PackagePath,
	}
	var stopHostBroker func()
	lp := instance.New(instance.Options{
		Connection: conn,
		Record:     record,
		Access:     access,
		CloseBackend: func() error {
			return loader.Unload(handle)
		},
		StopHostBroker: func() {
			if stopHostBroker != nil {
				stopHostBroker()
			}
		},
		CleanupDirs: []string{inherited.runtimeDir},
	})
	inheritedPaths := map[string]string{}
	req := spec.RegisterRequest{InstanceID: instanceID, Params: cfg.Params}
	if inherited.path != "" {
		inheritedPaths["proxy"] = inherited.path
		req.Listeners = []spec.InheritedListener{{
			ID: "proxy", FD: inherited.fd, Network: "unix", Address: inherited.path,
		}}
	}
	if err := l.bindings.Attach(instanceID, lp, handle.RootPath(), inheritedPaths); err != nil {
		lp.Cancel()
		_ = lp.Close()
		inherited.cleanup()
		return nil, err
	}
	if info.Type == "grpc" {
		grpcConnection, ok := conn.(adaptergrpc.Conn)
		if !ok {
			l.bindings.Detach(instanceID)
			lp.Cancel()
			_ = lp.Close()
			inherited.cleanup()
			return nil, fmt.Errorf("gRPC service connection has no broker")
		}
		brokerID, stopBroker, err := adaptergrpc.StartHostBroker(grpcConnection.Broker(), l.api, instanceID)
		if err != nil {
			l.bindings.Detach(instanceID)
			lp.Cancel()
			_ = lp.Close()
			inherited.cleanup()
			return nil, fmt.Errorf("start host broker: %w", err)
		}
		stopHostBroker = stopBroker
		req.HostBrokerID = brokerID
	}

	registerCtx, cancelRegister := l.registerContext()
	reg, err := conn.Register(registerCtx, req)
	cancelRegister()
	if err != nil {
		lp.Revoke()
		l.bindings.Detach(instanceID)
		lp.Cancel()
		_ = lp.Close()
		inherited.cleanup()
		return nil, fmt.Errorf("register service %q: %w", instanceID, err)
	}
	if err := validateRegisterResultIdentity(info, reg); err != nil {
		lp.Revoke()
		l.bindings.Detach(instanceID)
		lp.Cancel()
		_ = lp.Close()
		inherited.cleanup()
		return nil, err
	}
	lp.UpdateIdentity(reg.Name, reg.Version)
	return &LoadResult{
		Loaded: lp, RootPath: handle.RootPath(), RunAsUser: runAsUser,
	}, nil
}

// verifyPackageChecksum verifies the raw .plg archive bytes before the archive
// is extracted or its service code is loaded.
func verifyPackageChecksum(path string, cfg conf.Service) error {
	expected, enabled, err := cfg.SHA256Checksum()
	if err != nil {
		return fmt.Errorf("invalid configured checksum: %w", err)
	}
	if !enabled {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open service package: %w", err)
	}
	defer f.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return fmt.Errorf("hash service package: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return fmt.Errorf("checksum mismatch: expected sha256:%s, got sha256:%s", expected, actual)
	}
	return nil
}

type UnfaithfulServiceError struct {
	reason string
}

func (e *UnfaithfulServiceError) Error() string {
	return e.reason
}

func validateRegisterResultIdentity(info goservice.Info, reg *spec.RegisterResult) error {
	if reg == nil {
		return &UnfaithfulServiceError{reason: "RegisterReply is nil"}
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
	return &UnfaithfulServiceError{
		reason: "info.yaml and RegisterReply mismatch: " + strings.Join(mismatches, ", "),
	}
}

// registerContext returns the host control-plane context used for Register.
func (l *Loader) registerContext() (context.Context, context.CancelFunc) {
	if l.registerTimeout <= 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), l.registerTimeout)
}

func (l *Loader) newInner(tempDir, runAsUser string, override func(*goservice.ClientConfig)) (*goservice.Manager, error) {
	return goservice.NewManager(goservice.Config{
		TempDir: tempDir,
		GRPC: &goservice.GRPCConfig{
			HandshakeConfig:      handshake,
			RunAsUser:            strings.TrimSpace(runAsUser),
			SkipHostEnv:          true,
			AllowedProtocols:     []goservice.Protocol{goservice.ProtocolGRPC},
			SyncStderr:           os.Stderr,
			ClientConfigOverride: override,
			LoaderWithBroker: func(_ context.Context, broker *goservice.GRPCBroker, c *grpc.ClientConn) (any, error) {
				return adaptergrpc.NewConn(grpcpb.NewServiceClient(c), broker), nil
			},
		},
		WASM: &goservice.WASMConfig{
			Loader: l.wasmLoader,
			ClientConfigOverride: func(cfg *goservice.WASMClientConfig) {
				cfg.ModuleConfig = cfg.ModuleConfig.WithSysWalltime()
			},
		},
	})
}

func (l *Loader) wasmLoader(ctx context.Context, modulePath string, info goservice.Info, clientConfig *goservice.WASMClientConfig) (any, func(context.Context) error, error) {
	loader, err := wasmpb.NewServicePlugin(
		ctx,
		wasmpb.WazeroRuntime(clientConfig.NewRuntime),
		wasmpb.WazeroModuleConfig(clientConfig.ModuleConfig),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("new wasm loader: %w", err)
	}
	client, err := loader.Load(ctx, modulePath, adapterwasm.NewHostFunctions(l.api, info.Name))
	if err != nil {
		return nil, nil, fmt.Errorf("load wasm module: %w", err)
	}
	return client, func(ctx context.Context) error { return client.Close(ctx) }, nil
}

func (l *Loader) connFor(serviceType string, client any) (spec.Conn, error) {
	switch serviceType {
	case "wasm":
		pc, ok := client.(wasmpb.Service)
		if !ok {
			return nil, fmt.Errorf("unexpected wasm service client type %T", client)
		}
		return adapterwasm.NewConn(pc), nil
	case "grpc":
		pc, ok := client.(adaptergrpc.Conn)
		if !ok {
			return nil, fmt.Errorf("unexpected grpc service client type %T", client)
		}
		return pc, nil
	default:
		return nil, fmt.Errorf("unsupported service type %q", serviceType)
	}
}

type inheritedProxy struct {
	runtimeDir string
	path       string
	listener   inheritedUnixListener
	file       *os.File
	fd         uint32
}

func (p *inheritedProxy) prepare(config *goservice.ClientConfig) error {
	return p.prepareWith(config, func(address *net.UnixAddr) (inheritedUnixListener, error) {
		listener, err := net.ListenUnix("unix", address)
		if err != nil {
			return nil, err
		}
		// The child process owns a duplicate of this listener after launch.
		// Closing the parent's copy must not unlink the socket path while that
		// inherited listener is still serving.
		listener.SetUnlinkOnClose(false)
		return listener, nil
	})
}

type inheritedUnixListener interface {
	File() (*os.File, error)
	Close() error
}

func (p *inheritedProxy) prepareWith(
	config *goservice.ClientConfig,
	listen func(*net.UnixAddr) (inheritedUnixListener, error),
) error {
	if config == nil || config.Cmd == nil || strings.TrimSpace(config.Cmd.Dir) == "" {
		return fmt.Errorf("go-plugin command directory is unavailable")
	}
	if listen == nil {
		return fmt.Errorf("inherited listener factory is unavailable")
	}
	p.runtimeDir = filepath.Join(filepath.Dir(config.Cmd.Dir), ".runtime")
	if err := os.Mkdir(p.runtimeDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(p.runtimeDir, 0o700); err != nil {
		return err
	}
	p.path = filepath.Join(p.runtimeDir, "proxy.sock")
	address := &net.UnixAddr{Name: p.path, Net: "unix"}
	listener, err := listen(address)
	if err != nil {
		return err
	}
	p.listener = listener
	if err := os.Chmod(p.path, 0o600); err != nil {
		p.closeParent()
		return err
	}
	file, err := listener.File()
	if err != nil {
		p.closeParent()
		return err
	}
	p.file = file
	p.fd = uint32(3 + len(config.Cmd.ExtraFiles))
	config.Cmd.ExtraFiles = append(config.Cmd.ExtraFiles, file)
	config.Cmd.Env = append(config.Cmd.Env,
		fmt.Sprintf("ARUPA_PROXY_FD=%d", p.fd),
		"ARUPA_PROXY_LISTENER=proxy",
		"ARUPA_PROXY_NETWORK=unix",
		"ARUPA_PROXY_ADDRESS="+p.path,
	)
	return nil
}

func (p *inheritedProxy) closeParent() {
	if p.file != nil {
		_ = p.file.Close()
		p.file = nil
	}
	if p.listener != nil {
		_ = p.listener.Close()
		p.listener = nil
	}
}

func (p *inheritedProxy) cleanup() {
	p.closeParent()
	if p.runtimeDir != "" {
		_ = os.RemoveAll(p.runtimeDir)
	}
}
