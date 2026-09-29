package workload

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func smallAgentConfig() AgentConfig {
	cfg := DefaultAgentConfig()
	cfg.Duration = 30 * time.Second
	cfg.SessionsPerSecond = 2
	cfg.SystemWords = 50
	cfg.MinToolResultWords, cfg.MaxToolResultWords = 5, 20
	return cfg
}

func TestSegmentIsDeterministic(t *testing.T) {
	t.Parallel()
	a := renderSegments([]Segment{{Seed: 42, Words: 100}})
	b := renderSegments([]Segment{{Seed: 42, Words: 100}})
	c := renderSegments([]Segment{{Seed: 43, Words: 100}})
	if a != b {
		t.Error("the same segment rendered two different texts")
	}
	if a == c {
		t.Error("different seeds rendered the same text")
	}
	if got := len(strings.Fields(a)); got != 100 {
		t.Errorf("segment of 100 words rendered %d words", got)
	}
}

func TestShorterSegmentIsPrefixOfLonger(t *testing.T) {
	t.Parallel()
	// A partial final block must share its text with the full block of the
	// same seed, or trace prefixes would not line up.
	short := renderSegments([]Segment{{Seed: 7, Words: 30}})
	long := renderSegments([]Segment{{Seed: 7, Words: 512}})
	if !strings.HasPrefix(long, short) {
		t.Error("a 30-word segment is not a prefix of the 512-word segment with the same seed")
	}
}

func TestAgentIsReproducible(t *testing.T) {
	t.Parallel()
	a, err := Agent(smallAgentConfig())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Agent(smallAgentConfig())
	if diff := cmp.Diff(a, b); diff != "" {
		t.Errorf("same config produced different workloads (-first +second):\n%s", diff)
	}
	if len(a.Sessions) == 0 {
		t.Fatal("agent workload has no sessions")
	}
}

