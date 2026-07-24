// Package host implements backend-neutral kernel capabilities exposed to
// services. Protocol adapters delegate here after authenticating the caller.
package host

import (
	"context"
	"fmt"
	"log/slog"

	"arupa/internal/service/spec"
)

type KV interface {
	Get(ns, key string) ([]byte, bool)
	Set(ns, key string, value []byte) error
	Delete(ns, key string) error
	List(ns string) []string
}

// API is the backend-neutral Host implementation.
type API struct {
	kv         KV
	emitter    spec.Emitter
	dispatcher spec.MessageDispatcher
	params     spec.ParamsStore
	resources  spec.ResourceRegistrar
	log        *slog.Logger
}

func NewAPI(kv KV, emitter spec.Emitter, log *slog.Logger) *API {
	if log == nil {
		log = slog.Default()
	}
	return &API{kv: kv, emitter: emitter, log: log}
}

func (h *API) SetMessageDispatcher(dispatcher spec.MessageDispatcher) {
	h.dispatcher = dispatcher
}

func (h *API) SetParamsStore(params spec.ParamsStore) {
	h.params = params
}

func (h *API) SetResourceRegistrar(resources spec.ResourceRegistrar) {
	h.resources = resources
}

func (h *API) KVGet(ns, key string) ([]byte, bool)      { return h.kv.Get(ns, key) }
func (h *API) KVSet(ns, key string, value []byte) error { return h.kv.Set(ns, key, value) }
func (h *API) KVDelete(ns, key string) error            { return h.kv.Delete(ns, key) }
func (h *API) KVList(ns string) []string                { return h.kv.List(ns) }

func (h *API) Emit(instruction spec.EmitInstruction) error {
	if h.emitter == nil {
		return fmt.Errorf("emitter not configured")
	}
	return h.emitter.Emit(instruction)
}

func (h *API) ServiceMessage(ctx context.Context, source string, message spec.ServiceMessage) (string, error) {
	if source == "" {
		return "", fmt.Errorf("service message source is not authenticated")
	}
	if message.Target == "" {
		return "", fmt.Errorf("service message target is required")
	}
	if h.dispatcher == nil {
		return "", fmt.Errorf("service message dispatcher not configured")
	}
	message.Source = source
	return h.dispatcher.DispatchServiceMessage(ctx, message)
}

func (h *API) PatchParams(source string, patch spec.ParamsPatch) error {
	if source == "" {
		return fmt.Errorf("service params source is not authenticated")
	}
	if h.params == nil {
		return fmt.Errorf("service params patcher not configured")
	}
	return h.params.PatchServiceParams(source, patch)
}

func (h *API) GetParams(source string) (map[string]string, error) {
	if source == "" {
		return nil, fmt.Errorf("service params source is not authenticated")
	}
	if h.params == nil {
		return nil, fmt.Errorf("service params store not configured")
	}
	return h.params.GetServiceParams(source)
}

func (h *API) RegisterTransport(source string, declaration spec.Transport) error {
	if source == "" {
		return fmt.Errorf("transport source is not authenticated")
	}
	if h.resources == nil {
		return fmt.Errorf("transport registry is not configured")
	}
	return h.resources.RegisterTransport(source, declaration)
}

func (h *API) UnregisterTransport(source, id string) error {
	if source == "" {
		return fmt.Errorf("transport source is not authenticated")
	}
	if h.resources == nil {
		return fmt.Errorf("transport registry is not configured")
	}
	return h.resources.UnregisterTransport(source, id)
}

func (h *API) RegisterRoutes(source string, declarations []spec.Route) spec.RegistrationResult {
	if source == "" {
		return spec.RegistrationResult{Degraded: true, Error: "route source is not authenticated"}
	}
	if h.resources == nil {
		return spec.RegistrationResult{Degraded: true, Error: "route registry is not configured"}
	}
	return h.resources.RegisterRoutes(source, declarations)
}

func (h *API) UnregisterRoutes(source string, ids []string) spec.RegistrationResult {
	if source == "" {
		return spec.RegistrationResult{Error: "route source is not authenticated"}
	}
	if h.resources == nil {
		return spec.RegistrationResult{Error: "route registry is not configured"}
	}
	return h.resources.UnregisterRoutes(source, ids)
}

func (h *API) Log(source, level, message string) {
	log := h.log.With("component", "service", "from", source)
	switch level {
	case "debug":
		log.Debug(message)
	case "warn", "warning":
		log.Warn(message)
	case "error":
		log.Error(message)
	default:
		log.Info(message)
	}
}
