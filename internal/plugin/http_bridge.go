package plugin

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"arupa/internal/auth"
	"arupa/internal/conf"
	"arupa/internal/netx"
)

type pluginRouter struct {
	routeMu sync.RWMutex
	routes  map[httpRouteKey]*httpRouteBinding

	staticMu sync.RWMutex
	static   map[string]*staticMountBinding
}

// httpRouteBinding is a live HTTP route owned by a loaded plugin.
//
// The host mux does not receive one handler per plugin route. Instead, all
// plugin HTTP requests enter pluginRouter.ServeHTTP and are matched against this
// table by pattern and method, which makes stop/restart remove and re-add
// routes without rebuilding the host mux.
type httpRouteBinding struct {
	owner  string
	route  HTTPRoute
	plugin *loadedPlugin
}

// staticMountBinding is a live static file mount owned by a loaded plugin.
//
// Directory mounts are stored with a trailing-slash pattern and matched by
// prefix; file mounts are stored as exact patterns.
type staticMountBinding struct {
	owner   string
	mount   StaticMount
	handler http.Handler
}

func newPluginRouter() *pluginRouter {
	return &pluginRouter{
		routes: make(map[httpRouteKey]*httpRouteBinding),
		static: make(map[string]*staticMountBinding),
	}
}

// maxRequestBody caps the request body forwarded to plugins (8 MiB).
// TODO: Need to find a way so plugin can accept large files
const maxRequestBody = 8 << 20

type httpRouteKey struct {
	pattern string
	method  string
}

// ServeHTTP dispatches requests that did not match host routes to the current
// plugin HTTP/static route table. It chooses the longest matching plugin
// pattern; static mounts win ties so an exact static file mount can serve its
// asset without being shadowed by an HTTP route of the same length.
func (router *pluginRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, routePattern, routePlugin, routeLen, allowedMethods := router.matchPluginRoute(r.Method, r.URL.Path)
	mount, staticHandler, staticLen := router.matchPluginStatic(r.URL.Path)

	if staticHandler != nil && staticLen >= routeLen {
		router.handlePluginStatic(mount, staticHandler, w, r)
		return
	}
	if routePlugin != nil {
		router.handlePluginRoute(routePattern, route, routePlugin, w, r)
		return
	}
	if routeLen >= 0 {
		writeMethodNotAllowed(w, allowedMethods)
		return
	}
	if page, ok := conf.GetPagePath(http.StatusNotFound); ok && netx.WantsHTMLPage(r) && !netx.RequestPathMatches(r, page) {
		http.Redirect(w, r, page, http.StatusSeeOther)
		return
	}
	_ = netx.WriteNotFound(w)
}

// registerRoute wires a plugin HTTP route into the plugin dispatch table. Route
// ownership is exclusive for each pattern/method while the owning plugin is
// loaded; Stop removes the entry so another plugin can claim it later.
func (router *pluginRouter) registerRoute(owner string, route HTTPRoute, lp *loadedPlugin) error {
	if route.Pattern == "" {
		return fmt.Errorf("http route pattern is required")
	}
	if lp == nil || lp.conn == nil {
		return fmt.Errorf("http route %q requires a loaded plugin connection", route.Pattern)
	}

	pattern := route.Pattern
	method := normalizeRouteMethod(route.Method)
	route.Method = method
	key := httpRouteKey{pattern: pattern, method: method}

	router.routeMu.Lock()
	defer router.routeMu.Unlock()
	for existingKey, binding := range router.routes {
		if existingKey.pattern != pattern || binding == nil || binding.owner == owner {
			continue
		}
		if routeMethodsConflict(existingKey.method, method) {
			return fmt.Errorf("http route %s %q already owned by plugin %q", formatRouteMethod(existingKey.method), pattern, binding.owner)
		}
	}

	router.staticMu.RLock()
	staticBinding, staticExists := router.static[pattern]
	router.staticMu.RUnlock()
	if staticExists && staticBinding.owner != owner {
		return fmt.Errorf("http route pattern %q already owned by static mount from plugin %q", pattern, staticBinding.owner)
	}

	if binding, ok := router.routes[key]; ok {
		binding.owner = owner
		binding.route = route
		binding.plugin = lp
		return nil
	}

	router.routes[key] = &httpRouteBinding{owner: owner, route: route, plugin: lp}
	return nil
}

