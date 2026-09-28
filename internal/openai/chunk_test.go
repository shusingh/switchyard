package openai

import "testing"

func TestChunkHasContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		want bool
	}{
		{"role announcement", `{"choices":[{"delta":{"role":"assistant","content":""}}]}`, false},
		{"chat token", `{"choices":[{"delta":{"content":"Hello"}}]}`, true},
		{"completions token", `{"choices":[{"text":"Hello"}]}`, true},
		{"finish chunk", `{"choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`, false},
		{"usage only", `{"choices":[],"usage":{"prompt_tokens":3}}`, false},
		{"not json", `[DONE]`, false},
		{"malformed", `{"choices":[{"delta":{"content":`, false},
	}
	for _, tt := range tests {
		if got := ChunkHasContent([]byte(tt.data)); got != tt.want {
			t.Errorf("%s: ChunkHasContent(%s) = %v, want %v", tt.name, tt.data, got, tt.want)
		}
	}
}

func TestChunkUsage(t *testing.T) {
	t.Parallel()
	u, ok := ChunkUsage([]byte(`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`))
	if !ok || u != (Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}) {
		t.Errorf("ChunkUsage() = %+v, %v, want {7 3 10}, true", u, ok)
	}
	if _, ok := ChunkUsage([]byte(`{"choices":[{"delta":{"content":"x"}}]}`)); ok {
		t.Error("ChunkUsage(chunk without usage) ok = true, want false")
	}
	if _, ok := ChunkUsage([]byte(`{"usage":null}`)); ok {
		t.Error("ChunkUsage(null usage) ok = true, want false")
	}
}
