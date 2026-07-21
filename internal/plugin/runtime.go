package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"arupa/internal/conf"
)

type pluginRuntimeOptions struct {
	Config    conf.PluginSystem
	Catalog   *pluginCatalog
	Loader    *pluginLoader
	Registrar *pluginRegistrar
	Registry  *Registry
	Logger    *slog.Logger
}

type pluginRuntime struct {
	catalog   *pluginCatalog
	loader    *pluginLoader
	registrar *pluginRegistrar
	registry  *Registry
	log       *slog.Logger

	mu      sync.RWMutex
	config  conf.PluginSystem
	plugins map[string]*pluginEntry
}

type pluginEntry struct {
	info       DiscoveredPlugin
	config     conf.Plugin
	discovered bool
	loaded     *loadedPlugin
	status     PluginStatus
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

func newPluginRuntime(opts pluginRuntimeOptions) *pluginRuntime {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &pluginRuntime{
		catalog:   opts.Catalog,
		loader:    opts.Loader,
		registrar: opts.Registrar,
		registry:  opts.Registry,
		log:       log,
		config:    opts.Config.Clone(),
		plugins:   make(map[string]*pluginEntry),
	}
}

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

func (r *pluginRuntime) Config() conf.PluginSystem {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.Clone()
}

func (r *pluginRuntime) UpdateConfig(cfg conf.PluginSystem) {
	cfg = cfg.Clone()

	r.mu.Lock()
	r.config = cfg
	for name, entry := range r.plugins {
		entry.config = cfg.EffectivePlugin(name)
		if entry.loaded != nil {
			entry.loaded.updateAccessGroups(entry.config.Allow)
		}
	}
	r.mu.Unlock()
}

func (r *pluginRuntime) DispatchPluginMessage(ctx context.Context, msg PluginMessage) (string, error) {
	r.mu.RLock()
	entry, ok := r.plugins[msg.Target]
	var lp *loadedPlugin
	if ok {
		lp = entry.loaded
	}
	r.mu.RUnlock()
	if lp == nil {
		return "", fmt.Errorf("target plugin %q is not running", msg.Target)
	}
	ctx, cancel := lp.callContext(ctx)
	defer cancel()
	return lp.conn.HandlePluginMessage(ctx, &msg)
}

func (r *pluginRuntime) LoadConfigured() error {
	if err := r.Scan(); err != nil {
		return err
	}
	return r.StartConfigured()
}

func (r *pluginRuntime) Scan() error {
	cfg := r.Config()
	discovered, err := r.catalog.discover(cfg.PluginDir)
	if err != nil {
		return err
	}

	next := make(map[string]*pluginEntry, len(discovered))
	scanned := make(map[string]struct{}, len(discovered))
	for _, info := range discovered {
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
			r.log.Warn("configured plugin was not found in scan results", "name", name, "dir", cfg.PluginDir)
		}
	}

	prevDiscovered := make(map[string]struct{})
	r.mu.Lock()
	for name, entry := range r.plugins {
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
	r.config = cfg.Clone()
	r.plugins = next
	r.mu.Unlock()

	for name := range prevDiscovered {
		if _, ok := scanned[name]; !ok {
			r.catalog.unpublish(name)
		}
	}
	for _, entry := range next {
		if entry.discovered {
			r.catalog.publish(entry.info)
		}
	}
	return nil
}

func (r *pluginRuntime) Entries() []PluginEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]PluginEntry, 0, len(r.plugins))
	for _, entry := range r.plugins {
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

func (r *pluginRuntime) Discovered() []DiscoveredPlugin {
	entries := r.Entries()
	out := make([]DiscoveredPlugin, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.DiscoveredPlugin)
	}
	return out
}

func (r *pluginRuntime) StartByName(name string) (*loadedPlugin, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("plugin name is required")
	}

	r.mu.Lock()
	entry, ok := r.plugins[name]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("plugin %q not found in scan results", name)
	}
	if !entry.discovered {
		r.mu.Unlock()
		return nil, fmt.Errorf("plugin %q is not available in scan results", name)
	}
	status := entry.currentStatus()
	if !statusAllowsStart(status) {
		r.mu.Unlock()
		return nil, fmt.Errorf("plugin %q is %s", name, status)
	}
	entry.status = PluginStatusStarting
	info := entry.info
	cfg := entry.config.Clone()
	r.mu.Unlock()

	lp, degraded, err := r.loadScanned(info, cfg)
	if err != nil {
		r.finishStartFailure(name)
		return nil, err
	}
	if err := r.finishStartSuccess(name, info, cfg, lp, degraded); err != nil {
		_ = r.cleanupLoaded(name, lp)
		r.finishStartFailure(name)
		return nil, err
	}
	return lp, nil
}

