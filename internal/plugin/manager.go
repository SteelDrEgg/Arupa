package plugin

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
	// Config contains plugin directories and per-plugin runtime config.
	Config conf.PluginSystem
	// Mux receives the plugin HTTP dispatcher fallback. Host routes registered
	// on more specific patterns keep precedence over plugin routes.
	Mux *http.ServeMux
	// Socket is the global Socket.IO server plugins attach namespaces to. Required.
	Socket *netx.Socket
	// Logger is used for host and plugin logs. Optional.
	Logger *slog.Logger
}

// Manager is the public facade for the plugin system.
//
// The heavy pieces live behind narrower collaborators: catalog scanning,
// backend loading, lifecycle state, and HTTP/static/socket registration. Keep
// this type boring; it is the object other packages depend on.
type Manager struct {
	kv       *KV
	registry *Registry
	router   *pluginRouter

	callback *grpcHostServer
	runtime  *pluginRuntime
}

// NewManager builds a plugin manager, registers the plugin HTTP dispatcher on
// the host mux, and starts the gRPC host callback server.
func NewManager(opts Options) (*Manager, error) {
	cfg := opts.Config.Clone()
	if strings.TrimSpace(cfg.PluginDir) == "" {
		return nil, fmt.Errorf("PluginDir is required")
	}
	if strings.TrimSpace(cfg.PluginTempDir) == "" {
		return nil, fmt.Errorf("PluginTempDir is required")
	}
	if opts.Mux == nil {
		return nil, fmt.Errorf("Mux is required")
	}
	if opts.Socket == nil {
		return nil, fmt.Errorf("Socket is required")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	if err := os.MkdirAll(cfg.PluginTempDir, 0o755); err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	kv := NewKV()
	registry := NewRegistry(kv)
	socketBridge := newSocketBridge(opts.Socket, log)
	api := NewHostAPI(kv, socketBridge, log)
	callback := newGRPCHostServer(api)
	callbackAddr, err := callback.Start()
	if err != nil {
		return nil, err
	}

	loader, err := newPluginLoader(pluginLoaderOptions{
		TempDir:      cfg.PluginTempDir,
		API:          api,
		HostGRPC:     callback,
		HostGRPCAddr: callbackAddr,
	})
	if err != nil {
		callback.Stop()
		return nil, err
	}

	router := newPluginRouter()
	registrar := newPluginRegistrar(router, socketBridge, log)
	runtime := newPluginRuntime(pluginRuntimeOptions{
		Config:    cfg,
		Catalog:   newPluginCatalog(kv, log),
		Loader:    loader,
		Registrar: registrar,
		Registry:  registry,
		Logger:    log,
	})

	m := &Manager{
		kv:       kv,
		registry: registry,
		router:   router,
		callback: callback,
		runtime:  runtime,
	}
	api.SetMessageDispatcher(m)
	api.SetParamsPatcher(m)

	if err := netx.HandleSafe(opts.Mux, "/", http.HandlerFunc(m.ServeHTTP)); err != nil {
		_ = m.Close()
		return nil, err
	}
	return m, nil
}

// KV exposes the shared key-value store (e.g. for host-side seeding).
func (m *Manager) KV() *KV { return m.kv }

// Registry exposes the plugin registry.
func (m *Manager) Registry() *Registry { return m.registry }

// Config returns the plugin-system configuration currently held by the manager.
func (m *Manager) Config() conf.PluginSystem {
	return m.runtime.Config()
}

// UpdateConfig replaces the plugin-system configuration used by future scans
// and starts. The extraction temp dir is fixed when the manager is created.
func (m *Manager) UpdateConfig(cfg conf.PluginSystem) {
	m.runtime.UpdateConfig(cfg)
}

// DispatchPluginMessage delivers a host-authenticated plugin message to the
// target plugin named in msg.Target.
func (m *Manager) DispatchPluginMessage(ctx context.Context, msg PluginMessage) (string, error) {
	return m.runtime.DispatchPluginMessage(ctx, msg)
}

// PatchPluginParams persists a caller-scoped Params patch and refreshes runtime
// config snapshots used by future starts.
func (m *Manager) PatchPluginParams(name string, patch ParamsPatch) error {
	next, err := conf.PatchPluginParams(name, conf.PluginParamsPatch{
		Set:    patch.Set,
		Delete: patch.Delete,
	})
	if err != nil {
		return err
	}
	m.UpdateConfig(next.PluginSystem)
	return nil
}

// LoadConfigured scans the configured plugin directory and starts plugins whose
// effective config enables auto-start.
func (m *Manager) LoadConfigured() error {
	return m.runtime.LoadConfigured()
}

// Scan scans the configured plugin directory and stores package metadata
// together with effective plugin config.
func (m *Manager) Scan() error {
	return m.runtime.Scan()
}

// Entries returns a snapshot of discovered plugins, including effective config
// and runtime status.
func (m *Manager) Entries() []PluginEntry {
	return m.runtime.Entries()
}

// Discovered returns scanned plugin metadata snapshot.
func (m *Manager) Discovered() []DiscoveredPlugin {
	return m.runtime.Discovered()
}

// StartByName starts a previously scanned plugin by name.
func (m *Manager) StartByName(name string) (*loadedPlugin, error) {
	return m.runtime.StartByName(name)
}

// Start starts a previously scanned plugin by name.
func (m *Manager) Start(name string) error {
	return m.runtime.Start(name)
}

// Stop unloads a running plugin by instance/name and removes its live host
// bindings.
func (m *Manager) Stop(name string) error {
	return m.runtime.Stop(name)
}

// Restart stops a plugin when it is running, then starts the latest scanned
// package for the same name.
func (m *Manager) Restart(name string) error {
	return m.runtime.Restart(name)
}

// StartConfigured starts all discovered plugins whose effective config enables
// auto-start.
func (m *Manager) StartConfigured() error {
	return m.runtime.StartConfigured()
}

// Load extracts, loads, registers and wires a single plugin package.
func (m *Manager) Load(path string) (*loadedPlugin, error) {
	return m.runtime.Load(path)
}

// ServeHTTP dispatches requests that did not match host routes to the current
// plugin HTTP/static route table.
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.router.ServeHTTP(w, r)
}

// Close unloads all plugins and stops the host callback server.
func (m *Manager) Close() error {
	var err error
	if m.runtime != nil {
		err = m.runtime.Close()
	}
	if m.callback != nil {
		m.callback.Stop()
	}
	return err
}
