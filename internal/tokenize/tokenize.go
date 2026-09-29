// Package tokenize asks a model server for the exact prompt token IDs it
// would compute for a request, through vLLM's /tokenize endpoint, which
// applies the model's own chat template. Precise mode hashes these tokens the
// way the engine does, so its keys match the engine's KV cache events. See
// docs/engineering/design.md section 7.4.
package tokenize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/shusingh/switchyard/internal/openai"
)

// Client calls a server's /tokenize endpoint. It is safe for concurrent use.
type Client struct {
	http *http.Client
}

// New returns a Client that uses hc for its requests.
func New(hc *http.Client) *Client { return &Client{http: hc} }

// request holds the fields of a completion request that determine its prompt.
type request struct {
	Model    string          `json:"model"`
	Messages json.RawMessage `json:"messages,omitempty"`
	Tools    json.RawMessage `json:"tools,omitempty"`
	Prompt   json.RawMessage `json:"prompt,omitempty"`
}

// chatTokenizeRequest mirrors vLLM's TokenizeChatRequest. The chat
// completions endpoint appends the generation prompt by default, so tokenizing
// must too, or the token IDs, and therefore the block hashes, would differ.
type chatTokenizeRequest struct {
	Model               string          `json:"model"`
	Messages            json.RawMessage `json:"messages"`
	Tools               json.RawMessage `json:"tools,omitempty"`
	AddGenerationPrompt bool            `json:"add_generation_prompt"`
}

type completionTokenizeRequest struct {
	Model  string          `json:"model"`
	Prompt json.RawMessage `json:"prompt"`
}

type tokenizeResponse struct {
	Tokens []uint64 `json:"tokens"`
}

// Tokens returns the prompt token IDs the server at base would compute for a
// request with the given path and body.
func (c *Client) Tokens(ctx context.Context, base *url.URL, path string, body []byte) ([]uint64, error) {
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("decode request: %w", err)
	}
	var payload any
	if path == openai.PathChatCompletions {
		payload = chatTokenizeRequest{Model: req.Model, Messages: req.Messages, Tools: req.Tools, AddGenerationPrompt: true}
	} else {
		payload = completionTokenizeRequest{Model: req.Model, Prompt: req.Prompt}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode tokenize request: %w", err)
	}

	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, base.JoinPath("/tokenize").String(), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(hr) //nolint:gosec // G704: base is an operator-configured backend
	if err != nil {
		return nil, fmt.Errorf("tokenize: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("tokenize: status %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	var out tokenizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode tokenize response: %w", err)
	}
	return out.Tokens, nil
}
