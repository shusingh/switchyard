package openai

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Paths of the endpoints Switchyard serves.
const (
	PathChatCompletions = "/v1/chat/completions"
	PathCompletions     = "/v1/completions"
	PathModels          = "/v1/models"
)

// ErrMissingModel is returned when a request body has no model field.
var ErrMissingModel = errors.New("request has no model")

// RoutingFields holds the request fields the router reads. Every other field
// in the body is forwarded to the backend without being decoded.
type RoutingFields struct {
	Model         string         `json:"model"`
	Stream        bool           `json:"stream"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`

	// MaxTokens is the legacy output limit; MaxCompletionTokens supersedes it
	// for chat completions. Nil means the client did not set the field.
	MaxTokens           *int `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`
}

// StreamOptions configures a streaming response.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// DecodeRoutingFields extracts the routing fields from a JSON request body.
// It fails if the body is not a JSON object or does not name a model.
func DecodeRoutingFields(body []byte) (RoutingFields, error) {
	var f RoutingFields
	if err := json.Unmarshal(body, &f); err != nil {
		return RoutingFields{}, fmt.Errorf("decode request body: %w", err)
	}
	if f.Model == "" {
		return RoutingFields{}, ErrMissingModel
	}
	return f, nil
}

// MaxOutputTokens returns the client's output token limit, preferring
// max_completion_tokens over max_tokens. It returns 0 when neither is set.
func (f RoutingFields) MaxOutputTokens() int {
	switch {
	case f.MaxCompletionTokens != nil:
		return *f.MaxCompletionTokens
	case f.MaxTokens != nil:
		return *f.MaxTokens
	default:
		return 0
	}
}

// WantsUsage reports whether a streaming client asked for the final usage
// event.
func (f RoutingFields) WantsUsage() bool {
	return f.StreamOptions != nil && f.StreamOptions.IncludeUsage
}
