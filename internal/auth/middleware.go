package auth

import (
	"context"
	"net/http"
	"strings"

	"arupa/internal/conf"
	"arupa/internal/netx"

	"github.com/zishang520/socket.io/servers/socket/v3"
)

type userContextKey struct{}

// WithUser authenticates each HTTP request once and stores the result in its
// context for host and plugin handlers to reuse.
func WithUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := AuthenticateRequest(r)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, user)))
	})
}

// UserFromRequest returns the request's cached identity, authenticating lazily
// when the request did not pass through WithUser.
func UserFromRequest(r *http.Request) User {
	if user, ok := r.Context().Value(userContextKey{}).(User); ok {
		return user
	}
	return AuthenticateRequest(r)
}

// RequireAuth is a middleware that checks authentication for protected routes
func RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !UserFromRequest(r).Authenticated {
			if page, ok := conf.GetPagePath(http.StatusUnauthorized); ok && netx.WantsHTMLPage(r) && !netx.RequestPathMatches(r, page) {
				http.Redirect(w, r, page, http.StatusSeeOther)
				return
			}
			_ = netx.WriteUnauthorized(w, "Not authenticated")
			return
		}
		next(w, r)
	}
}

// RouteAccess applies the current top-level configured route group rules
// before the request reaches host or plugin routing. The longest matching
// pattern wins. Rules are read per request so configuration reloads take
// effect without rebuilding the HTTP server.
func RouteAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allow := conf.GetRouteAllow()
		_, groups, ok := matchRouteAccess(r.URL.Path, allow)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		decision := (AccessPolicy{Groups: groups}).Check(UserFromRequest(r))
		if WriteAccessError(w, decision) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func matchRouteAccess(path string, allow map[string][]string) (string, []string, bool) {
	best := ""
	var groups []string
	for pattern, candidateGroups := range allow {
		if !matchesRouteAccess(path, pattern) || len(pattern) <= len(best) {
			continue
		}
		best = pattern
		groups = candidateGroups
	}
	return best, groups, best != ""
}

func matchesRouteAccess(path, pattern string) bool {
	if strings.HasSuffix(pattern, "/*") {
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "*"))
	}
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(path, pattern)
	}
	return path == pattern
}

// WriteAccessError writes the HTTP response corresponding to an access
// decision and reports whether the request was rejected.
func WriteAccessError(w http.ResponseWriter, decision AccessDecision) bool {
	switch decision {
	case AccessAuthenticationRequired:
		_ = netx.WriteUnauthorized(w, "authentication required")
		return true
	case AccessForbidden:
		_ = netx.WriteError(w, http.StatusForbidden, "access forbidden", nil)
		return true
	default:
		return false
	}
}

// RequireAuthSocketIO is a middleware that checks authentication for protected Socket.IO endpoints
func RequireAuthSocketIO(client *socket.Socket, next func(*socket.ExtendedError)) {
	if _, ok := AuthenticateSocketIO(client); ok {
		next(nil)
	} else {
		next(socket.NewExtendedError("Unauthorized", "authentication required"))
	}
}

// AuthenticateSocketIO validates the session token carried by the Socket.IO
// handshake and resolves the current user's groups.
func AuthenticateSocketIO(client *socket.Socket) (User, bool) {
	if client == nil || client.Handshake() == nil {
		return User{}, false
	}

	var token string
	for key, raw := range client.Handshake().Headers {
		if !strings.EqualFold(key, "Cookie") {
			continue
		}
		switch values := raw.(type) {
		case []string:
			if len(values) > 0 {
				token = cookieToken(values[0])
			}
		case string:
			token = cookieToken(values)
		}
		break
	}
	if token == "" {
		for key, raw := range client.Handshake().Headers {
			if !strings.EqualFold(key, "Authorization") {
				continue
			}
			switch values := raw.(type) {
			case []string:
				if len(values) > 0 {
					token = strings.TrimPrefix(values[0], "Bearer ")
				}
			case string:
				token = strings.TrimPrefix(values, "Bearer ")
			}
			break
		}
	}
	username, ok := ValidateSession(token)
	if !ok {
		return User{}, false
	}
	return UserForUsername(username), true
}

func UserFromSocket(client *socket.Socket) User {
	user, _ := AuthenticateSocketIO(client)
	return user
}

func cookieToken(header string) string {
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, CookieName+"=") {
			return strings.TrimPrefix(part, CookieName+"=")
		}
	}
	return ""
}
