package plugin

import (
	"context"
	"fmt"
	"log/slog"
)

// HostAPI is the backend-agnostic business logic exposed to plugins. Both the
// WASM host functions and the gRPC host callback server delegate here so the
// two backends share identical behavior.
type HostAPI struct {
	kv         *KV
	emitter    Emitter
	dispatcher PluginMessageDispatcher
	params     ParamsPatcher
	log        *slog.Logger
}

// NewHostAPI builds a HostAPI over the given KV store and emitter.
func NewHostAPI(kv *KV, emitter Emitter, log *slog.Logger) *HostAPI {
	if log == nil {
		log = slog.Default()
	}
	return &HostAPI{kv: kv, emitter: emitter, log: log}
}

// SetMessageDispatcher installs the plugin message dispatcher. It is set after
// Manager construction because the Manager itself performs target lookup.
func (h *HostAPI) SetMessageDispatcher(dispatcher PluginMessageDispatcher) {
	h.dispatcher = dispatcher
}

// SetParamsPatcher installs the persistent Params updater.
func (h *HostAPI) SetParamsPatcher(params ParamsPatcher) {
	h.params = params
}

// KVGet returns the value for ns/key and whether it was found.
func (h *HostAPI) KVGet(ns, key string) ([]byte, bool) { return h.kv.Get(ns, key) }

// KVSet stores a value at ns/key.
func (h *HostAPI) KVSet(ns, key string, value []byte) error { return h.kv.Set(ns, key, value) }

// KVDelete removes ns/key.
func (h *HostAPI) KVDelete(ns, key string) error { return h.kv.Delete(ns, key) }

// KVList lists keys in ns, or namespace names when ns is empty.
func (h *HostAPI) KVList(ns string) []string { return h.kv.List(ns) }

// Emit sends a Socket.IO emit on behalf of a plugin.
func (h *HostAPI) Emit(instr EmitInstruction) error {
	if h.emitter == nil {
		return fmt.Errorf("emitter not configured")
	}
	return h.emitter.Emit(instr)
}

// PluginMessage sends msg to another plugin, replacing Source with the
// authenticated caller's registered plugin name.
func (h *HostAPI) PluginMessage(ctx context.Context, source string, msg PluginMessage) (string, error) {
	if source == "" {
		return "", fmt.Errorf("plugin message source is not authenticated")
	}
	if msg.Target == "" {
		return "", fmt.Errorf("plugin message target is required")
	}
	if h.dispatcher == nil {
		return "", fmt.Errorf("plugin message dispatcher not configured")
	}
	msg.Source = source
	return h.dispatcher.DispatchPluginMessage(ctx, msg)
}

// PatchParams persists a partial update to the authenticated plugin's explicit
// Params override.
func (h *HostAPI) PatchParams(source string, patch ParamsPatch) error {
	if source == "" {
		return fmt.Errorf("plugin params source is not authenticated")
	}
	if h.params == nil {
		return fmt.Errorf("plugin params patcher not configured")
	}
	return h.params.PatchPluginParams(source, patch)
}

// Log writes a plugin log line at the requested level. source is established
// by the authenticated host boundary; it is never supplied by a plugin.
func (h *HostAPI) Log(source, level, msg string) {
	log := h.log.With("component", "plugin", "from", source)
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
