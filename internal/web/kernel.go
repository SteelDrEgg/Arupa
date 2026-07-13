package web

import (
	"net/http"

	"arupa/internal/netx"
)

// StartKernel registers host-level kernel information and control endpoints.
// Access control is intentionally handled by the top-level Route.Allow
// middleware, so these handlers do not install a second policy.
func StartKernel(mux *http.ServeMux, version string, reload func() error) {
	mux.HandleFunc("/api/kernel/version", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			_ = netx.WriteMethodNotAllowed(w)
			return
		}

		_ = netx.WriteSuccess(w, "Kernel version fetched", map[string]string{
			"version": version,
		})
	})

	mux.HandleFunc("/api/kernel/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			_ = netx.WriteMethodNotAllowed(w)
			return
		}
		if reload == nil {
			_ = netx.WriteInternalServerError(w, "Configuration reload is unavailable", nil)
			return
		}
		if err := reload(); err != nil {
			_ = netx.WriteInternalServerError(w, "Failed to reload configuration", err)
			return
		}

		_ = netx.WriteSuccess(w, "Configuration reloaded", nil)
	})
}
