package plugin

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"minimalpanel/internal/conf"
	"minimalpanel/internal/netx"
	grpcpb "minimalpanel/pluginsdk/grpc/proto"
	wasmpb "minimalpanel/pluginsdk/wasm/proto"

	goplugin "github.com/SteelDrEgg/go-plugin"
	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"
)

// handshake is shared with gRPC plugins. Plugins must use the same values.
var handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "MINIMALPANEL_PLUGIN",
	MagicCookieValue: "minimalpanel",
}

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

// Manager loads plugins, exposes the shared host API to them, and owns the
// in-memory routing tables used by plugin HTTP/static dispatch.
type Manager struct {
	inner    *goplugin.Manager
	kv       *KV
	api      *HostAPI
	registry *Registry
	socket   *socketBridge
	mux      *http.ServeMux
	log      *slog.Logger
	tempDir  string
	config   conf.PluginSystem

	hostGRPC     *grpcHostServer
	hostGRPCAddr string

	mu      sync.RWMutex
	plugins map[string]*pluginEntry

	routeMu sync.RWMutex
	routes  map[httpRouteKey]*httpRouteBinding

	staticMu sync.RWMutex
	static   map[string]*staticMountBinding
}

type loadedPlugin struct {
	loader    *goplugin.Manager
	handle    *goplugin.Handle
	conn      pluginConn
	record    *PluginRecord
	grpcToken string
}

type pluginEntry struct {
	info       DiscoveredPlugin
	config     conf.Plugin
	discovered bool
	loaded     *loadedPlugin
	status     PluginStatus
}

// httpRouteBinding is a live HTTP route owned by a loaded plugin.
//
// The host mux does not receive one handler per plugin route. Instead, all
// plugin HTTP requests enter Manager.ServeHTTP and are matched against this
// table by pattern and method, which makes stop/restart remove and re-add
// routes without rebuilding the host mux.
type httpRouteBinding struct {
	owner string
	route HTTPRoute
	conn  pluginConn
}

// staticMountBinding is a live static file mount owned by a loaded plugin.
//
// Directory mounts are stored with a trailing-slash pattern and matched by
// prefix; file mounts are stored as exact patterns.
type staticMountBinding struct {
	owner   string
	mount   StaticMount
	handler http.Handler
}

// DiscoveredPlugin is metadata scanned from a .plg package's info.yaml without
// loading the plugin runtime.
type DiscoveredPlugin struct {
	Name            string
	Version         string
	Type            string
	ContractVersion int
	Command         string
	Metadata        map[string]any
	PackagePath     string
}

// PluginEntry is a snapshot of a plugin known to the manager.
type PluginEntry struct {
	DiscoveredPlugin
	Config conf.Plugin
	Status PluginStatus
}

// PluginStatus describes the runtime lifecycle state of a plugin.
type PluginStatus string

const (
	PluginStatusDiscovered PluginStatus = "discovered"
	PluginStatusStarting   PluginStatus = "starting"
	PluginStatusRunning    PluginStatus = "running"
	PluginStatusDegraded   PluginStatus = "degraded"
	PluginStatusStopping   PluginStatus = "stopping"
	PluginStatusFailed     PluginStatus = "failed"
)

func (e *pluginEntry) currentStatus() PluginStatus {
	if e == nil {
		return PluginStatusDiscovered
	}
	if e.status != "" {
		return e.status
	}
	if e.loaded != nil {
		return PluginStatusRunning
	}
	return PluginStatusDiscovered
}

func statusAllowsStart(status PluginStatus) bool {
	return status == PluginStatusDiscovered || status == PluginStatusFailed
}

func statusIsRunning(status PluginStatus) bool {
	return status == PluginStatusRunning || status == PluginStatusDegraded
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

	m := &Manager{
		kv:      NewKV(),
		mux:     opts.Mux,
		log:     log,
		tempDir: cfg.PluginTempDir,
		config:  cfg,
		plugins: make(map[string]*pluginEntry),
		routes:  make(map[httpRouteKey]*httpRouteBinding),
		static:  make(map[string]*staticMountBinding),
	}
	m.registry = NewRegistry(m.kv)
	m.socket = newSocketBridge(opts.Socket, log)
	m.api = NewHostAPI(m.kv, m.socket, log)
	m.api.SetMessageDispatcher(m)

	if err := netx.HandleSafe(m.mux, "/", http.HandlerFunc(m.ServeHTTP)); err != nil {
		return nil, err
	}

	m.hostGRPC = newGRPCHostServer(m.api)
	addr, err := m.hostGRPC.Start()
	if err != nil {
		return nil, err
	}
	m.hostGRPCAddr = addr

	inner, err := m.newInner("")
	if err != nil {
		m.hostGRPC.Stop()
		return nil, err
	}
	m.inner = inner
	return m, nil
}

