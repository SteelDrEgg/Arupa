// Package grpc adapts the Service v2 gRPC protocol to backend-neutral service
// contracts and Host capabilities.
package grpc

import (
	"context"
	"fmt"
	"net/http"

	"arupa/internal/service/host"
	"arupa/internal/service/spec"
	grpcpb "arupa/servicesdk/grpc/proto"

	goservice "github.com/SteelDrEgg/go-plugin"
	googlegrpc "google.golang.org/grpc"
)

type Conn struct {
	client grpcpb.ServiceClient
	broker *goservice.GRPCBroker
}

func NewConn(client grpcpb.ServiceClient, broker *goservice.GRPCBroker) Conn {
	return Conn{client: client, broker: broker}
}

func (c Conn) Broker() *goservice.GRPCBroker { return c.broker }

func (c Conn) Register(ctx context.Context, request spec.RegisterRequest) (*spec.RegisterResult, error) {
	listeners := make([]*grpcpb.InheritedListener, 0, len(request.Listeners))
	for _, listener := range request.Listeners {
		listeners = append(listeners, &grpcpb.InheritedListener{
			Id: listener.ID, Fd: listener.FD, Network: listener.Network, Address: listener.Address,
		})
	}
	reply, err := c.client.Register(ctx, &grpcpb.RegisterRequest{
		InstanceId: request.InstanceID, Params: request.Params,
		Listeners: listeners, HostBrokerId: request.HostBrokerID,
	})
	if err != nil {
		return nil, err
	}
	return &spec.RegisterResult{Name: reply.GetName(), Version: reply.GetVersion()}, nil
}

func (c Conn) HandleHTTP(ctx context.Context, request *spec.HTTPRequest) (*spec.HTTPResponse, error) {
	response, err := c.client.HandleHTTP(ctx, &grpcpb.HTTPRequest{
		RouteId: request.RouteID, RoutePattern: request.RoutePattern,
		Method: request.Method, Path: request.Path, Query: request.Query,
		Headers: headersToProto(request.Headers), Body: request.Body,
		RemoteAddr: request.RemoteAddr, User: userToProto(request.User),
	})
	if err != nil {
		return nil, err
	}
	return &spec.HTTPResponse{
		Status: int(response.GetStatus()), Headers: headersFromProto(response.GetHeaders()),
		Body: response.GetBody(),
	}, nil
}

