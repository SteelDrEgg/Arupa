package service

import (
	"context"
	"fmt"
	"log/slog"
)

// HostAPI is the backend-agnostic business logic exposed to services. Both the
// WASM host functions and the broker-hosted gRPC server delegate here so the
// two backends share identical behavior.
type HostAPI struct {
	kv         *KV
	emitter    Emitter
	dispatcher ServiceMessageDispatcher
	params     ParamsStore
	resources  ResourceRegistrar
	log        *slog.Logger
}

// NewHostAPI builds a HostAPI over the given KV store and emitter.
func NewHostAPI(kv *KV, emitter Emitter, log *slog.Logger) *HostAPI {
	if log == nil {
		log = slog.Default()
	}
	return &HostAPI{kv: kv, emitter: emitter, log: log}
}

// SetMessageDispatcher installs the service message dispatcher. It is set after
// Manager construction because the Manager itself performs target lookup.
func (h *HostAPI) SetMessageDispatcher(dispatcher ServiceMessageDispatcher) {
	h.dispatcher = dispatcher
}

// SetParamsStore installs the caller-scoped Params store.
func (h *HostAPI) SetParamsStore(params ParamsStore) {
	h.params = params
}

func (h *HostAPI) SetResourceRegistrar(resources ResourceRegistrar) {
	h.resources = resources
}

// KVGet returns the value for ns/key and whether it was found.
func (h *HostAPI) KVGet(ns, key string) ([]byte, bool) { return h.kv.Get(ns, key) }

// KVSet stores a value at ns/key.
func (h *HostAPI) KVSet(ns, key string, value []byte) error { return h.kv.Set(ns, key, value) }

// KVDelete removes ns/key.
func (h *HostAPI) KVDelete(ns, key string) error { return h.kv.Delete(ns, key) }

// KVList lists keys in ns, or namespace names when ns is empty.
func (h *HostAPI) KVList(ns string) []string { return h.kv.List(ns) }

// Emit sends a Socket.IO emit on behalf of a service.
func (h *HostAPI) Emit(instr EmitInstruction) error {
	if h.emitter == nil {
		return fmt.Errorf("emitter not configured")
	}
	return h.emitter.Emit(instr)
}

// ServiceMessage sends msg to another service, replacing Source with the
// authenticated caller's registered service name.
func (h *HostAPI) ServiceMessage(ctx context.Context, source string, msg ServiceMessage) (string, error) {
	if source == "" {
		return "", fmt.Errorf("service message source is not authenticated")
	}
	if msg.Target == "" {
		return "", fmt.Errorf("service message target is required")
	}
	if h.dispatcher == nil {
		return "", fmt.Errorf("service message dispatcher not configured")
	}
	msg.Source = source
	return h.dispatcher.DispatchServiceMessage(ctx, msg)
}

// PatchParams persists a partial update to the authenticated service's explicit
// Params override.
func (h *HostAPI) PatchParams(source string, patch ParamsPatch) error {
	if source == "" {
		return fmt.Errorf("service params source is not authenticated")
	}
	if h.params == nil {
		return fmt.Errorf("service params patcher not configured")
	}
	return h.params.PatchServiceParams(source, patch)
}

// GetParams returns the current effective Params for the authenticated service.
func (h *HostAPI) GetParams(source string) (map[string]string, error) {
	if source == "" {
		return nil, fmt.Errorf("service params source is not authenticated")
	}
	if h.params == nil {
		return nil, fmt.Errorf("service params store not configured")
	}
	return h.params.GetServiceParams(source)
}

func (h *HostAPI) RegisterTransport(source string, transport Transport) error {
	if source == "" {
		return fmt.Errorf("transport source is not authenticated")
	}
	if h.resources == nil {
		return fmt.Errorf("transport registry is not configured")
	}
	return h.resources.RegisterTransport(source, transport)
}

func (h *HostAPI) UnregisterTransport(source, id string) error {
	if source == "" {
		return fmt.Errorf("transport source is not authenticated")
	}
	if h.resources == nil {
		return fmt.Errorf("transport registry is not configured")
	}
	return h.resources.UnregisterTransport(source, id)
}

func (h *HostAPI) RegisterRoutes(source string, routes []Route) RegistrationResult {
	if source == "" {
		return RegistrationResult{Degraded: true, Error: "route source is not authenticated"}
	}
	if h.resources == nil {
		return RegistrationResult{Degraded: true, Error: "route registry is not configured"}
	}
	return h.resources.RegisterRoutes(source, routes)
}

func (h *HostAPI) UnregisterRoutes(source string, ids []string) RegistrationResult {
	if source == "" {
		return RegistrationResult{Error: "route source is not authenticated"}
	}
	if h.resources == nil {
		return RegistrationResult{Error: "route registry is not configured"}
	}
	return h.resources.UnregisterRoutes(source, ids)
}

// Log writes a service log line at the requested level. source is established
// by the authenticated host boundary; it is never supplied by a service.
func (h *HostAPI) Log(source, level, msg string) {
	log := h.log.With("component", "service", "from", source)
	switch level {
	case "debug":
		log.Debug(msg)
	case "warn", "warning":
		log.Warn(msg)
	case "error":
		log.Error(msg)
	default:
		log.Info(msg)
	}
}
