package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"arupa/internal/auth"
)

var ErrTransportInUse = errors.New("transport is still referenced by a route")

const (
	identityUserHeader          = "X-Arupa-User"
	identityGroupHeader         = "X-Arupa-Group"
	identityAuthenticatedHeader = "X-Arupa-Authenticated"
)

type transportKey struct {
	owner string
	id    string
}

type serviceSession struct {
	service   *loadedService
	root      string
	inherited map[string]string
}

type transportBinding struct {
	owner      string
	spec       Transport
	service    *loadedService
	handler    http.Handler
	staticPath string
	staticDir  bool
}

type transportRegistry struct {
	mu         sync.RWMutex
	transports map[transportKey]*transportBinding
	sessions   map[string]serviceSession
	routes     *routeRegistry
	registry   *Registry
}

func newTransportRegistry(registries ...*Registry) *transportRegistry {
	registry := (*Registry)(nil)
	if len(registries) > 0 {
		registry = registries[0]
	}
	return &transportRegistry{
		transports: make(map[transportKey]*transportBinding),
		sessions:   make(map[string]serviceSession),
		registry:   registry,
	}
}

func (r *transportRegistry) attach(owner string, service *loadedService, root string, inherited map[string]string) {
	r.mu.Lock()
	r.sessions[owner] = serviceSession{
		service: service, root: root, inherited: cloneStrings(inherited),
	}
	r.mu.Unlock()
}

func (r *transportRegistry) detach(owner string) {
	if r.routes != nil {
		r.routes.mu.Lock()
		defer r.routes.mu.Unlock()
		r.routes.removeOwnerLocked(owner)
	}
	r.mu.Lock()
	for key := range r.transports {
		if key.owner == owner {
			delete(r.transports, key)
		}
	}
	delete(r.sessions, owner)
	r.mu.Unlock()
}

func (r *transportRegistry) RegisterTransport(owner string, spec Transport) error {
	owner = strings.TrimSpace(owner)
	spec.ID = strings.TrimSpace(spec.ID)
	if owner == "" {
		return fmt.Errorf("transport owner is required")
	}
	if spec.ID == "" {
		return fmt.Errorf("transport id is required")
	}

	r.mu.RLock()
	session, attached := r.sessions[owner]
	r.mu.RUnlock()
	if !attached {
		return fmt.Errorf("service %q has no active session", owner)
	}

	binding, err := r.prepare(owner, spec, session)
	if err != nil {
		return err
	}
	key := transportKey{owner: owner, id: spec.ID}
	r.mu.Lock()
	if _, exists := r.transports[key]; exists {
		r.mu.Unlock()
		return fmt.Errorf("transport %q is already registered", spec.ID)
	}
	r.transports[key] = binding
	r.mu.Unlock()
	session.service.addTransport(spec)
	r.publish(session.service)
	return nil
}

func (r *transportRegistry) UnregisterTransport(owner, id string) error {
	id = strings.TrimSpace(id)
	key := transportKey{owner: owner, id: id}
	if r.routes != nil {
		r.routes.mu.RLock()
		defer r.routes.mu.RUnlock()
		for _, binding := range r.routes.byID {
			if binding.owner == owner && binding.route.TransportID == id {
				return fmt.Errorf("%w: %q", ErrTransportInUse, id)
			}
		}
	}
	r.mu.Lock()
	binding := r.transports[key]
	if binding == nil {
		r.mu.Unlock()
		return fmt.Errorf("transport %q is not registered by service %q", id, owner)
	}
	delete(r.transports, key)
	r.mu.Unlock()
	if binding != nil && binding.service != nil {
		binding.service.removeTransport(id)
		r.publish(binding.service)
	}
	return nil
}

func (r *transportRegistry) publish(service *loadedService) {
	if r.registry == nil || service == nil {
		return
	}
	service.publishMu.Lock()
	defer service.publishMu.Unlock()
	record := service.snapshotRecord()
	if record != nil && r.registry.Has(record.InstanceID) {
		r.registry.Add(record)
	}
}

func (r *transportRegistry) RegisterRoutes(owner string, routes []Route) RegistrationResult {
	if r.routes == nil {
		return RegistrationResult{Degraded: true, Error: "route registry is not configured"}
	}
	return r.routes.RegisterRoutes(owner, routes)
}

func (r *transportRegistry) UnregisterRoutes(owner string, ids []string) RegistrationResult {
	if r.routes == nil {
		return RegistrationResult{Error: "route registry is not configured"}
	}
	return r.routes.UnregisterRoutes(owner, ids)
}

