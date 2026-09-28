package openai

import (
	"bytes"
	"encoding/json"
)

// Usage reports token counts for a completed request.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamChunk is the subset of a streamed chunk that Switchyard inspects. It
// covers both chat completions (choices[].delta.content) and legacy
// completions (choices[].text).
type StreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		Text string `json:"text"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

var contentMarkers = [][]byte{[]byte(`"content"`), []byte(`"text"`)}

// ChunkHasContent reports whether a streamed chunk carries generated text.
//
// Servers such as vLLM open a chat stream with a chunk that announces the
// assistant role with an empty content delta. That chunk is not the first
// token, so time to first token is measured at the first chunk for which this
// returns true.
func ChunkHasContent(data []byte) bool {
	// Most chunks are cheap to rule out without decoding.
	if !bytes.Contains(data, contentMarkers[0]) && !bytes.Contains(data, contentMarkers[1]) {
		return false
	}
	var c StreamChunk
	if err := json.Unmarshal(data, &c); err != nil {
		return false
	}
	for _, ch := range c.Choices {
		if ch.Delta.Content != "" || ch.Text != "" {
			return true
		}
	}
	return false
}

// ChunkUsage returns the usage reported by a streamed chunk, if any.
func ChunkUsage(data []byte) (Usage, bool) {
	if !bytes.Contains(data, []byte(`"usage"`)) {
		return Usage{}, false
	}
	var c StreamChunk
	if err := json.Unmarshal(data, &c); err != nil || c.Usage == nil {
		return Usage{}, false
	}
	return *c.Usage, true
}
