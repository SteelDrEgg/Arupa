package service

import "arupa/internal/service/host"

const SysNamespace = host.SysNamespace

type KV = host.Store

func NewKV() *KV {
	return host.NewStore()
}