func (m *Manager) newInner(runAsUser string) (*goplugin.Manager, error) {
	return goplugin.NewManager(goplugin.Config{
		TempDir: m.tempDir,
		GRPC: &goplugin.GRPCConfig{
			HandshakeConfig:  handshake,
			RunAsUser:        strings.TrimSpace(runAsUser),
			AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
			SyncStderr:       os.Stderr,
			LoaderWithBroker: func(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
				return grpcpb.NewPluginClient(c), nil
			},
		},
		WASM: &goplugin.WASMConfig{
			Loader: m.wasmLoader,
		},
	})
}

// KV exposes the shared key-value store (e.g. for host-side seeding).
func (m *Manager) KV() *KV { return m.kv }

// Registry exposes the plugin registry.
func (m *Manager) Registry() *Registry { return m.registry }

func (m *Manager) wasmLoader(ctx context.Context, modulePath string, info goplugin.Info) (any, func(context.Context) error, error) {
	loader, err := wasmpb.NewPluginPlugin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("new wasm loader: %w", err)
	}
	client, err := loader.Load(ctx, modulePath, wasmHostFns{api: m.api, source: info.Name})
	if err != nil {
		return nil, nil, fmt.Errorf("load wasm module: %w", err)
	}
	return client, func(ctx context.Context) error { return client.Close(ctx) }, nil
}

// Config returns the plugin-system configuration currently held by the manager.
func (m *Manager) Config() conf.PluginSystem {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Clone()
}

// UpdateConfig replaces the plugin-system configuration used by future scans
// and starts. The extraction temp dir is fixed when the manager is created.
func (m *Manager) UpdateConfig(cfg conf.PluginSystem) {
	cfg = cfg.Clone()

	m.mu.Lock()
	m.config = cfg
	for name, entry := range m.plugins {
		entry.config = cfg.EffectivePlugin(name)
	}
	m.mu.Unlock()
}

// DispatchPluginMessage delivers a host-authenticated plugin message to the
// target plugin named in msg.Target.
func (m *Manager) DispatchPluginMessage(ctx context.Context, msg PluginMessage) error {
	m.mu.RLock()
	entry, ok := m.plugins[msg.Target]
	var lp *loadedPlugin
	if ok {
		lp = entry.loaded
	}
	m.mu.RUnlock()
	if lp == nil {
		return fmt.Errorf("target plugin %q is not running", msg.Target)
	}
	return lp.conn.HandlePluginMessage(ctx, &msg)
}

// LoadConfigured scans the configured plugin directory and starts plugins whose
// effective config enables auto-start.
func (m *Manager) LoadConfigured() error {
	if err := m.Scan(); err != nil {
		return err
	}
	return m.StartConfigured()
}

// Scan scans the configured plugin directory, reads info.yaml from each .plg
// package, and stores package metadata together with effective plugin config.
func (m *Manager) Scan() error {
	cfg := m.Config()
	return m.scanDir(cfg.PluginDir, cfg)
}

