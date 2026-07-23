package host

import (
	"fmt"
	"sort"
	"sync"
)

const SysNamespace = "sys"

// Store is the in-memory key-value capability exposed to services.
type Store struct {
	mu       sync.RWMutex
	data     map[string]map[string][]byte
	readOnly map[string]bool
}

func NewStore() *Store {
	return &Store{
		data:     make(map[string]map[string][]byte),
		readOnly: map[string]bool{SysNamespace: true},
	}
}

func (s *Store) Get(namespace, key string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values, ok := s.data[namespace]
	if !ok {
		return nil, false
	}
	value, ok := values[key]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), value...), true
}

func (s *Store) Set(namespace, key string, value []byte) error {
	if s.readOnly[namespace] {
		return fmt.Errorf("namespace %q is read-only", namespace)
	}
	s.write(namespace, key, value)
	return nil
}

func (s *Store) Delete(namespace, key string) error {
	if s.readOnly[namespace] {
		return fmt.Errorf("namespace %q is read-only", namespace)
	}
	s.remove(namespace, key)
	return nil
}

func (s *Store) List(namespace string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	if namespace == "" {
		for name := range s.data {
			out = append(out, name)
		}
	} else {
		for key := range s.data[namespace] {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Store) SystemSet(namespace, key string, value []byte) {
	s.write(namespace, key, value)
}

func (s *Store) SystemDelete(namespace, key string) {
	s.remove(namespace, key)
}

func (s *Store) write(namespace, key string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, ok := s.data[namespace]
	if !ok {
		values = make(map[string][]byte)
		s.data[namespace] = values
	}
	values[key] = append([]byte(nil), value...)
}

func (s *Store) remove(namespace, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if values, ok := s.data[namespace]; ok {
		delete(values, key)
	}
}