func (router *pluginRouter) unregisterRoutes(owner string) {
	router.routeMu.Lock()
	defer router.routeMu.Unlock()
	for key, binding := range router.routes {
		if binding.owner == owner {
			delete(router.routes, key)
		}
	}
}

// matchPluginRoute returns the longest HTTP route pattern that matches path,
// then selects the handler by method within that pattern. Empty route methods
// are wildcard handlers. Patterns ending in "/" are prefix matches; all other
// patterns are exact matches.
func (router *pluginRouter) matchPluginRoute(method, path string) (HTTPRoute, string, *loadedPlugin, int, []string) {
	router.routeMu.RLock()
	defer router.routeMu.RUnlock()

	requestMethod := normalizeRouteMethod(method)
	bestPattern := ""
	for key, binding := range router.routes {
		if binding == nil || binding.plugin == nil || binding.plugin.conn == nil {
			continue
		}
		if !pathMatchesRoutePattern(path, key.pattern) {
			continue
		}
		if len(key.pattern) > len(bestPattern) {
			bestPattern = key.pattern
		}
	}
	if bestPattern == "" {
		return HTTPRoute{}, "", nil, -1, nil
	}

	var exact *httpRouteBinding
	var wildcard *httpRouteBinding
	allowed := make(map[string]struct{})
	for key, binding := range router.routes {
		if key.pattern != bestPattern || binding == nil || binding.plugin == nil || binding.plugin.conn == nil {
			continue
		}
		if key.method == "" {
			wildcard = binding
			continue
		}
		allowed[key.method] = struct{}{}
		if key.method == requestMethod {
			exact = binding
		}
	}
	if exact != nil {
		return exact.route, bestPattern, exact.plugin, len(bestPattern), nil
	}
	if wildcard != nil {
		return wildcard.route, bestPattern, wildcard.plugin, len(bestPattern), nil
	}
	return HTTPRoute{}, bestPattern, nil, len(bestPattern), sortedMethods(allowed)
}

// handlePluginRoute serializes an HTTP request into the plugin contract, calls
// the owning plugin, and writes the plugin's response back to the client.
func (router *pluginRouter) handlePluginRoute(pattern string, route HTTPRoute, lp *loadedPlugin, w http.ResponseWriter, r *http.Request) {
	method := normalizeRouteMethod(route.Method)
	if method != "" && !strings.EqualFold(r.Method, method) {
		writeMethodNotAllowed(w, []string{method})
		return
	}
	if route.Protected {
		if _, ok := auth.IsAuthenticated(r); !ok {
			if page, ok := conf.GetPagePath(http.StatusUnauthorized); ok && netx.WantsHTMLPage(r) && !netx.RequestPathMatches(r, page) {
				http.Redirect(w, r, page, http.StatusSeeOther)
				return
			}
			_ = netx.WriteUnauthorized(w, "authentication required")
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		_ = netx.WriteBadRequest(w, "failed to read request body")
		return
	}
	if len(body) > maxRequestBody {
		_ = netx.WritePayloadTooLarge(w, "request body too large")
		return
	}

	req := &HTTPRequest{
		RoutePattern: pattern,
		Method:       r.Method,
		Path:         r.URL.Path,
		Query:        r.URL.RawQuery,
		Headers:      flattenHeaders(r.Header),
		Body:         body,
		RemoteAddr:   r.RemoteAddr,
	}

	ctx, cancel := lp.callContext(r.Context())
	defer cancel()
	resp, err := lp.conn.HandleHTTP(ctx, req)
	if err != nil {
		_ = netx.WriteError(w, http.StatusBadGateway, "plugin handler failed", err)
		return
	}

	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(resp.Body)
}

// pathMatchesPattern implements the plugin dispatcher's small matching model:
// trailing-slash patterns are subtree prefixes, and every other pattern is an
// exact match.
func pathMatchesPattern(path, pattern string) bool {
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(path, pattern)
	}
	return path == pattern
}

func pathMatchesRoutePattern(path, pattern string) bool {
	if pattern == "/" {
		return path == "/"
	}
	return pathMatchesPattern(path, pattern)
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

func writeMethodNotAllowed(w http.ResponseWriter, allowedMethods []string) {
	if len(allowedMethods) > 0 {
		w.Header().Set("Allow", strings.Join(allowedMethods, ", "))
	}
	_ = netx.WriteMethodNotAllowed(w)
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}
