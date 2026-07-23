package service

import (
	"context"
	"fmt"

	grpcpb "arupa/servicesdk/grpc/proto"
	goservice "github.com/SteelDrEgg/go-plugin"
)

type grpcConn struct {
	client grpcpb.ServiceClient
	broker *goservice.GRPCBroker
}

func (c grpcConn) Register(ctx context.Context, req RegisterRequest) (*RegisterResult, error) {
	listeners := make([]*grpcpb.InheritedListener, 0, len(req.Listeners))
	for _, listener := range req.Listeners {
		listeners = append(listeners, &grpcpb.InheritedListener{
			Id: listener.ID, Fd: listener.FD, Network: listener.Network, Address: listener.Address,
		})
	}
	reply, err := c.client.Register(ctx, &grpcpb.RegisterRequest{
		InstanceId: req.InstanceID, Params: req.Params,
		Listeners: listeners, HostBrokerId: req.HostBrokerID,
	})
	if err != nil {
		return nil, err
	}
	return &RegisterResult{Name: reply.GetName(), Version: reply.GetVersion()}, nil
}

func (c grpcConn) HandleHTTP(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error) {
	resp, err := c.client.HandleHTTP(ctx, &grpcpb.HTTPRequest{
		RouteId:      req.RouteID,
		RoutePattern: req.RoutePattern,
		Method:       req.Method,
		Path:         req.Path,
		Query:        req.Query,
		Headers:      headersToGRPC(req.Headers),
		Body:         req.Body,
		RemoteAddr:   req.RemoteAddr,
		User:         userToGRPC(req.User),
	})
	if err != nil {
		return nil, err
	}
	return &HTTPResponse{
		Status:  int(resp.GetStatus()),
		Headers: headersFromGRPC(resp.GetHeaders()),
		Body:    resp.GetBody(),
	}, nil
}

func (c grpcConn) HandleSocketEvent(ctx context.Context, ev *SocketEvent) ([]EmitInstruction, error) {
	reply, err := c.client.HandleSocketEvent(ctx, &grpcpb.SocketEvent{
		RouteId: ev.RouteID, Namespace: ev.Namespace, Event: ev.Event,
		SocketId: ev.SocketID, User: userToGRPC(ev.User), Payload: ev.Payload,
	})
	if err != nil {
		return nil, err
	}
	emits := make([]EmitInstruction, 0, len(reply.GetEmits()))
	for _, emit := range reply.GetEmits() {
		emits = append(emits, EmitInstruction{
			Namespace: emit.GetNamespace(), Target: emit.GetTarget(),
			Event: emit.GetEvent(), Payload: emit.GetPayload(),
		})
	}
	return emits, nil
}

func (c grpcConn) HandleServiceMessage(ctx context.Context, msg *ServiceMessage) (string, error) {
	reply, err := c.client.HandleServiceMessage(ctx, &grpcpb.ServiceMessage{
		Source: msg.Source, Target: msg.Target, Topic: msg.Topic, Payload: msg.Payload,
	})
	if err != nil {
		return "", err
	}
	if reply.GetError() != "" {
		return "", fmt.Errorf("service message reply: %s", reply.GetError())
	}
	return reply.GetMessage(), nil
}
