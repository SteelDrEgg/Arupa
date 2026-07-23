// Package supervisor owns service discovery state and lifecycle transitions.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"arupa/internal/conf"
	"arupa/internal/service/binding"
	"arupa/internal/service/catalog"
	"arupa/internal/service/instance"
	"arupa/internal/service/runner"
	"arupa/internal/service/spec"
)

type Registry interface {
	Add(*spec.ServiceRecord)
	Remove(string)
}

type Options struct {
	Config   conf.ServiceSystem
	Catalog  *catalog.Catalog
	Loader   *runner.Loader
	Bindings *binding.Controller
	Registry Registry
	Logger   *slog.Logger
}

type Supervisor struct {
	catalog  *catalog.Catalog
	loader   *runner.Loader
	bindings *binding.Controller
	registry Registry
	log      *slog.Logger

	mu       sync.RWMutex
	config   conf.ServiceSystem
	services map[string]*serviceEntry
}

type serviceEntry struct {
	info       catalog.DiscoveredService
	config     conf.Service
	discovered bool
	loaded     *instance.Instance
	status     ServiceStatus
}

// ServiceEntry is a snapshot of a service known to the manager.
type ServiceEntry struct {
	catalog.DiscoveredService
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

func New(opts Options) *Supervisor {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Supervisor{
		catalog:  opts.Catalog,
		loader:   opts.Loader,
		bindings: opts.Bindings,
		registry: opts.Registry,
		log:      log,
		config:   opts.Config.Clone(),
		services: make(map[string]*serviceEntry),
	}
}

func (e *serviceEntry) currentStatus() ServiceStatus {
	if e == nil {
		return ServiceStatusDiscovered
	}
	if e.loaded != nil && e.loaded.Degraded() &&
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

func (r *Supervisor) Config() conf.ServiceSystem {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.Clone()
}

func (r *Supervisor) UpdateConfig(cfg conf.ServiceSystem) {
	cfg = cfg.Clone()

	r.mu.Lock()
	r.config = cfg
	for name, entry := range r.services {
		entry.config = cfg.EffectiveService(name)
		if entry.loaded != nil {
			entry.loaded.UpdateAccessGroups(entry.config.Allow)
		}
	}
	r.mu.Unlock()
}

func (r *Supervisor) DispatchServiceMessage(ctx context.Context, msg spec.ServiceMessage) (string, error) {
	r.mu.RLock()
	entry, ok := r.services[msg.Target]
	var lp *instance.Instance
	if ok {
		lp = entry.loaded
	}
	r.mu.RUnlock()
	if lp == nil || lp.Connection() == nil {
		return "", fmt.Errorf("target service %q does not accept service messages", msg.Target)
	}
	ctx, cancel := lp.CallContext(ctx)
	defer cancel()
	return lp.Connection().HandleServiceMessage(ctx, &msg)
}

func (r *Supervisor) LoadConfigured() error {
	if err := r.Scan(); err != nil {
		return err
	}
	return r.StartConfigured()
}

func (r *Supervisor) Scan() error {
	cfg := r.Config()
	discovered, err := r.catalog.Discover(cfg.ServiceDir)
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
			r.catalog.Unpublish(name)
		}
	}
	for _, entry := range next {
		if entry.discovered {
			r.catalog.Publish(entry.info)
		}
	}
	return nil
}

func (r *Supervisor) Entries() []ServiceEntry {
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

func (r *Supervisor) Discovered() []catalog.DiscoveredService {
	entries := r.Entries()
	out := make([]catalog.DiscoveredService, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.DiscoveredService)
	}
	return out
}

func (r *Supervisor) StartByName(name string) (*instance.Instance, error) {
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

func (r *Supervisor) Start(name string) error {
	_, err := r.StartByName(name)
	return err
}

func (r *Supervisor) Stop(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("service name is required")
	}

	r.mu.Lock()
	entry, ok := r.services[name]
	var lp *instance.Instance
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

func (r *Supervisor) Restart(name string) error {
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

func (r *Supervisor) StartConfigured() error {
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

func (r *Supervisor) Load(path string) (*instance.Instance, error) {
	scanned, err := catalog.ReadInfo(path)
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

func (r *Supervisor) loadScanned(scanned catalog.DiscoveredService, cfg conf.Service) (*instance.Instance, bool, error) {
	result, err := r.loader.Load(scanned, cfg)
	if err != nil {
		var unfaithful *runner.UnfaithfulServiceError
		if errors.As(err, &unfaithful) {
			r.log.Error("unfaithful service", "name", scanned.Name, "path", scanned.PackagePath, "err", err)
		} else {
			r.log.Error("failed to load service", "name", scanned.Name, "path", scanned.PackagePath, "err", err)
		}
		return nil, false, err
	}

	degraded := result.Loaded.Degraded()
	r.logLoadResult(result, degraded)
	return result.Loaded, degraded, nil
}

func (r *Supervisor) logLoadResult(result *runner.LoadResult, degraded bool) {
	rec := result.Loaded.SnapshotRecord()
	logArgs := []any{
		"name", rec.Name,
		"version", rec.Version,
		"type", rec.Type,
		"transports", len(rec.Transports),
		"routes", len(rec.Routes),
	}
	if rec.Type == "grpc" && result.RunAsUser != "" {
		logArgs = append(logArgs, "run_as_user", result.RunAsUser)
	}
	if degraded {
		r.log.Warn("loaded service with degraded host bindings", logArgs...)
	} else {
		r.log.Info("loaded service", logArgs...)
	}
}

func (r *Supervisor) finishStartSuccess(name string, scanned catalog.DiscoveredService, cfg conf.Service, lp *instance.Instance, degraded bool) error {
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

	r.registry.Add(lp.SnapshotRecord())
	return nil
}

func (r *Supervisor) finishStartFailure(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.services[name]; entry != nil && entry.currentStatus() == ServiceStatusStarting {
		entry.loaded = nil
		entry.status = ServiceStatusFailed
	}
}

func (r *Supervisor) finishStop(name string, status ServiceStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.services[name]; entry != nil && entry.currentStatus() == ServiceStatusStopping {
		entry.loaded = nil
		entry.status = status
	}
}

func (r *Supervisor) cleanupLoaded(name string, lp *instance.Instance) error {
	if lp == nil {
		return nil
	}
	lp.Cancel()
	lp.Revoke()
	if r.bindings != nil {
		r.bindings.Detach(name)
	}
	if r.registry != nil {
		r.registry.Remove(lp.InstanceID())
	}
	return lp.Close()
}

func (r *Supervisor) Close() error {
	r.mu.Lock()
	services := make([]*instance.Instance, 0, len(r.services))
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
		name := instanceID(lp)
		if err := r.cleanupLoaded(name, lp); err != nil {
			r.log.Error("failed to unload service", "service", name, "err", err)
			r.finishStop(name, ServiceStatusFailed)
			continue
		}
		r.finishStop(name, ServiceStatusDiscovered)
	}
	return nil
}

func instanceID(running *instance.Instance) string {
	if running == nil {
		return ""
	}
	return running.InstanceID()
}
