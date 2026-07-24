// Package wasm adapts the Service v2 WASM protocol to backend-neutral service
// contracts and Host capabilities.
package wasm

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"arupa/internal/service/host"
	"arupa/internal/service/spec"
	wasmpb "arupa/servicesdk/wasm/proto"
)

type hostFunctions struct {
	api    *host.API
	source string
}

func NewHostFunctions(api *host.API, source string) wasmpb.Host {
	return hostFunctions{api: api, source: source}
}

func (w hostFunctions) KVGet(_ context.Context, request *wasmpb.KVGetRequest) (*wasmpb.KVGetReply, error) {
	value, ok := w.api.KVGet(request.GetNamespace(), request.GetKey())
	return &wasmpb.KVGetReply{Found: ok, Value: value}, nil
}

func (w hostFunctions) KVSet(_ context.Context, request *wasmpb.KVSetRequest) (*wasmpb.KVSetReply, error) {
	return &wasmpb.KVSetReply{Error: errorString(w.api.KVSet(request.GetNamespace(), request.GetKey(), request.GetValue()))}, nil
}

func (w hostFunctions) KVDelete(_ context.Context, request *wasmpb.KVDeleteRequest) (*wasmpb.KVDeleteReply, error) {
	return &wasmpb.KVDeleteReply{Error: errorString(w.api.KVDelete(request.GetNamespace(), request.GetKey()))}, nil
}

func (w hostFunctions) KVList(_ context.Context, request *wasmpb.KVListRequest) (*wasmpb.KVListReply, error) {
	return &wasmpb.KVListReply{Keys: w.api.KVList(request.GetNamespace())}, nil
}

func (w hostFunctions) GetParams(_ context.Context, _ *wasmpb.ParamsGetRequest) (*wasmpb.ParamsGetReply, error) {
	params, err := w.api.GetParams(w.source)
	return &wasmpb.ParamsGetReply{Params: params, Error: errorString(err)}, nil
}

func (w hostFunctions) PatchParams(_ context.Context, request *wasmpb.ParamsPatchRequest) (*wasmpb.ParamsPatchReply, error) {
	err := w.api.PatchParams(w.source, spec.ParamsPatch{Set: request.GetSet(), Delete: request.GetDelete()})
	return &wasmpb.ParamsPatchReply{Error: errorString(err)}, nil
}

func (w hostFunctions) Emit(_ context.Context, request *wasmpb.EmitInstruction) (*wasmpb.EmitReply, error) {
	err := w.api.Emit(spec.EmitInstruction{
		Namespace: request.GetNamespace(), Target: request.GetTarget(),
		Event: request.GetEvent(), Payload: request.GetPayload(),
	})
	return &wasmpb.EmitReply{Error: errorString(err)}, nil
}

func (w hostFunctions) SendServiceMessage(ctx context.Context, request *wasmpb.ServiceMessage) (*wasmpb.ServiceMessageReply, error) {
	message, err := w.api.ServiceMessage(ctx, w.source, spec.ServiceMessage{
		Target: request.GetTarget(), Topic: request.GetTopic(), Payload: request.GetPayload(),
	})
	return &wasmpb.ServiceMessageReply{Message: message, Error: errorString(err)}, nil
}

func (w hostFunctions) RegisterTransport(_ context.Context, request *wasmpb.RegisterTransportRequest) (*wasmpb.RegistrationReply, error) {
	declaration, err := transportFromProto(request.GetTransport())
	if err == nil {
		err = w.api.RegisterTransport(w.source, declaration)
	}
	return registrationReply(singleRegistration(declaration.ID, err)), nil
}

func (w hostFunctions) UnregisterTransport(_ context.Context, request *wasmpb.UnregisterTransportRequest) (*wasmpb.RegistrationReply, error) {
	return registrationReply(singleRegistration(request.GetId(), w.api.UnregisterTransport(w.source, request.GetId()))), nil
}

func (w hostFunctions) RegisterRoutes(_ context.Context, request *wasmpb.RegisterRoutesRequest) (*wasmpb.RegistrationReply, error) {
	routes, err := routesFromProto(request.GetRoutes())
	if err != nil {
		return registrationReply(spec.RegistrationResult{Degraded: true, Error: err.Error()}), nil
	}
	return registrationReply(w.api.RegisterRoutes(w.source, routes)), nil
}