func TestAgentTurnsShareGrowingPrefix(t *testing.T) {
	t.Parallel()
	w, err := Agent(smallAgentConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := w.Sessions[0]
	if len(s.Turns) < 2 {
		t.Fatalf("session has %d turns, want at least 2", len(s.Turns))
	}
	if s.Turns[0].ThinkTime != 0 {
		t.Errorf("first turn think time = %v, want 0", s.Turns[0].ThinkTime)
	}
	for i := 1; i < len(s.Turns); i++ {
		prev, cur := messagesText(t, &s.Turns[i-1]), messagesText(t, &s.Turns[i])
		if len(cur) != len(prev)+2 {
			t.Fatalf("turn %d has %d messages, want %d (previous plus a call and a result)", i, len(cur), len(prev)+2)
		}
		if diff := cmp.Diff(prev, cur[:len(prev)]); diff != "" {
			t.Errorf("turn %d does not start with turn %d's messages:\n%s", i, i-1, diff)
		}
	}
}

func TestAgentSessionsOfOneAppShareSystemPrompt(t *testing.T) {
	t.Parallel()
	cfg := smallAgentConfig()
	cfg.Apps = 1
	w, err := Agent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first := messagesText(t, &w.Sessions[0].Turns[0])[0]
	for _, s := range w.Sessions[1:] {
		if got := messagesText(t, &s.Turns[0])[0]; got != first {
			t.Fatalf("session %s has a different system prompt with a single app", s.ID)
		}
	}
}

func TestAgentConfigValidation(t *testing.T) {
	t.Parallel()
	for _, mutate := range []func(*AgentConfig){
		func(c *AgentConfig) { c.Duration = 0 },
		func(c *AgentConfig) { c.SessionsPerSecond = 0 },
		func(c *AgentConfig) { c.Apps = 0 },
		func(c *AgentConfig) { c.MinTurns, c.MaxTurns = 5, 2 },
		func(c *AgentConfig) { c.OutputTokens = 0 },
		func(c *AgentConfig) { c.LongPauseProbability = 2 },
	} {
		cfg := DefaultAgentConfig()
		mutate(&cfg)
		if _, err := Agent(cfg); err == nil {
			t.Errorf("Agent(%+v) error = nil, want a validation error", cfg)
		}
	}
}

func TestTurnBody(t *testing.T) {
	t.Parallel()
	turn := Turn{Prompt: []Segment{{Seed: 1, Words: 3}}, OutputTokens: 9}
	body, err := turn.Body("m")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if req["model"] != "m" || req["stream"] != true || req["ignore_eos"] != true || req["max_tokens"] != float64(9) {
		t.Errorf("body = %s, want model m, streaming, ignore_eos, max_tokens 9", body)
	}
	if turn.Path() != "/v1/completions" {
		t.Errorf("Path() = %q, want /v1/completions", turn.Path())
	}
}

func TestPrefixRepetition(t *testing.T) {
	t.Parallel()
	cfg := DefaultPrefixRepetitionConfig()
	cfg.Prefixes = 2
	cfg.PrefixWords, cfg.SuffixWords = 20, 5
	w, err := PrefixRepetition(cfg)
	if err != nil {
		t.Fatal(err)
	}
	prefixes := map[uint64]bool{}
	for _, s := range w.Sessions {
		prefixes[s.Turns[0].Prompt[0].Seed] = true
	}
	if len(prefixes) != 2 {
		t.Errorf("workload used %d distinct prefixes, want 2", len(prefixes))
	}
}

const sampleTrace = `{"timestamp": 0, "input_length": 600, "output_length": 50, "hash_ids": [0, 1]}
{"timestamp": 1000, "input_length": 1100, "output_length": 20, "hash_ids": [0, 1, 2]}

{"timestamp": 5000, "input_length": 5000, "output_length": 10, "hash_ids": [3, 4, 5, 6, 7, 8, 9, 10, 11, 12]}
`

func TestParseMooncake(t *testing.T) {
	t.Parallel()
	w, stats, err := ParseMooncake(strings.NewReader(sampleTrace), MooncakeConfig{TimeScale: 2, MaxPromptTokens: 2000, MaxOutputTokens: 30})
	if err != nil {
		t.Fatal(err)
	}
	if want := (MooncakeStats{Read: 3, Kept: 2, TooLong: 1}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	if got := w.Sessions[1].Start; got != 2*time.Second {
		t.Errorf("second request starts at %v, want 2s with time scale 2", got)
	}
	if got := w.Sessions[0].Turns[0].OutputTokens; got != 30 {
		t.Errorf("output tokens = %d, want capped at 30", got)
	}
	// The first request is blocks [0, 1] with block 1 partial; the second is
	// [0, 1, 2]. The whole first prompt is therefore a prefix of the second.
	a := renderSegments(w.Sessions[0].Turns[0].Prompt)
	b := renderSegments(w.Sessions[1].Turns[0].Prompt)
	if !strings.HasPrefix(b, a) {
		t.Error("requests with shared hash IDs do not share rendered prefixes")
	}
	if got := w.Sessions[0].Turns[0].PromptWords(); got != 600 {
		t.Errorf("PromptWords() = %d, want 600 (the trace's input_length)", got)
	}
}

func TestParseMooncakeRejectsInconsistentRecords(t *testing.T) {
	t.Parallel()
	for _, rec := range []string{
		`{"timestamp": 0, "input_length": 10, "output_length": 1, "hash_ids": []}`,
		`{"timestamp": 0, "input_length": 2000, "output_length": 1, "hash_ids": [1]}`,
		`not json`,
	} {
		if _, _, err := ParseMooncake(strings.NewReader(rec), MooncakeConfig{}); err == nil {
			t.Errorf("ParseMooncake(%s) error = nil, want an error", rec)
		}
	}
}

// TestRealMooncakeTraces parses the downloaded traces when present
// (make traces). It is skipped in environments without them.
func TestRealMooncakeTraces(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"toolagent_trace.jsonl", "conversation_trace.jsonl"} {
		path := filepath.Join("..", "..", "bench", "traces", name)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("%s not downloaded; run make traces", name)
		}
		_, stats, err := Mooncake(MooncakeConfig{Path: path})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if stats.Kept == 0 || stats.Kept != stats.Read {
			t.Errorf("%s: stats = %+v, want every record kept with no filters", name, stats)
		}
	}
}

func messagesText(t *testing.T, turn *Turn) []string {
	t.Helper()
	out := make([]string, len(turn.Messages))
	for i, m := range turn.Messages {
		out[i] = m.Role + ":" + renderSegments(m.Content)
	}
	return out
}
