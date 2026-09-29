package workload

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

// MooncakeBlockTokens is the block size behind the hash_ids in the Mooncake
// traces: each ID names one 512-token block together with everything before
// it.
const MooncakeBlockTokens = 512

// MooncakeConfig controls how a Mooncake trace is replayed.
//
// The traces come from a large production deployment; replaying them at full
// rate would overwhelm a single consumer GPU. TimeScale stretches the
// timeline (2 halves the arrival rate), and MaxPromptTokens drops requests
// that exceed the replicas' context window. Dropped requests are counted, not
// hidden.
type MooncakeConfig struct {
	Path string
	// TimeScale multiplies every arrival time. Values above 1 slow the trace.
	TimeScale float64
	// Start and Duration select a window of the trace, measured in trace
	// time. A zero Duration means until the end.
	Start    time.Duration
	Duration time.Duration
	// MaxPromptTokens drops requests with longer prompts. Zero keeps all.
	MaxPromptTokens int
	// MaxOutputTokens caps each request's output length. Zero keeps the
	// trace's value.
	MaxOutputTokens int
	// MaxRequests stops after this many requests. Zero keeps all.
	MaxRequests int
}

// MooncakeStats reports what a replay kept and dropped.
type MooncakeStats struct {
	Read       int
	Kept       int
	TooLong    int
	OutsideWin int
}

type mooncakeRecord struct {
	Timestamp    int64   `json:"timestamp"` // milliseconds
	InputLength  int     `json:"input_length"`
	OutputLength int     `json:"output_length"`
	HashIDs      []int64 `json:"hash_ids"`
}

// Mooncake builds a workload from a Mooncake trace file. Every request becomes
// a single-turn session. Requests sharing leading hash IDs get byte-identical
// leading text, so they share a cacheable prefix exactly as in the trace.
func Mooncake(cfg MooncakeConfig) (*Workload, MooncakeStats, error) {
	f, err := os.Open(cfg.Path)
	if err != nil {
		return nil, MooncakeStats{}, fmt.Errorf("open trace: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only file; a close error cannot lose data
	return ParseMooncake(f, cfg)
}

// ParseMooncake builds a workload from Mooncake trace records read from r.
func ParseMooncake(r io.Reader, cfg MooncakeConfig) (*Workload, MooncakeStats, error) {
	if cfg.TimeScale <= 0 {
		cfg.TimeScale = 1
	}
	var stats MooncakeStats
	w := &Workload{Name: "mooncake"}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for line := 1; sc.Scan(); line++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var rec mooncakeRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return nil, stats, fmt.Errorf("trace line %d: %w", line, err)
		}
		if err := rec.validate(); err != nil {
			return nil, stats, fmt.Errorf("trace line %d: %w", line, err)
		}
		stats.Read++

		at := time.Duration(rec.Timestamp) * time.Millisecond
		if at < cfg.Start || (cfg.Duration > 0 && at >= cfg.Start+cfg.Duration) {
			stats.OutsideWin++
			continue
		}
		if cfg.MaxPromptTokens > 0 && rec.InputLength > cfg.MaxPromptTokens {
			stats.TooLong++
			continue
		}
		output := rec.OutputLength
		if cfg.MaxOutputTokens > 0 {
			output = min(output, cfg.MaxOutputTokens)
		}
		w.Sessions = append(w.Sessions, Session{
			ID:    "mooncake-" + strconv.Itoa(line),
			Start: time.Duration(float64(at-cfg.Start) * cfg.TimeScale),
			Turns: []Turn{{Prompt: rec.segments(), OutputTokens: max(output, 1)}},
		})
		stats.Kept++
		if cfg.MaxRequests > 0 && stats.Kept >= cfg.MaxRequests {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, stats, fmt.Errorf("read trace: %w", err)
	}
	return w, stats, nil
}

func (r mooncakeRecord) validate() error {
	if len(r.HashIDs) == 0 {
		return errors.New("record has no hash_ids")
	}
	full := (len(r.HashIDs) - 1) * MooncakeBlockTokens
	if r.InputLength <= full || r.InputLength > full+MooncakeBlockTokens {
		return fmt.Errorf("input_length %d is inconsistent with %d hash_ids of %d tokens",
			r.InputLength, len(r.HashIDs), MooncakeBlockTokens)
	}
	return nil
}

// segments renders one segment per trace block; the final block is partial.
func (r mooncakeRecord) segments() []Segment {
	segs := make([]Segment, len(r.HashIDs))
	for i, id := range r.HashIDs {
		segs[i] = Segment{Seed: SeedFor("mooncake", strconv.FormatInt(id, 10)), Words: MooncakeBlockTokens}
	}
	segs[len(segs)-1].Words = r.InputLength - (len(r.HashIDs)-1)*MooncakeBlockTokens
	return segs
}
