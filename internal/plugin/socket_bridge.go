package plugin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"arupa/internal/auth"
	"arupa/internal/netx"
	"github.com/zishang520/socket.io/servers/socket/v3"
)

// socketBridge wires plugin-declared Socket.IO namespaces and events into the
// global Socket.IO server. Incoming events are forwarded to the owning plugin;
// emits requested by plugins (either in-reply for WASM or via the Host.Emit
// callback for gRPC) are sent out through this bridge.
type socketBridge struct {
	server *netx.Socket
	log    *slog.Logger

	mu         sync.RWMutex
	owners     map[string]socketOwner // namespace -> current plugin binding
	registered map[string]struct{}    // namespace -> dynamic handlers installed
}

type socketOwner struct {
	pluginName      string
	plugin          *loadedPlugin
	decl            SocketNamespaceDecl
	events          map[string]struct{}
	protectedEvents map[string]struct{}
}

func newSocketBridge(server *netx.Socket, log *slog.Logger) *socketBridge {
	if log == nil {
		log = slog.Default()
	}
	return &socketBridge{
		server:     server,
		log:        log,
		owners:     make(map[string]socketOwner),
		registered: make(map[string]struct{}),
	}
}

// register attaches a plugin's namespace and its event handlers.
//
// Socket.IO namespaces are kept as stable shells. The namespace middleware and
// connection handler are installed once; each connection and event dispatch
// resolves the current owner and event declaration from b.owners. Stop clears
// the owner and disconnects sockets from that namespace; Restart replaces it.
func (b *socketBridge) register(pluginName string, decl SocketNamespaceDecl, lp *loadedPlugin) error {
	if decl.Name == "" {
		return fmt.Errorf("socket namespace name is required")
	}
	if lp == nil || lp.conn == nil {
		return fmt.Errorf("socket namespace %q requires a plugin connection", decl.Name)
	}

	b.server.AddNamespace(decl.Name)
	ns := b.server.GetNamespace(decl.Name)
	if ns.Raw() == nil {
		return fmt.Errorf("failed to create socket namespace %q", decl.Name)
	}

	b.mu.Lock()
	if owner, exists := b.owners[decl.Name]; exists && owner.pluginName != "" && owner.pluginName != pluginName {
		b.mu.Unlock()
		return fmt.Errorf("socket namespace %q already owned by plugin %q", decl.Name, owner.pluginName)
	}
	_, alreadyRegistered := b.registered[decl.Name]
	if !alreadyRegistered {
		b.installNamespaceHandlersLocked(decl.Name, ns)
		b.registered[decl.Name] = struct{}{}
	}
	b.owners[decl.Name] = newSocketOwner(pluginName, decl, lp)
	b.mu.Unlock()
	return nil
}

func newSocketOwner(pluginName string, decl SocketNamespaceDecl, lp *loadedPlugin) socketOwner {
	events := make(map[string]struct{}, len(decl.Events))
	for _, event := range decl.Events {
		events[event] = struct{}{}
	}

	protectedEvents := make(map[string]struct{}, len(decl.ProtectedEvents))
	for _, event := range decl.ProtectedEvents {
		protectedEvents[event] = struct{}{}
	}

	return socketOwner{
		pluginName:      pluginName,
		plugin:          lp,
		decl:            cloneSocketNamespaceDecl(decl),
		events:          events,
		protectedEvents: protectedEvents,
	}
}

func cloneSocketNamespaceDecl(decl SocketNamespaceDecl) SocketNamespaceDecl {
	decl.Events = append([]string(nil), decl.Events...)
	decl.ProtectedEvents = append([]string(nil), decl.ProtectedEvents...)
	return decl
}

func (b *socketBridge) installNamespaceHandlersLocked(nsName string, ns netx.Namespace) {
	ns.AddMiddleware(func(client *socket.Socket, next func(*socket.ExtendedError)) {
		if err := b.authorizeNamespace(nsName, client); err != nil {
			next(err)
			return
		}
		next(nil)
	})

	ns.OnConnection(func(client *socket.Socket) {
		client.OnAny(func(args ...any) {
			b.handleAny(nsName, client, args)
		})
	})
}

func (b *socketBridge) authorizeNamespace(nsName string, client *socket.Socket) *socket.ExtendedError {
	owner, ok := b.ownerForNamespace(nsName)
	if !ok {
		return socket.NewExtendedError("Unavailable", "namespace is not owned by a running plugin")
	}
	if !owner.decl.Protected {
		return nil
	}

	var authErr *socket.ExtendedError
	auth.RequireAuthSocketIO(client, func(err *socket.ExtendedError) {
		authErr = err
	})
	return authErr
}

