// Package registry maintains the read model of running services.
package registry

import (
	"encoding/json"
	"sync"

	"arupa/internal/service/host"
	"arupa/internal/service/spec"
)

// registryKVPrefix is the key prefix used to publish service records into the
// read-only "sys" KV namespace.
const registryKVPrefix = "service/"

// Registry tracks loaded services and mirrors them into the sys KV namespace.
type SystemStore interface {
	SystemSet(ns, key string, value []byte)
	SystemDelete(ns, key string)
}

type Registry struct {
	kv SystemStore

	mu      sync.RWMutex
	records map[string]*spec.ServiceRecord
}

// New creates a registry backed by the given KV store.
func New(kv SystemStore) *Registry {
	return &Registry{
		kv:      kv,
		records: make(map[string]*spec.ServiceRecord),
	}
}

// Add records a service and publishes it to the sys namespace.
func (r *Registry) Add(rec *spec.ServiceRecord) {
	if rec == nil {
		return
	}
	rec = spec.CloneServiceRecord(rec)
	r.mu.Lock()
	r.records[rec.InstanceID] = rec
	r.mu.Unlock()

	if b, err := json.Marshal(rec); err == nil {
		r.kv.SystemSet(host.SysNamespace, registryKVPrefix+rec.InstanceID, b)
	}
}

// Remove deletes a service record and its sys namespace entry.
func (r *Registry) Remove(instanceID string) {
	r.mu.Lock()
	delete(r.records, instanceID)
	r.mu.Unlock()

	r.kv.SystemDelete(host.SysNamespace, registryKVPrefix+instanceID)
}

// Has reports whether a service with the given instance id is registered.
func (r *Registry) Has(instanceID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.records[instanceID]
	return ok
}

// List returns a snapshot of all service records.
func (r *Registry) List() []*spec.ServiceRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*spec.ServiceRecord, 0, len(r.records))
	for _, rec := range r.records {
		out = append(out, spec.CloneServiceRecord(rec))
	}
	return out
}
