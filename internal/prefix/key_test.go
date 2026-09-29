package prefix

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/shusingh/switchyard/internal/openai"
)

type msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func chatJSON(t testing.TB, model string, msgs ...msg) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"model": model, "messages": msgs, "stream": true})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseReturnsRoutingFields(t *testing.T) {
	t.Parallel()
	k := NewKeyer(16, 1024)
	req, err := k.Parse([]byte(`{"model":"m","stream":true,"max_tokens":7,"messages":[]}`), true)
	if err != nil {
		t.Fatal(err)
	}
	if req.Fields.Model != "m" || !req.Fields.Stream || req.Fields.MaxOutputTokens() != 7 {
		t.Errorf("Fields = %+v, want model m, streaming, max tokens 7", req.Fields)
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	k := NewKeyer(16, 1024)
	if _, err := k.Parse([]byte(`{"messages":[]}`), true); !errors.Is(err, openai.ErrMissingModel) {
		t.Errorf("missing model: error = %v, want %v", err, openai.ErrMissingModel)
	}
	if _, err := k.Parse([]byte(`{"model":`), true); err == nil {
		t.Error("malformed body: error = nil, want an error")
	}
}

func TestSharedPrefixSharesLeadingHashes(t *testing.T) {
	t.Parallel()
	k := NewKeyer(32, 1024)
	system := msg{"system", strings.Repeat("You are a careful assistant. ", 40)}
	short, _ := k.Parse(chatJSON(t, "m", system, msg{"user", "first question"}), true)
	long, _ := k.Parse(chatJSON(t, "m", system, msg{"user", "first question"},
		msg{"assistant", "an answer"}, msg{"user", "a follow-up"}), true)

	if len(short.Hashes) == 0 || len(long.Hashes) <= len(short.Hashes) {
		t.Fatalf("got %d and %d hashes, want the longer conversation to have more", len(short.Hashes), len(long.Hashes))
	}
	// All of the short request's full blocks lie inside the shared prefix,
	// except possibly the last one, which may straddle the point where the
	// conversations diverge.
	shared := len(short.Hashes) - 1
	if !slices.Equal(short.Hashes[:shared], long.Hashes[:shared]) {
		t.Error("requests with a shared prefix do not share leading hashes")
	}
}

func TestDifferentModelsNeverShareKeys(t *testing.T) {
	t.Parallel()
	k := NewKeyer(16, 1024)
	body := msg{"user", strings.Repeat("same text ", 50)}
	a, _ := k.Parse(chatJSON(t, "model-a", body), true)
	b, _ := k.Parse(chatJSON(t, "model-b", body), true)
	if a.Hashes[0] == b.Hashes[0] {
		t.Error("identical prompts to different models share a first block hash")
	}
}

func TestSeparatorsPreventAmbiguity(t *testing.T) {
	t.Parallel()
	// Role "us" + content "er..." must not key like role "user" + content "...".
	k := NewKeyer(8, 1024)
	a, _ := k.Parse(chatJSON(t, "m", msg{"us", "erhello there, friend"}), true)
	b, _ := k.Parse(chatJSON(t, "m", msg{"user", "hello there, friend"}), true)
	if slices.Equal(a.Hashes, b.Hashes) {
		t.Error("different messages produced identical keys")
	}
}

func TestPartialBlockIsNotHashed(t *testing.T) {
	t.Parallel()
	k := NewKeyer(64, 1024)
	req, _ := k.Parse([]byte(`{"model":"m","prompt":"`+strings.Repeat("x", 100)+`"}`), false)
	if want := req.CanonicalBytes / 64; len(req.Hashes) != want {
		t.Errorf("got %d hashes for %d canonical bytes, want %d full blocks", len(req.Hashes), req.CanonicalBytes, want)
	}
}

func TestBlockCap(t *testing.T) {
	t.Parallel()
	k := NewKeyer(16, 3)
	req, _ := k.Parse([]byte(`{"model":"m","prompt":"`+strings.Repeat("y", 1000)+`"}`), false)
	if len(req.Hashes) != 3 {
		t.Errorf("got %d hashes, want the cap of 3", len(req.Hashes))
	}
}

func TestKeysAreDeterministicWithinAKeyer(t *testing.T) {
	t.Parallel()
	k := NewKeyer(16, 1024)
	body := chatJSON(t, "m", msg{"user", strings.Repeat("abc ", 100)})
	a, _ := k.Parse(body, true)
	b, _ := k.Parse(body, true)
	if !slices.Equal(a.Hashes, b.Hashes) {
		t.Error("the same body produced different keys")
	}
}

// FuzzParse checks that arbitrary bodies never panic and that appending a
// message never changes the leading full blocks of the key.
func FuzzParse(f *testing.F) {
	f.Add(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`, "more")
	f.Add(`{"model":"m","prompt":"abc"}`, "x")
	f.Add(`{"model":"m","tools":[{"type":"function"}],"messages":[]}`, "")
	f.Add(`not json`, "y")
	f.Fuzz(func(t *testing.T, raw, extra string) {
		k := NewKeyer(8, 4096)
		_, _ = k.Parse([]byte(raw), true)
		_, _ = k.Parse([]byte(raw), false)

		base := chatJSON(t, "m", msg{"user", raw})
		grown := chatJSON(t, "m", msg{"user", raw}, msg{"user", extra})
		a, errA := k.Parse(base, true)
		b, errB := k.Parse(grown, true)
		if errA != nil || errB != nil {
			t.Fatalf("valid bodies failed to parse: %v, %v", errA, errB)
		}
		if len(a.Hashes) > 0 && !slices.Equal(a.Hashes[:len(a.Hashes)-1], b.Hashes[:len(a.Hashes)-1]) {
			t.Fatal("appending a message changed the key's leading blocks")
		}
	})
}

func benchmarkParse(b *testing.B, promptBytes int) {
	k := NewKeyer(128, 4096)
	body := chatJSON(b, "m",
		msg{"system", strings.Repeat("s", promptBytes/2)},
		msg{"user", strings.Repeat("u", promptBytes/2)})
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := k.Parse(body, true); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParse1KB(b *testing.B)   { benchmarkParse(b, 1<<10) }
func BenchmarkParse32KB(b *testing.B)  { benchmarkParse(b, 32<<10) }
func BenchmarkParse512KB(b *testing.B) { benchmarkParse(b, 512<<10) }
