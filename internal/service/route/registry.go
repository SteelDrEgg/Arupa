// Package route owns route validation, conflict policy, matching, and request
// dispatch. It resolves transports but does not create or unregister them.
package route

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"arupa/internal/auth"
	"arupa/internal/conf"
	"arupa/internal/netx"
	"arupa/internal/service/spec"
	"arupa/internal/service/transport"
)

const MaxRequestBody = 8 << 20

type TransportResolver interface {
	Lookup(owner, id string) (*transport.Binding, bool)
}

type SocketRegistry interface {
	Register(owner, routeID string, declaration spec.SocketIORoute, endpoint spec.Endpoint) error
	Unregister(owner, namespace string)
	RemoveOwner(owner string)
}

type routeKey struct {
	owner string
	id    string
}

type httpRouteKey struct {
	pattern string
	method  string
}

type binding struct {
	owner     string
	route     spec.Route
	transport *transport.Binding
}

// Registry is the route table used by both the control plane and HTTP data
// plane.
type Registry struct {
	mu         sync.RWMutex
	byID       map[routeKey]*binding
	http       map[httpRouteKey]*binding
	reserved   map[string]string
	transports TransportResolver
	socket     SocketRegistry
}

func NewRegistry(transports TransportResolver, socket SocketRegistry) *Registry {
	return &Registry{
		byID:       make(map[routeKey]*binding),
		http:       make(map[httpRouteKey]*binding),
		reserved:   make(map[string]string),
		transports: transports,
		socket:     socket,
	}
}

func (r *Registry) Reserve(owner string, patterns ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, pattern := range patterns {
		if err := netx.ValidatePathPattern(pattern); err != nil {
			return fmt.Errorf("invalid reserved route: %w", err)
		}
		if current, ok := r.reserved[pattern]; ok && current != owner {
			return fmt.Errorf("path %q is already reserved by %q", pattern, current)
		}
		for key, current := range r.http {
			if key.pattern == pattern {
				return fmt.Errorf("path %q is already owned by service %q", pattern, current.owner)
			}
		}
	}
	for _, pattern := range patterns {
		r.reserved[pattern] = owner
	}
	return nil
}