func (m *Manager) scanDir(dir string, cfg conf.PluginSystem) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			m.log.Warn("plugin directory does not exist; skipping", "dir", dir)
			return nil
		}
		return fmt.Errorf("read plugin dir: %w", err)
	}

	var paths []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".plg" {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	sort.Strings(paths)

	next := make(map[string]*pluginEntry, len(paths))
	scanned := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		info, err := readPluginInfo(p)
		if err != nil {
			m.log.Error("failed to scan plugin package", "path", p, "err", err)
			continue
		}
		if _, exists := next[info.Name]; exists {
			m.log.Error("duplicate plugin name found in packages; keeping first", "name", info.Name, "path", p)
			continue
		}
		next[info.Name] = &pluginEntry{
			info:       info,
			config:     cfg.EffectivePlugin(info.Name),
			discovered: true,
			status:     PluginStatusDiscovered,
		}
		scanned[info.Name] = struct{}{}
	}

	for _, name := range cfg.ConfiguredPluginNames() {
		if _, ok := scanned[name]; !ok {
			m.log.Warn("configured plugin was not found in scan results", "name", name, "dir", dir)
		}
	}

	prevDiscovered := make(map[string]struct{})
	m.mu.Lock()
	for name, entry := range m.plugins {
		if entry.discovered {
			prevDiscovered[name] = struct{}{}
		}
		if nextEntry, ok := next[name]; ok {
			nextEntry.loaded = entry.loaded
			nextEntry.status = entry.currentStatus()
		} else if entry.loaded != nil {
			entry.discovered = false
			entry.config = cfg.EffectivePlugin(name)
			entry.status = entry.currentStatus()
			next[name] = entry
		} else if entry.currentStatus() == PluginStatusStarting || entry.currentStatus() == PluginStatusStopping {
			entry.discovered = false
			entry.config = cfg.EffectivePlugin(name)
			next[name] = entry
		}
	}
	m.config = cfg.Clone()
	m.plugins = next
	m.mu.Unlock()

	for name := range prevDiscovered {
		if _, ok := scanned[name]; !ok {
			m.kv.SystemDelete(SysNamespace, registryKVPrefix+"catalog/"+name)
		}
	}
	for _, entry := range next {
		if entry.discovered {
			m.publishDiscovered(entry.info)
		}
	}
	return nil
}

// Entries returns a snapshot of discovered plugins, including effective config
// and runtime status.
func (m *Manager) Entries() []PluginEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]PluginEntry, 0, len(m.plugins))
	for _, entry := range m.plugins {
		if !entry.discovered {
			continue
		}
		out = append(out, PluginEntry{
			DiscoveredPlugin: entry.info,
			Config:           entry.config.Clone(),
			Status:           entry.currentStatus(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Discovered returns scanned plugin metadata snapshot.
func (m *Manager) Discovered() []DiscoveredPlugin {
	entries := m.Entries()
	out := make([]DiscoveredPlugin, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.DiscoveredPlugin)
	}
	return out
}

// StartByName starts a previously scanned plugin by name.
func (m *Manager) StartByName(name string) (*loadedPlugin, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("plugin name is required")
	}

	m.mu.Lock()
	entry, ok := m.plugins[name]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("plugin %q not found in scan results", name)
	}
	if !entry.discovered {
		m.mu.Unlock()
		return nil, fmt.Errorf("plugin %q is not available in scan results", name)
	}
	status := entry.currentStatus()
	if !statusAllowsStart(status) {
		m.mu.Unlock()
		return nil, fmt.Errorf("plugin %q is %s", name, status)
	}
	entry.status = PluginStatusStarting
	info := entry.info
	cfg := entry.config.Clone()
	m.mu.Unlock()

	lp, degraded, err := m.loadScanned(info, cfg)
	if err != nil {
		m.finishStartFailure(name)
		return nil, err
	}
	if err := m.finishStartSuccess(name, info, cfg, lp, degraded); err != nil {
		m.cleanupLoaded(name, lp)
		m.finishStartFailure(name)
		return nil, err
	}
	return lp, nil
}

// Start starts a previously scanned plugin by name.
func (m *Manager) Start(name string) error {
	_, err := m.StartByName(name)
	return err
}

// Stop unloads a running plugin by instance/name and removes its HTTP, static,
// Socket.IO, registry and callback-token bindings.
func (m *Manager) Stop(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("plugin name is required")
	}

	m.mu.Lock()
	entry, ok := m.plugins[name]
	var lp *loadedPlugin
	if ok {
		status := entry.currentStatus()
		if status == PluginStatusStarting || status == PluginStatusStopping {
			m.mu.Unlock()
			return fmt.Errorf("plugin %q is %s", name, status)
		}
		if !statusIsRunning(status) || entry.loaded == nil {
			m.mu.Unlock()
			return fmt.Errorf("plugin %q is not running", name)
		}
		lp = entry.loaded
		entry.loaded = nil
		entry.status = PluginStatusStopping
	}
	m.mu.Unlock()
	if lp == nil {
		return fmt.Errorf("plugin %q is not running", name)
	}

	if err := m.cleanupLoaded(name, lp); err != nil {
		m.finishStop(name, PluginStatusFailed)
		return fmt.Errorf("unload plugin %q: %w", name, err)
	}
	m.finishStop(name, PluginStatusDiscovered)
	m.log.Info("stopped plugin", "name", name)
	return nil
}

