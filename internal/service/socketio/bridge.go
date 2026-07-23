// Package socketio owns the Socket.IO data-plane integration for services.
package socketio

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"arupa/internal/auth"
	"arupa/internal/netx"
	"arupa/internal/service/spec"

	"github.com/zishang520/socket.io/servers/socket/v3"
)

// Bridge wires service-declared namespaces and events into the global
// Socket.IO server.
type Bridge struct {
	server *netx.Socket
	log    *slog.Logger

	mu         sync.RWMutex
	owners     map[string]owner
	registered map[string]struct{}
}

type owner struct {
	serviceName string
	endpoint    spec.Endpoint
	routeID     string
	decl        spec.SocketIORoute
	events      map[string]struct{}
}

func New(server *netx.Socket, log *slog.Logger) *Bridge {
	if log == nil {
		log = slog.Default()
	}
	return &Bridge{
		server:     server,
		log:        log.With("component", "kernel", "from", "socketio"),
		owners:     make(map[string]owner),
		registered: make(map[string]struct{}),
	}
}

// Register attaches a namespace to its authenticated service endpoint.
func (b *Bridge) Register(serviceName, routeID string, decl spec.SocketIORoute, endpoint spec.Endpoint) error {
	if decl.Namespace == "" {
		return fmt.Errorf("socket namespace name is required")
	}
	if endpoint == nil || endpoint.Connection() == nil {
		return fmt.Errorf("socket namespace %q requires a service connection", decl.Namespace)
	}

	b.server.AddNamespace(decl.Namespace)
	ns := b.server.GetNamespace(decl.Namespace)
	if ns.Raw() == nil {
		return fmt.Errorf("failed to create socket namespace %q", decl.Namespace)
	}

	b.mu.Lock()
	if current, exists := b.owners[decl.Namespace]; exists && current.serviceName != "" {
		b.mu.Unlock()
		return fmt.Errorf("socket namespace %q already owned by service %q", decl.Namespace, current.serviceName)
	}
	if _, installed := b.registered[decl.Namespace]; !installed {
		b.installNamespaceHandlersLocked(decl.Namespace, ns)
		b.registered[decl.Namespace] = struct{}{}
	}
	b.owners[decl.Namespace] = newOwner(serviceName, routeID, decl, endpoint)
	b.mu.Unlock()
	return nil
}

func newOwner(serviceName, routeID string, decl spec.SocketIORoute, endpoint spec.Endpoint) owner {
	events := make(map[string]struct{}, len(decl.Events))
	for _, event := range decl.Events {
		events[event] = struct{}{}
	}
	return owner{
		serviceName: serviceName,
		endpoint:    endpoint,
		routeID:     routeID,
		decl:        cloneRoute(decl),
		events:      events,
	}
}

func cloneRoute(decl spec.SocketIORoute) spec.SocketIORoute {
	decl.Events = append([]string(nil), decl.Events...)
	decl.Access.Groups = append([]string(nil), decl.Access.Groups...)
	if len(decl.EventAccess) > 0 {
		eventAccess := make(map[string]spec.AccessPolicy, len(decl.EventAccess))
		for event, policy := range decl.EventAccess {
			policy.Groups = append([]string(nil), policy.Groups...)
			eventAccess[event] = policy
		}
		decl.EventAccess = eventAccess
	}
	return decl
}

func (b *Bridge) Unregister(serviceName, namespace string) {
	b.mu.Lock()
	current := b.owners[namespace]
	if current.serviceName == serviceName {
		b.owners[namespace] = owner{}
	}
	b.mu.Unlock()
	if current.serviceName == serviceName {
		b.server.GetNamespace(namespace).DisconnectSockets(false)
	}
}

// RemoveOwner releases every namespace owned by a stopping service.
func (b *Bridge) RemoveOwner(serviceName string) {
	b.mu.Lock()
	var released []string
	for namespace, current := range b.owners {
		if current.serviceName == serviceName {
			b.owners[namespace] = owner{}
			released = append(released, namespace)
		}
	}
	b.mu.Unlock()

	for _, namespace := range released {
		b.server.GetNamespace(namespace).DisconnectSockets(false)
	}
}

func (b *Bridge) installNamespaceHandlersLocked(namespace string, ns netx.Namespace) {
	ns.AddMiddleware(func(client *socket.Socket, next func(*socket.ExtendedError)) {
		if err := b.authorizeNamespace(namespace, client); err != nil {
			next(err)
			return
		}
		next(nil)
	})
	ns.OnConnection(func(client *socket.Socket) {
		b.log.Info("socket connected", "namespace", namespace, "socket_id", string(client.Id()))

		// Bind disconnect to the service that accepted this socket.
		// Namespace ownership may be cleared or replaced before disconnect runs.
		current, hasOwner := b.ownerForNamespace(namespace)
		user := auth.UserFromSocket(client)
		client.On("disconnect", func(args ...any) {
			b.log.Info("socket disconnected", "namespace", namespace, "socket_id", string(client.Id()))
			if hasOwner {
				b.handleDisconnect(current, namespace, string(client.Id()), user, args)
			}
		})
		client.OnAny(func(args ...any) {
			b.handleAny(namespace, client, args)
		})
	})
}

