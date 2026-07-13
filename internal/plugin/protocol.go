package plugin

import (
	grpcpb "arupa/pluginsdk/grpc/proto"
	wasmpb "arupa/pluginsdk/wasm/proto"
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