// Restart stops a plugin when it is running, then starts the latest scanned
// package for the same name.
func (m *Manager) Restart(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("plugin name is required")
	}

	m.mu.Lock()
	entry, ok := m.plugins[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q not found in scan results", name)
	}
	if !entry.discovered {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q is not available in scan results", name)
	}
	status := entry.currentStatus()
	if status == PluginStatusStarting || status == PluginStatusStopping {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q is %s", name, status)
	}
	info := entry.info
	cfg := entry.config.Clone()
	lp := entry.loaded
	if statusIsRunning(status) && lp != nil {
		entry.loaded = nil
		entry.status = PluginStatusStopping
	} else {
		entry.status = PluginStatusStarting
	}
	m.mu.Unlock()

	if lp != nil {
		if err := m.cleanupLoaded(name, lp); err != nil {
			m.finishStop(name, PluginStatusFailed)
			return err
		}
		m.mu.Lock()
		if entry := m.plugins[name]; entry != nil {
			entry.status = PluginStatusStarting
		}
		m.mu.Unlock()
	}

	next, degraded, err := m.loadScanned(info, cfg)
	if err != nil {
		m.finishStartFailure(name)
		return err
	}
	if err := m.finishStartSuccess(name, info, cfg, next, degraded); err != nil {
		m.cleanupLoaded(name, next)
		m.finishStartFailure(name)
		return err
	}
	return nil
}

// StartConfigured starts all discovered plugins whose effective config enables
// auto-start.
func (m *Manager) StartConfigured() error {
	for _, entry := range m.Entries() {
		if !entry.Config.AutoStart() {
			m.log.Info("plugin auto-start disabled by config", "name", entry.Name)
			continue
		}
		if !statusAllowsStart(entry.Status) {
			continue
		}
		if _, err := m.StartByName(entry.Name); err != nil {
			m.log.Error("failed to start plugin", "name", entry.Name, "path", entry.PackagePath, "err", err)
		}
	}
	return nil
}

// Load extracts, loads, registers and wires a single plugin package.
func (m *Manager) Load(path string) (*loadedPlugin, error) {
	scanned, err := readPluginInfo(path)
	if err != nil {
		return nil, err
	}
	cfg := m.Config().EffectivePlugin(scanned.Name)
	m.mu.Lock()
	entry, ok := m.plugins[scanned.Name]
	if !ok {
		entry = &pluginEntry{
			info:       scanned,
			config:     cfg.Clone(),
			discovered: true,
			status:     PluginStatusDiscovered,
		}
		m.plugins[scanned.Name] = entry
	}
	status := entry.currentStatus()
	if !statusAllowsStart(status) {
		m.mu.Unlock()
		return nil, fmt.Errorf("plugin %q is %s", scanned.Name, status)
	}
	entry.info = scanned
	entry.config = cfg.Clone()
	entry.discovered = true
	entry.status = PluginStatusStarting
	m.mu.Unlock()

	lp, degraded, err := m.loadScanned(scanned, cfg)
	if err != nil {
		m.finishStartFailure(scanned.Name)
		return nil, err
	}
	if err := m.finishStartSuccess(scanned.Name, scanned, cfg, lp, degraded); err != nil {
		m.cleanupLoaded(scanned.Name, lp)
		m.finishStartFailure(scanned.Name)
		return nil, err
	}
	return lp, nil
}

