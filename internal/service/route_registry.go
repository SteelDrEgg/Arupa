package service

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
)

const maxRequestBody = 8 << 20

type routeKey struct {
	owner string
	id    string
}

type httpRouteKey struct {
	pattern string
	method  string
}

type routeBinding struct {
	owner     string
	route     Route
	transport *transportBinding
}

type routeRegistry struct {
	mu         sync.RWMutex
	byID       map[routeKey]*routeBinding
	http       map[httpRouteKey]*routeBinding
	reserved   map[string]string
	transports *transportRegistry
	socket     *socketBridge
}

func newRouteRegistry(transports *transportRegistry, socket *socketBridge) *routeRegistry {
	routes := &routeRegistry{
		byID: make(map[routeKey]*routeBinding), http: make(map[httpRouteKey]*routeBinding),
		reserved:   make(map[string]string),
		transports: transports, socket: socket,
	}
	transports.routes = routes
	return routes
}

func (r *routeRegistry) reserve(owner string, patterns ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, pattern := range patterns {
		if err := netx.ValidatePathPattern(pattern); err != nil {
			return fmt.Errorf("invalid reserved route: %w", err)
		}
		if existing, ok := r.reserved[pattern]; ok && existing != owner {
			return fmt.Errorf("path %q is already reserved by %q", pattern, existing)
		}
		for key, binding := range r.http {
			if key.pattern == pattern {
				return fmt.Errorf("path %q is already owned by service %q", pattern, binding.owner)
			}
		}
	}
	for _, pattern := range patterns {
		r.reserved[pattern] = owner
	}
	return nil
}

func (r *routeRegistry) RegisterRoutes(owner string, routes []Route) RegistrationResult {
	result := RegistrationResult{}
	for _, route := range routes {
		if err := r.register(owner, route); err != nil {
			result.Failures = append(result.Failures, RegistrationFailure{ID: route.ID, Error: err.Error()})
			result.Degraded = true
			continue
		}
		result.Registered = append(result.Registered, route.ID)
	}
	if result.Degraded {
		r.transports.markDegraded(owner)
	}
	return result
}

func (r *routeRegistry) UnregisterRoutes(owner string, ids []string) RegistrationResult {
	result := RegistrationResult{}
	for _, id := range ids {
		if err := r.unregister(owner, id); err != nil {
			result.Failures = append(result.Failures, RegistrationFailure{ID: id, Error: err.Error()})
			continue
		}
		result.Registered = append(result.Registered, id)
	}
	return result
}

