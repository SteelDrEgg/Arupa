// Package plugin implements the host side of the arupa plugin system.
//
// It wraps github.com/SteelDrEgg/go-plugin to load WASM and gRPC plugins,
// exposes a shared host API (KV, Socket.IO emit, logging) to them, and bridges
// HTTP requests and Socket.IO events declared by plugins into the running host.
//
// The types in this file are backend-agnostic plain Go values. Each backend
// (WASM, gRPC) has a thin adapter that converts between these values and the
// backend-specific generated protobuf types.
package plugin

import (
	"context"

	"arupa/internal/auth"
)

type User = auth.User
type AccessPolicy = auth.AccessPolicy

// HTTPRoute is a single HTTP route a plugin handles.
type HTTPRoute struct {
	Method  string       `json:"method"`  // GET/POST/...; empty means any method
	Pattern string       `json:"pattern"` // URL path pattern
	Access  AccessPolicy `json:"access"`
}

// StaticMount declares a URL prefix to host static files from a directory.
type StaticMount struct {
	Prefix    string       `json:"prefix"`    // URL path; use trailing '/' for directories, exact path for files
	Directory string       `json:"directory"` // host path; can be a directory or a single file
	Access    AccessPolicy `json:"access"`
}

// SocketNamespaceDecl declares a Socket.IO namespace and the events a plugin
// handles within it.
type SocketNamespaceDecl struct {
	Name        string                  `json:"name"`
	Events      []string                `json:"events"`
	Access      AccessPolicy            `json:"access"`
	EventAccess map[string]AccessPolicy `json:"event_access,omitempty"`
}

// RegisterResult is the backend-agnostic result of registering a plugin.
type RegisterResult struct {
	Name       string
	Version    string
	Routes     []HTTPRoute
	Static     []StaticMount
	Namespaces []SocketNamespaceDecl
}

// RegisterRequest carries host-provided data to a plugin at registration.
type RegisterRequest struct {
	InstanceID        string
	HostCallbackAddr  string            // gRPC plugins dial this to reach the host callback API
	HostCallbackToken string            // auth token for the host callback API
	Params            map[string]string // config params from [Plugins.<name>.params]
}

// HTTPRequest is a serialized HTTP request forwarded to a plugin.
type HTTPRequest struct {
	RoutePattern string
	Method       string
	Path         string
	Query        string
	Headers      map[string]string
	Body         []byte
	RemoteAddr   string
	User         *User
}

// HTTPResponse is a plugin's reply to an HTTPRequest.
type HTTPResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// SocketEvent is a serialized Socket.IO event forwarded to a plugin.
type SocketEvent struct {
	Namespace string
	Event     string
	SocketID  string
	User      *User
	Payload   []byte // JSON-encoded array of event arguments
}

// EmitInstruction asks the host to emit a Socket.IO event.
type EmitInstruction struct {
	Namespace string
	Target    string // socket id; empty broadcasts to the whole namespace
	Event     string
	Payload   []byte // JSON-encoded array of emit arguments
}

// PluginMessage is the plugin-to-plugin message envelope. Source is always set
// by the host to the registered source plugin name before delivery.
type PluginMessage struct {
	Source  string
	Target  string
	Topic   string
	Payload []byte
}

// ParamsPatch is a partial update to the caller plugin's persisted Params
// override.
type ParamsPatch struct {
	Set    map[string]string
	Delete []string
}

// pluginConn is the backend-agnostic handle the host uses to call into a plugin.
type pluginConn interface {
	Register(ctx context.Context, req RegisterRequest) (*RegisterResult, error)
	HandleHTTP(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error)
	HandleSocketEvent(ctx context.Context, ev *SocketEvent) ([]EmitInstruction, error)
	HandlePluginMessage(ctx context.Context, msg *PluginMessage) (string, error)
}

// Emitter sends Socket.IO emits requested by plugins.
type Emitter interface {
	Emit(instr EmitInstruction) error
}

// PluginMessageDispatcher delivers trusted plugin messages to their target.
type PluginMessageDispatcher interface {
	DispatchPluginMessage(ctx context.Context, msg PluginMessage) (string, error)
}

// ParamsStore reads and persists caller-scoped plugin Params.
type ParamsStore interface {
	GetPluginParams(name string) (map[string]string, error)
	PatchPluginParams(name string, patch ParamsPatch) error
}
