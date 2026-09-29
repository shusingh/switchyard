// Package workload describes benchmark traffic: sessions of requests with
// arrival times, think times, and prompts built from deterministic synthetic
// text. Generators produce workloads from real traces (Mooncake) or from
// parameterized models of agent and shared-prefix traffic.
//
// Prompts are stored as segments and rendered only when sent, so a workload
// of thousands of long requests stays small in memory.
package workload

import (
	"encoding/json"
	"time"

	"github.com/shusingh/switchyard/internal/openai"
)

// Workload is a set of sessions ordered by start time.
type Workload struct {
	Name     string
	Sessions []Session
}

// Session is a sequence of dependent requests, such as one agent run. Its
// first turn starts at Start (measured from the beginning of the run); each
// later turn starts ThinkTime after the previous turn completes.
type Session struct {
	ID    string
	Start time.Duration
	Turns []Turn
}

// Turn is one request.
type Turn struct {
	// ThinkTime is the pause after the previous turn completed. It is zero
	// for a session's first turn.
	ThinkTime time.Duration
	// Messages is set for chat completions.
	Messages []Message
	// Prompt is set for legacy completions.
	Prompt []Segment
	// OutputTokens is the exact number of tokens to generate.
	OutputTokens int
}

// Message is one chat message whose content is synthetic text.
type Message struct {
	Role    string
	Content []Segment
}

// Requests returns the total number of turns in w.
func (w *Workload) Requests() int {
	n := 0
	for _, s := range w.Sessions {
		n += len(s.Turns)
	}
	return n
}

// Path returns the API path the turn is sent to.
func (t *Turn) Path() string {
	if t.Messages != nil {
		return openai.PathChatCompletions
	}
	return openai.PathCompletions
}

// Body renders the turn as a streaming request for model.
//
// ignore_eos makes the engine generate exactly OutputTokens tokens, so output
// length is a property of the workload rather than of the model's choices,
// and runs are comparable across policies.
func (t *Turn) Body(model string) ([]byte, error) {
	req := map[string]any{
		"model":          model,
		"stream":         true,
		"stream_options": map[string]bool{"include_usage": true},
		"max_tokens":     t.OutputTokens,
		"ignore_eos":     true,
		"temperature":    0,
	}
	if t.Messages != nil {
		msgs := make([]map[string]string, len(t.Messages))
		for i, m := range t.Messages {
			msgs[i] = map[string]string{"role": m.Role, "content": renderSegments(m.Content)}
		}
		req["messages"] = msgs
	} else {
		req["prompt"] = renderSegments(t.Prompt)
	}
	return json.Marshal(req)
}

// PromptWords returns the number of synthetic words in the turn's prompt, a
// close proxy for its token count.
func (t *Turn) PromptWords() int {
	n := 0
	for _, m := range t.Messages {
		for _, s := range m.Content {
			n += s.Words
		}
	}
	for _, s := range t.Prompt {
		n += s.Words
	}
	return n
}
