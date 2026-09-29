package server

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shusingh/switchyard/internal/admission"
	"github.com/shusingh/switchyard/internal/openai"
	"github.com/shusingh/switchyard/internal/scheduler"
	"github.com/shusingh/switchyard/internal/sim"
	"github.com/shusingh/switchyard/internal/sim/simtest"
)

// streamCompleted reads a whole streaming response body and reports whether
// it ended with the [DONE] event.
func streamCompleted(body io.Reader) bool {
	b, _ := io.ReadAll(body)
	return strings.Contains(string(b), "[DONE]")
}

func TestCrashMidStreamLeavesOtherStreamsIntact(t *testing.T) {
	t.Parallel()
	crashing := sim.Options{Cost: simtest.FastCost(), DefaultOutputTokens: 20, Faults: sim.Faults{AbortRate: 1}}
	healthy := sim.Options{Cost: simtest.FastCost(), DefaultOutputTokens: 20}
	h := newHarness(t, harnessOptions{engines: []sim.Options{crashing, healthy}})

	var mu sync.Mutex
	completed, truncated := 0, 0
	var wg sync.WaitGroup
	for range 8 { // round robin sends half to each engine
		wg.Go(func() {
			resp := h.post(t, context.Background(), openai.PathChatCompletions, `{"model":"sim-model","stream":true,"messages":[]}`)
			done := streamCompleted(resp.Body)
			resp.Body.Close()
			mu.Lock()
			defer mu.Unlock()
			if done {
				completed++
			} else {
				truncated++
			}
		})
	}
	wg.Wait()
	if completed != 4 || truncated != 4 {
		t.Errorf("completed=%d truncated=%d, want 4 and 4: crashes must stay confined to their own streams", completed, truncated)
	}
	if got := h.engines[0].Stats().Aborted; got != 4 {
		t.Errorf("crashing engine aborted %d streams, want 4", got)
	}
}

func TestFlappingBackendIsAvoidedThenForgotten(t *testing.T) {
	t.Parallel()
	policy, _ := scheduler.New(scheduler.PolicyPrefixAffinity, scheduler.Options{})
	h := newHarness(t, harnessOptions{engines: fastEngines(2), policy: policy})
	body := `{"model":"sim-model","max_tokens":2,"messages":[{"role":"system","content":"` +
		strings.Repeat("shared system prompt ", 80) + `"}]}`
	send := func() {
		resp := h.post(t, context.Background(), openai.PathChatCompletions, body)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
	}
	completed := func(i int) int64 { return h.engines[i].Stats().Completed }

	send()
	home := 0 // the engine the shared prefix settled on
	if completed(1) == 1 {
		home = 1
	}
	other := 1 - home

	// The home engine drops out: traffic moves to the other engine.
	h.engines[home].SetUnhealthy(true)
	h.checker.CheckAll(context.Background())
	send()
	send()
	if completed(other) != 2 {
		t.Fatalf("other engine completed %d requests while home was down, want 2", completed(other))
	}

	// The home engine returns, possibly restarted. Its old beliefs belong to
	// a previous generation, so the prefix now lives only on the other
	// engine and traffic stays there.
	h.engines[home].Restart()
	h.engines[home].SetUnhealthy(false)
	h.checker.CheckAll(context.Background())
	send()
	send()
	if completed(other) != 4 {
		t.Errorf("after recovery the other engine completed %d requests, want 4: stale beliefs about the returned engine must not attract traffic", completed(other))
	}
}

func TestEstimatedTTFTLearnsToAvoidASlowBackend(t *testing.T) {
	t.Parallel()
	slow, fast := simtest.FastCost(), simtest.FastCost()
	slow.PrefillTokensPerSecond = 5000 // about 0.2s for a 1,000-token prompt
	fast.PrefillTokensPerSecond = 500000
	est := testEstimator(2)
	policy, err := scheduler.New(scheduler.PolicyEstimatedTTFT, scheduler.Options{
		Estimator: est, BalanceAbs: 16, BalanceRel: 1.5, TieEpsilon: 0.05,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, harnessOptions{
		engines:   []sim.Options{{Cost: slow, DefaultOutputTokens: 1}, {Cost: fast, DefaultOutputTokens: 1}},
		policy:    policy,
		estimator: est,
	})
	for i := range 30 {
		// Unique prompts that differ from their first byte, so cache reuse
		// plays no part: only throughput decides.
		body := `{"model":"sim-model","stream":true,"messages":[{"role":"user","content":"` +
			strings.Repeat(strconv.Itoa(i)+" request ", 300) + `"}]}`
		resp := h.post(t, context.Background(), openai.PathChatCompletions, body)
		streamCompleted(resp.Body)
		resp.Body.Close()
	}
	if slowServed := h.engines[0].Stats().Completed; slowServed > 8 {
		t.Errorf("slow engine served %d of 30 requests; the estimator should learn to avoid it", slowServed)
	}
}

func TestOverloadedTenantDoesNotStarveOthers(t *testing.T) {
	t.Parallel()
	ac, err := admission.New(admission.Config{
		MaxInFlight: 2, QueueTimeout: 30 * time.Second, Quantum: 100,
		Default: admission.TenantConfig{Name: "default", Weight: 1, MaxQueued: 100},
		Tenants: []admission.TenantConfig{
			{Name: "heavy", APIKeys: []string{"sk-heavy"}, Weight: 1, MaxQueued: 100},
			{Name: "light", APIKeys: []string{"sk-light"}, Weight: 1, MaxQueued: 100},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	slow := simtest.FastCost()
	slow.StepOverhead = 10 * time.Millisecond
	h := newHarness(t, harnessOptions{engines: []sim.Options{{Cost: slow, DefaultOutputTokens: 5}}, admission: ac})

	var mu sync.Mutex
	var order []string
	send := func(key string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, h.url+openai.PathChatCompletions,
			strings.NewReader(`{"model":"sim-model","max_tokens":5,"messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := h.client.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		mu.Lock()
		order = append(order, key)
		mu.Unlock()
	}

	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() { send("sk-heavy") })
	}
	// Let the heavy tenant's backlog form before the light tenant arrives.
	deadline := time.Now().Add(5 * time.Second)
	for ac.Stats().Queued < 20 {
		if time.Now().After(deadline) {
			t.Fatal("heavy tenant's backlog never formed")
		}
		time.Sleep(time.Millisecond)
	}
	for range 5 {
		wg.Go(func() { send("sk-light") })
	}
	wg.Wait()

	lastLight := 0
	for i, key := range order {
		if key == "sk-light" {
			lastLight = i
		}
	}
	// With equal weights the fair queue alternates tenants, so the light
	// tenant's five requests finish long before the heavy backlog drains.
	if lastLight > 20 {
		t.Errorf("light tenant's last request finished at position %d of %d; it was starved behind the heavy tenant", lastLight+1, len(order))
	}
}