func (m *Manager) loadScanned(scanned DiscoveredPlugin, cfg conf.Plugin) (*loadedPlugin, bool, error) {
	loader := m.inner
	runAsUser := ""
	if scanned.Type == "grpc" {
		runAsUser = strings.TrimSpace(cfg.RunAsUser)
	}
	if runAsUser != "" {
		var err error
		loader, err = m.newInner(runAsUser)
		if err != nil {
			return nil, false, err
		}
	}

	handle, err := loader.Load(scanned.PackagePath)
	if err != nil {
		return nil, false, err
	}

	info := handle.Info()
	conn, err := m.connFor(info.Type, handle.Client())
	if err != nil {
		_ = loader.Unload(handle)
		return nil, false, err
	}

	instanceID := info.Name
	if instanceID != scanned.Name {
		_ = loader.Unload(handle)
		return nil, false, fmt.Errorf("plugin package name changed from %q to %q while loading", scanned.Name, instanceID)
	}

	req := RegisterRequest{InstanceID: instanceID}
	req.Params = cfg.Params
	var grpcToken string
	if info.Type == "grpc" {
		token, err := m.hostGRPC.issueToken(instanceID)
		if err != nil {
			_ = loader.Unload(handle)
			return nil, false, fmt.Errorf("issue host callback token: %w", err)
		}
		grpcToken = token
		req.HostCallbackAddr = m.hostGRPCAddr
		req.HostCallbackToken = token
	}

	reg, err := conn.Register(context.Background(), req)
	if err != nil {
		if grpcToken != "" {
			m.hostGRPC.revokeToken(grpcToken)
		}
		_ = loader.Unload(handle)
		return nil, false, fmt.Errorf("register plugin %q: %w", instanceID, err)
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

	degraded := false
	for _, route := range reg.Routes {
		if err := m.registerRoute(instanceID, route, conn); err != nil {
			degraded = true
			m.log.Error("failed to register plugin route", "plugin", instanceID, "pattern", route.Pattern, "err", err)
		}
	}
	for _, mount := range reg.Static {
		if err := m.registerStatic(instanceID, handle.RootPath(), mount); err != nil {
			degraded = true
			m.log.Error("failed to register plugin static mount", "plugin", instanceID, "prefix", mount.Prefix, "dir", mount.Directory, "err", err)
		}
	}
	for _, ns := range reg.Namespaces {
		if err := m.socket.register(instanceID, ns, conn); err != nil {
			degraded = true
			m.log.Error("failed to register plugin socket namespace", "plugin", instanceID, "namespace", ns.Name, "err", err)
		}
	}

	lp := &loadedPlugin{loader: loader, handle: handle, conn: conn, record: record, grpcToken: grpcToken}

	logArgs := []any{"name", reg.Name, "version", reg.Version, "type", info.Type,
		"routes", len(reg.Routes), "static_mounts", len(reg.Static), "namespaces", len(reg.Namespaces)}
	if info.Type == "grpc" && runAsUser != "" {
		logArgs = append(logArgs, "run_as_user", runAsUser)
	}
	if degraded {
		m.log.Warn("loaded plugin with degraded host bindings", logArgs...)
	} else {
		m.log.Info("loaded plugin", logArgs...)
	}
	return lp, degraded, nil
}

func (m *Manager) finishStartSuccess(name string, scanned DiscoveredPlugin, cfg conf.Plugin, lp *loadedPlugin, degraded bool) error {
	m.mu.Lock()
	entry, ok := m.plugins[name]
	if !ok {
		entry = &pluginEntry{}
		m.plugins[name] = entry
	}
	if entry.currentStatus() != PluginStatusStarting {
		status := entry.currentStatus()
		m.mu.Unlock()
		return fmt.Errorf("plugin %q start completed while status is %s", name, status)
	}
	entry.info = scanned
	entry.config = cfg.Clone()
	entry.discovered = true
	entry.loaded = lp
	if degraded {
		entry.status = PluginStatusDegraded
	} else {
		entry.status = PluginStatusRunning
	}
	m.mu.Unlock()

	m.registry.Add(lp.record)
	return nil
}

func (m *Manager) finishStartFailure(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.plugins[name]; entry != nil && entry.currentStatus() == PluginStatusStarting {
		entry.loaded = nil
		entry.status = PluginStatusFailed
	}
}

func (m *Manager) finishStop(name string, status PluginStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.plugins[name]; entry != nil && entry.currentStatus() == PluginStatusStopping {
		entry.loaded = nil
		entry.status = status
	}
}

func (m *Manager) cleanupLoaded(name string, lp *loadedPlugin) error {
	if lp == nil {
		return nil
	}
	if lp.grpcToken != "" && m.hostGRPC != nil {
		m.hostGRPC.revokeToken(lp.grpcToken)
	}
	m.unregisterRoutes(name)
	m.unregisterStatic(name)
	if m.socket != nil {
		m.socket.unregisterPlugin(name)
	}
	if m.registry != nil && lp.record != nil {
		m.registry.Remove(lp.record.InstanceID)
	}
	if lp.loader != nil && lp.handle != nil {
		return lp.loader.Unload(lp.handle)
	}
	return nil
}

func readPluginInfo(path string) (DiscoveredPlugin, error) {
	f, err := os.Open(path)
	if err != nil {
		return DiscoveredPlugin{}, fmt.Errorf("open plugin package: %w", err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return DiscoveredPlugin{}, fmt.Errorf("stat plugin package: %w", err)
	}

	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return DiscoveredPlugin{}, fmt.Errorf("read zip plugin package: %w", err)
	}

	var info goplugin.Info
	for _, zf := range zr.File {
		if filepath.Clean(zf.Name) != "info.yaml" {
			continue
		}
		r, err := zf.Open()
		if err != nil {
			return DiscoveredPlugin{}, fmt.Errorf("open info.yaml: %w", err)
		}
		b, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			return DiscoveredPlugin{}, fmt.Errorf("read info.yaml: %w", err)
		}
		if err := yaml.Unmarshal(b, &info); err != nil {
			return DiscoveredPlugin{}, fmt.Errorf("parse info.yaml: %w", err)
		}
		break
	}

	if strings.TrimSpace(info.Name) == "" {
		return DiscoveredPlugin{}, fmt.Errorf("info.yaml Name is required")
	}
	if strings.TrimSpace(info.Version) == "" {
		return DiscoveredPlugin{}, fmt.Errorf("info.yaml Version is required")
	}
	if info.Type != "grpc" && info.Type != "wasm" {
		return DiscoveredPlugin{}, fmt.Errorf("info.yaml Type must be grpc or wasm")
	}
	if info.ContractVersion == 0 {
		return DiscoveredPlugin{}, fmt.Errorf("info.yaml ContractVersion is required")
	}
	if strings.TrimSpace(info.Command) == "" {
		return DiscoveredPlugin{}, fmt.Errorf("info.yaml Command is required")
	}

	return DiscoveredPlugin{
		Name:            info.Name,
		Version:         info.Version,
		Type:            info.Type,
		ContractVersion: info.ContractVersion,
		Command:         info.Command,
		Metadata:        info.Metadata,
		PackagePath:     path,
	}, nil
}

