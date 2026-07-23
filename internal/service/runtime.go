package service

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

type serviceRuntimeOptions struct {
	Config    conf.ServiceSystem
	Catalog   *serviceCatalog
	Loader    *serviceLoader
	Resources *transportRegistry
	Registry  *Registry
	Logger    *slog.Logger
}

type serviceRuntime struct {
	catalog   *serviceCatalog
	loader    *serviceLoader
	resources *transportRegistry
	registry  *Registry
	log       *slog.Logger

	mu       sync.RWMutex
	config   conf.ServiceSystem
	services map[string]*serviceEntry
}

type serviceEntry struct {
	info       DiscoveredService
	config     conf.Service
	discovered bool
	loaded     *loadedService
	status     ServiceStatus
}

// ServiceEntry is a snapshot of a service known to the manager.
type ServiceEntry struct {
	DiscoveredService
	Config conf.Service
	Status ServiceStatus
}

// ServiceStatus describes the runtime lifecycle state of a service.
type ServiceStatus string

const (
	ServiceStatusDiscovered ServiceStatus = "discovered"
	ServiceStatusStarting   ServiceStatus = "starting"
	ServiceStatusRunning    ServiceStatus = "running"
	ServiceStatusDegraded   ServiceStatus = "degraded"
	ServiceStatusStopping   ServiceStatus = "stopping"
	ServiceStatusFailed     ServiceStatus = "failed"
)

func newServiceRuntime(opts serviceRuntimeOptions) *serviceRuntime {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &serviceRuntime{
		catalog:   opts.Catalog,
		loader:    opts.Loader,
		resources: opts.Resources,
		registry:  opts.Registry,
		log:       log,
		config:    opts.Config.Clone(),
		services:  make(map[string]*serviceEntry),
	}
}

func (e *serviceEntry) currentStatus() ServiceStatus {
	if e == nil {
		return ServiceStatusDiscovered
	}
	if e.loaded != nil && e.loaded.degraded.Load() &&
		(e.status == ServiceStatusRunning || e.status == ServiceStatusDegraded) {
		return ServiceStatusDegraded
	}
	if e.status != "" {
		return e.status
	}
	if e.loaded != nil {
		return ServiceStatusRunning
	}
	return ServiceStatusDiscovered
}

func statusAllowsStart(status ServiceStatus) bool {
	return status == ServiceStatusDiscovered || status == ServiceStatusFailed
}

func statusIsRunning(status ServiceStatus) bool {
	return status == ServiceStatusRunning || status == ServiceStatusDegraded
}

func (r *serviceRuntime) Config() conf.ServiceSystem {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.Clone()
}

func (r *serviceRuntime) UpdateConfig(cfg conf.ServiceSystem) {
	cfg = cfg.Clone()

	r.mu.Lock()
	r.config = cfg
	for name, entry := range r.services {
		entry.config = cfg.EffectiveService(name)
		if entry.loaded != nil {
			entry.loaded.updateAccessGroups(entry.config.Allow)
		}
	}
	r.mu.Unlock()
}

func (r *serviceRuntime) DispatchServiceMessage(ctx context.Context, msg ServiceMessage) (string, error) {
	r.mu.RLock()
	entry, ok := r.services[msg.Target]
	var lp *loadedService
	if ok {
		lp = entry.loaded
	}
	r.mu.RUnlock()
	if lp == nil || lp.conn == nil {
		return "", fmt.Errorf("target service %q does not accept service messages", msg.Target)
	}
	ctx, cancel := lp.callContext(ctx)
	defer cancel()
	return lp.conn.HandleServiceMessage(ctx, &msg)
}

func (r *serviceRuntime) LoadConfigured() error {
	if err := r.Scan(); err != nil {
		return err
	}
	return r.StartConfigured()
}

