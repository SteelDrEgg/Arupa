package service

import (
	"context"
	"fmt"

	grpcpb "arupa/servicesdk/grpc/proto"

	goservice "github.com/SteelDrEgg/go-plugin"
	"google.golang.org/grpc"
)

// grpcHostService is scoped to one authenticated go-plugin broker stream. The
// source is fixed by the kernel and never accepted from an RPC payload.
type grpcHostService struct {
	grpcpb.UnimplementedHostServer
	api    *HostAPI
	source string
}

func newGRPCHostService(api *HostAPI, source string) *grpcHostService {
	return &grpcHostService{api: api, source: source}
}

func startGRPCHostBroker(broker *goservice.GRPCBroker, api *HostAPI, source string) (uint32, func(), error) {
	if broker == nil {
		return 0, nil, fmt.Errorf("go-plugin gRPC broker is unavailable")
	}
	id := broker.NextID()
	go broker.AcceptAndServe(id, func(server *grpc.Server) {
		grpcpb.RegisterHostServer(server, newGRPCHostService(api, source))
	})
	// The broker owns this server and closes it with the go-plugin client.
	return id, func() {}, nil
}

func (s *grpcHostService) KVGet(_ context.Context, req *grpcpb.KVGetRequest) (*grpcpb.KVGetReply, error) {
	value, ok := s.api.KVGet(req.GetNamespace(), req.GetKey())
	return &grpcpb.KVGetReply{Found: ok, Value: value}, nil
}

func (s *grpcHostService) KVSet(_ context.Context, req *grpcpb.KVSetRequest) (*grpcpb.KVSetReply, error) {
	return &grpcpb.KVSetReply{Error: errorString(s.api.KVSet(req.GetNamespace(), req.GetKey(), req.GetValue()))}, nil
}

func (s *grpcHostService) KVDelete(_ context.Context, req *grpcpb.KVDeleteRequest) (*grpcpb.KVDeleteReply, error) {
	return &grpcpb.KVDeleteReply{Error: errorString(s.api.KVDelete(req.GetNamespace(), req.GetKey()))}, nil
}

func (s *grpcHostService) KVList(_ context.Context, req *grpcpb.KVListRequest) (*grpcpb.KVListReply, error) {
	return &grpcpb.KVListReply{Keys: s.api.KVList(req.GetNamespace())}, nil
}

func (s *grpcHostService) GetParams(_ context.Context, _ *grpcpb.ParamsGetRequest) (*grpcpb.ParamsGetReply, error) {
	params, err := s.api.GetParams(s.source)
	return &grpcpb.ParamsGetReply{Params: params, Error: errorString(err)}, nil
}

func (s *grpcHostService) PatchParams(_ context.Context, req *grpcpb.ParamsPatchRequest) (*grpcpb.ParamsPatchReply, error) {
	err := s.api.PatchParams(s.source, ParamsPatch{Set: req.GetSet(), Delete: req.GetDelete()})
	return &grpcpb.ParamsPatchReply{Error: errorString(err)}, nil
}

func (s *grpcHostService) Emit(_ context.Context, req *grpcpb.EmitInstruction) (*grpcpb.EmitReply, error) {
	err := s.api.Emit(EmitInstruction{
		Namespace: req.GetNamespace(), Target: req.GetTarget(),
		Event: req.GetEvent(), Payload: req.GetPayload(),
	})
	return &grpcpb.EmitReply{Error: errorString(err)}, nil
}

func (s *grpcHostService) SendServiceMessage(ctx context.Context, req *grpcpb.ServiceMessage) (*grpcpb.ServiceMessageReply, error) {
	message, err := s.api.ServiceMessage(ctx, s.source, ServiceMessage{
		Target: req.GetTarget(), Topic: req.GetTopic(), Payload: req.GetPayload(),
	})
	return &grpcpb.ServiceMessageReply{Message: message, Error: errorString(err)}, nil
}

func (s *grpcHostService) RegisterTransport(_ context.Context, req *grpcpb.RegisterTransportRequest) (*grpcpb.RegistrationReply, error) {
	transport, err := transportFromGRPC(req.GetTransport())
	if err == nil {
		err = s.api.RegisterTransport(s.source, transport)
	}
	return registrationReplyToGRPC(singleRegistration(transport.ID, err)), nil
}

func (s *grpcHostService) UnregisterTransport(_ context.Context, req *grpcpb.UnregisterTransportRequest) (*grpcpb.RegistrationReply, error) {
	return registrationReplyToGRPC(singleRegistration(req.GetId(), s.api.UnregisterTransport(s.source, req.GetId()))), nil
}

func (s *grpcHostService) RegisterRoutes(_ context.Context, req *grpcpb.RegisterRoutesRequest) (*grpcpb.RegistrationReply, error) {
	routes, err := routesFromGRPC(req.GetRoutes())
	if err != nil {
		return registrationReplyToGRPC(RegistrationResult{Degraded: true, Error: err.Error()}), nil
	}
	return registrationReplyToGRPC(s.api.RegisterRoutes(s.source, routes)), nil
}

func (s *grpcHostService) UnregisterRoutes(_ context.Context, req *grpcpb.UnregisterRoutesRequest) (*grpcpb.RegistrationReply, error) {
	return registrationReplyToGRPC(s.api.UnregisterRoutes(s.source, req.GetIds())), nil
}

func (s *grpcHostService) Log(_ context.Context, req *grpcpb.LogRequest) (*grpcpb.LogReply, error) {
	s.api.Log(s.source, req.GetLevel(), req.GetMessage())
	return &grpcpb.LogReply{}, nil
}

func registrationReplyToGRPC(result RegistrationResult) *grpcpb.RegistrationReply {
	failures := make([]*grpcpb.RegistrationFailure, 0, len(result.Failures))
	for _, failure := range result.Failures {
		failures = append(failures, &grpcpb.RegistrationFailure{Id: failure.ID, Error: failure.Error})
	}
	return &grpcpb.RegistrationReply{
		Registered: result.Registered, Failures: failures,
		Degraded: result.Degraded, Error: result.Error,
	}
}