func (r *pluginRuntime) Start(name string) error {
	_, err := r.StartByName(name)
	return err
}

func (r *pluginRuntime) Stop(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("plugin name is required")
	}

	r.mu.Lock()
	entry, ok := r.plugins[name]
	var lp *loadedPlugin
	if ok {
		status := entry.currentStatus()
		if status == PluginStatusStarting || status == PluginStatusStopping {
			r.mu.Unlock()
			return fmt.Errorf("plugin %q is %s", name, status)
		}
		if !statusIsRunning(status) || entry.loaded == nil {
			r.mu.Unlock()
			return fmt.Errorf("plugin %q is not running", name)
		}
		lp = entry.loaded
		entry.loaded = nil
		entry.status = PluginStatusStopping
	}
	r.mu.Unlock()
	if lp == nil {
		return fmt.Errorf("plugin %q is not running", name)
	}

	if err := r.cleanupLoaded(name, lp); err != nil {
		r.finishStop(name, PluginStatusFailed)
		return fmt.Errorf("unload plugin %q: %w", name, err)
	}
	r.finishStop(name, PluginStatusDiscovered)
	r.log.Info("stopped plugin", "name", name)
	return nil
}

func (r *pluginRuntime) Restart(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("plugin name is required")
	}

	r.mu.Lock()
	entry, ok := r.plugins[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("plugin %q not found in scan results", name)
	}
	if !entry.discovered {
		r.mu.Unlock()
		return fmt.Errorf("plugin %q is not available in scan results", name)
	}
	status := entry.currentStatus()
	if status == PluginStatusStarting || status == PluginStatusStopping {
		r.mu.Unlock()
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
	r.mu.Unlock()

	if lp != nil {
		if err := r.cleanupLoaded(name, lp); err != nil {
			r.finishStop(name, PluginStatusFailed)
			return err
		}
		r.mu.Lock()
		if entry := r.plugins[name]; entry != nil {
			entry.status = PluginStatusStarting
		}
		r.mu.Unlock()
	}

	next, degraded, err := r.loadScanned(info, cfg)
	if err != nil {
		r.finishStartFailure(name)
		return err
	}
	if err := r.finishStartSuccess(name, info, cfg, next, degraded); err != nil {
		_ = r.cleanupLoaded(name, next)
		r.finishStartFailure(name)
		return err
	}
	return nil
}

func (r *pluginRuntime) StartConfigured() error {
	for _, entry := range r.Entries() {
		if !entry.Config.AutoStart() {
			r.log.Info("plugin auto-start disabled by config", "name", entry.Name)
			continue
		}
		if !statusAllowsStart(entry.Status) {
			continue
		}
		if _, err := r.StartByName(entry.Name); err != nil {
			// StartByName records the load failure with its package path. Continue
			// starting the kernel even when an individual plugin is unavailable.
		}
	}
	return nil
}

func (r *pluginRuntime) Load(path string) (*loadedPlugin, error) {
	scanned, err := readPluginInfo(path)
	if err != nil {
		return nil, err
	}
	cfg := r.Config().EffectivePlugin(scanned.Name)
	r.mu.Lock()
	entry, ok := r.plugins[scanned.Name]
	if !ok {
		entry = &pluginEntry{
			info:       scanned,
			config:     cfg.Clone(),
			discovered: true,
			status:     PluginStatusDiscovered,
		}
		r.plugins[scanned.Name] = entry
	}
	status := entry.currentStatus()
	if !statusAllowsStart(status) {
		r.mu.Unlock()
		return nil, fmt.Errorf("plugin %q is %s", scanned.Name, status)
	}
	entry.info = scanned
	entry.config = cfg.Clone()
	entry.discovered = true
	entry.status = PluginStatusStarting
	r.mu.Unlock()

	lp, degraded, err := r.loadScanned(scanned, cfg)
	if err != nil {
		r.finishStartFailure(scanned.Name)
		return nil, err
	}
	if err := r.finishStartSuccess(scanned.Name, scanned, cfg, lp, degraded); err != nil {
		_ = r.cleanupLoaded(scanned.Name, lp)
		r.finishStartFailure(scanned.Name)
		return nil, err
	}
	return lp, nil
}

