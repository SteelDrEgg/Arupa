package service

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
	"sync"
	"sync/atomic"
	"time"

	"arupa/internal/auth"
	"arupa/internal/conf"
	grpcpb "arupa/servicesdk/grpc/proto"
	wasmpb "arupa/servicesdk/wasm/proto"

	goservice "github.com/SteelDrEgg/go-plugin"
	"google.golang.org/grpc"
)

// handshake is shared with gRPC services. Services must use the same values.
var handshake = goservice.HandshakeConfig{
	ProtocolVersion:  ContractVersion,
	MagicCookieKey:   "ARUPA_SERVICE",
	MagicCookieValue: "arupa-service-v2",
}

// defaultRegisterTimeout bounds the host control-plane wait for service
// registration.
const defaultRegisterTimeout = 15 * time.Second

type serviceLoaderOptions struct {
	TempDir   string
	API       *HostAPI
	Resources *transportRegistry
	// RegisterTimeout bounds Register calls while loading services. A zero value
	// uses defaultRegisterTimeout; a negative value disables the timeout.
	RegisterTimeout time.Duration
}

type serviceLoader struct {
	tempDir         string
	api             *HostAPI
	resources       *transportRegistry
	registerTimeout time.Duration
}

type serviceLoadResult struct {
	loaded    *loadedService
	rootPath  string
	runAsUser string
}

type loadedService struct {
	loader         *goservice.Manager
	handle         *goservice.Handle
	conn           serviceConn
	record         *ServiceRecord
	recordMu       sync.Mutex
	publishMu      sync.Mutex
	hostBrokerStop func()
	accessMu       sync.RWMutex
	access         auth.AccessPolicy
	// lifecycle is canceled when the host stops or replaces this loaded service.
	lifecycle  context.Context
	cancel     context.CancelFunc
	runtimeDir string
	cleanupDir string
	degraded   atomic.Bool
}

func newServiceLoader(opts serviceLoaderOptions) (*serviceLoader, error) {
	registerTimeout := opts.RegisterTimeout
	if registerTimeout == 0 {
		registerTimeout = defaultRegisterTimeout
	}
	l := &serviceLoader{
		tempDir:         opts.TempDir,
		api:             opts.API,
		resources:       opts.Resources,
		registerTimeout: registerTimeout,
	}
	if l.resources == nil {
		return nil, fmt.Errorf("resource registry is required")
	}
	if l.api == nil {
		return nil, fmt.Errorf("host API is required")
	}
	return l, nil
}

