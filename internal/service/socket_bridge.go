package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"arupa/internal/auth"
	"arupa/internal/netx"
	"github.com/zishang520/socket.io/servers/socket/v3"
)

// socketBridge wires service-declared Socket.IO namespaces and events into the
// global Socket.IO server. Incoming events are forwarded to the owning service;
// emits requested by services (either in-reply for WASM or via the Host.Emit
// broker Host API for gRPC) are sent out through this bridge.
type socketBridge struct {
	server *netx.Socket
	log    *slog.Logger

	mu         sync.RWMutex
	owners     map[string]socketOwner // namespace -> current service binding
	registered map[string]struct{}    // namespace -> dynamic handlers installed
}

type socketOwner struct {
	serviceName string
	service     *loadedService
	routeID     string
	decl        SocketIORoute
	events      map[string]struct{}
}

func newSocketBridge(server *netx.Socket, log *slog.Logger) *socketBridge {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "kernel", "from", "socketio")
	return &socketBridge{
		server:     server,
		log:        log,
		owners:     make(map[string]socketOwner),
		registered: make(map[string]struct{}),
	}
}

// register attaches a service's namespace and its event handlers.
//
// Socket.IO namespaces are kept as stable shells. The namespace middleware and
// connection handler are installed once; each connection and event dispatch
// resolves the current owner and event declaration from b.owners. Stop clears
// the owner and disconnects sockets from that namespace; Restart replaces it.
func (b *socketBridge) register(serviceName, routeID string, decl SocketIORoute, lp *loadedService) error {
	if decl.Namespace == "" {
		return fmt.Errorf("socket namespace name is required")
	}
	if lp == nil || lp.conn == nil {
		return fmt.Errorf("socket namespace %q requires a service connection", decl.Namespace)
	}

	b.server.AddNamespace(decl.Namespace)
	ns := b.server.GetNamespace(decl.Namespace)
	if ns.Raw() == nil {
		return fmt.Errorf("failed to create socket namespace %q", decl.Namespace)
	}

	b.mu.Lock()
	if owner, exists := b.owners[decl.Namespace]; exists && owner.serviceName != "" {
		b.mu.Unlock()
		return fmt.Errorf("socket namespace %q already owned by service %q", decl.Namespace, owner.serviceName)
	}
	_, alreadyRegistered := b.registered[decl.Namespace]
	if !alreadyRegistered {
		b.installNamespaceHandlersLocked(decl.Namespace, ns)
		b.registered[decl.Namespace] = struct{}{}
	}
	b.owners[decl.Namespace] = newSocketOwner(serviceName, routeID, decl, lp)
	b.mu.Unlock()
	return nil
}

func newSocketOwner(serviceName, routeID string, decl SocketIORoute, lp *loadedService) socketOwner {
	events := make(map[string]struct{}, len(decl.Events))
	for _, event := range decl.Events {
		events[event] = struct{}{}
	}

	return socketOwner{
		serviceName: serviceName,
		service:     lp,
		routeID:     routeID,
		decl:        cloneSocketIORoute(decl),
		events:      events,
	}
}

func cloneSocketIORoute(decl SocketIORoute) SocketIORoute {
	decl.Events = append([]string(nil), decl.Events...)
	if len(decl.Access.Groups) > 0 {
		decl.Access.Groups = append([]string(nil), decl.Access.Groups...)
	}
	if len(decl.EventAccess) > 0 {
		eventAccess := make(map[string]AccessPolicy, len(decl.EventAccess))
		for event, policy := range decl.EventAccess {
			policy.Groups = append([]string(nil), policy.Groups...)
			eventAccess[event] = policy
		}
		decl.EventAccess = eventAccess
	}
	return decl
}

func (b *socketBridge) unregister(serviceName, namespace string) {
	b.mu.Lock()
	owner := b.owners[namespace]
	if owner.serviceName == serviceName {
		b.owners[namespace] = socketOwner{}
	}
	b.mu.Unlock()
	if owner.serviceName == serviceName {
		b.server.GetNamespace(namespace).DisconnectSockets(false)
	}
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
		b.log.Info("socket connected", "namespace", nsName, "socket_id", string(client.Id()))
		client.On("disconnect", func(_ ...any) {
			b.log.Info("socket disconnected", "namespace", nsName, "socket_id", string(client.Id()))
		})
		client.OnAny(func(args ...any) {
			b.handleAny(nsName, client, args)
		})
	})
}

