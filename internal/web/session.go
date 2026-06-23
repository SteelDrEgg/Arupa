package web

import (
	"minimalpanel/internal/auth"
	"minimalpanel/internal/netx"
	"net/http"
)

// StartSessionUtil registers session helper routes for frontend usage.
func StartSessionUtil(mux *http.ServeMux) {
	mux.HandleFunc("/api/session/me", CurrentUser)
}

// CurrentUser returns the authenticated username for frontend code.
func CurrentUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		netx.WriteMethodNotAllowed(w)
		return
	}

	username, ok := auth.IsAuthenticated(r)
	if !ok {
		netx.WriteSuccess(w, "No authenticated user", map[string]string{
			"username": "",
		})
		return
	}

	netx.WriteSuccess(w, "Authenticated", map[string]string{
		"username": username,
	})
}
