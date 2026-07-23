// Package service assembles and exposes the kernel service system.
//
// Backend-neutral declarations live in spec. These aliases keep the public
// facade compact while callers migrate independently from the implementation
// package layout.
package service

import (
	"arupa/internal/service/catalog"
	"arupa/internal/service/spec"
	"arupa/internal/service/supervisor"
)

type User = spec.User
type AccessPolicy = spec.AccessPolicy

const ContractVersion = spec.ContractVersion

type TransportType = spec.TransportType

const (
	TransportStatic   = spec.TransportStatic
	TransportHTTP     = spec.TransportHTTP
	TransportSocketIO = spec.TransportSocketIO
	TransportProxy    = spec.TransportProxy
)

type ProxyNetwork = spec.ProxyNetwork

const (
	ProxyInherited = spec.ProxyInherited
	ProxyUnix      = spec.ProxyUnix
	ProxyTCP       = spec.ProxyTCP
)

type ProxyTarget = spec.ProxyTarget
type Transport = spec.Transport
type HTTPRoute = spec.HTTPRoute
type SocketIORoute = spec.SocketIORoute
type Route = spec.Route
type RegistrationFailure = spec.RegistrationFailure
type RegistrationResult = spec.RegistrationResult
type InheritedListener = spec.InheritedListener
type RegisterResult = spec.RegisterResult
type RegisterRequest = spec.RegisterRequest
type HTTPRequest = spec.HTTPRequest
type HTTPResponse = spec.HTTPResponse
type SocketEvent = spec.SocketEvent
type EmitInstruction = spec.EmitInstruction
type ServiceMessage = spec.ServiceMessage
type ParamsPatch = spec.ParamsPatch
type ServiceRecord = spec.ServiceRecord
type DiscoveredService = catalog.DiscoveredService
type ServiceEntry = supervisor.ServiceEntry
type ServiceStatus = supervisor.ServiceStatus

const (
	ServiceStatusDiscovered = supervisor.ServiceStatusDiscovered
	ServiceStatusStarting   = supervisor.ServiceStatusStarting
	ServiceStatusRunning    = supervisor.ServiceStatusRunning
	ServiceStatusDegraded   = supervisor.ServiceStatusDegraded
	ServiceStatusStopping   = supervisor.ServiceStatusStopping
	ServiceStatusFailed     = supervisor.ServiceStatusFailed
)

type serviceConn = spec.Conn
type Emitter = spec.Emitter
type ServiceMessageDispatcher = spec.MessageDispatcher
type ParamsStore = spec.ParamsStore
type ResourceRegistrar = spec.ResourceRegistrar