func (r *pluginRuntime) loadScanned(scanned DiscoveredPlugin, cfg conf.Plugin) (*loadedPlugin, bool, error) {
	result, err := r.loader.load(scanned, cfg)
	if err != nil {
		var unfaithful *unfaithfulPluginError
		if errors.As(err, &unfaithful) {
			r.log.Error("unfaithful plugin", "name", scanned.Name, "path", scanned.PackagePath, "err", err)
		} else {
			r.log.Error("failed to load plugin", "name", scanned.Name, "path", scanned.PackagePath, "err", err)
		}
		return nil, false, err
	}

	degraded := r.registrar.register(result.loaded.record.InstanceID, result.rootPath, result.registration, result.loaded)
	r.logLoadResult(result, degraded)
	return result.loaded, degraded, nil
}

func (r *pluginRuntime) logLoadResult(result *pluginLoadResult, degraded bool) {
	rec := result.loaded.record
	logArgs := []any{
		"name", rec.Name,
		"version", rec.Version,
		"type", rec.Type,
		"routes", len(rec.Routes),
		"static_mounts", len(rec.Static),
		"namespaces", len(rec.Namespaces),
	}
	if rec.Type == "grpc" && result.runAsUser != "" {
		logArgs = append(logArgs, "run_as_user", result.runAsUser)
	}
	if degraded {
		r.log.Warn("loaded plugin with degraded host bindings", logArgs...)
	} else {
		r.log.Info("loaded plugin", logArgs...)
	}
}

func (r *pluginRuntime) finishStartSuccess(name string, scanned DiscoveredPlugin, cfg conf.Plugin, lp *loadedPlugin, degraded bool) error {
	r.mu.Lock()
	entry, ok := r.plugins[name]
	if !ok {
		entry = &pluginEntry{}
		r.plugins[name] = entry
	}
	if entry.currentStatus() != PluginStatusStarting {
		status := entry.currentStatus()
		r.mu.Unlock()
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
	r.mu.Unlock()

	r.registry.Add(lp.record)
	return nil
}

func (r *pluginRuntime) finishStartFailure(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.plugins[name]; entry != nil && entry.currentStatus() == PluginStatusStarting {
		entry.loaded = nil
		entry.status = PluginStatusFailed
	}
}

func (r *pluginRuntime) finishStop(name string, status PluginStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.plugins[name]; entry != nil && entry.currentStatus() == PluginStatusStopping {
		entry.loaded = nil
		entry.status = status
	}
}

func (r *pluginRuntime) cleanupLoaded(name string, lp *loadedPlugin) error {
	if lp == nil {
		return nil
	}
	r.registrar.unregister(name)
	lp.cancelLifecycle()
	r.loader.revoke(lp)
	if r.registry != nil && lp.record != nil {
		r.registry.Remove(lp.record.InstanceID)
	}
	return r.loader.unload(lp)
}

func (r *pluginRuntime) Close() error {
	r.mu.Lock()
	plugins := make([]*loadedPlugin, 0, len(r.plugins))
	for _, entry := range r.plugins {
		if entry.loaded != nil {
			plugins = append(plugins, entry.loaded)
			entry.loaded = nil
			entry.status = PluginStatusStopping
		} else if entry.currentStatus() == PluginStatusStarting {
			entry.status = PluginStatusFailed
		}
	}
	r.mu.Unlock()

	for _, lp := range plugins {
		name := loadedPluginInstanceID(lp)
		if err := r.cleanupLoaded(name, lp); err != nil {
			r.log.Error("failed to unload plugin", "plugin", name, "err", err)
			r.finishStop(name, PluginStatusFailed)
			continue
		}
		r.finishStop(name, PluginStatusDiscovered)
	}
	return nil
}

func loadedPluginInstanceID(lp *loadedPlugin) string {
	if lp == nil || lp.record == nil {
		return ""
	}
	return lp.record.InstanceID
}
