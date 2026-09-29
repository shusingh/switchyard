package sim_test

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/shusingh/switchyard/internal/openai"
	"github.com/shusingh/switchyard/internal/sim"
	"github.com/shusingh/switchyard/internal/sim/simtest"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const model = "sim-model"

// doPost sends a chat completion request and returns the status and body.
// It is safe to call from any goroutine.
func doPost(url, body string) (int, string, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url+openai.PathChatCompletions, strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

func post(t *testing.T, url, body string) (int, string) {
	t.Helper()
	code, respBody, err := doPost(url, body)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return code, respBody
}

func chatBody(system, user string, maxTokens int, stream bool) string {
	s := `{"model":"sim-model","max_tokens":` + strconv.Itoa(maxTokens)
	if stream {
		s += `,"stream":true`
	}
	return s + `,"messages":[{"role":"system","content":"` + system + `"},{"role":"user","content":"` + user + `"}]}`
}

func TestRepeatedPrefixHitsCache(t *testing.T) {
	t.Parallel()
	srv := simtest.Start(t, sim.Options{Model: model, Cost: simtest.FastCost()})
	system := strings.Repeat("policy text ", 500) // about 1,500 tokens

	if code, body := post(t, srv.URL, chatBody(system, "first question", 2, false)); code != http.StatusOK {
		t.Fatalf("first request: %d %s", code, body)
	}
	first := srv.Engine.Stats()
	if first.CachedPromptTokens != 0 {
		t.Errorf("cold cache reported %d cached tokens, want 0", first.CachedPromptTokens)
	}

	if code, body := post(t, srv.URL, chatBody(system, "second question", 2, false)); code != http.StatusOK {
		t.Fatalf("second request: %d %s", code, body)
	}
	second := srv.Engine.Stats()
	cached := second.CachedPromptTokens - first.CachedPromptTokens
	prompt := second.PromptTokens - first.PromptTokens
	// Everything up to the user message is shared; only the tail differs.
	if cached < prompt*9/10 {
		t.Errorf("second request reused %d of %d prompt tokens, want at least 90%%", cached, prompt)
	}
}

func TestResetPrefixCache(t *testing.T) {
	t.Parallel()
	srv := simtest.Start(t, sim.Options{Model: model, Cost: simtest.FastCost()})
	system := strings.Repeat("shared prefix ", 300)
	post(t, srv.URL, chatBody(system, "a", 1, false))

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/reset_prefix_cache", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	http.DefaultClient.CloseIdleConnections()

	before := srv.Engine.Stats().CachedPromptTokens
	post(t, srv.URL, chatBody(system, "a", 1, false))
	if got := srv.Engine.Stats().CachedPromptTokens - before; got != 0 {
		t.Errorf("request after reset reused %d cached tokens, want 0", got)
	}
}

func TestCachedPrefixLowersTimeToFirstToken(t *testing.T) {
	t.Parallel()
	cost := simtest.FastCost()
	cost.PrefillTokensPerSecond = 20000 // 4,000 tokens take about 200ms
	cost.BytesPerToken = 4
	srv := simtest.Start(t, sim.Options{Model: model, Cost: cost})
	system := strings.Repeat("abcd ", 3200) // 16,000 bytes: 4,000 tokens

	ttft := func(user string) time.Duration {
		start := time.Now()
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
			srv.URL+openai.PathChatCompletions, strings.NewReader(chatBody(system, user, 2, true)))
		transport := &http.Transport{}
		defer transport.CloseIdleConnections()
		resp, err := (&http.Client{Transport: transport}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		events := openai.NewEventReader(resp.Body, 0)
		for {
			ev, err := events.Next()
			if err != nil {
				t.Fatal("stream ended before the first token")
			}
			if openai.ChunkHasContent(openai.EventData(ev)) {
				return time.Since(start)
			}
		}
	}
	cold := ttft("one")
	warm := ttft("two")
	if cold < 150*time.Millisecond {
		t.Errorf("cold TTFT = %v, want at least 150ms for a 4,000-token prefill", cold)
	}
	if warm > cold/3 {
		t.Errorf("warm TTFT = %v, want well below cold TTFT %v", warm, cold)
	}
}

func TestRejectsOverlongRequests(t *testing.T) {
	t.Parallel()
	cost := simtest.FastCost()
	cost.MaxModelLen = 100
	srv := simtest.Start(t, sim.Options{Model: model, Cost: cost})
	code, body := post(t, srv.URL, chatBody(strings.Repeat("x", 1000), "q", 10, false))
	if code != http.StatusBadRequest || !strings.Contains(body, "maximum context length") {
		t.Errorf("overlong request = %d %s, want 400 naming the context length", code, body)
	}
}

func TestRejectsUnknownModel(t *testing.T) {
	t.Parallel()
	srv := simtest.Start(t, sim.Options{Model: model, Cost: simtest.FastCost()})
	code, _ := post(t, srv.URL, `{"model":"other","messages":[]}`)
	if code != http.StatusNotFound {
		t.Errorf("unknown model status = %d, want 404", code)
	}
}

func TestMetricsExposeVLLMNames(t *testing.T) {
	t.Parallel()
	srv := simtest.Start(t, sim.Options{Model: model, Cost: simtest.FastCost()})
	post(t, srv.URL, chatBody("s", "u", 1, false))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/metrics", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	defer http.DefaultClient.CloseIdleConnections()
	body, _ := io.ReadAll(resp.Body)
	for _, name := range []string{
		"vllm:num_requests_running{", "vllm:num_requests_waiting{", "vllm:kv_cache_usage_perc{",
		"vllm:prefix_cache_queries_total{", "vllm:prefix_cache_hits_total{", `num_gpu_blocks="2340"`,
	} {
		if !strings.Contains(string(body), name) {
			t.Errorf("metrics output lacks %s", name)
		}
	}
}

func TestQueuedRequestsWaitForCapacity(t *testing.T) {
	t.Parallel()
	cost := simtest.FastCost()
	cost.MaxRunning = 1
	cost.StepOverhead = 20 * time.Millisecond
	srv := simtest.Start(t, sim.Options{Model: model, Cost: cost})

	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, _, err := doPost(srv.URL, chatBody("s", "u", 10, false))
			errs <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for srv.Engine.Stats().Waiting == 0 {
		if time.Now().After(deadline) {
			t.Fatal("second request never queued behind the first")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("request failed: %v", err)
		}
	}
	if s := srv.Engine.Stats(); s.Completed != 2 || s.Waiting != 0 {
		t.Errorf("stats after both finished = %+v, want 2 completed, 0 waiting", s)
	}
}