func (r *transportRegistry) lookup(owner, id string) (*transportBinding, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	binding, ok := r.transports[transportKey{owner: owner, id: id}]
	return binding, ok
}

func (r *transportRegistry) markDegraded(owner string) {
	r.mu.RLock()
	session, ok := r.sessions[owner]
	r.mu.RUnlock()
	if ok && session.service != nil {
		session.service.degraded.Store(true)
	}
}

func (r *transportRegistry) prepare(owner string, spec Transport, session serviceSession) (*transportBinding, error) {
	binding := &transportBinding{owner: owner, spec: spec, service: session.service}
	switch spec.Type {
	case TransportHTTP, TransportSocketIO:
		if session.service == nil || session.service.conn == nil {
			return nil, fmt.Errorf("%s transport requires a WASM or gRPC service", spec.Type)
		}
	case TransportStatic:
		staticPath, isDir, err := resolveStaticSource(session.root, spec.StaticSource)
		if err != nil {
			return nil, err
		}
		binding.staticPath = staticPath
		binding.staticDir = isDir
	case TransportProxy:
		handler, err := newProxyHandler(spec.Proxy, session.inherited)
		if err != nil {
			return nil, err
		}
		binding.handler = handler
	default:
		return nil, fmt.Errorf("unsupported transport type %q", spec.Type)
	}
	return binding, nil
}

func resolveStaticSource(root, source string) (string, bool, error) {
	root = filepath.Clean(root)
	source = strings.TrimSpace(source)
	if root == "." || root == "" {
		return "", false, fmt.Errorf("static transport has no service root")
	}
	if source == "" || filepath.IsAbs(source) {
		return "", false, fmt.Errorf("static source must be a relative path")
	}
	clean := filepath.Clean(source)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("static source escapes the service root")
	}
	path := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("static source escapes the service root")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", false, fmt.Errorf("stat static source %q: %w", source, err)
	}
	return path, info.IsDir(), nil
}

func newProxyHandler(target *ProxyTarget, inherited map[string]string) (http.Handler, error) {
	if target == nil {
		return nil, fmt.Errorf("proxy target is required")
	}
	scheme := strings.ToLower(strings.TrimSpace(target.Scheme))
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("proxy scheme must be http or https")
	}

	address := strings.TrimSpace(target.Address)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	upstream := &url.URL{Scheme: scheme}
	switch target.Network {
	case ProxyInherited:
		id := address
		if id == "" {
			id = "proxy"
		}
		address = inherited[id]
		if address == "" {
			return nil, fmt.Errorf("inherited listener %q is unavailable", id)
		}
		upstream.Host = "service"
		transport.DialContext = unixDialer(address)
	case ProxyUnix:
		if !filepath.IsAbs(address) {
			return nil, fmt.Errorf("unix proxy address must be absolute")
		}
		upstream.Host = "service"
		transport.DialContext = unixDialer(address)
	case ProxyTCP:
		if address == "" {
			return nil, fmt.Errorf("tcp proxy address is required")
		}
		upstream.Host = address
	default:
		return nil, fmt.Errorf("unsupported proxy network %q", target.Network)
	}

	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(request *httputil.ProxyRequest) {
			originalHost := request.In.Host
			request.SetURL(upstream)
			request.Out.Host = originalHost
			request.SetXForwarded()
			injectVerifiedIdentity(request.Out.Header, authUser(request.In))
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "bad gateway: "+err.Error(), http.StatusBadGateway)
		},
	}
	return proxy, nil
}

func unixDialer(path string) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", path)
	}
}

func authUser(request *http.Request) User {
	return auth.UserFromRequest(request)
}

func injectVerifiedIdentity(headers http.Header, user User) {
	headers.Del(identityUserHeader)
	headers.Del(identityGroupHeader)
	headers.Del(identityAuthenticatedHeader)
	if !user.Authenticated {
		headers.Set(identityAuthenticatedHeader, "false")
		return
	}
	headers.Set(identityAuthenticatedHeader, "true")
	headers.Set(identityUserHeader, user.Username)
	for _, group := range user.Groups {
		headers.Add(identityGroupHeader, group)
	}
}

func cloneStrings(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func singleRegistration(id string, err error) RegistrationResult {
	if err == nil {
		return RegistrationResult{Registered: []string{id}}
	}
	return RegistrationResult{
		Failures: []RegistrationFailure{{ID: id, Error: err.Error()}},
		Error:    err.Error(),
	}
}
