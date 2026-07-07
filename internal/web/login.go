package web

import (
	"arupa/internal/auth"
	"arupa/internal/netx"
	"encoding/json"
	"net/http"
)

// LoginRequest represents the login request payload
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// StartLogin registers all login-related routes with the given mux
// TODO: api location better be dynamic
func StartLogin(mux *http.ServeMux) {
	// API endpoints
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/api/logout", handleLogout)
	mux.HandleFunc("/api/check-auth", handleCheckAuth)
}

// handleLogin processes login requests
func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		netx.WriteMethodNotAllowed(w)
		return
	}

	var loginReq LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&loginReq); err != nil {
		netx.WriteBadRequest(w, "Invalid request format")
		return
	}

	// Verify credentials using user.go functions
	if !auth.VerifyPassword(loginReq.Username, loginReq.Password) {
		netx.WriteUnauthorized(w, "Invalid username or password")
		return
	}

	// Create session using cookie.go functions
	token, err := auth.CreateSession(loginReq.Username)
	if err != nil {
		netx.WriteInternalServerError(w, "Failed to create session", err)
		return
	}

	// Set cookie
	auth.SetCookie(w, token)

	// Return both cookie (for browser) and token (for frontend token-based auth)
	netx.WriteAuthSuccessWithToken(w, "Login successful", loginReq.Username, token)
}

// handleLogout processes logout requests
func handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		netx.WriteMethodNotAllowed(w)
		return
	}

	// Get token from cookie
	token, exists := auth.GetTokenFromCookie(r)
	if exists {
		// Delete session
		auth.DeleteSession(token)
	}

	// Clear cookie
	auth.ClearCookie(w)

	netx.WriteAuthSuccess(w, "Logout successful", "")
}

// handleCheckAuth checks if the user is authenticated
func handleCheckAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		netx.WriteMethodNotAllowed(w)
		return
	}

	username, authenticated := auth.IsAuthenticated(r)
	if !authenticated {
		netx.WriteUnauthorized(w, "Not authenticated")
		return
	}

	netx.WriteAuthSuccess(w, "Authenticated", username)
}
