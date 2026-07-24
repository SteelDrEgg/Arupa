package auth

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

var (
	CookieName     = "arupa-auth"
	cookieLifespan = 24 * time.Hour
)

// SessionStore holds active user sessions
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]SessionData
}

// SessionData contains user session information
type SessionData struct {
	Username  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Global session store
var Sessions = &SessionStore{
	sessions: make(map[string]SessionData),
}

// GenerateToken creates a random token
func GenerateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// CreateSession creates a new session for the user and returns a token
func CreateSession(username string) (string, error) {
	token, err := GenerateToken()
	if err != nil {
		return "", err
	}

	Sessions.mu.Lock()
	defer Sessions.mu.Unlock()

	expiresAt := time.Now().Add(cookieLifespan)
	Sessions.sessions[token] = SessionData{
		Username:  username,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
	}

	return token, nil
}

// ValidateSession checks if a token is valid and returns the username
func ValidateSession(token string) (string, bool) {
	Sessions.mu.RLock()
	defer Sessions.mu.RUnlock()

	session, exists := Sessions.sessions[token]
	if !exists {
		return "", false
	}

	// Check if session has expired
	if time.Now().After(session.ExpiresAt) {
		// Remove expired session
		Sessions.mu.RUnlock()
		Sessions.mu.Lock()
		delete(Sessions.sessions, token)
		Sessions.mu.Unlock()
		Sessions.mu.RLock()
		return "", false
	}

	// TODO(config): define whether removing or changing a configured user should
	// invalidate that user's existing sessions. The minimal behavior keeps a
	// token valid until logout or expiry.
	return session.Username, true
}

// DeleteSession removes a session (logout)
func DeleteSession(token string) {
	Sessions.mu.Lock()
	defer Sessions.mu.Unlock()
	delete(Sessions.sessions, token)
}

// SetCookie sets an HTTP cookie with the session token
func SetCookie(w http.ResponseWriter, token string) {
	cookie := &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   false, // Set to true in production with HTTPS
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(cookieLifespan),
	}
	http.SetCookie(w, cookie)
}

// GetTokenFromCookie extracts the session token from HTTP request cookies
func GetTokenFromCookie(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return "", false
	}
	return cookie.Value, true
}

// ClearCookie removes the session cookie
func ClearCookie(w http.ResponseWriter) {
	cookie := &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Expires:  time.Unix(0, 0), // Expired time
	}
	http.SetCookie(w, cookie)
}

// GetTokenFromHeader extracts the session token from Authorization header
func GetTokenFromHeader(r *http.Request) (string, bool) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", false
	}

	// Support both "Bearer token" and "token" formats
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		return authHeader[7:], true
	}

	return authHeader, true
}

// IsAuthenticated checks if the request has a valid session (cookie or header)
func IsAuthenticated(r *http.Request) (string, bool) {
	user := UserFromRequest(r)
	return user.Username, user.Authenticated
}

// AuthenticateRequest validates the session token carried by r and resolves
// the user's configured groups.
func AuthenticateRequest(r *http.Request) User {
	// Try cookie first
	token, exists := GetTokenFromCookie(r)

	// If no cookie, try Authorization header
	if !exists {
		token, exists = GetTokenFromHeader(r)
		if !exists {
			return User{}
		}
	}

	username, valid := ValidateSession(token)
	if !valid {
		return User{}
	}
	return UserForUsername(username)
}
