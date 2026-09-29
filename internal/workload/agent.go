package workload

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"time"
)

// AgentConfig parameterizes synthetic agent sessions.
//
// Each session belongs to one of Apps applications. Every session of an app
// starts with the same system prompt and tool definitions, then grows by one
// assistant tool call and one tool result per turn, and resends its whole
// history each time, as tool-using agents do. The shared per-app prefix and
// the growing per-session context are the two kinds of reuse a prefix-aware
// router can exploit.
type AgentConfig struct {
	Seed uint64
	// Duration is the window in which sessions start.
	Duration time.Duration
	// SessionsPerSecond is the mean session arrival rate (Poisson).
	SessionsPerSecond float64

	// Apps is the number of distinct system prompts. AppSkew > 0 makes some
	// apps more popular than others (Zipf exponent); 0 means uniform.
	Apps    int
	AppSkew float64
	// SystemWords is the size of each app's system prompt and tool
	// definitions combined.
	SystemWords int
	// TaskWords is the size of each session's opening user message.
	TaskWords int

	// MinTurns and MaxTurns bound the number of requests per session.
	MinTurns, MaxTurns int
	// ToolCallWords is the size of each assistant turn kept in history.
	ToolCallWords int
	// MinToolResultWords and MaxToolResultWords bound each tool result.
	MinToolResultWords, MaxToolResultWords int
	// OutputTokens is the number of tokens each request generates.
	OutputTokens int
	// MaxContextWords ends a session before a turn whose prompt would exceed
	// it, as a real agent compacts or finishes before overflowing the
	// model's context window. Zero means no limit.
	MaxContextWords int

	// MedianThinkTime is the typical pause between turns (tool execution).
	// A fraction LongPauseProbability of pauses is instead drawn uniformly
	// from [LongPauseMin, LongPauseMax], modeling slower tools and humans.
	MedianThinkTime      time.Duration
	LongPauseProbability float64
	LongPauseMin         time.Duration
	LongPauseMax         time.Duration
}

// DefaultAgentConfig returns a configuration sized against the reference GPU
// setup (docs/adr/0009-benchmark-replica-configuration.md): 16 apps with
// 4,000-word prefixes make a 64,000-token shared working set, more than one
// replica's 37,440-token cache and less than the pool's 149,760.
func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		Seed:                 1,
		Duration:             5 * time.Minute,
		SessionsPerSecond:    0.5,
		Apps:                 16,
		AppSkew:              1.0,
		SystemWords:          4000,
		TaskWords:            150,
		MinTurns:             4,
		MaxTurns:             16,
		ToolCallWords:        60,
		MinToolResultWords:   100,
		MaxToolResultWords:   800,
		OutputTokens:         64,
		MaxContextWords:      12000,
		MedianThinkTime:      2 * time.Second,
		LongPauseProbability: 0.1,
		LongPauseMin:         20 * time.Second,
		LongPauseMax:         60 * time.Second,
	}
}

// Validate reports the first invalid field.
func (c AgentConfig) Validate() error {
	switch {
	case c.Duration <= 0:
		return fmt.Errorf("agent: duration must be positive")
	case c.SessionsPerSecond <= 0:
		return fmt.Errorf("agent: sessions per second must be positive")
	case c.Apps < 1:
		return fmt.Errorf("agent: apps must be at least 1")
	case c.MinTurns < 1 || c.MaxTurns < c.MinTurns:
		return fmt.Errorf("agent: need 1 <= min turns <= max turns, got %d and %d", c.MinTurns, c.MaxTurns)
	case c.MinToolResultWords < 0 || c.MaxToolResultWords < c.MinToolResultWords:
		return fmt.Errorf("agent: need 0 <= min tool result words <= max, got %d and %d", c.MinToolResultWords, c.MaxToolResultWords)
	case c.OutputTokens < 1:
		return fmt.Errorf("agent: output tokens must be at least 1")
	case c.LongPauseProbability < 0 || c.LongPauseProbability > 1:
		return fmt.Errorf("agent: long pause probability must be in [0, 1]")
	}
	return nil
}