func (r *serviceRuntime) Scan() error {
	cfg := r.Config()
	discovered, err := r.catalog.discover(cfg.ServiceDir)
	if err != nil {
		return err
	}

	next := make(map[string]*serviceEntry, len(discovered))
	scanned := make(map[string]struct{}, len(discovered))
	for _, info := range discovered {
		next[info.Name] = &serviceEntry{
			info:       info,
			config:     cfg.EffectiveService(info.Name),
			discovered: true,
			status:     ServiceStatusDiscovered,
		}
		scanned[info.Name] = struct{}{}
	}

	for _, name := range cfg.ConfiguredServiceNames() {
		if _, ok := scanned[name]; !ok {
			r.log.Warn("configured service was not found in scan results", "name", name, "dir", cfg.ServiceDir)
		}
	}

	prevDiscovered := make(map[string]struct{})
	r.mu.Lock()
	for name, entry := range r.services {
		if entry.discovered {
			prevDiscovered[name] = struct{}{}
		}
		if nextEntry, ok := next[name]; ok {
			nextEntry.loaded = entry.loaded
			nextEntry.status = entry.currentStatus()
		} else if entry.loaded != nil {
			entry.discovered = false
			entry.config = cfg.EffectiveService(name)
			entry.status = entry.currentStatus()
			next[name] = entry
		} else if entry.currentStatus() == ServiceStatusStarting || entry.currentStatus() == ServiceStatusStopping {
			entry.discovered = false
			entry.config = cfg.EffectiveService(name)
			next[name] = entry
		}
	}
	r.config = cfg.Clone()
	r.services = next
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

func (r *serviceRuntime) Entries() []ServiceEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]ServiceEntry, 0, len(r.services))
	for _, entry := range r.services {
		if !entry.discovered {
			continue
		}
		out = append(out, ServiceEntry{
			DiscoveredService: entry.info,
			Config:            entry.config.Clone(),
			Status:            entry.currentStatus(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *serviceRuntime) Discovered() []DiscoveredService {
	entries := r.Entries()
	out := make([]DiscoveredService, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.DiscoveredService)
	}
	return out
}

func (r *serviceRuntime) StartByName(name string) (*loadedService, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("service name is required")
	}

	r.mu.Lock()
	entry, ok := r.services[name]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("service %q not found in scan results", name)
	}
	if !entry.discovered {
		r.mu.Unlock()
		return nil, fmt.Errorf("service %q is not available in scan results", name)
	}
	status := entry.currentStatus()
	if !statusAllowsStart(status) {
		r.mu.Unlock()
		return nil, fmt.Errorf("service %q is %s", name, status)
	}
	entry.status = ServiceStatusStarting
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

func (r *serviceRuntime) Start(name string) error {
	_, err := r.StartByName(name)
	return err
}

func (r *serviceRuntime) Stop(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("service name is required")
	}

	r.mu.Lock()
	entry, ok := r.services[name]
	var lp *loadedService
	if ok {
		status := entry.currentStatus()
		if status == ServiceStatusStarting || status == ServiceStatusStopping {
			r.mu.Unlock()
			return fmt.Errorf("service %q is %s", name, status)
		}
		if !statusIsRunning(status) || entry.loaded == nil {
			r.mu.Unlock()
			return fmt.Errorf("service %q is not running", name)
		}
		lp = entry.loaded
		entry.loaded = nil
		entry.status = ServiceStatusStopping
	}
	r.mu.Unlock()
	if lp == nil {
		return fmt.Errorf("service %q is not running", name)
	}

	if err := r.cleanupLoaded(name, lp); err != nil {
		r.finishStop(name, ServiceStatusFailed)
		return fmt.Errorf("unload service %q: %w", name, err)
	}
	r.finishStop(name, ServiceStatusDiscovered)
	r.log.Info("stopped service", "name", name)
	return nil
}

