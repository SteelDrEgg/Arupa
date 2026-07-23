package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"arupa/internal/conf"
	"arupa/internal/netx"
)

// Options configures a Manager.
type Options struct {
	// Config contains service directories and per-service runtime config.
	Config conf.ServiceSystem
	// Mux receives the service HTTP dispatcher fallback. Host routes registered
	// on more specific patterns keep precedence over service routes.
	Mux *http.ServeMux
	// Socket is the global Socket.IO server services attach namespaces to. Required.
	Socket *netx.Socket
	// Logger is used for host and service logs. Optional.
	Logger *slog.Logger
	// ReservedHTTP lists kernel-owned paths that services may not claim.
	ReservedHTTP []string
}

// Manager is the public facade for the service system.
//
// The heavy pieces live behind narrower collaborators: catalog scanning,
// backend loading, lifecycle state, and HTTP/static/socket registration. Keep
// this type boring; it is the object other packages depend on.
type Manager struct {
	kv        *KV
	registry  *Registry
	routes    *routeRegistry
	resources *transportRegistry

	runtime *serviceRuntime
}

// NewManager builds a service manager and registers its HTTP dispatcher.
func NewManager(opts Options) (*Manager, error) {
	cfg := opts.Config.Clone()
	if strings.TrimSpace(cfg.ServiceDir) == "" {
		return nil, fmt.Errorf("ServiceDir is required")
	}
	if strings.TrimSpace(cfg.ServiceTempDir) == "" {
		return nil, fmt.Errorf("ServiceTempDir is required")
	}
	if opts.Mux == nil {
		return nil, fmt.Errorf("Mux is required")
	}
	if opts.Socket == nil {
		return nil, fmt.Errorf("Socket is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	managerLog := logger.With("component", "kernel", "from", "service_manager")

	if err := os.MkdirAll(cfg.ServiceTempDir, 0o755); err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	kv := NewKV()
	registry := NewRegistry(kv)
	socketBridge := newSocketBridge(opts.Socket, logger)
	resources := newTransportRegistry(registry)
	routes := newRouteRegistry(resources, socketBridge)
	if err := routes.reserve("kernel", opts.ReservedHTTP...); err != nil {
		return nil, err
	}
	api := NewHostAPI(kv, socketBridge, logger)
	api.SetResourceRegistrar(resources)
	loader, err := newServiceLoader(serviceLoaderOptions{
		TempDir: cfg.ServiceTempDir, API: api, Resources: resources,
	})
	if err != nil {
		return nil, err
	}

	runtime := newServiceRuntime(serviceRuntimeOptions{
		Config: cfg, Catalog: newServiceCatalog(kv, managerLog),
		Loader: loader, Resources: resources, Registry: registry, Logger: managerLog,
	})

	m := &Manager{
		kv:        kv,
		registry:  registry,
		routes:    routes,
		resources: resources,
		runtime:   runtime,
	}
	api.SetMessageDispatcher(m)
	api.SetParamsStore(m)

	if err := netx.HandleSafe(opts.Mux, "/", http.HandlerFunc(m.ServeHTTP)); err != nil {
		_ = m.Close()
		return nil, err
	}
	return m, nil
}

// KV exposes the shared key-value store (e.g. for host-side seeding).
func (m *Manager) KV() *KV { return m.kv }

// Registry exposes the service registry.
func (m *Manager) Registry() *Registry { return m.registry }

// Config returns the service-system configuration currently held by the manager.
func (m *Manager) Config() conf.ServiceSystem {
	return m.runtime.Config()
}

// UpdateConfig replaces the service-system configuration used by future scans
// and starts. The extraction temp dir is fixed when the manager is created.
func (m *Manager) UpdateConfig(cfg conf.ServiceSystem) {
	m.runtime.UpdateConfig(cfg)
}

// DispatchServiceMessage delivers a host-authenticated service message to the
// target service named in msg.Target.
func (m *Manager) DispatchServiceMessage(ctx context.Context, msg ServiceMessage) (string, error) {
	return m.runtime.DispatchServiceMessage(ctx, msg)
}

// PatchServiceParams persists a caller-scoped Params patch and refreshes runtime
// config snapshots used by future starts.
func (m *Manager) PatchServiceParams(name string, patch ParamsPatch) error {
	next, err := conf.PatchServiceParams(name, conf.ServiceParamsPatch{
		Set:    patch.Set,
		Delete: patch.Delete,
	})
	if err != nil {
		return err
	}
	m.UpdateConfig(next.ServiceSystem)
	return nil
}

// GetServiceParams returns the current effective, environment-resolved Params
// for a registered service. The caller identity is established at the protocol
// boundary and is never provided by the service request itself.
func (m *Manager) GetServiceParams(name string) (map[string]string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("service name is required")
	}
	params, err := m.Config().EffectiveService(name).ResolveParams(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	return params, nil
}

// LoadConfigured scans the configured service directory and starts services whose
// effective config enables auto-start.
func (m *Manager) LoadConfigured() error {
	return m.runtime.LoadConfigured()
}

// Scan scans the configured service directory and stores package metadata
// together with effective service config.
func (m *Manager) Scan() error {
	return m.runtime.Scan()
}

// Entries returns a snapshot of discovered services, including effective config
// and runtime status.
func (m *Manager) Entries() []ServiceEntry {
	return m.runtime.Entries()
}

// Discovered returns scanned service metadata snapshot.
func (m *Manager) Discovered() []DiscoveredService {
	return m.runtime.Discovered()
}

// StartByName starts a previously scanned service by name.
func (m *Manager) StartByName(name string) (*loadedService, error) {
	return m.runtime.StartByName(name)
}

// Start starts a previously scanned service by name.
func (m *Manager) Start(name string) error {
	return m.runtime.Start(name)
}

// Stop unloads a running service by instance/name and removes its live host
// bindings.
func (m *Manager) Stop(name string) error {
	return m.runtime.Stop(name)
}

// Restart stops a service when it is running, then starts the latest scanned
// package for the same name.
func (m *Manager) Restart(name string) error {
	return m.runtime.Restart(name)
}

// StartConfigured starts all discovered services whose effective config enables
// auto-start.
func (m *Manager) StartConfigured() error {
	return m.runtime.StartConfigured()
}

// Load extracts, loads, registers and wires a single service package.
func (m *Manager) Load(path string) (*loadedService, error) {
	return m.runtime.Load(path)
}

// ServeHTTP dispatches requests that did not match host routes to the current
// service HTTP/static route table.
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.routes.ServeHTTP(w, r)
}

// Close unloads all services and their broker-hosted callback servers.
func (m *Manager) Close() error {
	if m.runtime != nil {
		return m.runtime.Close()
	}
	return nil
}