func (r *Registry) Register(owner string, declaration spec.Route) error {
	owner = strings.TrimSpace(owner)
	declaration.ID = strings.TrimSpace(declaration.ID)
	declaration.TransportID = strings.TrimSpace(declaration.TransportID)
	if owner == "" {
		return fmt.Errorf("route owner is required")
	}
	if declaration.ID == "" {
		return fmt.Errorf("route id is required")
	}
	if declaration.TransportID == "" {
		return fmt.Errorf("route %q transport is required", declaration.ID)
	}
	if (declaration.HTTP == nil) == (declaration.SocketIO == nil) {
		return fmt.Errorf("route %q must contain exactly one route kind", declaration.ID)
	}
	resolved, ok := r.transports.Lookup(owner, declaration.TransportID)
	if !ok {
		return fmt.Errorf("transport %q is not registered by service %q", declaration.TransportID, owner)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	k := routeKey{owner: owner, id: declaration.ID}
	if _, exists := r.byID[k]; exists {
		return fmt.Errorf("route id %q is already registered", declaration.ID)
	}

	prepared := &binding{owner: owner, route: declaration, transport: resolved}
	if declaration.HTTP != nil {
		if err := r.registerHTTPLocked(prepared); err != nil {
			return err
		}
	} else {
		if resolved.Spec().Type != spec.TransportSocketIO {
			return fmt.Errorf("socket.io route %q requires a socket.io transport", declaration.ID)
		}
		if r.socket == nil {
			return fmt.Errorf("socket.io registry is unavailable")
		}
		if err := r.socket.Register(owner, declaration.ID, *declaration.SocketIO, resolved.Endpoint()); err != nil {
			return err
		}
	}
	r.byID[k] = prepared
	return nil
}

func (r *Registry) registerHTTPLocked(prepared *binding) error {
	declaration := *prepared.route.HTTP
	if err := netx.ValidatePathPattern(declaration.Pattern); err != nil {
		return fmt.Errorf("invalid http route pattern: %w", err)
	}
	switch prepared.transport.Spec().Type {
	case spec.TransportHTTP, spec.TransportStatic, spec.TransportProxy:
	default:
		return fmt.Errorf("http route %q cannot use %s transport", prepared.route.ID, prepared.transport.Spec().Type)
	}
	declaration.Method = normalizeMethod(declaration.Method)
	prepared.route.HTTP = &declaration
	if owner, reserved := r.reserved[declaration.Pattern]; reserved {
		return fmt.Errorf("path %q is reserved by %q", declaration.Pattern, owner)
	}
	if prepared.transport.Spec().Type == spec.TransportStatic {
		if declaration.Method != "" {
			return fmt.Errorf("static route %q must use the wildcard method", prepared.route.ID)
		}
		if prepared.transport.StaticDirectory() && !strings.HasSuffix(declaration.Pattern, "/") {
			return fmt.Errorf("static directory route %q must end with '/'", prepared.route.ID)
		}
		if !prepared.transport.StaticDirectory() && strings.HasSuffix(declaration.Pattern, "/") {
			return fmt.Errorf("static file route %q must be exact", prepared.route.ID)
		}
	}

	for key, current := range r.http {
		if key.pattern != declaration.Pattern || current == nil {
			continue
		}
		if current.owner != prepared.owner {
			return fmt.Errorf("path %q is already owned by service %q", declaration.Pattern, current.owner)
		}
		if current.transport.Spec().Type == spec.TransportStatic || prepared.transport.Spec().Type == spec.TransportStatic {
			return fmt.Errorf("static and non-static routes conflict at path %q", declaration.Pattern)
		}
		if methodsConflict(key.method, declaration.Method) {
			return fmt.Errorf("route %s %q conflicts with route %q", formatMethod(declaration.Method), declaration.Pattern, current.route.ID)
		}
	}
	r.http[httpRouteKey{pattern: declaration.Pattern, method: declaration.Method}] = prepared
	return nil
}

func (r *Registry) Unregister(owner, id string) error {
	k := routeKey{owner: owner, id: strings.TrimSpace(id)}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.byID[k]
	if !ok {
		return fmt.Errorf("route %q is not registered by service %q", id, owner)
	}
	r.removeLocked(k, current)
	return nil
}

func (r *Registry) RemoveOwner(owner string) {
	r.mu.Lock()
	for k, current := range r.byID {
		if current.owner == owner {
			r.removeLocked(k, current)
		}
	}
	r.mu.Unlock()
	if r.socket != nil {
		r.socket.RemoveOwner(owner)
	}
}

func (r *Registry) removeLocked(k routeKey, current *binding) {
	delete(r.byID, k)
	if current.route.HTTP != nil {
		delete(r.http, httpRouteKey{
			pattern: current.route.HTTP.Pattern,
			method:  normalizeMethod(current.route.HTTP.Method),
		})
		return
	}
	if current.route.SocketIO != nil && r.socket != nil {
		r.socket.Unregister(current.owner, current.route.SocketIO.Namespace)
	}
}

func (r *Registry) UsesTransport(owner, id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, current := range r.byID {
		if current.owner == owner && current.route.TransportID == id {
			return true
		}
	}
	return false
}

func (r *Registry) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	current, matchedPattern, allowed := r.matchHTTP(request.Method, request.URL.Path)
	if current == nil {
		if matchedPattern {
			writeMethodNotAllowed(w, allowed)
			return
		}
		if page, ok := conf.GetPagePath(http.StatusNotFound); ok &&
			netx.WantsHTMLPage(request) && !netx.RequestPathMatches(request, page) {
			http.Redirect(w, request, page, http.StatusSeeOther)
			return
		}
		_ = netx.WriteNotFound(w)
		return
	}
	r.serveBinding(current, w, request)
}

func (r *Registry) matchHTTP(method, path string) (*binding, bool, []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	best := ""
	for key, current := range r.http {
		rootMode := netx.RootPathExact
		if current.transport.Spec().Type == spec.TransportStatic && current.transport.StaticDirectory() {
			rootMode = netx.RootPathSubtree
		}
		if netx.MatchPathPattern(path, key.pattern, rootMode) && len(key.pattern) > len(best) {
			best = key.pattern
		}
	}
	if best == "" {
		return nil, false, nil
	}
	method = normalizeMethod(method)
	allowedSet := make(map[string]struct{})
	var wildcard *binding
	for key, current := range r.http {
		if key.pattern != best {
			continue
		}
		if key.method == method {
			return current, true, nil
		}
		if key.method == "" {
			wildcard = current
		} else {
			allowedSet[key.method] = struct{}{}
		}
	}
	if wildcard != nil {
		return wildcard, true, nil
	}
	return nil, true, sortedMethods(allowedSet)
}