func (b *Bridge) authorizeNamespace(namespace string, client *socket.Socket) *socket.ExtendedError {
	current, ok := b.ownerForNamespace(namespace)
	if !ok {
		b.log.Warn("socket namespace unavailable", "namespace", namespace)
		return socket.NewExtendedError("Unavailable", "namespace is not owned by a running service")
	}
	user := auth.UserFromSocket(client)
	if err := accessError(current.endpoint.AccessPolicy().Check(user)); err != nil {
		b.log.Warn("socket namespace access denied", "namespace", namespace, "service", current.serviceName)
		return err
	}
	if err := accessError(current.decl.Access.Check(user)); err != nil {
		b.log.Warn("socket namespace access denied", "namespace", namespace, "service", current.serviceName)
		return err
	}
	return nil
}

func (b *Bridge) handleAny(namespace string, client *socket.Socket, args []any) {
	event, data, ok := eventFromAnyArgs(args)
	if !ok {
		b.log.Debug("ignore malformed socket event", "namespace", namespace)
		return
	}

	current, ok := b.ownerForNamespace(namespace)
	if !ok {
		return
	}
	if !current.handlesEvent(event) {
		b.log.Debug("ignore undeclared socket event", "namespace", namespace, "event", event, "service", current.serviceName)
		return
	}
	user := auth.UserFromSocket(client)
	if decision := current.endpoint.AccessPolicy().Check(user); decision != auth.AccessAllowed {
		b.emitAccessError(client, event, decision)
		return
	}
	if decision := current.decl.Access.Check(user); decision != auth.AccessAllowed {
		b.emitAccessError(client, event, decision)
		return
	}
	if policy, protected := current.eventAccess(event); protected {
		if decision := policy.Check(user); decision != auth.AccessAllowed {
			b.emitAccessError(client, event, decision)
			return
		}
	}
	b.handle(current, namespace, event, string(client.Id()), user, data)
}

func (b *Bridge) handleDisconnect(current owner, namespace, socketID string, user spec.User, data []any) {
	const event = "disconnect"
	if !current.handlesEvent(event) {
		b.log.Debug("ignore undeclared socket event", "namespace", namespace, "event", event, "service", current.serviceName)
		return
	}
	b.handle(current, namespace, event, socketID, user, data)
}

func (b *Bridge) emitAccessError(client *socket.Socket, event string, decision auth.AccessDecision) {
	code := "FORBIDDEN"
	message := "access forbidden"
	if decision == auth.AccessAuthenticationRequired {
		code = "UNAUTHORIZED"
		message = "authentication required"
	}
	if err := client.Emit("error", map[string]any{
		"code": code, "message": message, "event": event,
	}); err != nil {
		b.log.Debug("failed to emit socket access error", "event", event, "err", err)
	}
}

func accessError(decision auth.AccessDecision) *socket.ExtendedError {
	switch decision {
	case auth.AccessAuthenticationRequired:
		return socket.NewExtendedError("Unauthorized", "authentication required")
	case auth.AccessForbidden:
		return socket.NewExtendedError("Forbidden", "access forbidden")
	default:
		return nil
	}
}

func (current owner) eventAccess(event string) (spec.AccessPolicy, bool) {
	policy, ok := current.decl.EventAccess[event]
	return policy, ok
}

func (current owner) handlesEvent(event string) bool {
	_, ok := current.events[event]
	return ok
}

func (b *Bridge) handle(current owner, namespace, event, socketID string, user spec.User, data []any) {
	start := time.Now()
	payload, err := json.Marshal(data)
	if err != nil {
		b.log.Error("marshal socket event payload", "namespace", namespace, "event", event, "err", err)
		return
	}

	ctx, cancel := current.endpoint.EventContext()
	defer cancel()
	emits, err := current.endpoint.Connection().HandleSocketEvent(ctx, &spec.SocketEvent{
		RouteID: current.routeID, Namespace: namespace, Event: event,
		SocketID: socketID, User: userOrNil(user), Payload: payload,
	})
	if err != nil {
		b.log.Error("service socket handler failed", "namespace", namespace, "event", event, "service", current.serviceName, "err", err)
		return
	}
	b.log.Debug("socket event handled", "namespace", namespace, "event", event, "service", current.serviceName, "duration_ms", time.Since(start).Milliseconds())
	for _, instruction := range emits {
		if err := b.Emit(instruction); err != nil {
			b.log.Error("apply service emit", "namespace", instruction.Namespace, "event", instruction.Event, "err", err)
		}
	}
}

func eventFromAnyArgs(args []any) (string, []any, bool) {
	if len(args) == 0 {
		return "", nil, false
	}
	event, ok := args[0].(string)
	if !ok || event == "" {
		return "", nil, false
	}
	return event, args[1:], true
}

func (b *Bridge) ownerForNamespace(namespace string) (owner, bool) {
	b.mu.RLock()
	current := b.owners[namespace]
	b.mu.RUnlock()
	return current, current.serviceName != "" && current.endpoint != nil && current.endpoint.Connection() != nil
}

func (b *Bridge) Emit(instruction spec.EmitInstruction) error {
	ns := b.server.GetNamespace(instruction.Namespace)
	if ns.Raw() == nil {
		return fmt.Errorf("unknown socket namespace %q", instruction.Namespace)
	}
	args, err := decodeEmitArgs(instruction.Payload)
	if err != nil {
		return fmt.Errorf("decode emit payload: %w", err)
	}
	if instruction.Target != "" {
		return ns.EmitTo(instruction.Target, instruction.Event, args...)
	}
	return ns.Emit(instruction.Event, args...)
}

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

func userOrNil(user spec.User) *spec.User {
	if !user.Authenticated {
		return nil
	}
	return &user
}
