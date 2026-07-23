// Package service implements the kernel side of the Arupa service system.
//
// A service owns transports and routes. Transports describe how a request is
// handled; routes only bind public names to a registered transport. WASM and
// gRPC services share the same protobuf contract, while static services are
// declared by manifest.yaml and registered by the kernel on their behalf.
package service

import (
	"context"
	"net/http"

	"arupa/internal/auth"
)

type User = auth.User
type AccessPolicy = auth.AccessPolicy

const ContractVersion = 2

type TransportType string

const (
	TransportStatic   TransportType = "static"
	TransportHTTP     TransportType = "http"
	TransportSocketIO TransportType = "socket.io"
	TransportProxy    TransportType = "proxy"
)

type ProxyNetwork string

const (
	ProxyInherited ProxyNetwork = "inherited"
	ProxyUnix      ProxyNetwork = "unix"
	ProxyTCP       ProxyNetwork = "tcp"
)

type ProxyTarget struct {
	Network ProxyNetwork `json:"network" yaml:"network"`
	Address string       `json:"address,omitempty" yaml:"address,omitempty"`
	Scheme  string       `json:"scheme,omitempty" yaml:"scheme,omitempty"`
}

type Transport struct {
	ID           string        `json:"id" yaml:"id"`
	Type         TransportType `json:"type" yaml:"type"`
	StaticSource string        `json:"source,omitempty" yaml:"source,omitempty"`
	Proxy        *ProxyTarget  `json:"proxy,omitempty" yaml:"proxy,omitempty"`
}

type HTTPRoute struct {
	Method  string       `json:"method,omitempty" yaml:"method,omitempty"`
	Pattern string       `json:"pattern" yaml:"pattern"`
	Access  AccessPolicy `json:"access,omitempty" yaml:"access,omitempty"`
}

type SocketIORoute struct {
	Namespace   string                  `json:"namespace" yaml:"namespace"`
	Events      []string                `json:"events" yaml:"events"`
	Access      AccessPolicy            `json:"access,omitempty" yaml:"access,omitempty"`
	EventAccess map[string]AccessPolicy `json:"event_access,omitempty" yaml:"event_access,omitempty"`
}

type Route struct {
	ID          string         `json:"id" yaml:"id"`
	TransportID string         `json:"transport" yaml:"transport"`
	HTTP        *HTTPRoute     `json:"http,omitempty" yaml:"http,omitempty"`
	SocketIO    *SocketIORoute `json:"socket_io,omitempty" yaml:"socket_io,omitempty"`
}

type RegistrationFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

type RegistrationResult struct {
	Registered []string              `json:"registered,omitempty"`
	Failures   []RegistrationFailure `json:"failures,omitempty"`
	Degraded   bool                  `json:"degraded"`
	Error      string                `json:"error,omitempty"`
}

type InheritedListener struct {
	ID      string
	FD      uint32
	Network string
	Address string
}

type RegisterResult struct {
	Name    string
	Version string
}

type RegisterRequest struct {
	InstanceID   string
	Params       map[string]string
	Listeners    []InheritedListener
	HostBrokerID uint32
}

type HTTPRequest struct {
	RouteID      string
	RoutePattern string
	Method       string
	Path         string
	Query        string
	Headers      http.Header
	Body         []byte
	RemoteAddr   string
	User         *User
}

type HTTPResponse struct {
	Status  int
	Headers http.Header
	Body    []byte
}

type SocketEvent struct {
	RouteID   string
	Namespace string
	Event     string
	SocketID  string
	User      *User
	Payload   []byte
}

type EmitInstruction struct {
	Namespace string
	Target    string
	Event     string
	Payload   []byte
}

type ServiceMessage struct {
	Source  string
	Target  string
	Topic   string
	Payload []byte
}

type ParamsPatch struct {
	Set    map[string]string
	Delete []string
}

type serviceConn interface {
	Register(context.Context, RegisterRequest) (*RegisterResult, error)
	HandleHTTP(context.Context, *HTTPRequest) (*HTTPResponse, error)
	HandleSocketEvent(context.Context, *SocketEvent) ([]EmitInstruction, error)
	HandleServiceMessage(context.Context, *ServiceMessage) (string, error)
}

type Emitter interface {
	Emit(EmitInstruction) error
}

type ServiceMessageDispatcher interface {
	DispatchServiceMessage(context.Context, ServiceMessage) (string, error)
}

type ParamsStore interface {
	GetServiceParams(string) (map[string]string, error)
	PatchServiceParams(string, ParamsPatch) error
}

type ResourceRegistrar interface {
	RegisterTransport(string, Transport) error
	UnregisterTransport(string, string) error
	RegisterRoutes(string, []Route) RegistrationResult
	UnregisterRoutes(string, []string) RegistrationResult
}