func (r *serviceRuntime) Restart(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("service name is required")
	}

	r.mu.Lock()
	entry, ok := r.services[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("service %q not found in scan results", name)
	}
	if !entry.discovered {
		r.mu.Unlock()
		return fmt.Errorf("service %q is not available in scan results", name)
	}
	status := entry.currentStatus()
	if status == ServiceStatusStarting || status == ServiceStatusStopping {
		r.mu.Unlock()
		return fmt.Errorf("service %q is %s", name, status)
	}
	info := entry.info
	cfg := entry.config.Clone()
	lp := entry.loaded
	if statusIsRunning(status) && lp != nil {
		entry.loaded = nil
		entry.status = ServiceStatusStopping
	} else {
		entry.status = ServiceStatusStarting
	}
	r.mu.Unlock()

	if lp != nil {
		if err := r.cleanupLoaded(name, lp); err != nil {
			r.finishStop(name, ServiceStatusFailed)
			return err
		}
		r.mu.Lock()
		if entry := r.services[name]; entry != nil {
			entry.status = ServiceStatusStarting
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

func (r *serviceRuntime) StartConfigured() error {
	for _, entry := range r.Entries() {
		if !entry.Config.AutoStart() {
			r.log.Info("service auto-start disabled by config", "name", entry.Name)
			continue
		}
		if !statusAllowsStart(entry.Status) {
			continue
		}
		if _, err := r.StartByName(entry.Name); err != nil {
			// StartByName records the load failure with its package path. Continue
			// starting the kernel even when an individual service is unavailable.
		}
	}
	return nil
}

func (r *serviceRuntime) Load(path string) (*loadedService, error) {
	scanned, err := readServiceInfo(path)
	if err != nil {
		return nil, err
	}
	cfg := r.Config().EffectiveService(scanned.Name)
	r.mu.Lock()
	entry, ok := r.services[scanned.Name]
	if !ok {
		entry = &serviceEntry{
			info:       scanned,
			config:     cfg.Clone(),
			discovered: true,
			status:     ServiceStatusDiscovered,
		}
		r.services[scanned.Name] = entry
	}
	status := entry.currentStatus()
	if !statusAllowsStart(status) {
		r.mu.Unlock()
		return nil, fmt.Errorf("service %q is %s", scanned.Name, status)
	}
	entry.info = scanned
	entry.config = cfg.Clone()
	entry.discovered = true
	entry.status = ServiceStatusStarting
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

func (r *serviceRuntime) loadScanned(scanned DiscoveredService, cfg conf.Service) (*loadedService, bool, error) {
	result, err := r.loader.load(scanned, cfg)
	if err != nil {
		var unfaithful *unfaithfulServiceError
		if errors.As(err, &unfaithful) {
			r.log.Error("unfaithful service", "name", scanned.Name, "path", scanned.PackagePath, "err", err)
		} else {
			r.log.Error("failed to load service", "name", scanned.Name, "path", scanned.PackagePath, "err", err)
		}
		return nil, false, err
	}

	degraded := result.loaded.degraded.Load()
	r.logLoadResult(result, degraded)
	return result.loaded, degraded, nil
}

func (r *serviceRuntime) logLoadResult(result *serviceLoadResult, degraded bool) {
	rec := result.loaded.snapshotRecord()
	logArgs := []any{
		"name", rec.Name,
		"version", rec.Version,
		"type", rec.Type,
		"transports", len(rec.Transports),
		"routes", len(rec.Routes),
	}
	if rec.Type == "grpc" && result.runAsUser != "" {
		logArgs = append(logArgs, "run_as_user", result.runAsUser)
	}
	if degraded {
		r.log.Warn("loaded service with degraded host bindings", logArgs...)
	} else {
		r.log.Info("loaded service", logArgs...)
	}
}

func (r *serviceRuntime) finishStartSuccess(name string, scanned DiscoveredService, cfg conf.Service, lp *loadedService, degraded bool) error {
	r.mu.Lock()
	entry, ok := r.services[name]
	if !ok {
		entry = &serviceEntry{}
		r.services[name] = entry
	}
	if entry.currentStatus() != ServiceStatusStarting {
		status := entry.currentStatus()
		r.mu.Unlock()
		return fmt.Errorf("service %q start completed while status is %s", name, status)
	}
	entry.info = scanned
	entry.config = cfg.Clone()
	entry.discovered = true
	entry.loaded = lp
	if degraded {
		entry.status = ServiceStatusDegraded
	} else {
		entry.status = ServiceStatusRunning
	}
	r.mu.Unlock()

	r.registry.Add(lp.snapshotRecord())
	if r.resources != nil {
		r.resources.publish(lp)
	}
	return nil
}

func (r *serviceRuntime) finishStartFailure(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.services[name]; entry != nil && entry.currentStatus() == ServiceStatusStarting {
		entry.loaded = nil
		entry.status = ServiceStatusFailed
	}
}

func (r *serviceRuntime) finishStop(name string, status ServiceStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.services[name]; entry != nil && entry.currentStatus() == ServiceStatusStopping {
		entry.loaded = nil
		entry.status = status
	}
}

func (r *serviceRuntime) cleanupLoaded(name string, lp *loadedService) error {
	if lp == nil {
		return nil
	}
	if r.resources != nil {
		r.resources.detach(name)
	}
	lp.cancelLifecycle()
	r.loader.revoke(lp)
	if r.registry != nil && lp.record != nil {
		r.registry.Remove(lp.record.InstanceID)
	}
	return r.loader.unload(lp)
}

func (r *serviceRuntime) Close() error {
	r.mu.Lock()
	services := make([]*loadedService, 0, len(r.services))
	for _, entry := range r.services {
		if entry.loaded != nil {
			services = append(services, entry.loaded)
			entry.loaded = nil
			entry.status = ServiceStatusStopping
		} else if entry.currentStatus() == ServiceStatusStarting {
			entry.status = ServiceStatusFailed
		}
	}
	r.mu.Unlock()

	for _, lp := range services {
		name := loadedServiceInstanceID(lp)
		if err := r.cleanupLoaded(name, lp); err != nil {
			r.log.Error("failed to unload service", "service", name, "err", err)
			r.finishStop(name, ServiceStatusFailed)
			continue
		}
		r.finishStop(name, ServiceStatusDiscovered)
	}
	return nil
}

func loadedServiceInstanceID(lp *loadedService) string {
	if lp == nil || lp.record == nil {
		return ""
	}
	return lp.record.InstanceID
}