func (b *socketBridge) authorizeNamespace(nsName string, client *socket.Socket) *socket.ExtendedError {
	owner, ok := b.ownerForNamespace(nsName)
	if !ok {
		b.log.Warn("socket namespace unavailable", "namespace", nsName)
		return socket.NewExtendedError("Unavailable", "namespace is not owned by a running service")
	}
	user := auth.UserFromSocket(client)
	if err := socketAccessError(owner.service.accessPolicy().Check(user)); err != nil {
		b.log.Warn("socket namespace access denied", "namespace", nsName, "service", owner.serviceName)
		return err
	}
	if err := socketAccessError(owner.decl.Access.Check(user)); err != nil {
		b.log.Warn("socket namespace access denied", "namespace", nsName, "service", owner.serviceName)
		return err
	}
	return nil
}

// unregisterService releases all namespace ownership held by serviceName. The
// underlying Socket.IO namespace and dynamic handlers remain installed, but
// future connections are rejected until a service registers the namespace again.
// Existing sockets are disconnected from the released namespace.
func (b *socketBridge) unregisterService(serviceName string) {
	b.mu.Lock()
	var released []string
	for ns, owner := range b.owners {
		if owner.serviceName == serviceName {
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
		b.log.Debug("ignore undeclared socket event", "namespace", nsName, "event", event, "service", owner.serviceName)
		return
	}
	user := auth.UserFromSocket(client)
	if decision := owner.service.accessPolicy().Check(user); decision != auth.AccessAllowed {
		b.emitAccessError(client, event, decision)
		return
	}
	if decision := owner.decl.Access.Check(user); decision != auth.AccessAllowed {
		b.emitAccessError(client, event, decision)
		return
	}
	if policy, protected := owner.eventAccess(event); protected {
		if decision := policy.Check(user); decision != auth.AccessAllowed {
			b.emitAccessError(client, event, decision)
			return
		}
	}

	b.handle(owner, nsName, event, client, user, data)
}

func (b *socketBridge) emitAccessError(client *socket.Socket, event string, decision auth.AccessDecision) {
	code := "FORBIDDEN"
	message := "access forbidden"
	if decision == auth.AccessAuthenticationRequired {
		code = "UNAUTHORIZED"
		message = "authentication required"
	}
	if err := client.Emit("error", map[string]any{
		"code":    code,
		"message": message,
		"event":   event,
	}); err != nil {
		b.log.Debug("failed to emit socket access error", "event", event, "err", err)
	}
}

func socketAccessError(decision auth.AccessDecision) *socket.ExtendedError {
	switch decision {
	case auth.AccessAuthenticationRequired:
		return socket.NewExtendedError("Unauthorized", "authentication required")
	case auth.AccessForbidden:
		return socket.NewExtendedError("Forbidden", "access forbidden")
	default:
		return nil
	}
}

func (owner socketOwner) eventAccess(event string) (AccessPolicy, bool) {
	policy, ok := owner.decl.EventAccess[event]
	return policy, ok
}

func (owner socketOwner) handlesEvent(event string) bool {
	_, ok := owner.events[event]
	return ok
}

func (b *socketBridge) handle(owner socketOwner, ns, event string, client *socket.Socket, user User, data []any) {
	start := time.Now()
	payload, err := json.Marshal(data)
	if err != nil {
		b.log.Error("marshal socket event payload", "namespace", ns, "event", event, "err", err)
		return
	}

	ctx, cancel := owner.service.eventContext()
	defer cancel()
	emits, err := owner.service.conn.HandleSocketEvent(ctx, &SocketEvent{
		RouteID:   owner.routeID,
		Namespace: ns,
		Event:     event,
		SocketID:  string(client.Id()),
		User:      userOrNil(user),
		Payload:   payload,
	})
	if err != nil {
		b.log.Error("service socket handler failed", "namespace", ns, "event", event, "service", owner.serviceName, "err", err)
		return
	}
	b.log.Debug("socket event handled", "namespace", ns, "event", event, "service", owner.serviceName, "duration_ms", time.Since(start).Milliseconds())
	for _, e := range emits {
		if err := b.Emit(e); err != nil {
			b.log.Error("apply service emit", "namespace", e.Namespace, "event", e.Event, "err", err)
		}
	}
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
	return owner, owner.serviceName != "" && owner.service != nil && owner.service.conn != nil
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
