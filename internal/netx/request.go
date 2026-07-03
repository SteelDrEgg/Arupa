package netx

import (
	"net/http"
	"strings"
)

// WantsHTMLPage reports whether a request should receive a browser page instead
// of a JSON API response.
func WantsHTMLPage(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet {
		return false
	}
	if r.URL != nil {
		path := r.URL.Path
		if path == "/api" || strings.HasPrefix(path, "/api/") {
			return false
		}
	}
	accept := r.Header.Get("Accept")
	return accept == "" || strings.Contains(accept, "text/html")
}

// RequestPathMatches reports whether the request path already points at path.
func RequestPathMatches(r *http.Request, path string) bool {
	return r != nil && r.URL != nil && r.URL.Path == path
}