// Agent generates synthetic agent sessions. The same config always produces
// the same workload.
func Agent(cfg AgentConfig) (*Workload, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewPCG(cfg.Seed, 0x5eed)) //nolint:gosec // G404: reproducible workload, not security
	pickApp := appPicker(rng, cfg.Apps, cfg.AppSkew)

	w := &Workload{Name: "agent"}
	var at time.Duration
	for i := 0; ; i++ {
		at += exponential(rng, cfg.SessionsPerSecond)
		if at >= cfg.Duration {
			break
		}
		app := strconv.Itoa(pickApp())
		id := "agent-" + strconv.Itoa(i)
		system := Message{Role: "system", Content: []Segment{{Seed: SeedFor("app", app, "system"), Words: cfg.SystemWords}}}
		history := []Message{system, {Role: "user", Content: []Segment{{Seed: SeedFor(id, "task"), Words: cfg.TaskWords}}}}

		turns := cfg.MinTurns + rng.IntN(cfg.MaxTurns-cfg.MinTurns+1)
		s := Session{ID: id, Start: at, Turns: make([]Turn, 0, turns)}
		for turn := range turns {
			if cfg.MaxContextWords > 0 && messageWords(history) > cfg.MaxContextWords {
				break
			}
			var think time.Duration
			if turn > 0 {
				think = thinkTime(rng, cfg)
			}
			s.Turns = append(s.Turns, Turn{
				ThinkTime:    think,
				Messages:     append([]Message(nil), history...),
				OutputTokens: cfg.OutputTokens,
			})
			// The agent appends its tool call and the tool's result, then
			// resends everything on the next turn.
			resultWords := cfg.MinToolResultWords + rng.IntN(cfg.MaxToolResultWords-cfg.MinToolResultWords+1)
			history = append(history,
				Message{Role: "assistant", Content: []Segment{{Seed: SeedFor(id, "call", strconv.Itoa(turn)), Words: cfg.ToolCallWords}}},
				Message{Role: "user", Content: []Segment{{Seed: SeedFor(id, "result", strconv.Itoa(turn)), Words: resultWords}}},
			)
		}
		if len(s.Turns) > 0 {
			w.Sessions = append(w.Sessions, s)
		}
	}
	return w, nil
}

func messageWords(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		for _, s := range m.Content {
			n += s.Words
		}
	}
	return n
}

// appPicker returns a function that draws app indices, uniformly if skew is
// zero and Zipf-distributed otherwise.
func appPicker(rng *rand.Rand, apps int, skew float64) func() int {
	if skew <= 0 {
		return func() int { return rng.IntN(apps) }
	}
	cdf := make([]float64, apps)
	total := 0.0
	for i := range apps {
		total += 1 / math.Pow(float64(i+1), skew)
		cdf[i] = total
	}
	return func() int {
		u := rng.Float64() * total
		for i, c := range cdf {
			if u < c {
				return i
			}
		}
		return apps - 1
	}
}

func thinkTime(rng *rand.Rand, cfg AgentConfig) time.Duration {
	if rng.Float64() < cfg.LongPauseProbability {
		span := cfg.LongPauseMax - cfg.LongPauseMin
		return cfg.LongPauseMin + time.Duration(rng.Float64()*float64(span))
	}
	// Log-normal around the median with sigma 0.75: mostly quick tool calls,
	// with a moderate tail.
	return time.Duration(float64(cfg.MedianThinkTime) * math.Exp(0.75*rng.NormFloat64()))
}

// exponential draws an inter-arrival gap for a Poisson process with the given
// rate per second.
func exponential(rng *rand.Rand, perSecond float64) time.Duration {
	return time.Duration(rng.ExpFloat64() / perSecond * float64(time.Second))
}