func (r *routeRegistry) register(owner string, route Route) error {
	owner = strings.TrimSpace(owner)
	route.ID = strings.TrimSpace(route.ID)
	route.TransportID = strings.TrimSpace(route.TransportID)
	if owner == "" {
		return fmt.Errorf("route owner is required")
	}
	if route.ID == "" {
		return fmt.Errorf("route id is required")
	}
	if route.TransportID == "" {
		return fmt.Errorf("route %q transport is required", route.ID)
	}
	if (route.HTTP == nil) == (route.SocketIO == nil) {
		return fmt.Errorf("route %q must contain exactly one route kind", route.ID)
	}

	transport, ok := r.transports.lookup(owner, route.TransportID)
	if !ok {
		return fmt.Errorf("transport %q is not registered by service %q", route.TransportID, owner)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	transport, ok = r.transports.lookup(owner, route.TransportID)
	if !ok {
		return fmt.Errorf("transport %q is no longer registered by service %q", route.TransportID, owner)
	}
	key := routeKey{owner: owner, id: route.ID}
	if _, exists := r.byID[key]; exists {
		return fmt.Errorf("route id %q is already registered", route.ID)
	}

	binding := &routeBinding{owner: owner, route: route, transport: transport}
	if route.HTTP != nil {
		if err := r.registerHTTPLocked(binding); err != nil {
			return err
		}
	} else {
		if transport.spec.Type != TransportSocketIO {
			return fmt.Errorf("socket.io route %q requires a socket.io transport", route.ID)
		}
		if r.socket == nil {
			return fmt.Errorf("socket.io registry is unavailable")
		}
		decl := *route.SocketIO
		if err := r.socket.register(owner, route.ID, decl, transport.service); err != nil {
			return err
		}
	}
	r.byID[key] = binding
	binding.transport.service.addRoute(route)
	r.transports.publish(binding.transport.service)
	return nil
}

func (r *routeRegistry) registerHTTPLocked(binding *routeBinding) error {
	route := *binding.route.HTTP
	if err := netx.ValidatePathPattern(route.Pattern); err != nil {
		return fmt.Errorf("invalid http route pattern: %w", err)
	}
	switch binding.transport.spec.Type {
	case TransportHTTP, TransportStatic, TransportProxy:
	default:
		return fmt.Errorf("http route %q cannot use %s transport", binding.route.ID, binding.transport.spec.Type)
	}
	route.Method = normalizeRouteMethod(route.Method)
	binding.route.HTTP = &route
	if owner, reserved := r.reserved[route.Pattern]; reserved {
		return fmt.Errorf("path %q is reserved by %q", route.Pattern, owner)
	}
	if binding.transport.spec.Type == TransportStatic {
		if route.Method != "" {
			return fmt.Errorf("static route %q must use the wildcard method", binding.route.ID)
		}
		if binding.transport.staticDir && !strings.HasSuffix(route.Pattern, "/") {
			return fmt.Errorf("static directory route %q must end with '/'", binding.route.ID)
		}
		if !binding.transport.staticDir && strings.HasSuffix(route.Pattern, "/") {
			return fmt.Errorf("static file route %q must be exact", binding.route.ID)
		}
	}

	for key, existing := range r.http {
		if key.pattern != route.Pattern || existing == nil {
			continue
		}
		if existing.owner != binding.owner {
			return fmt.Errorf("path %q is already owned by service %q", route.Pattern, existing.owner)
		}
		if existing.transport.spec.Type == TransportStatic || binding.transport.spec.Type == TransportStatic {
			return fmt.Errorf("static and non-static routes conflict at path %q", route.Pattern)
		}
		if routeMethodsConflict(key.method, route.Method) {
			return fmt.Errorf("route %s %q conflicts with route %q", formatRouteMethod(route.Method), route.Pattern, existing.route.ID)
		}
	}
	r.http[httpRouteKey{pattern: route.Pattern, method: route.Method}] = binding
	return nil
}

func (r *routeRegistry) unregister(owner, id string) error {
	key := routeKey{owner: owner, id: strings.TrimSpace(id)}
	r.mu.Lock()
	defer r.mu.Unlock()
	binding, ok := r.byID[key]
	if !ok {
		return fmt.Errorf("route %q is not registered by service %q", id, owner)
	}
	r.removeLocked(key, binding)
	if binding.transport.service != nil {
		binding.transport.service.removeRoute(id)
		r.transports.publish(binding.transport.service)
	}
	return nil
}

func (r *routeRegistry) removeLocked(key routeKey, binding *routeBinding) {
	delete(r.byID, key)
	if binding.route.HTTP != nil {
		delete(r.http, httpRouteKey{
			pattern: binding.route.HTTP.Pattern,
			method:  normalizeRouteMethod(binding.route.HTTP.Method),
		})
		return
	}
	if binding.route.SocketIO != nil {
		r.socket.unregister(binding.owner, binding.route.SocketIO.Namespace)
	}
}

func (r *routeRegistry) removeOwnerLocked(owner string) {
	for key, binding := range r.byID {
		if binding.owner == owner {
			r.removeLocked(key, binding)
		}
	}
}

func (r *routeRegistry) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	binding, matchedPattern, allowed := r.matchHTTP(request.Method, request.URL.Path)
	if binding == nil {
		if matchedPattern {
			writeMethodNotAllowed(w, allowed)
			return
		}
		if page, ok := conf.GetPagePath(http.StatusNotFound); ok && netx.WantsHTMLPage(request) && !netx.RequestPathMatches(request, page) {
			http.Redirect(w, request, page, http.StatusSeeOther)
			return
		}
		_ = netx.WriteNotFound(w)
		return
	}
	r.serveBinding(binding, w, request)
}