// unregisterPlugin releases all namespace ownership held by pluginName. The
// underlying Socket.IO namespace and dynamic handlers remain installed, but
// future connections are rejected until a plugin registers the namespace again.
// Existing sockets are disconnected from the released namespace.
func (b *socketBridge) unregisterPlugin(pluginName string) {
	b.mu.Lock()
	var released []string
	for ns, owner := range b.owners {
		if owner.pluginName == pluginName {
			b.owners[ns] = socketOwner{}
			released = append(released, ns)
		}
	}
	b.mu.Unlock()

	for _, nsName := range released {
		ns := b.server.GetNamespace(nsName)
		ns.DisconnectSockets(false)
	}
}

func isSocketAuthenticated(client *socket.Socket) bool {
	allowed := false
	auth.RequireAuthSocketIO(client, func(err *socket.ExtendedError) {
		allowed = err == nil
	})
	return allowed
}

func (b *socketBridge) handleAny(nsName string, client *socket.Socket, args []any) {
	event, data, ok := socketEventFromAnyArgs(args)
	if !ok {
		b.log.Debug("ignore malformed socket event", "namespace", nsName)
		return
	}

	owner, ok := b.ownerForNamespace(nsName)
	if !ok {
		return
	}
	if !owner.handlesEvent(event) {
		b.log.Debug("ignore undeclared socket event", "namespace", nsName, "event", event, "plugin", owner.pluginName)
		return
	}
	if owner.protectsEvent(event) && !isSocketAuthenticated(client) {
		_ = client.Emit("error", map[string]any{
			"code":    "UNAUTHORIZED",
			"message": "authentication required",
			"event":   event,
		})
		return
	}

	b.handle(owner, nsName, event, client, data)
}

func socketEventFromAnyArgs(args []any) (string, []any, bool) {
	if len(args) == 0 {
		return "", nil, false
	}
	event, ok := args[0].(string)
	if !ok || event == "" {
		return "", nil, false
	}
	return event, args[1:], true
}

func (b *socketBridge) ownerForNamespace(ns string) (socketOwner, bool) {
	b.mu.RLock()
	owner := b.owners[ns]
	b.mu.RUnlock()
	return owner, owner.pluginName != "" && owner.plugin != nil && owner.plugin.conn != nil
}

func (owner socketOwner) handlesEvent(event string) bool {
	_, ok := owner.events[event]
	return ok
}

func (owner socketOwner) protectsEvent(event string) bool {
	_, ok := owner.protectedEvents[event]
	return ok
}

func (b *socketBridge) handle(owner socketOwner, ns, event string, client *socket.Socket, data []any) {
	payload, err := json.Marshal(data)
	if err != nil {
		b.log.Error("marshal socket event payload", "namespace", ns, "event", event, "err", err)
		return
	}

	ctx, cancel := owner.plugin.eventContext()
	defer cancel()
	emits, err := owner.plugin.conn.HandleSocketEvent(ctx, &SocketEvent{
		Namespace: ns,
		Event:     event,
		SocketID:  string(client.Id()),
		Payload:   payload,
	})
	if err != nil {
		b.log.Error("plugin socket handler failed", "namespace", ns, "event", event, "err", err)
		return
	}
	for _, e := range emits {
		if err := b.Emit(e); err != nil {
			b.log.Error("apply plugin emit", "namespace", e.Namespace, "event", e.Event, "err", err)
		}
	}
}

// Emit implements the Emitter interface used by HostAPI.
func (b *socketBridge) Emit(instr EmitInstruction) error {
	ns := b.server.GetNamespace(instr.Namespace)
	if ns.Raw() == nil {
		return fmt.Errorf("unknown socket namespace %q", instr.Namespace)
	}

	args, err := decodeEmitArgs(instr.Payload)
	if err != nil {
		return fmt.Errorf("decode emit payload: %w", err)
	}

	if instr.Target != "" {
		return ns.EmitTo(instr.Target, instr.Event, args...)
	}
	return ns.Emit(instr.Event, args...)
}

// decodeEmitArgs interprets the payload as a JSON array of emit arguments. An
// empty payload yields no arguments.
func decodeEmitArgs(payload []byte) ([]any, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	var args []any
	if err := json.Unmarshal(payload, &args); err != nil {
		return nil, err
	}
	return args, nil
}
