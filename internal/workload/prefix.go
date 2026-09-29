package workload

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"
)

// PrefixRepetitionConfig parameterizes single requests that share one of a
// few long prefixes, the shape of vLLM's prefix_repetition benchmark dataset.
type PrefixRepetitionConfig struct {
	Seed              uint64
	Duration          time.Duration
	RequestsPerSecond float64
	Prefixes          int
	PrefixWords       int
	SuffixWords       int
	OutputTokens      int
}

// DefaultPrefixRepetitionConfig returns a small shared-prefix workload.
func DefaultPrefixRepetitionConfig() PrefixRepetitionConfig {
	return PrefixRepetitionConfig{
		Seed:              1,
		Duration:          2 * time.Minute,
		RequestsPerSecond: 4,
		Prefixes:          8,
		PrefixWords:       6000,
		SuffixWords:       200,
		OutputTokens:      64,
	}
}

// PrefixRepetition generates the workload. Arrivals are Poisson and each
// request picks a prefix uniformly at random.
func PrefixRepetition(cfg PrefixRepetitionConfig) (*Workload, error) {
	switch {
	case cfg.Duration <= 0 || cfg.RequestsPerSecond <= 0:
		return nil, fmt.Errorf("prefix repetition: duration and rate must be positive")
	case cfg.Prefixes < 1 || cfg.PrefixWords < 1 || cfg.OutputTokens < 1:
		return nil, fmt.Errorf("prefix repetition: prefixes, prefix words, and output tokens must be at least 1")
	}
	rng := rand.New(rand.NewPCG(cfg.Seed, 0x9e3779b9)) //nolint:gosec // G404: reproducible workload, not security
	w := &Workload{Name: "prefix_repetition"}
	var at time.Duration
	for i := 0; ; i++ {
		at += exponential(rng, cfg.RequestsPerSecond)
		if at >= cfg.Duration {
			break
		}
		id := "prefix-" + strconv.Itoa(i)
		prefix := strconv.Itoa(rng.IntN(cfg.Prefixes))
		w.Sessions = append(w.Sessions, Session{ID: id, Start: at, Turns: []Turn{{
			Prompt: []Segment{
				{Seed: SeedFor("prefix", prefix), Words: cfg.PrefixWords},
				{Seed: SeedFor(id, "suffix"), Words: cfg.SuffixWords},
			},
			OutputTokens: cfg.OutputTokens,
		}}})
	}
	return w, nil
}