func (w hostFunctions) UnregisterRoutes(_ context.Context, request *wasmpb.UnregisterRoutesRequest) (*wasmpb.RegistrationReply, error) {
	return registrationReply(w.api.UnregisterRoutes(w.source, request.GetIds())), nil
}

func (w hostFunctions) Log(_ context.Context, request *wasmpb.LogRequest) (*wasmpb.LogReply, error) {
	w.api.Log(w.source, request.GetLevel(), request.GetMessage())
	return &wasmpb.LogReply{}, nil
}

// Conn serializes calls because a WASM service instance is not reentrant.
type Conn struct {
	client wasmpb.Service
	mu     sync.Mutex
}

func NewConn(client wasmpb.Service) *Conn {
	return &Conn{client: client}
}

func (c *Conn) Register(ctx context.Context, request spec.RegisterRequest) (*spec.RegisterResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reply, err := c.client.Register(ctx, &wasmpb.RegisterRequest{
		InstanceId: request.InstanceID, Params: request.Params,
	})
	if err != nil {
		return nil, err
	}
	return &spec.RegisterResult{Name: reply.GetName(), Version: reply.GetVersion()}, nil
}

func (c *Conn) HandleHTTP(ctx context.Context, request *spec.HTTPRequest) (*spec.HTTPResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	response, err := c.client.HandleHTTP(ctx, &wasmpb.HTTPRequest{
		RouteId: request.RouteID, RoutePattern: request.RoutePattern, Method: request.Method,
		Path: request.Path, Query: request.Query, Headers: headersToProto(request.Headers),
		Body: request.Body, RemoteAddr: request.RemoteAddr, User: userToProto(request.User),
	})
	if err != nil {
		return nil, err
	}
	return &spec.HTTPResponse{
		Status: int(response.GetStatus()), Headers: headersFromProto(response.GetHeaders()),
		Body: response.GetBody(),
	}, nil
}

func (c *Conn) HandleSocketEvent(ctx context.Context, event *spec.SocketEvent) ([]spec.EmitInstruction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reply, err := c.client.HandleSocketEvent(ctx, &wasmpb.SocketEvent{
		RouteId: event.RouteID, Namespace: event.Namespace, Event: event.Event,
		SocketId: event.SocketID, User: userToProto(event.User), Payload: event.Payload,
	})
	if err != nil {
		return nil, err
	}
	emits := make([]spec.EmitInstruction, 0, len(reply.GetEmits()))
	for _, emit := range reply.GetEmits() {
		emits = append(emits, spec.EmitInstruction{
			Namespace: emit.GetNamespace(), Target: emit.GetTarget(),
			Event: emit.GetEvent(), Payload: emit.GetPayload(),
		})
	}
	return emits, nil
}

func (c *Conn) HandleServiceMessage(ctx context.Context, message *spec.ServiceMessage) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reply, err := c.client.HandleServiceMessage(ctx, &wasmpb.ServiceMessage{
		Source: message.Source, Target: message.Target, Topic: message.Topic, Payload: message.Payload,
	})
	if err != nil {
		return "", err
	}
	if reply.GetError() != "" {
		return "", fmt.Errorf("service message reply: %s", reply.GetError())
	}
	return reply.GetMessage(), nil
}

func headersToProto(headers http.Header) []*wasmpb.Header {
	out := make([]*wasmpb.Header, 0, len(headers))
	for name, values := range headers {
		out = append(out, &wasmpb.Header{Name: name, Values: append([]string(nil), values...)})
	}
	return out
}

func headersFromProto(headers []*wasmpb.Header) http.Header {
	out := make(http.Header, len(headers))
	for _, header := range headers {
		if header != nil {
			out[header.GetName()] = append([]string(nil), header.GetValues()...)
		}
	}
	return out
}

func userToProto(user *spec.User) *wasmpb.User {
	if user == nil {
		return nil
	}
	return &wasmpb.User{Username: user.Username, Groups: append([]string(nil), user.Groups...)}
}

