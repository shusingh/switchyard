package openai

import (
	"encoding/json"
	"net/http"
)

// Error types used in error bodies. Clients such as the official SDKs map
// these to exception classes, so they follow OpenAI's vocabulary.
const (
	ErrTypeInvalidRequest = "invalid_request_error"
	ErrTypeRateLimit      = "rate_limit_error"
	ErrTypeServer         = "server_error"
	ErrTypeUnavailable    = "service_unavailable"
)

// ErrorResponse is the JSON body of an error response.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody describes a single error.
type ErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// WriteError writes an OpenAI-style JSON error response.
func WriteError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	// The status line is already sent, so an encoding failure here means the
	// client connection is gone; there is nothing useful left to report.
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: ErrorBody{Message: message, Type: errType}})
}