func (r *routeRegistry) matchHTTP(method, path string) (*routeBinding, bool, []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	best := ""
	for key, binding := range r.http {
		rootMode := netx.RootPathExact
		if binding.transport.spec.Type == TransportStatic && binding.transport.staticDir {
			rootMode = netx.RootPathSubtree
		}
		if netx.MatchPathPattern(path, key.pattern, rootMode) && len(key.pattern) > len(best) {
			best = key.pattern
		}
	}
	if best == "" {
		return nil, false, nil
	}
	method = normalizeRouteMethod(method)
	allowedSet := make(map[string]struct{})
	var wildcard *routeBinding
	for key, binding := range r.http {
		if key.pattern != best {
			continue
		}
		if key.method == method {
			return binding, true, nil
		}
		if key.method == "" {
			wildcard = binding
		} else {
			allowedSet[key.method] = struct{}{}
		}
	}
	if wildcard != nil {
		return wildcard, true, nil
	}
	return nil, true, sortedMethods(allowedSet)
}

func (r *routeRegistry) serveBinding(binding *routeBinding, w http.ResponseWriter, request *http.Request) {
	user := auth.UserFromRequest(request)
	if binding.transport.service != nil && writeServiceAccessError(w, request, binding.transport.service.accessPolicy().Check(user)) {
		return
	}
	if writeServiceAccessError(w, request, binding.route.HTTP.Access.Check(user)) {
		return
	}

	switch binding.transport.spec.Type {
	case TransportHTTP:
		r.serveRPC(binding, w, request, user)
	case TransportStatic:
		serveStatic(binding, w, request)
	case TransportProxy:
		binding.transport.handler.ServeHTTP(w, request)
	default:
		_ = netx.WriteError(w, http.StatusBadGateway, "invalid route transport", nil)
	}
}

func serveStatic(binding *routeBinding, w http.ResponseWriter, request *http.Request) {
	path := binding.transport.staticPath
	if binding.transport.staticDir {
		prefix := strings.TrimSuffix(binding.route.HTTP.Pattern, "/")
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

func (r *routeRegistry) serveRPC(binding *routeBinding, w http.ResponseWriter, request *http.Request, user User) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBody+1))
	if err != nil {
		_ = netx.WriteBadRequest(w, "failed to read request body")
		return
	}
	if len(body) > maxRequestBody {
		_ = netx.WritePayloadTooLarge(w, "request body too large")
		return
	}
	headers := request.Header.Clone()
	injectVerifiedIdentity(headers, user)
	service := binding.transport.service
	ctx, cancel := service.callContext(request.Context())
	defer cancel()
	response, err := service.conn.HandleHTTP(ctx, &HTTPRequest{
		RouteID: binding.route.ID, RoutePattern: binding.route.HTTP.Pattern,
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

func userOrNil(user User) *User {
	if !user.Authenticated {
		return nil
	}
	return &user
}

func writeServiceAccessError(w http.ResponseWriter, request *http.Request, decision auth.AccessDecision) bool {
	if decision == auth.AccessAuthenticationRequired {
		if page, ok := conf.GetPagePath(http.StatusUnauthorized); ok && netx.WantsHTMLPage(request) && !netx.RequestPathMatches(request, page) {
			http.Redirect(w, request, page, http.StatusSeeOther)
			return true
		}
	}
	return auth.WriteAccessError(w, decision)
}

func normalizeRouteMethod(method string) string {
	return strings.ToUpper(strings.TrimSpace(method))
}

func routeMethodsConflict(a, b string) bool {
	return a == b || a == "" || b == ""
}

func formatRouteMethod(method string) string {
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