func transportFromProto(in *wasmpb.Transport) (spec.Transport, error) {
	if in == nil {
		return spec.Transport{}, nil
	}
	out := spec.Transport{ID: in.GetId()}
	switch in.GetType() {
	case wasmpb.TransportType_TRANSPORT_TYPE_STATIC:
		out.Type = spec.TransportStatic
		if config := in.GetStatic(); config != nil {
			out.StaticSource = config.GetSource()
		}
	case wasmpb.TransportType_TRANSPORT_TYPE_HTTP:
		out.Type = spec.TransportHTTP
	case wasmpb.TransportType_TRANSPORT_TYPE_SOCKET_IO:
		out.Type = spec.TransportSocketIO
	case wasmpb.TransportType_TRANSPORT_TYPE_PROXY:
		out.Type = spec.TransportProxy
		out.Proxy = proxyFromProto(in.GetProxy())
	default:
		return spec.Transport{}, fmt.Errorf("unsupported transport type %v", in.GetType())
	}
	return out, nil
}

func proxyFromProto(in *wasmpb.ProxyTransport) *spec.ProxyTarget {
	if in == nil {
		return nil
	}
	var network spec.ProxyNetwork
	switch in.GetNetwork() {
	case wasmpb.ProxyNetwork_PROXY_NETWORK_INHERITED:
		network = spec.ProxyInherited
	case wasmpb.ProxyNetwork_PROXY_NETWORK_UNIX:
		network = spec.ProxyUnix
	case wasmpb.ProxyNetwork_PROXY_NETWORK_TCP:
		network = spec.ProxyTCP
	}
	return &spec.ProxyTarget{Network: network, Address: in.GetAddress(), Scheme: in.GetScheme()}
}

func routesFromProto(routes []*wasmpb.Route) ([]spec.Route, error) {
	out := make([]spec.Route, 0, len(routes))
	for _, in := range routes {
		if in == nil {
			continue
		}
		declaration := spec.Route{ID: in.GetId(), TransportID: in.GetTransportId()}
		if httpRoute := in.GetHttp(); httpRoute != nil {
			declaration.HTTP = &spec.HTTPRoute{
				Method: httpRoute.GetMethod(), Pattern: httpRoute.GetPattern(),
				Access: accessFromProto(httpRoute.GetAccess()),
			}
		} else if socketRoute := in.GetSocketIo(); socketRoute != nil {
			eventAccess := make(map[string]spec.AccessPolicy, len(socketRoute.GetEventAccess()))
			for event, access := range socketRoute.GetEventAccess() {
				eventAccess[event] = accessFromProto(access)
			}
			declaration.SocketIO = &spec.SocketIORoute{
				Namespace: socketRoute.GetNamespace(), Events: append([]string(nil), socketRoute.GetEvents()...),
				Access: accessFromProto(socketRoute.GetAccess()), EventAccess: eventAccess,
			}
		} else {
			return nil, fmt.Errorf("route %q has no route kind", declaration.ID)
		}
		out = append(out, declaration)
	}
	return out, nil
}

func accessFromProto(policy *wasmpb.AccessPolicy) spec.AccessPolicy {
	if policy == nil {
		return spec.AccessPolicy{}
	}
	return spec.AccessPolicy{
		RequireAuth: policy.GetRequireAuth(),
		Groups:      append([]string(nil), policy.GetGroups()...),
	}
}

func registrationReply(result spec.RegistrationResult) *wasmpb.RegistrationReply {
	failures := make([]*wasmpb.RegistrationFailure, 0, len(result.Failures))
	for _, failure := range result.Failures {
		failures = append(failures, &wasmpb.RegistrationFailure{Id: failure.ID, Error: failure.Error})
	}
	return &wasmpb.RegistrationReply{
		Registered: result.Registered, Failures: failures,
		Degraded: result.Degraded, Error: result.Error,
	}
}

func singleRegistration(id string, err error) spec.RegistrationResult {
	if err == nil {
		return spec.RegistrationResult{Registered: []string{id}}
	}
	return spec.RegistrationResult{
		Failures: []spec.RegistrationFailure{{ID: id, Error: err.Error()}},
		Error:    err.Error(),
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
