// Package instance owns the backend-neutral state of one running service.
package instance

import (
	"context"
	"os"
	"sync"
	"sync/atomic"

	"arupa/internal/auth"
	"arupa/internal/service/spec"
)

type Options struct {
	Connection     spec.Conn
	Record         *spec.ServiceRecord
	Access         func() auth.AccessPolicy
	CloseBackend   func() error
	StopHostBroker func()
	CleanupDirs    []string
}

// Instance is the service runtime session shared through narrow interfaces.
// Process handles and go-plugin details stay captured in CloseBackend.
type Instance struct {
	conn spec.Conn

	recordMu sync.Mutex
	record   *spec.ServiceRecord

	access func() auth.AccessPolicy

	lifecycle context.Context
	cancel    context.CancelFunc
	degraded  atomic.Bool

	closeOnce      sync.Once
	closeErr       error
	closeBackend   func() error
	stopHostBroker func()
	cleanupDirs    []string
}

func New(options Options) *Instance {
	lifecycle, cancel := context.WithCancel(context.Background())
	return &Instance{
		conn: options.Connection, record: spec.CloneServiceRecord(options.Record),
		access: options.Access, lifecycle: lifecycle, cancel: cancel,
		closeBackend: options.CloseBackend, stopHostBroker: options.StopHostBroker,
		cleanupDirs: append([]string(nil), options.CleanupDirs...),
	}
}

func (i *Instance) Connection() spec.Conn {
	if i == nil {
		return nil
	}
	return i.conn
}

func (i *Instance) AccessPolicy() auth.AccessPolicy {
	if i == nil || i.access == nil {
		return auth.AccessPolicy{}
	}
	access := i.access()
	return auth.AccessPolicy{
		RequireAuth: access.RequireAuth,
		Groups:      append([]string(nil), access.Groups...),
	}
}

func (i *Instance) CallContext(parent context.Context) (context.Context, context.CancelFunc) {
	if i == nil {
		return mergeContext(parent, nil)
	}
	return mergeContext(parent, i.lifecycle)
}

func (i *Instance) EventContext() (context.Context, context.CancelFunc) {
	return i.CallContext(context.Background())
}

func (i *Instance) Cancel() {
	if i != nil && i.cancel != nil {
		i.cancel()
	}
}

func (i *Instance) AddTransport(declaration spec.Transport) {
	if i == nil || i.record == nil {
		return
	}
	i.recordMu.Lock()
	i.record.Transports = append(i.record.Transports, declaration)
	i.recordMu.Unlock()
}

func (i *Instance) RemoveTransport(id string) {
	if i == nil || i.record == nil {
		return
	}
	i.recordMu.Lock()
	defer i.recordMu.Unlock()
	for index, declaration := range i.record.Transports {
		if declaration.ID == id {
			i.record.Transports = append(i.record.Transports[:index], i.record.Transports[index+1:]...)
			return
		}
	}
}

func (i *Instance) AddRoute(declaration spec.Route) {
	if i == nil || i.record == nil {
		return
	}
	i.recordMu.Lock()
	i.record.Routes = append(i.record.Routes, declaration)
	i.recordMu.Unlock()
}

func (i *Instance) RemoveRoute(id string) {
	if i == nil || i.record == nil {
		return
	}
	i.recordMu.Lock()
	defer i.recordMu.Unlock()
	for index, declaration := range i.record.Routes {
		if declaration.ID == id {
			i.record.Routes = append(i.record.Routes[:index], i.record.Routes[index+1:]...)
			return
		}
	}
}

func (i *Instance) MarkDegraded() {
	if i != nil {
		i.degraded.Store(true)
	}
}

func (i *Instance) Degraded() bool {
	return i != nil && i.degraded.Load()
}

func (i *Instance) SnapshotRecord() *spec.ServiceRecord {
	if i == nil || i.record == nil {
		return nil
	}
	i.recordMu.Lock()
	defer i.recordMu.Unlock()
	return spec.CloneServiceRecord(i.record)
}

func (i *Instance) UpdateIdentity(name, version string) {
	if i == nil || i.record == nil {
		return
	}
	i.recordMu.Lock()
	i.record.Name = name
	i.record.Version = version
	i.recordMu.Unlock()
}

func (i *Instance) InstanceID() string {
	record := i.SnapshotRecord()
	if record == nil {
		return ""
	}
	return record.InstanceID
}

// Revoke stops callbacks before bindings are detached and the backend closes.
func (i *Instance) Revoke() {
	if i != nil && i.stopHostBroker != nil {
		i.stopHostBroker()
	}
}

// Close terminates backend resources and removes runtime directories once.
func (i *Instance) Close() error {
	if i == nil {
		return nil
	}
	i.closeOnce.Do(func() {
		if i.closeBackend != nil {
			i.closeErr = i.closeBackend()
		}
		for _, dir := range i.cleanupDirs {
			if dir == "" {
				continue
			}
			if err := os.RemoveAll(dir); i.closeErr == nil {
				i.closeErr = err
			}
		}
	})
	return i.closeErr
}

func mergeContext(parent, lifecycle context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if lifecycle == nil {
		return context.WithCancel(parent)
	}
	ctx, cancel := context.WithCancel(parent)
	if lifecycle.Err() != nil {
		cancel()
		return ctx, func() {}
	}
	stop := context.AfterFunc(lifecycle, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
