package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"sync"

	grpcpb "arupa/pluginsdk/grpc/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// hostCallbackTokenMD is the gRPC metadata key plugins use to authenticate
// against the host callback server.
const hostCallbackTokenMD = "x-panel-token"

type grpcPluginSourceKey struct{}

// grpcHostServer is a single, host-run gRPC server implementing the Host
// callback service. Every gRPC plugin dials it (using a per-instance token) to
// perform KV, Emit and Log operations.
type grpcHostServer struct {
	grpcpb.UnimplementedHostServer

	api *HostAPI

	srv      *grpc.Server
	listener net.Listener

	mu     sync.RWMutex
	tokens map[string]string // token -> instance id
}

func newGRPCHostServer(api *HostAPI) *grpcHostServer {
	return &grpcHostServer{
		api:    api,
		tokens: make(map[string]string),
	}
}

// Start binds a localhost listener and serves the callback API. It returns the
// address plugins should dial.
func (s *grpcHostServer) Start() (string, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen host callback server: %w", err)
	}
	s.listener = lis
	s.srv = grpc.NewServer(grpc.UnaryInterceptor(s.authInterceptor))
	grpcpb.RegisterHostServer(s.srv, s)

	go func() { _ = s.srv.Serve(lis) }()
	return lis.Addr().String(), nil
}

// Stop gracefully shuts the callback server down.
func (s *grpcHostServer) Stop() {
	if s.srv != nil {
		s.srv.GracefulStop()
	}
}

// issueToken allocates an auth token bound to an instance id.
func (s *grpcHostServer) issueToken(instanceID string) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	s.mu.Lock()
	s.tokens[token] = instanceID
	s.mu.Unlock()
	return token, nil
}

func (s *grpcHostServer) revokeToken(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

func (s *grpcHostServer) sourceForToken(token string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	source, ok := s.tokens[token]
	return source, ok
}

func (s *grpcHostServer) authInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing host callback metadata")
	}
	vals := md.Get(hostCallbackTokenMD)
	if len(vals) == 0 {
		return nil, status.Error(codes.Unauthenticated, "invalid host callback token")
	}
	source, ok := s.sourceForToken(vals[0])
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "invalid host callback token")
	}
	ctx = context.WithValue(ctx, grpcPluginSourceKey{}, source)
	return handler(ctx, req)
}

func (s *grpcHostServer) KVGet(_ context.Context, req *grpcpb.KVGetRequest) (*grpcpb.KVGetReply, error) {
	v, ok := s.api.KVGet(req.GetNamespace(), req.GetKey())
	return &grpcpb.KVGetReply{Found: ok, Value: v}, nil
}

func (s *grpcHostServer) KVSet(_ context.Context, req *grpcpb.KVSetRequest) (*grpcpb.KVSetReply, error) {
	var errStr string
	if err := s.api.KVSet(req.GetNamespace(), req.GetKey(), req.GetValue()); err != nil {
		errStr = err.Error()
	}
	return &grpcpb.KVSetReply{Error: errStr}, nil
}

func (s *grpcHostServer) KVDelete(_ context.Context, req *grpcpb.KVDeleteRequest) (*grpcpb.KVDeleteReply, error) {
	var errStr string
	if err := s.api.KVDelete(req.GetNamespace(), req.GetKey()); err != nil {
		errStr = err.Error()
	}
	return &grpcpb.KVDeleteReply{Error: errStr}, nil
}

func (s *grpcHostServer) KVList(_ context.Context, req *grpcpb.KVListRequest) (*grpcpb.KVListReply, error) {
	return &grpcpb.KVListReply{Keys: s.api.KVList(req.GetNamespace())}, nil
}

func (s *grpcHostServer) PatchParams(ctx context.Context, req *grpcpb.ParamsPatchRequest) (*grpcpb.ParamsPatchReply, error) {
	source, _ := ctx.Value(grpcPluginSourceKey{}).(string)
	var errStr string
	if err := s.api.PatchParams(source, ParamsPatch{
		Set:    req.GetSet(),
		Delete: req.GetDelete(),
	}); err != nil {
		errStr = err.Error()
	}
	return &grpcpb.ParamsPatchReply{Error: errStr}, nil
}

func (s *grpcHostServer) Emit(_ context.Context, req *grpcpb.EmitInstruction) (*grpcpb.EmitReply, error) {
	var errStr string
	if err := s.api.Emit(EmitInstruction{
		Namespace: req.GetNamespace(),
		Target:    req.GetTarget(),
		Event:     req.GetEvent(),
		Payload:   req.GetPayload(),
	}); err != nil {
		errStr = err.Error()
	}
	return &grpcpb.EmitReply{Error: errStr}, nil
}

func (s *grpcHostServer) SendPluginMessage(ctx context.Context, req *grpcpb.PluginMessage) (*grpcpb.PluginMessageReply, error) {
	source, _ := ctx.Value(grpcPluginSourceKey{}).(string)
	var errStr string
	message, err := s.api.PluginMessage(ctx, source, PluginMessage{
		Target:  req.GetTarget(),
		Topic:   req.GetTopic(),
		Payload: req.GetPayload(),
	})
	if err != nil {
		errStr = err.Error()
	}
	return &grpcpb.PluginMessageReply{Error: errStr, Message: message}, nil
}

func (s *grpcHostServer) Log(ctx context.Context, req *grpcpb.LogRequest) (*grpcpb.LogReply, error) {
	source, _ := ctx.Value(grpcPluginSourceKey{}).(string)
	s.api.Log(source, req.GetLevel(), req.GetMessage())
	return &grpcpb.LogReply{}, nil
}
