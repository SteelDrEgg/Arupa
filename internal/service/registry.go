package service

import serviceregistry "arupa/internal/service/registry"

type Registry = serviceregistry.Registry

func NewRegistry(kv *KV) *Registry {
	return serviceregistry.New(kv)
}
