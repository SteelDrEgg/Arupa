package service

import (
	"fmt"
	"net/http"

	grpcpb "arupa/servicesdk/grpc/proto"
	wasmpb "arupa/servicesdk/wasm/proto"
)

func accessFromGRPC(policy *grpcpb.AccessPolicy) AccessPolicy {
	if policy == nil {
		return AccessPolicy{}
	}
	return AccessPolicy{
		RequireAuth: policy.GetRequireAuth(),
		Groups:      append([]string(nil), policy.GetGroups()...),
	}
}

func accessFromWASM(policy *wasmpb.AccessPolicy) AccessPolicy {
	if policy == nil {
		return AccessPolicy{}
	}
	return AccessPolicy{
		RequireAuth: policy.GetRequireAuth(),
		Groups:      append([]string(nil), policy.GetGroups()...),
	}
}

func userToGRPC(user *User) *grpcpb.User {
	if user == nil {
		return nil
	}
	return &grpcpb.User{Username: user.Username, Groups: append([]string(nil), user.Groups...)}
}

func userToWASM(user *User) *wasmpb.User {
	if user == nil {
		return nil
	}
	return &wasmpb.User{Username: user.Username, Groups: append([]string(nil), user.Groups...)}
}

func headersToGRPC(headers http.Header) []*grpcpb.Header {
	out := make([]*grpcpb.Header, 0, len(headers))
	for name, values := range headers {
		out = append(out, &grpcpb.Header{Name: name, Values: append([]string(nil), values...)})
	}
	return out
}

func headersFromGRPC(headers []*grpcpb.Header) http.Header {
	out := make(http.Header, len(headers))
	for _, header := range headers {
		if header != nil {
			out[header.GetName()] = append([]string(nil), header.GetValues()...)
		}
	}
	return out
}

func headersToWASM(headers http.Header) []*wasmpb.Header {
	out := make([]*wasmpb.Header, 0, len(headers))
	for name, values := range headers {
		out = append(out, &wasmpb.Header{Name: name, Values: append([]string(nil), values...)})
	}
	return out
}

func headersFromWASM(headers []*wasmpb.Header) http.Header {
	out := make(http.Header, len(headers))
	for _, header := range headers {
		if header != nil {
			out[header.GetName()] = append([]string(nil), header.GetValues()...)
		}
	}
	return out
}

func transportFromGRPC(in *grpcpb.Transport) (Transport, error) {
	if in == nil {
		return Transport{}, nil
	}
	out := Transport{ID: in.GetId()}
	switch in.GetType() {
	case grpcpb.TransportType_TRANSPORT_TYPE_STATIC:
		out.Type = TransportStatic
		if cfg := in.GetStatic(); cfg != nil {
			out.StaticSource = cfg.GetSource()
		}
	case grpcpb.TransportType_TRANSPORT_TYPE_HTTP:
		out.Type = TransportHTTP
	case grpcpb.TransportType_TRANSPORT_TYPE_SOCKET_IO:
		out.Type = TransportSocketIO
	case grpcpb.TransportType_TRANSPORT_TYPE_PROXY:
		out.Type = TransportProxy
		out.Proxy = proxyFromGRPC(in.GetProxy())
	default:
		return Transport{}, fmt.Errorf("unsupported transport type %v", in.GetType())
	}
	return out, nil
}

func proxyFromGRPC(in *grpcpb.ProxyTransport) *ProxyTarget {
	if in == nil {
		return nil
	}
	network := ProxyNetwork("")
	switch in.GetNetwork() {
	case grpcpb.ProxyNetwork_PROXY_NETWORK_INHERITED:
		network = ProxyInherited
	case grpcpb.ProxyNetwork_PROXY_NETWORK_UNIX:
		network = ProxyUnix
	case grpcpb.ProxyNetwork_PROXY_NETWORK_TCP:
		network = ProxyTCP
	}
	return &ProxyTarget{Network: network, Address: in.GetAddress(), Scheme: in.GetScheme()}
}

