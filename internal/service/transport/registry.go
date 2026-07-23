// Package transport creates and owns service transport implementations.
//
// It has no knowledge of routes or lifecycle state. Cross-resource operations
// such as "transport is still in use" are coordinated by package binding.
package transport

import (
	"context"
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
	"arupa/internal/service/spec"
)

const (
	IdentityUserHeader          = "X-Arupa-User"
	IdentityGroupHeader         = "X-Arupa-Group"
	IdentityAuthenticatedHeader = "X-Arupa-Authenticated"
)

type key struct {
	owner string
	id    string
}

// Session contains only the service state needed to build a transport.
type Session struct {
	Endpoint  spec.Endpoint
	Root      string
	Inherited map[string]string
}

// Binding is an immutable, prepared transport.
type Binding struct {
	owner      string
	spec       spec.Transport
	endpoint   spec.Endpoint
	handler    http.Handler
	staticPath string
	staticDir  bool
	close      func()
}

func (b *Binding) Owner() string           { return b.owner }
func (b *Binding) Spec() spec.Transport    { return b.spec }
func (b *Binding) Endpoint() spec.Endpoint { return b.endpoint }
func (b *Binding) Handler() http.Handler   { return b.handler }
func (b *Binding) StaticPath() string      { return b.staticPath }
func (b *Binding) StaticDirectory() bool   { return b.staticDir }
func (b *Binding) Close() {
	if b != nil && b.close != nil {
		b.close()
	}
}

// Registry owns prepared transports keyed by authenticated service owner.
type Registry struct {
	mu         sync.RWMutex
	transports map[key]*Binding
}

func NewRegistry() *Registry {
	return &Registry{transports: make(map[key]*Binding)}
}

func (r *Registry) Register(owner string, declaration spec.Transport, session Session) error {
	owner = strings.TrimSpace(owner)
	declaration.ID = strings.TrimSpace(declaration.ID)
	if owner == "" {
		return fmt.Errorf("transport owner is required")
	}
	if declaration.ID == "" {
		return fmt.Errorf("transport id is required")
	}

	binding, err := prepare(owner, declaration, session)
	if err != nil {
		return err
	}
	k := key{owner: owner, id: declaration.ID}
	r.mu.Lock()
	if _, exists := r.transports[k]; exists {
		r.mu.Unlock()
		binding.Close()
		return fmt.Errorf("transport %q is already registered", declaration.ID)
	}
	r.transports[k] = binding
	r.mu.Unlock()
	return nil
}

func (r *Registry) Unregister(owner, id string) error {
	id = strings.TrimSpace(id)
	k := key{owner: owner, id: id}
	r.mu.Lock()
	binding := r.transports[k]
	if binding == nil {
		r.mu.Unlock()
		return fmt.Errorf("transport %q is not registered by service %q", id, owner)
	}
	delete(r.transports, k)
	r.mu.Unlock()
	binding.Close()
	return nil
}

func (r *Registry) Lookup(owner, id string) (*Binding, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	binding, ok := r.transports[key{owner: owner, id: id}]
	return binding, ok
}

func (r *Registry) RemoveOwner(owner string) {
	r.mu.Lock()
	var removed []*Binding
	for k, binding := range r.transports {
		if k.owner == owner {
			removed = append(removed, binding)
			delete(r.transports, k)
		}
	}
	r.mu.Unlock()
	for _, binding := range removed {
		binding.Close()
	}
}

func prepare(owner string, declaration spec.Transport, session Session) (*Binding, error) {
	binding := &Binding{owner: owner, spec: declaration, endpoint: session.Endpoint}
	switch declaration.Type {
	case spec.TransportHTTP, spec.TransportSocketIO:
		if session.Endpoint == nil || session.Endpoint.Connection() == nil {
			return nil, fmt.Errorf("%s transport requires a WASM or gRPC service", declaration.Type)
		}
	case spec.TransportStatic:
		staticPath, isDir, err := resolveStaticSource(session.Root, declaration.StaticSource)
		if err != nil {
			return nil, err
		}
		binding.staticPath = staticPath
		binding.staticDir = isDir
	case spec.TransportProxy:
		handler, closeTransport, err := NewProxyHandler(declaration.Proxy, session.Inherited)
		if err != nil {
			return nil, err
		}
		binding.handler = handler
		binding.close = closeTransport
	default:
		return nil, fmt.Errorf("unsupported transport type %q", declaration.Type)
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

// NewProxyHandler builds a reverse proxy and returns an explicit transport
// cleanup function for unregister and service shutdown.
func NewProxyHandler(target *spec.ProxyTarget, inherited map[string]string) (http.Handler, func(), error) {
	if target == nil {
		return nil, nil, fmt.Errorf("proxy target is required")
	}
	scheme := strings.ToLower(strings.TrimSpace(target.Scheme))
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return nil, nil, fmt.Errorf("proxy scheme must be http or https")
	}

	address := strings.TrimSpace(target.Address)
	httpTransport := http.DefaultTransport.(*http.Transport).Clone()
	upstream := &url.URL{Scheme: scheme}
	switch target.Network {
	case spec.ProxyInherited:
		id := address
		if id == "" {
			id = "proxy"
		}
		address = inherited[id]
		if address == "" {
			return nil, nil, fmt.Errorf("inherited listener %q is unavailable", id)
		}
		upstream.Host = "service"
		httpTransport.DialContext = unixDialer(address)
	case spec.ProxyUnix:
		if !filepath.IsAbs(address) {
			return nil, nil, fmt.Errorf("unix proxy address must be absolute")
		}
		upstream.Host = "service"
		httpTransport.DialContext = unixDialer(address)
	case spec.ProxyTCP:
		if address == "" {
			return nil, nil, fmt.Errorf("tcp proxy address is required")
		}
		upstream.Host = address
	default:
		return nil, nil, fmt.Errorf("unsupported proxy network %q", target.Network)
	}

	proxy := &httputil.ReverseProxy{
		Transport: httpTransport,
		Rewrite: func(request *httputil.ProxyRequest) {
			originalHost := request.In.Host
			request.SetURL(upstream)
			request.Out.Host = originalHost
			request.SetXForwarded()
			InjectVerifiedIdentity(request.Out.Header, auth.UserFromRequest(request.In))
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "bad gateway: "+err.Error(), http.StatusBadGateway)
		},
	}
	return proxy, httpTransport.CloseIdleConnections, nil
}

func unixDialer(path string) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", path)
	}
}

// InjectVerifiedIdentity replaces any caller-supplied identity headers with
// identity established by the kernel authentication layer.
func InjectVerifiedIdentity(headers http.Header, user spec.User) {
	headers.Del(IdentityUserHeader)
	headers.Del(IdentityGroupHeader)
	headers.Del(IdentityAuthenticatedHeader)
	if !user.Authenticated {
		headers.Set(IdentityAuthenticatedHeader, "false")
		return
	}
	headers.Set(IdentityAuthenticatedHeader, "true")
	headers.Set(IdentityUserHeader, user.Username)
	for _, group := range user.Groups {
		headers.Add(IdentityGroupHeader, group)
	}
}

func CloneStrings(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, value := range in {
		out[k] = value
	}
	return out
}
