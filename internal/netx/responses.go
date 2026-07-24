package netx

import (
	"encoding/json"
	"net/http"
)

// APIResponse represents a standard API response structure
type APIResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// AuthResponse represents authentication-related responses
type AuthResponse struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	Username string `json:"username,omitempty"`
	Token    string `json:"token,omitempty"`
}

// ErrorResponse represents error responses
type ErrorResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Error   string `json:"error"`
}

// WriteJSON writes a JSON response with the specified status code
func WriteJSON(w http.ResponseWriter, statusCode int, data interface{}) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	return json.NewEncoder(w).Encode(data)
}

// WriteSuccess writes a successful JSON response
func WriteSuccess(w http.ResponseWriter, message string, data interface{}) error {
	return WriteJSON(w, http.StatusOK, APIResponse{
		Success: true,
		Message: message,
		Data:    data,
	})
}

// WriteError writes an error JSON response
func WriteError(w http.ResponseWriter, statusCode int, message string, err error) error {
	response := ErrorResponse{
		Success: false,
		Message: message,
	}

	if err != nil {
		response.Error = err.Error()
	}

	return WriteJSON(w, statusCode, response)
}

// WriteAuthSuccess writes a successful authentication response
func WriteAuthSuccess(w http.ResponseWriter, message string, username string) error {
	return WriteAuthSuccessWithToken(w, message, username, "")
}

// WriteAuthSuccessWithToken writes a successful authentication response with token
func WriteAuthSuccessWithToken(w http.ResponseWriter, message string, username string, token string) error {
	return WriteJSON(w, http.StatusOK, AuthResponse{
		Success:  true,
		Message:  message,
		Username: username,
		Token:    token,
	})
}

// WriteAuthError writes an authentication error response
func WriteAuthError(w http.ResponseWriter, statusCode int, message string) error {
	return WriteJSON(w, statusCode, AuthResponse{
		Success: false,
		Message: message,
	})
}

// WriteMethodNotAllowed writes a method not allowed response
func WriteMethodNotAllowed(w http.ResponseWriter) error {
	return WriteError(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
}

// WriteNotFound writes a not-found response.
func WriteNotFound(w http.ResponseWriter) error {
	return WriteError(w, http.StatusNotFound, "Not found", nil)
}

// WriteBadRequest writes a bad request response
func WriteBadRequest(w http.ResponseWriter, message string) error {
	return WriteError(w, http.StatusBadRequest, message, nil)
}

// WritePayloadTooLarge writes a payload-too-large response.
func WritePayloadTooLarge(w http.ResponseWriter, message string) error {
	return WriteError(w, http.StatusRequestEntityTooLarge, message, nil)
}

// WriteUnauthorized writes an unauthorized response
func WriteUnauthorized(w http.ResponseWriter, message string) error {
	return WriteAuthError(w, http.StatusUnauthorized, message)
}

// WriteForbidden writes a permission-denied response.
func WriteForbidden(w http.ResponseWriter, message string) error {
	return WriteError(w, http.StatusForbidden, message, nil)
}

// WriteInternalServerError writes an internal server error response
func WriteInternalServerError(w http.ResponseWriter, message string, err error) error {
	return WriteError(w, http.StatusInternalServerError, message, err)
}
