package service

import (
	"encoding/json"
	"sync"
)

// registryKVPrefix is the key prefix used to publish service records into the
// read-only "sys" KV namespace.
const registryKVPrefix = "service/"

// ServiceRecord describes a loaded service. It is exposed to services through the
// read-only "sys" KV namespace as JSON under "service/<instance_id>".
type ServiceRecord struct {
	InstanceID string      `json:"instance_id"`
	Name       string      `json:"name"`
	Version    string      `json:"version"`
	Type       string      `json:"type"` // static | wasm | grpc
	Path       string      `json:"path"`
	Transports []Transport `json:"transports,omitempty"`
	Routes     []Route     `json:"routes,omitempty"`
}

// Registry tracks loaded services and mirrors them into the sys KV namespace.
type Registry struct {
	kv *KV

	mu      sync.RWMutex
	records map[string]*ServiceRecord
}

// NewRegistry creates a registry backed by the given KV store.
func NewRegistry(kv *KV) *Registry {
	return &Registry{
		kv:      kv,
		records: make(map[string]*ServiceRecord),
	}
}

// Add records a service and publishes it to the sys namespace.
func (r *Registry) Add(rec *ServiceRecord) {
	if rec == nil {
		return
	}
	rec = cloneServiceRecord(rec)
	r.mu.Lock()
	r.records[rec.InstanceID] = rec
	r.mu.Unlock()

	if b, err := json.Marshal(rec); err == nil {
		r.kv.SystemSet(SysNamespace, registryKVPrefix+rec.InstanceID, b)
	}
}

// Remove deletes a service record and its sys namespace entry.
func (r *Registry) Remove(instanceID string) {
	r.mu.Lock()
	delete(r.records, instanceID)
	r.mu.Unlock()

	r.kv.SystemDelete(SysNamespace, registryKVPrefix+instanceID)
}

// Has reports whether a service with the given instance id is registered.
func (r *Registry) Has(instanceID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.records[instanceID]
	return ok
}

// List returns a snapshot of all service records.
func (r *Registry) List() []*ServiceRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*ServiceRecord, 0, len(r.records))
	for _, rec := range r.records {
		out = append(out, cloneServiceRecord(rec))
	}
	return out
}

func cloneServiceRecord(record *ServiceRecord) *ServiceRecord {
	if record == nil {
		return nil
	}
	out := *record
	out.Transports = make([]Transport, len(record.Transports))
	for index, transport := range record.Transports {
		out.Transports[index] = transport
		if transport.Proxy != nil {
			proxy := *transport.Proxy
			out.Transports[index].Proxy = &proxy
		}
	}
	out.Routes = make([]Route, len(record.Routes))
	for index, route := range record.Routes {
		out.Routes[index] = route
		if route.HTTP != nil {
			httpRoute := *route.HTTP
			httpRoute.Access.Groups = append([]string(nil), route.HTTP.Access.Groups...)
			out.Routes[index].HTTP = &httpRoute
		}
		if route.SocketIO != nil {
			socketRoute := *route.SocketIO
			socketRoute.Events = append([]string(nil), route.SocketIO.Events...)
			socketRoute.Access.Groups = append([]string(nil), route.SocketIO.Access.Groups...)
			if len(route.SocketIO.EventAccess) > 0 {
				socketRoute.EventAccess = make(map[string]AccessPolicy, len(route.SocketIO.EventAccess))
				for event, policy := range route.SocketIO.EventAccess {
					policy.Groups = append([]string(nil), policy.Groups...)
					socketRoute.EventAccess[event] = policy
				}
			}
			out.Routes[index].SocketIO = &socketRoute
		}
	}
	return &out
}