func (c Conn) HandleSocketEvent(ctx context.Context, event *spec.SocketEvent) ([]spec.EmitInstruction, error) {
	reply, err := c.client.HandleSocketEvent(ctx, &grpcpb.SocketEvent{
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

func (c Conn) HandleServiceMessage(ctx context.Context, message *spec.ServiceMessage) (string, error) {
	reply, err := c.client.HandleServiceMessage(ctx, &grpcpb.ServiceMessage{
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

type hostServer struct {
	grpcpb.UnimplementedHostServer
	api    *host.API
	source string
}

// StartHostBroker exposes Host capabilities on a caller-scoped go-plugin
// broker stream. The source identity is fixed by the kernel.
func StartHostBroker(broker *goservice.GRPCBroker, api *host.API, source string) (uint32, func(), error) {
	if broker == nil {
		return 0, nil, fmt.Errorf("go-plugin gRPC broker is unavailable")
	}
	id := broker.NextID()
	go broker.AcceptAndServe(id, func(server *googlegrpc.Server) {
		grpcpb.RegisterHostServer(server, &hostServer{api: api, source: source})
	})
	return id, func() {}, nil
}

func (s *hostServer) KVGet(_ context.Context, request *grpcpb.KVGetRequest) (*grpcpb.KVGetReply, error) {
	value, ok := s.api.KVGet(request.GetNamespace(), request.GetKey())
	return &grpcpb.KVGetReply{Found: ok, Value: value}, nil
}

func (s *hostServer) KVSet(_ context.Context, request *grpcpb.KVSetRequest) (*grpcpb.KVSetReply, error) {
	return &grpcpb.KVSetReply{Error: errorString(s.api.KVSet(request.GetNamespace(), request.GetKey(), request.GetValue()))}, nil
}

func (s *hostServer) KVDelete(_ context.Context, request *grpcpb.KVDeleteRequest) (*grpcpb.KVDeleteReply, error) {
	return &grpcpb.KVDeleteReply{Error: errorString(s.api.KVDelete(request.GetNamespace(), request.GetKey()))}, nil
}

func (s *hostServer) KVList(_ context.Context, request *grpcpb.KVListRequest) (*grpcpb.KVListReply, error) {
	return &grpcpb.KVListReply{Keys: s.api.KVList(request.GetNamespace())}, nil
}

func (s *hostServer) GetParams(_ context.Context, _ *grpcpb.ParamsGetRequest) (*grpcpb.ParamsGetReply, error) {
	params, err := s.api.GetParams(s.source)
	return &grpcpb.ParamsGetReply{Params: params, Error: errorString(err)}, nil
}

func (s *hostServer) PatchParams(_ context.Context, request *grpcpb.ParamsPatchRequest) (*grpcpb.ParamsPatchReply, error) {
	err := s.api.PatchParams(s.source, spec.ParamsPatch{Set: request.GetSet(), Delete: request.GetDelete()})
	return &grpcpb.ParamsPatchReply{Error: errorString(err)}, nil
}

func (s *hostServer) Emit(_ context.Context, request *grpcpb.EmitInstruction) (*grpcpb.EmitReply, error) {
	err := s.api.Emit(spec.EmitInstruction{
		Namespace: request.GetNamespace(), Target: request.GetTarget(),
		Event: request.GetEvent(), Payload: request.GetPayload(),
	})
	return &grpcpb.EmitReply{Error: errorString(err)}, nil
}

func (s *hostServer) SendServiceMessage(ctx context.Context, request *grpcpb.ServiceMessage) (*grpcpb.ServiceMessageReply, error) {
	message, err := s.api.ServiceMessage(ctx, s.source, spec.ServiceMessage{
		Target: request.GetTarget(), Topic: request.GetTopic(), Payload: request.GetPayload(),
	})
	return &grpcpb.ServiceMessageReply{Message: message, Error: errorString(err)}, nil
}

func (s *hostServer) RegisterTransport(_ context.Context, request *grpcpb.RegisterTransportRequest) (*grpcpb.RegistrationReply, error) {
	declaration, err := transportFromProto(request.GetTransport())
	if err == nil {
		err = s.api.RegisterTransport(s.source, declaration)
	}
	return registrationReply(singleRegistration(declaration.ID, err)), nil
}

func (s *hostServer) UnregisterTransport(_ context.Context, request *grpcpb.UnregisterTransportRequest) (*grpcpb.RegistrationReply, error) {
	return registrationReply(singleRegistration(request.GetId(), s.api.UnregisterTransport(s.source, request.GetId()))), nil
}

func (s *hostServer) RegisterRoutes(_ context.Context, request *grpcpb.RegisterRoutesRequest) (*grpcpb.RegistrationReply, error) {
	routes, err := routesFromProto(request.GetRoutes())
	if err != nil {
		return registrationReply(spec.RegistrationResult{Degraded: true, Error: err.Error()}), nil
	}
	return registrationReply(s.api.RegisterRoutes(s.source, routes)), nil
}

func (s *hostServer) UnregisterRoutes(_ context.Context, request *grpcpb.UnregisterRoutesRequest) (*grpcpb.RegistrationReply, error) {
	return registrationReply(s.api.UnregisterRoutes(s.source, request.GetIds())), nil
}

func (s *hostServer) Log(_ context.Context, request *grpcpb.LogRequest) (*grpcpb.LogReply, error) {
	s.api.Log(s.source, request.GetLevel(), request.GetMessage())
	return &grpcpb.LogReply{}, nil
}

func headersToProto(headers http.Header) []*grpcpb.Header {
	out := make([]*grpcpb.Header, 0, len(headers))
	for name, values := range headers {
		out = append(out, &grpcpb.Header{Name: name, Values: append([]string(nil), values...)})
	}
	return out
}

func headersFromProto(headers []*grpcpb.Header) http.Header {
	out := make(http.Header, len(headers))
	for _, header := range headers {
		if header != nil {
			out[header.GetName()] = append([]string(nil), header.GetValues()...)
		}
	}
	return out
}

func userToProto(user *spec.User) *grpcpb.User {
	if user == nil {
		return nil
	}
	return &grpcpb.User{Username: user.Username, Groups: append([]string(nil), user.Groups...)}
}

func transportFromProto(in *grpcpb.Transport) (spec.Transport, error) {
	if in == nil {
		return spec.Transport{}, nil
	}
	out := spec.Transport{ID: in.GetId()}
	switch in.GetType() {
	case grpcpb.TransportType_TRANSPORT_TYPE_STATIC:
		out.Type = spec.TransportStatic
		if config := in.GetStatic(); config != nil {
			out.StaticSource = config.GetSource()
		}
	case grpcpb.TransportType_TRANSPORT_TYPE_HTTP:
		out.Type = spec.TransportHTTP
	case grpcpb.TransportType_TRANSPORT_TYPE_SOCKET_IO:
		out.Type = spec.TransportSocketIO
	case grpcpb.TransportType_TRANSPORT_TYPE_PROXY:
		out.Type = spec.TransportProxy
		out.Proxy = proxyFromProto(in.GetProxy())
	default:
		return spec.Transport{}, fmt.Errorf("unsupported transport type %v", in.GetType())
	}
	return out, nil
}

func proxyFromProto(in *grpcpb.ProxyTransport) *spec.ProxyTarget {
	if in == nil {
		return nil
	}
	var network spec.ProxyNetwork
	switch in.GetNetwork() {
	case grpcpb.ProxyNetwork_PROXY_NETWORK_INHERITED:
		network = spec.ProxyInherited
	case grpcpb.ProxyNetwork_PROXY_NETWORK_UNIX:
		network = spec.ProxyUnix
	case grpcpb.ProxyNetwork_PROXY_NETWORK_TCP:
		network = spec.ProxyTCP
	}
	return &spec.ProxyTarget{Network: network, Address: in.GetAddress(), Scheme: in.GetScheme()}
}

func routesFromProto(routes []*grpcpb.Route) ([]spec.Route, error) {
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

func accessFromProto(policy *grpcpb.AccessPolicy) spec.AccessPolicy {
	if policy == nil {
		return spec.AccessPolicy{}
	}
	return spec.AccessPolicy{
		RequireAuth: policy.GetRequireAuth(),
		Groups:      append([]string(nil), policy.GetGroups()...),
	}
}

func registrationReply(result spec.RegistrationResult) *grpcpb.RegistrationReply {
	failures := make([]*grpcpb.RegistrationFailure, 0, len(result.Failures))
	for _, failure := range result.Failures {
		failures = append(failures, &grpcpb.RegistrationFailure{Id: failure.ID, Error: failure.Error})
	}
	return &grpcpb.RegistrationReply{
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
