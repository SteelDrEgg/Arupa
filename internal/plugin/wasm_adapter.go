package plugin

import (
	"context"
	"fmt"
	"sync"

	wasmpb "arupa/pluginsdk/wasm/proto"
)

// wasmHostFns implements the generated wasmpb.Host interface by delegating to
// the shared HostAPI. An instance of this is passed to each WASM module so the
// module's host-function imports resolve to real host behavior.
type wasmHostFns struct {
	api    *HostAPI
	source string
}

func (w wasmHostFns) KVGet(_ context.Context, req *wasmpb.KVGetRequest) (*wasmpb.KVGetReply, error) {
	v, ok := w.api.KVGet(req.GetNamespace(), req.GetKey())
	return &wasmpb.KVGetReply{Found: ok, Value: v}, nil
}

func (w wasmHostFns) KVSet(_ context.Context, req *wasmpb.KVSetRequest) (*wasmpb.KVSetReply, error) {
	var errStr string
	if err := w.api.KVSet(req.GetNamespace(), req.GetKey(), req.GetValue()); err != nil {
		errStr = err.Error()
	}
	return &wasmpb.KVSetReply{Error: errStr}, nil
}

func (w wasmHostFns) KVDelete(_ context.Context, req *wasmpb.KVDeleteRequest) (*wasmpb.KVDeleteReply, error) {
	var errStr string
	if err := w.api.KVDelete(req.GetNamespace(), req.GetKey()); err != nil {
		errStr = err.Error()
	}
	return &wasmpb.KVDeleteReply{Error: errStr}, nil
}

func (w wasmHostFns) KVList(_ context.Context, req *wasmpb.KVListRequest) (*wasmpb.KVListReply, error) {
	return &wasmpb.KVListReply{Keys: w.api.KVList(req.GetNamespace())}, nil
}

func (w wasmHostFns) PatchParams(_ context.Context, req *wasmpb.ParamsPatchRequest) (*wasmpb.ParamsPatchReply, error) {
	var errStr string
	if err := w.api.PatchParams(w.source, ParamsPatch{
		Set:    req.GetSet(),
		Delete: req.GetDelete(),
	}); err != nil {
		errStr = err.Error()
	}
	return &wasmpb.ParamsPatchReply{Error: errStr}, nil
}

func (w wasmHostFns) Emit(_ context.Context, req *wasmpb.EmitInstruction) (*wasmpb.EmitReply, error) {
	var errStr string
	if err := w.api.Emit(EmitInstruction{
		Namespace: req.GetNamespace(),
		Target:    req.GetTarget(),
		Event:     req.GetEvent(),
		Payload:   req.GetPayload(),
	}); err != nil {
		errStr = err.Error()
	}
	return &wasmpb.EmitReply{Error: errStr}, nil
}

func (w wasmHostFns) SendPluginMessage(ctx context.Context, req *wasmpb.PluginMessage) (*wasmpb.PluginMessageReply, error) {
	var errStr string
	message, err := w.api.PluginMessage(ctx, w.source, PluginMessage{
		Target:  req.GetTarget(),
		Topic:   req.GetTopic(),
		Payload: req.GetPayload(),
	})
	if err != nil {
		errStr = err.Error()
	}
	return &wasmpb.PluginMessageReply{Error: errStr, Message: message}, nil
}

func (w wasmHostFns) Log(_ context.Context, req *wasmpb.LogRequest) (*wasmpb.LogReply, error) {
	w.api.Log(w.source, req.GetLevel(), req.GetMessage())
	return &wasmpb.LogReply{}, nil
}

// wasmConn adapts a loaded WASM plugin (wasmpb.Plugin) to the backend-agnostic
// pluginConn interface.
type wasmConn struct {
	client wasmpb.Plugin
	mu     sync.Mutex
}

func newWASMConn(client wasmpb.Plugin) *wasmConn {
	return &wasmConn{client: client}
}

func (c *wasmConn) Register(ctx context.Context, req RegisterRequest) (*RegisterResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	reply, err := c.client.Register(ctx, &wasmpb.RegisterRequest{
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
			Access:  accessFromWASM(r.GetAccess()),
		})
	}
	for _, s := range reply.GetStaticMounts() {
		res.Static = append(res.Static, StaticMount{
			Prefix:    s.GetPrefix(),
			Directory: s.GetDirectory(),
			Access:    accessFromWASM(s.GetAccess()),
		})
	}
	for _, ns := range reply.GetSocketNamespaces() {
		eventAccess := make(map[string]AccessPolicy, len(ns.GetEventAccess()))
		for event, policy := range ns.GetEventAccess() {
			eventAccess[event] = accessFromWASM(policy)
		}
		res.Namespaces = append(res.Namespaces, SocketNamespaceDecl{
			Name:        ns.GetName(),
			Events:      ns.GetEvents(),
			Access:      accessFromWASM(ns.GetAccess()),
			EventAccess: eventAccess,
		})
	}
	return res, nil
}

func (c *wasmConn) HandleHTTP(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	resp, err := c.client.HandleHTTP(ctx, &wasmpb.HTTPRequest{
		RoutePattern: req.RoutePattern,
		Method:       req.Method,
		Path:         req.Path,
		Query:        req.Query,
		Headers:      req.Headers,
		Body:         req.Body,
		RemoteAddr:   req.RemoteAddr,
		User:         userToWASM(req.User),
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

func (c *wasmConn) HandleSocketEvent(ctx context.Context, ev *SocketEvent) ([]EmitInstruction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	reply, err := c.client.HandleSocketEvent(ctx, &wasmpb.SocketEvent{
		Namespace: ev.Namespace,
		Event:     ev.Event,
		SocketId:  ev.SocketID,
		User:      userToWASM(ev.User),
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

func (c *wasmConn) HandlePluginMessage(ctx context.Context, msg *PluginMessage) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	reply, err := c.client.HandlePluginMessage(ctx, &wasmpb.PluginMessage{
		Source:  msg.Source,
		Target:  msg.Target,
		Topic:   msg.Topic,
		Payload: msg.Payload,
	})
	if err != nil {
		return "", err
	}
	if reply.GetError() != "" {
		return "", fmt.Errorf("plugin message reply: %s", reply.GetError())
	}
	return reply.GetMessage(), nil
}
