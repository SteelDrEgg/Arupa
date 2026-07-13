package plugin

import (
	"context"
	"fmt"

	grpcpb "arupa/pluginsdk/grpc/proto"
)

// grpcConn adapts a gRPC plugin client to the backend-agnostic pluginConn.
type grpcConn struct {
	client grpcpb.PluginClient
}

func (c grpcConn) Register(ctx context.Context, req RegisterRequest) (*RegisterResult, error) {
	reply, err := c.client.Register(ctx, &grpcpb.RegisterRequest{
		InstanceId:        req.InstanceID,
		HostCallbackAddr:  req.HostCallbackAddr,
		HostCallbackToken: req.HostCallbackToken,
		Params:            req.Params,
	})
	if err != nil {
		return nil, err
	}
	res := &RegisterResult{Name: reply.GetName(), Version: reply.GetVersion()}
	for _, r := range reply.GetHttpRoutes() {
		res.Routes = append(res.Routes, HTTPRoute{
			Method:  r.GetMethod(),
			Pattern: r.GetPattern(),
			Access:  accessFromGRPC(r.GetAccess()),
		})
	}
	for _, s := range reply.GetStaticMounts() {
		res.Static = append(res.Static, StaticMount{
			Prefix:    s.GetPrefix(),
			Directory: s.GetDirectory(),
			Access:    accessFromGRPC(s.GetAccess()),
		})
	}
	for _, ns := range reply.GetSocketNamespaces() {
		eventAccess := make(map[string]AccessPolicy, len(ns.GetEventAccess()))
		for event, policy := range ns.GetEventAccess() {
			eventAccess[event] = accessFromGRPC(policy)
		}
		res.Namespaces = append(res.Namespaces, SocketNamespaceDecl{
			Name:        ns.GetName(),
			Events:      ns.GetEvents(),
			Access:      accessFromGRPC(ns.GetAccess()),
			EventAccess: eventAccess,
		})
	}
	return res, nil
}

func (c grpcConn) HandleHTTP(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error) {
	resp, err := c.client.HandleHTTP(ctx, &grpcpb.HTTPRequest{
		RoutePattern: req.RoutePattern,
		Method:       req.Method,
		Path:         req.Path,
		Query:        req.Query,
		Headers:      req.Headers,
		Body:         req.Body,
		RemoteAddr:   req.RemoteAddr,
		User:         userToGRPC(req.User),
	})
	if err != nil {
		return nil, err
	}
	return &HTTPResponse{
		Status:  int(resp.GetStatus()),
		Headers: resp.GetHeaders(),
		Body:    resp.GetBody(),
	}, nil
}

func (c grpcConn) HandleSocketEvent(ctx context.Context, ev *SocketEvent) ([]EmitInstruction, error) {
	reply, err := c.client.HandleSocketEvent(ctx, &grpcpb.SocketEvent{
		Namespace: ev.Namespace,
		Event:     ev.Event,
		SocketId:  ev.SocketID,
		User:      userToGRPC(ev.User),
		Payload:   ev.Payload,
	})
	if err != nil {
		return nil, err
	}
	var emits []EmitInstruction
	for _, e := range reply.GetEmits() {
		emits = append(emits, EmitInstruction{
			Namespace: e.GetNamespace(),
			Target:    e.GetTarget(),
			Event:     e.GetEvent(),
			Payload:   e.GetPayload(),
		})
	}
	return emits, nil
}

func (c grpcConn) HandlePluginMessage(ctx context.Context, msg *PluginMessage) error {
	reply, err := c.client.HandlePluginMessage(ctx, &grpcpb.PluginMessage{
		Source:  msg.Source,
		Target:  msg.Target,
		Topic:   msg.Topic,
		Payload: msg.Payload,
	})
	if err != nil {
		return err
	}
	if reply.GetError() != "" {
		return fmt.Errorf("plugin message reply: %s", reply.GetError())
	}
	return nil
}
