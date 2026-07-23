package service

import (
	"log/slog"

	"arupa/internal/service/host"
)

// HostAPI remains an alias at the facade boundary. Its implementation is
// isolated in package host and shared by all runtime adapters.
type HostAPI = host.API

func NewHostAPI(kv *KV, emitter Emitter, log *slog.Logger) *HostAPI {
	return host.NewAPI(kv, emitter, log)
}