func (m *Manager) publishDiscovered(d DiscoveredPlugin) {
	// Keep scanned info available through read-only sys KV.
	b, err := json.Marshal(d)
	if err != nil {
		return
	}
	m.kv.SystemSet(SysNamespace, registryKVPrefix+"catalog/"+d.Name, b)
}

func (m *Manager) connFor(pluginType string, client any) (pluginConn, error) {
	switch pluginType {
	case "wasm":
		pc, ok := client.(wasmpb.Plugin)
		if !ok {
			return nil, fmt.Errorf("unexpected wasm plugin client type %T", client)
		}
		return wasmConn{client: pc}, nil
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

// Close unloads all plugins and stops the host callback server.
func (m *Manager) Close() error {
	m.mu.Lock()
	plugins := make([]*loadedPlugin, 0, len(m.plugins))
	for _, entry := range m.plugins {
		if entry.loaded != nil {
			plugins = append(plugins, entry.loaded)
			entry.loaded = nil
			entry.status = PluginStatusStopping
		} else if entry.currentStatus() == PluginStatusStarting {
			entry.status = PluginStatusFailed
		}
	}
	m.mu.Unlock()

	for _, lp := range plugins {
		if err := m.cleanupLoaded(lp.record.InstanceID, lp); err != nil {
			m.log.Error("failed to unload plugin", "plugin", lp.record.InstanceID, "err", err)
			m.finishStop(lp.record.InstanceID, PluginStatusFailed)
			continue
		}
		m.finishStop(lp.record.InstanceID, PluginStatusDiscovered)
	}

	if m.hostGRPC != nil {
		m.hostGRPC.Stop()
	}
	return nil
}
