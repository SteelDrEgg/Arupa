package netx

import (
	"fmt"
	"strings"
)

// RootPathMatchMode defines how the root pattern ("/") is interpreted.
type RootPathMatchMode uint8

const (
	// RootPathExact makes "/" match only the root path.
	RootPathExact RootPathMatchMode = iota
	// RootPathSubtree makes "/" match every absolute request path.
	RootPathSubtree
)

// ValidatePathPattern validates the small path-pattern language shared by
// access rules and service registrations. Patterns are exact unless they end
// in "/", in which case they match that subtree. The legacy "/*" notation is
// deliberately rejected: it looks like a general wildcard but is not one.
func ValidatePathPattern(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("path pattern is required")
	}
	if !strings.HasPrefix(pattern, "/") {
		return fmt.Errorf("path pattern %q must start with '/'", pattern)
	}
	if strings.HasSuffix(pattern, "/*") {
		return fmt.Errorf("path pattern %q must use a trailing '/' for a subtree; '/*' is not supported", pattern)
	}
	return nil
}

// MatchPathPattern reports whether path matches pattern. Except for "/",
// patterns ending in "/" match a subtree and all other patterns match
// exactly. rootMode makes the root behavior explicit for callers whose
// resource model treats it as a subtree (such as a static-file mount).
func MatchPathPattern(path, pattern string, rootMode RootPathMatchMode) bool {
	if path == "" || pattern == "" {
		return false
	}
	if pattern == "/" {
		return rootMode == RootPathSubtree && strings.HasPrefix(path, "/") || path == "/"
	}
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(path, pattern)
	}
	return path == pattern
}