func (l *serviceLoader) load(scanned DiscoveredService, cfg conf.Service) (*serviceLoadResult, error) {
	if scanned.Type == "static" {
		return l.loadStatic(scanned, cfg)
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
	loader, err := l.newInner(runAsUser, func(client *goservice.ClientConfig) {
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

	record := &ServiceRecord{
		InstanceID: instanceID, Name: info.Name, Version: info.Version,
		Type: info.Type, Path: scanned.PackagePath,
	}
	lifecycle, cancelLifecycle := context.WithCancel(context.Background())
	lp := &loadedService{
		loader: loader, handle: handle, conn: conn, record: record,
		access:    auth.AccessPolicy{Groups: append([]string(nil), cfg.Allow...)},
		lifecycle: lifecycle, cancel: cancelLifecycle, runtimeDir: inherited.runtimeDir,
	}
	inheritedPaths := map[string]string{}
	req := RegisterRequest{InstanceID: instanceID, Params: cfg.Params}
	if inherited.path != "" {
		inheritedPaths["proxy"] = inherited.path
		req.Listeners = []InheritedListener{{
			ID: "proxy", FD: inherited.fd, Network: "unix", Address: inherited.path,
		}}
	}
	l.resources.attach(instanceID, lp, handle.RootPath(), inheritedPaths)
	if info.Type == "grpc" {
		grpcConnection, ok := conn.(grpcConn)
		if !ok {
			l.resources.detach(instanceID)
			cancelLifecycle()
			_ = loader.Unload(handle)
			inherited.cleanup()
			return nil, fmt.Errorf("gRPC service connection has no broker")
		}
		brokerID, stopBroker, err := startGRPCHostBroker(grpcConnection.broker, l.api, instanceID)
		if err != nil {
			cancelLifecycle()
			_ = loader.Unload(handle)
			l.resources.detach(instanceID)
			inherited.cleanup()
			return nil, fmt.Errorf("start host broker: %w", err)
		}
		lp.hostBrokerStop = stopBroker
		req.HostBrokerID = brokerID
	}

	registerCtx, cancelRegister := l.registerContext()
	reg, err := conn.Register(registerCtx, req)
	cancelRegister()
	if err != nil {
		if lp.hostBrokerStop != nil {
			lp.hostBrokerStop()
		}
		l.resources.detach(instanceID)
		cancelLifecycle()
		_ = loader.Unload(handle)
		inherited.cleanup()
		return nil, fmt.Errorf("register service %q: %w", instanceID, err)
	}
	if err := validateRegisterResultIdentity(info, reg); err != nil {
		if lp.hostBrokerStop != nil {
			lp.hostBrokerStop()
		}
		l.resources.detach(instanceID)
		cancelLifecycle()
		_ = loader.Unload(handle)
		inherited.cleanup()
		return nil, err
	}
	lp.recordMu.Lock()
	lp.record.Name = reg.Name
	lp.record.Version = reg.Version
	lp.recordMu.Unlock()
	return &serviceLoadResult{
		loaded: lp, rootPath: handle.RootPath(), runAsUser: runAsUser,
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

func (lp *loadedService) accessPolicy() auth.AccessPolicy {
	lp.accessMu.RLock()
	defer lp.accessMu.RUnlock()
	return auth.AccessPolicy{
		Groups: append([]string(nil), lp.access.Groups...),
	}
}

func (lp *loadedService) updateAccessGroups(groups []string) {
	lp.accessMu.Lock()
	lp.access.Groups = append([]string(nil), groups...)
	lp.accessMu.Unlock()
}

type unfaithfulServiceError struct {
	reason string
}

func (e *unfaithfulServiceError) Error() string {
	return e.reason
}

func validateRegisterResultIdentity(info goservice.Info, reg *RegisterResult) error {
	if reg == nil {
		return &unfaithfulServiceError{reason: "RegisterReply is nil"}
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
	return &unfaithfulServiceError{
		reason: "info.yaml and RegisterReply mismatch: " + strings.Join(mismatches, ", "),
	}
}

// registerContext returns the host control-plane context used for Register.
func (l *serviceLoader) registerContext() (context.Context, context.CancelFunc) {
	if l.registerTimeout <= 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), l.registerTimeout)
}

func (l *serviceLoader) newInner(runAsUser string, override func(*goservice.ClientConfig)) (*goservice.Manager, error) {
	return goservice.NewManager(goservice.Config{
		TempDir: l.tempDir,
		GRPC: &goservice.GRPCConfig{
			HandshakeConfig:      handshake,
			RunAsUser:            strings.TrimSpace(runAsUser),
			SkipHostEnv:          true,
			AllowedProtocols:     []goservice.Protocol{goservice.ProtocolGRPC},
			SyncStderr:           os.Stderr,
			ClientConfigOverride: override,
			LoaderWithBroker: func(_ context.Context, broker *goservice.GRPCBroker, c *grpc.ClientConn) (any, error) {
				return grpcConn{client: grpcpb.NewServiceClient(c), broker: broker}, nil
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

func (l *serviceLoader) wasmLoader(ctx context.Context, modulePath string, info goservice.Info, clientConfig *goservice.WASMClientConfig) (any, func(context.Context) error, error) {
	loader, err := wasmpb.NewServicePlugin(
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

func (l *serviceLoader) connFor(serviceType string, client any) (serviceConn, error) {
	switch serviceType {
	case "wasm":
		pc, ok := client.(wasmpb.Service)
		if !ok {
			return nil, fmt.Errorf("unexpected wasm service client type %T", client)
		}
		return newWASMConn(pc), nil
	case "grpc":
		pc, ok := client.(grpcConn)
		if !ok {
			return nil, fmt.Errorf("unexpected grpc service client type %T", client)
		}
		return pc, nil
	default:
		return nil, fmt.Errorf("unsupported service type %q", serviceType)
	}
}

func (l *serviceLoader) revoke(lp *loadedService) {
	if lp != nil && lp.hostBrokerStop != nil {
		lp.hostBrokerStop()
	}
}

func (l *serviceLoader) unload(lp *loadedService) error {
	if lp == nil {
		return nil
	}
	var err error
	if lp.loader != nil && lp.handle != nil {
		err = lp.loader.Unload(lp.handle)
	}
	if lp.runtimeDir != "" {
		if cleanupErr := os.RemoveAll(lp.runtimeDir); err == nil {
			err = cleanupErr
		}
	}
	if lp.cleanupDir != "" {
		if cleanupErr := os.RemoveAll(lp.cleanupDir); err == nil {
			err = cleanupErr
		}
	}
	return err
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
		return net.ListenUnix("unix", address)
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

// callContext returns a call context tied to this loaded service's lifetime.
func (lp *loadedService) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if lp == nil {
		return mergeServiceContext(ctx, nil)
	}
	return mergeServiceContext(ctx, lp.lifecycle)
}

// eventContext returns the host-side context for service events that do not have a
// natural parent request context.
func (lp *loadedService) eventContext() (context.Context, context.CancelFunc) {
	return lp.callContext(context.Background())
}

// cancelLifecycle cancels in-flight and future host calls associated with this
// loaded service.
func (lp *loadedService) cancelLifecycle() {
	if lp != nil && lp.cancel != nil {
		lp.cancel()
	}
}

func (lp *loadedService) addTransport(transport Transport) {
	if lp == nil || lp.record == nil {
		return
	}
	lp.recordMu.Lock()
	lp.record.Transports = append(lp.record.Transports, transport)
	lp.recordMu.Unlock()
}

func (lp *loadedService) removeTransport(id string) {
	if lp == nil || lp.record == nil {
		return
	}
	lp.recordMu.Lock()
	defer lp.recordMu.Unlock()
	for index, transport := range lp.record.Transports {
		if transport.ID == id {
			lp.record.Transports = append(lp.record.Transports[:index], lp.record.Transports[index+1:]...)
			return
		}
	}
}

func (lp *loadedService) addRoute(route Route) {
	if lp == nil || lp.record == nil {
		return
	}
	lp.recordMu.Lock()
	lp.record.Routes = append(lp.record.Routes, route)
	lp.recordMu.Unlock()
}

func (lp *loadedService) removeRoute(id string) {
	if lp == nil || lp.record == nil {
		return
	}
	lp.recordMu.Lock()
	defer lp.recordMu.Unlock()
	for index, route := range lp.record.Routes {
		if route.ID == id {
			lp.record.Routes = append(lp.record.Routes[:index], lp.record.Routes[index+1:]...)
			return
		}
	}
}

func (lp *loadedService) snapshotRecord() *ServiceRecord {
	if lp == nil || lp.record == nil {
		return nil
	}
	lp.recordMu.Lock()
	defer lp.recordMu.Unlock()
	return cloneServiceRecord(lp.record)
}