func routesFromGRPC(routes []*grpcpb.Route) ([]Route, error) {
	out := make([]Route, 0, len(routes))
	for _, in := range routes {
		if in == nil {
			continue
		}
		route := Route{ID: in.GetId(), TransportID: in.GetTransportId()}
		if httpRoute := in.GetHttp(); httpRoute != nil {
			route.HTTP = &HTTPRoute{
				Method: httpRoute.GetMethod(), Pattern: httpRoute.GetPattern(),
				Access: accessFromGRPC(httpRoute.GetAccess()),
			}
		} else if socketRoute := in.GetSocketIo(); socketRoute != nil {
			eventAccess := make(map[string]AccessPolicy, len(socketRoute.GetEventAccess()))
			for event, access := range socketRoute.GetEventAccess() {
				eventAccess[event] = accessFromGRPC(access)
			}
			route.SocketIO = &SocketIORoute{
				Namespace: socketRoute.GetNamespace(), Events: append([]string(nil), socketRoute.GetEvents()...),
				Access: accessFromGRPC(socketRoute.GetAccess()), EventAccess: eventAccess,
			}
		} else {
			return nil, fmt.Errorf("route %q has no route kind", route.ID)
		}
		out = append(out, route)
	}
	return out, nil
}

func transportFromWASM(in *wasmpb.Transport) (Transport, error) {
	if in == nil {
		return Transport{}, nil
	}
	out := Transport{ID: in.GetId()}
	switch in.GetType() {
	case wasmpb.TransportType_TRANSPORT_TYPE_STATIC:
		out.Type = TransportStatic
		if cfg := in.GetStatic(); cfg != nil {
			out.StaticSource = cfg.GetSource()
		}
	case wasmpb.TransportType_TRANSPORT_TYPE_HTTP:
		out.Type = TransportHTTP
	case wasmpb.TransportType_TRANSPORT_TYPE_SOCKET_IO:
		out.Type = TransportSocketIO
	case wasmpb.TransportType_TRANSPORT_TYPE_PROXY:
		out.Type = TransportProxy
		out.Proxy = proxyFromWASM(in.GetProxy())
	default:
		return Transport{}, fmt.Errorf("unsupported transport type %v", in.GetType())
	}
	return out, nil
}

func proxyFromWASM(in *wasmpb.ProxyTransport) *ProxyTarget {
	if in == nil {
		return nil
	}
	network := ProxyNetwork("")
	switch in.GetNetwork() {
	case wasmpb.ProxyNetwork_PROXY_NETWORK_INHERITED:
		network = ProxyInherited
	case wasmpb.ProxyNetwork_PROXY_NETWORK_UNIX:
		network = ProxyUnix
	case wasmpb.ProxyNetwork_PROXY_NETWORK_TCP:
		network = ProxyTCP
	}
	return &ProxyTarget{Network: network, Address: in.GetAddress(), Scheme: in.GetScheme()}
}

func routesFromWASM(routes []*wasmpb.Route) ([]Route, error) {
	out := make([]Route, 0, len(routes))
	for _, in := range routes {
		if in == nil {
			continue
		}
		route := Route{ID: in.GetId(), TransportID: in.GetTransportId()}
		if httpRoute := in.GetHttp(); httpRoute != nil {
			route.HTTP = &HTTPRoute{
				Method: httpRoute.GetMethod(), Pattern: httpRoute.GetPattern(),
				Access: accessFromWASM(httpRoute.GetAccess()),
			}
		} else if socketRoute := in.GetSocketIo(); socketRoute != nil {
			eventAccess := make(map[string]AccessPolicy, len(socketRoute.GetEventAccess()))
			for event, access := range socketRoute.GetEventAccess() {
				eventAccess[event] = accessFromWASM(access)
			}
			route.SocketIO = &SocketIORoute{
				Namespace: socketRoute.GetNamespace(), Events: append([]string(nil), socketRoute.GetEvents()...),
				Access: accessFromWASM(socketRoute.GetAccess()), EventAccess: eventAccess,
			}
		} else {
			return nil, fmt.Errorf("route %q has no route kind", route.ID)
		}
		out = append(out, route)
	}
	return out, nil
}
