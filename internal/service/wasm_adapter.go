package service

import (
	"context"
	"fmt"
	"sync"

	wasmpb "arupa/servicesdk/wasm/proto"
)

type wasmHostFns struct {
	api    *HostAPI
	source string
}

func (w wasmHostFns) KVGet(_ context.Context, req *wasmpb.KVGetRequest) (*wasmpb.KVGetReply, error) {
	value, ok := w.api.KVGet(req.GetNamespace(), req.GetKey())
	return &wasmpb.KVGetReply{Found: ok, Value: value}, nil
}

func (w wasmHostFns) KVSet(_ context.Context, req *wasmpb.KVSetRequest) (*wasmpb.KVSetReply, error) {
	return &wasmpb.KVSetReply{Error: errorString(w.api.KVSet(req.GetNamespace(), req.GetKey(), req.GetValue()))}, nil
}

func (w wasmHostFns) KVDelete(_ context.Context, req *wasmpb.KVDeleteRequest) (*wasmpb.KVDeleteReply, error) {
	return &wasmpb.KVDeleteReply{Error: errorString(w.api.KVDelete(req.GetNamespace(), req.GetKey()))}, nil
}

func (w wasmHostFns) KVList(_ context.Context, req *wasmpb.KVListRequest) (*wasmpb.KVListReply, error) {
	return &wasmpb.KVListReply{Keys: w.api.KVList(req.GetNamespace())}, nil
}

func (w wasmHostFns) GetParams(_ context.Context, _ *wasmpb.ParamsGetRequest) (*wasmpb.ParamsGetReply, error) {
	params, err := w.api.GetParams(w.source)
	return &wasmpb.ParamsGetReply{Params: params, Error: errorString(err)}, nil
}

func (w wasmHostFns) PatchParams(_ context.Context, req *wasmpb.ParamsPatchRequest) (*wasmpb.ParamsPatchReply, error) {
	err := w.api.PatchParams(w.source, ParamsPatch{Set: req.GetSet(), Delete: req.GetDelete()})
	return &wasmpb.ParamsPatchReply{Error: errorString(err)}, nil
}

func (w wasmHostFns) Emit(_ context.Context, req *wasmpb.EmitInstruction) (*wasmpb.EmitReply, error) {
	err := w.api.Emit(EmitInstruction{
		Namespace: req.GetNamespace(), Target: req.GetTarget(),
		Event: req.GetEvent(), Payload: req.GetPayload(),
	})
	return &wasmpb.EmitReply{Error: errorString(err)}, nil
}

func (w wasmHostFns) SendServiceMessage(ctx context.Context, req *wasmpb.ServiceMessage) (*wasmpb.ServiceMessageReply, error) {
	message, err := w.api.ServiceMessage(ctx, w.source, ServiceMessage{
		Target: req.GetTarget(), Topic: req.GetTopic(), Payload: req.GetPayload(),
	})
	return &wasmpb.ServiceMessageReply{Message: message, Error: errorString(err)}, nil
}

func (w wasmHostFns) RegisterTransport(_ context.Context, req *wasmpb.RegisterTransportRequest) (*wasmpb.RegistrationReply, error) {
	transport, err := transportFromWASM(req.GetTransport())
	if err == nil {
		err = w.api.RegisterTransport(w.source, transport)
	}
	return registrationReplyToWASM(singleRegistration(transport.ID, err)), nil
}

func (w wasmHostFns) UnregisterTransport(_ context.Context, req *wasmpb.UnregisterTransportRequest) (*wasmpb.RegistrationReply, error) {
	return registrationReplyToWASM(singleRegistration(req.GetId(), w.api.UnregisterTransport(w.source, req.GetId()))), nil
}

func (w wasmHostFns) RegisterRoutes(_ context.Context, req *wasmpb.RegisterRoutesRequest) (*wasmpb.RegistrationReply, error) {
	routes, err := routesFromWASM(req.GetRoutes())
	if err != nil {
		return registrationReplyToWASM(RegistrationResult{Degraded: true, Error: err.Error()}), nil
	}
	return registrationReplyToWASM(w.api.RegisterRoutes(w.source, routes)), nil
}

func (w wasmHostFns) UnregisterRoutes(_ context.Context, req *wasmpb.UnregisterRoutesRequest) (*wasmpb.RegistrationReply, error) {
	return registrationReplyToWASM(w.api.UnregisterRoutes(w.source, req.GetIds())), nil
}

func (w wasmHostFns) Log(_ context.Context, req *wasmpb.LogRequest) (*wasmpb.LogReply, error) {
	w.api.Log(w.source, req.GetLevel(), req.GetMessage())
	return &wasmpb.LogReply{}, nil
}

type wasmConn struct {
	client wasmpb.Service
	mu     sync.Mutex
}

func newWASMConn(client wasmpb.Service) *wasmConn {
	return &wasmConn{client: client}
}

func (c *wasmConn) Register(ctx context.Context, req RegisterRequest) (*RegisterResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reply, err := c.client.Register(ctx, &wasmpb.RegisterRequest{
		InstanceId: req.InstanceID, Params: req.Params,
	})
	if err != nil {
		return nil, err
	}
	return &RegisterResult{Name: reply.GetName(), Version: reply.GetVersion()}, nil
}

func (c *wasmConn) HandleHTTP(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	resp, err := c.client.HandleHTTP(ctx, &wasmpb.HTTPRequest{
		RouteId: req.RouteID, RoutePattern: req.RoutePattern, Method: req.Method,
		Path: req.Path, Query: req.Query, Headers: headersToWASM(req.Headers),
		Body: req.Body, RemoteAddr: req.RemoteAddr, User: userToWASM(req.User),
	})
	if err != nil {
		return nil, err
	}
	return &HTTPResponse{
		Status: int(resp.GetStatus()), Headers: headersFromWASM(resp.GetHeaders()), Body: resp.GetBody(),
	}, nil
}

func (c *wasmConn) HandleSocketEvent(ctx context.Context, ev *SocketEvent) ([]EmitInstruction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reply, err := c.client.HandleSocketEvent(ctx, &wasmpb.SocketEvent{
		RouteId: ev.RouteID, Namespace: ev.Namespace, Event: ev.Event,
		SocketId: ev.SocketID, User: userToWASM(ev.User), Payload: ev.Payload,
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

func (c *wasmConn) HandleServiceMessage(ctx context.Context, msg *ServiceMessage) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reply, err := c.client.HandleServiceMessage(ctx, &wasmpb.ServiceMessage{
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

func registrationReplyToWASM(result RegistrationResult) *wasmpb.RegistrationReply {
	failures := make([]*wasmpb.RegistrationFailure, 0, len(result.Failures))
	for _, failure := range result.Failures {
		failures = append(failures, &wasmpb.RegistrationFailure{Id: failure.ID, Error: failure.Error})
	}
	return &wasmpb.RegistrationReply{
		Registered: result.Registered, Failures: failures,
		Degraded: result.Degraded, Error: result.Error,
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
