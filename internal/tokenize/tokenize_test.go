package tokenize

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/shusingh/switchyard/internal/openai"
)

func TestTokensBuildsVLLMTokenizeRequests(t *testing.T) {
	t.Parallel()
	var got map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokenize" {
			t.Errorf("path = %q, want /tokenize", r.URL.Path)
		}
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = body
		_, _ = w.Write([]byte(`{"count":3,"max_model_len":16384,"tokens":[151644,8948,198]}`))
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	c := New(srv.Client())

	tokens, err := c.Tokens(context.Background(), base, openai.PathChatCompletions,
		[]byte(`{"model":"m","stream":true,"max_tokens":9,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tokens, []uint64{151644, 8948, 198}) {
		t.Errorf("tokens = %v", tokens)
	}
	// Only prompt-shaping fields are sent, plus the generation prompt that
	// chat completions add by default.
	for _, k := range []string{"model", "messages", "tools", "add_generation_prompt"} {
		if _, ok := got[k]; !ok {
			t.Errorf("tokenize request lacks %q: %v", k, got)
		}
	}
	for _, k := range []string{"stream", "max_tokens"} {
		if _, ok := got[k]; ok {
			t.Errorf("tokenize request carries generation field %q", k)
		}
	}
	if string(got["add_generation_prompt"]) != "true" {
		t.Errorf("add_generation_prompt = %s, want true", got["add_generation_prompt"])
	}

	if _, err := c.Tokens(context.Background(), base, openai.PathCompletions, []byte(`{"model":"m","prompt":"abc"}`)); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["messages"]; ok || string(got["prompt"]) != `"abc"` {
		t.Errorf("completions tokenize request = %v, want model and prompt only", got)
	}
}

func TestTokensReportsServerErrors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	if _, err := New(srv.Client()).Tokens(context.Background(), base, openai.PathCompletions, []byte(`{"model":"m","prompt":"x"}`)); err == nil {
		t.Error("error = nil for a 404 from the server")
	}
}