func (r *Registry) serveBinding(current *binding, w http.ResponseWriter, request *http.Request) {
	user := auth.UserFromRequest(request)
	endpoint := current.transport.Endpoint()
	if endpoint != nil && writeAccessError(w, request, endpoint.AccessPolicy().Check(user)) {
		return
	}
	if writeAccessError(w, request, current.route.HTTP.Access.Check(user)) {
		return
	}

	switch current.transport.Spec().Type {
	case spec.TransportHTTP:
		serveRPC(current, w, request, user)
	case spec.TransportStatic:
		serveStatic(current, w, request)
	case spec.TransportProxy:
		current.transport.Handler().ServeHTTP(w, request)
	default:
		_ = netx.WriteError(w, http.StatusBadGateway, "invalid route transport", nil)
	}
}

func serveStatic(current *binding, w http.ResponseWriter, request *http.Request) {
	path := current.transport.StaticPath()
	if current.transport.StaticDirectory() {
		prefix := strings.TrimSuffix(current.route.HTTP.Pattern, "/")
		http.StripPrefix(prefix, http.FileServer(http.Dir(path))).ServeHTTP(w, request)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, request)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, request)
		return
	}
	http.ServeContent(w, request, filepath.Base(path), info.ModTime(), file)
}

func serveRPC(current *binding, w http.ResponseWriter, request *http.Request, user spec.User) {
	body, err := io.ReadAll(io.LimitReader(request.Body, MaxRequestBody+1))
	if err != nil {
		_ = netx.WriteBadRequest(w, "failed to read request body")
		return
	}
	if len(body) > MaxRequestBody {
		_ = netx.WritePayloadTooLarge(w, "request body too large")
		return
	}
	headers := request.Header.Clone()
	transport.InjectVerifiedIdentity(headers, user)
	endpoint := current.transport.Endpoint()
	ctx, cancel := endpoint.CallContext(request.Context())
	defer cancel()
	response, err := endpoint.Connection().HandleHTTP(ctx, &spec.HTTPRequest{
		RouteID: current.route.ID, RoutePattern: current.route.HTTP.Pattern,
		Method: request.Method, Path: request.URL.Path, Query: request.URL.RawQuery,
		Headers: headers, Body: body, RemoteAddr: request.RemoteAddr,
		User: userOrNil(user),
	})
	if err != nil {
		_ = netx.WriteError(w, http.StatusBadGateway, "service handler failed", err)
		return
	}
	for name, values := range response.Headers {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	status := response.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(response.Body)
}

func userOrNil(user spec.User) *spec.User {
	if !user.Authenticated {
		return nil
	}
	return &user
}

func writeAccessError(w http.ResponseWriter, request *http.Request, decision auth.AccessDecision) bool {
	if decision == auth.AccessAuthenticationRequired {
		if page, ok := conf.GetPagePath(http.StatusUnauthorized); ok &&
			netx.WantsHTMLPage(request) && !netx.RequestPathMatches(request, page) {
			http.Redirect(w, request, page, http.StatusSeeOther)
			return true
		}
	}
	return auth.WriteAccessError(w, decision)
}

func normalizeMethod(method string) string {
	return strings.ToUpper(strings.TrimSpace(method))
}

func methodsConflict(a, b string) bool {
	return a == b || a == "" || b == ""
}

func formatMethod(method string) string {
	if method == "" {
		return "ANY"
	}
	return method
}

func sortedMethods(methods map[string]struct{}) []string {
	out := make([]string, 0, len(methods))
	for method := range methods {
		out = append(out, method)
	}
	sort.Strings(out)
	return out
}

func writeMethodNotAllowed(w http.ResponseWriter, allowed []string) {
	if len(allowed) > 0 {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
	}
	_ = netx.WriteMethodNotAllowed(w)
}
