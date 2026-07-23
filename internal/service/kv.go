package service

import (
	"fmt"
	"sort"
	"sync"
)

// SysNamespace holds host-managed, service-read-only state such as the service
// registry. Services may read it but cannot write to it.
const SysNamespace = "sys"

// KV is an in-memory key-value store shared by all services.
//
// Layout is namespace -> key -> value. Every service gets its own namespace by
// convention but any service may read or write any namespace, except read-only
// namespaces (e.g. "sys") which only the host can modify. Values are raw bytes
// and are cleared on restart.
type KV struct {
	mu       sync.RWMutex
	data     map[string]map[string][]byte
	readOnly map[string]bool
}

// NewKV creates an empty store with the "sys" namespace marked read-only.
func NewKV() *KV {
	return &KV{
		data:     make(map[string]map[string][]byte),
		readOnly: map[string]bool{SysNamespace: true},
	}
}

// Get returns a copy of the value for ns/key and whether it was found.
func (k *KV) Get(ns, key string) ([]byte, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	m, ok := k.data[ns]
	if !ok {
		return nil, false
	}
	v, ok := m[key]
	if !ok {
		return nil, false
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, true
}

// Set stores value at ns/key. It fails for read-only namespaces.
func (k *KV) Set(ns, key string, value []byte) error {
	if k.readOnly[ns] {
		return fmt.Errorf("namespace %q is read-only", ns)
	}
	k.write(ns, key, value)
	return nil
}

// Delete removes ns/key. It fails for read-only namespaces.
func (k *KV) Delete(ns, key string) error {
	if k.readOnly[ns] {
		return fmt.Errorf("namespace %q is read-only", ns)
	}
	k.remove(ns, key)
	return nil
}

// List returns the sorted keys in ns, or the sorted namespace names when ns is
// empty.
func (k *KV) List(ns string) []string {
	k.mu.RLock()
	defer k.mu.RUnlock()

	var out []string
	if ns == "" {
		for name := range k.data {
			out = append(out, name)
		}
	} else {
		for key := range k.data[ns] {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// SystemSet stores value at ns/key, bypassing read-only protection. Host use only.
func (k *KV) SystemSet(ns, key string, value []byte) { k.write(ns, key, value) }

// SystemDelete removes ns/key, bypassing read-only protection. Host use only.
func (k *KV) SystemDelete(ns, key string) { k.remove(ns, key) }

func (k *KV) write(ns, key string, value []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, ok := k.data[ns]
	if !ok {
		m = make(map[string][]byte)
		k.data[ns] = m
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	m[key] = cp
}

func (k *KV) remove(ns, key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if m, ok := k.data[ns]; ok {
		delete(m, key)
	}
}
