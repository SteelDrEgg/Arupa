package auth

import (
	"sort"

	"arupa/internal/conf"
)

// User is the host-verified identity attached to an inbound service request.
// An unauthenticated request has Authenticated=false and no user payload is
// sent across the service protocol.
type User struct {
	Username      string
	Groups        []string
	Authenticated bool
}

// AccessPolicy describes authentication and optional group requirements for a
// resource declared by a service.
//
// An empty policy is public. RequireAuth permits any authenticated user. A
// non-empty Groups list requires an authenticated user in at least one group.
type AccessPolicy struct {
	RequireAuth bool     `json:"require_auth,omitempty" yaml:"require_auth,omitempty"`
	Groups      []string `json:"groups,omitempty" yaml:"groups,omitempty"`
}

type AccessDecision uint8

const (
	AccessAllowed AccessDecision = iota
	AccessAuthenticationRequired
	AccessForbidden
)

// Check evaluates the policy for user.
func (p AccessPolicy) Check(user User) AccessDecision {
	if !p.RequireAuth && len(p.Groups) == 0 {
		return AccessAllowed
	}
	if !user.Authenticated {
		return AccessAuthenticationRequired
	}
	if len(p.Groups) == 0 || user.HasAnyGroup(p.Groups) {
		return AccessAllowed
	}
	return AccessForbidden
}

func (u User) HasAnyGroup(groups []string) bool {
	if !u.Authenticated {
		return false
	}
	owned := make(map[string]struct{}, len(u.Groups))
	for _, group := range u.Groups {
		owned[group] = struct{}{}
	}
	for _, group := range groups {
		if _, ok := owned[group]; ok {
			return true
		}
	}
	return false
}

// UserForUsername builds the host-verified user object from configured group
// membership. Group names and membership are copied so callers cannot mutate
// global configuration through a request object.
func UserForUsername(username string) User {
	if username == "" {
		return User{}
	}

	groups := conf.GetGroups()
	userGroups := make([]string, 0)
	for group, users := range groups {
		for _, candidate := range users {
			if candidate == username {
				userGroups = append(userGroups, group)
				break
			}
		}
	}
	sort.Strings(userGroups)
	return User{
		Username:      username,
		Groups:        userGroups,
		Authenticated: true,
	}
}
