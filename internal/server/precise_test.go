package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/shusingh/switchyard/internal/openai"
	"github.com/shusingh/switchyard/internal/prefix"
	"github.com/shusingh/switchyard/internal/scheduler"
)

// fakeTokenizer returns fixed tokens, or an error.
type fakeTokenizer struct {
	tokens []uint64
	err    error
}

func (f fakeTokenizer) Tokens(context.Context, *url.URL, string, []byte) ([]uint64, error) {
	return f.tokens, f.err
}

func TestPreciseKeysRouteToTheBackendHoldingTheBlocks(t *testing.T) {
	t.Parallel()
	tokens := make([]uint64, 64) // four engine blocks of 16
	for i := range tokens {
		tokens[i] = uint64(1000 + i)
	}
	index := testIndex(3)
	// Backend 2 reported storing the request's first three blocks, keyed as
	// vLLM keys them.
	blocks := prefix.VLLMBlockHashes(tokens, 16)
	keys := make([]uint64, 3)
	for i := range keys {
		keys[i] = blocks[i].Low64()
	}
	policy, _ := scheduler.New(scheduler.PolicyPrefixAffinity, scheduler.Options{})
	h := newHarness(t, harnessOptions{
		engines:   fastEngines(3),
		policy:    policy,
		index:     index,
		tokenizer: fakeTokenizer{tokens: tokens},
	})
	index.Insert(2, 1, keys) // backends are in their first generation

	resp := h.post(t, context.Background(), openai.PathChatCompletions, `{"model":"sim-model","messages":[]}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := h.engines[2].Stats().Completed; got != 1 {
		t.Errorf("backend 2 served %d requests, want 1: it holds the request's blocks", got)
	}
}

func TestTokenizeFailureStillRoutes(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{
		engines:   fastEngines(2),
		tokenizer: fakeTokenizer{err: errors.New("engine unavailable")},
	})
	resp := h.post(t, context.Background(), openai.PathChatCompletions, `{"model":"sim-model","messages":[]}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200: prefix keys only guide routing", resp.StatusCode)
	}
	if got := testutil.ToFloat64(h.metrics.TokenizeFailures); got != 1 {
		t.Errorf("tokenize failures = %v, want 1", got)
	}
}
