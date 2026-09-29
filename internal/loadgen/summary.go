package loadgen

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Summary aggregates the records of one run.
type Summary struct {
	Requests  int `json:"requests"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	// DurationS spans from the first send to the last completion.
	DurationS          float64     `json:"duration_s"`
	RequestsPerSecond  float64     `json:"requests_per_second"`
	OutputTokensPerSec float64     `json:"output_tokens_per_second"`
	TTFT               Percentiles `json:"ttft_ms"`
	TPOT               Percentiles `json:"tpot_ms"`
	E2E                Percentiles `json:"e2e_ms"`
	// SLOMS is the TTFT objective used for Goodput.
	SLOMS float64 `json:"slo_ms"`
	// Goodput is the fraction of requests that succeeded with TTFT within
	// the objective.
	Goodput float64 `json:"goodput"`
	// PredictionError holds percentiles of |predicted - actual| TTFT over
	// requests the router annotated with a prediction.
	PredictionError Percentiles `json:"prediction_error_ms"`
	// PredictionBiasMS is the median of predicted minus actual TTFT:
	// positive means the router overestimates.
	PredictionBiasMS float64 `json:"prediction_bias_ms"`
	// BelievedHitRate is the fraction of prompt blocks the router believed
	// were cached on the chosen backend. Compared with the engines' measured
	// hit rate it shows how far the router's index drifts from reality.
	BelievedHitRate float64 `json:"believed_hit_rate"`
}

// Percentiles holds latency percentiles in milliseconds.
type Percentiles struct {
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

// Summarize computes a Summary. Latency percentiles cover successful requests
// only; failures are counted separately and lower goodput.
func Summarize(records []Record, slo time.Duration) Summary {
	s := Summary{Requests: len(records), SLOMS: ms(slo)}
	var ttft, tpot, e2e, predErr, predBias []float64
	matchedBlocks, promptBlocks := 0, 0
	first, last := math.Inf(1), math.Inf(-1)
	outputTokens, good := 0, 0
	for i := range records {
		r := &records[i]
		first = math.Min(first, r.SentMS)
		last = math.Max(last, r.SentMS+r.E2EMS)
		if !r.OK() {
			s.Failed++
			continue
		}
		s.Succeeded++
		outputTokens += r.CompletionTokens
		ttft = append(ttft, r.TTFTMS)
		e2e = append(e2e, r.E2EMS)
		if r.CompletionTokens > 1 {
			tpot = append(tpot, r.TPOTMS)
		}
		if r.TTFTMS <= s.SLOMS {
			good++
		}
		if r.PredictedTTFTMS > 0 {
			predErr = append(predErr, math.Abs(r.PredictedTTFTMS-r.TTFTMS))
			predBias = append(predBias, r.PredictedTTFTMS-r.TTFTMS)
		}
		matchedBlocks += r.MatchedBlocks
		promptBlocks += r.PromptBlocks
	}
	if s.Requests > 0 {
		s.Goodput = float64(good) / float64(s.Requests)
	}
	if span := (last - first) / 1000; span > 0 {
		s.DurationS = span
		s.RequestsPerSecond = float64(s.Succeeded) / span
		s.OutputTokensPerSec = float64(outputTokens) / span
	}
	s.TTFT, s.TPOT, s.E2E = percentiles(ttft), percentiles(tpot), percentiles(e2e)
	s.PredictionError = percentiles(predErr)
	s.PredictionBiasMS = percentiles(predBias).P50
	if promptBlocks > 0 {
		s.BelievedHitRate = float64(matchedBlocks) / float64(promptBlocks)
	}
	return s
}

func percentiles(v []float64) Percentiles {
	if len(v) == 0 {
		return Percentiles{}
	}
	slices.Sort(v)
	return Percentiles{P50: quantile(v, 0.50), P90: quantile(v, 0.90), P99: quantile(v, 0.99), Max: v[len(v)-1]}
}

// quantile returns the q-quantile of sorted values by linear interpolation
// between closest ranks.
func quantile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// ReadRecords reads a JSONL file of records.
func ReadRecords(path string) ([]Record, error) {
	f, err := os.Open(path) //nolint:gosec // G304: path is an operator-supplied results file
	if err != nil {
		return nil, fmt.Errorf("open results: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only file; a close error cannot lose data
	var records []Record
	dec := json.NewDecoder(bufio.NewReader(f))
	for {
		var r Record
		if err := dec.Decode(&r); err != nil {
			if errors.Is(err, io.EOF) {
				return records, nil
			}
			return nil, fmt.Errorf("decode %s record %d: %w", path, len(records)+1, err)
		}
		records = append(records, r)
	}
}

// CacheCounters are vLLM's cumulative prefix-cache counters, summed over a
// set of servers. Both count prompt tokens.
type CacheCounters struct {
	Queries float64 `json:"queries"`
	Hits    float64 `json:"hits"`
}

// HitRate returns the fraction of prompt tokens served from cache between
// two snapshots of the counters.
func HitRate(before, after CacheCounters) float64 {
	queries := after.Queries - before.Queries
	if queries <= 0 {
		return 0
	}
	return (after.Hits - before.Hits) / queries
}

// ScrapeCacheCounters reads and sums the prefix-cache counters from each
// server's Prometheus endpoint. It works against vLLM and the simulated
// engine, which use the same metric names.
func ScrapeCacheCounters(ctx context.Context, client *http.Client, baseURLs []string) (CacheCounters, error) {
	var total CacheCounters
	for _, base := range baseURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/metrics", http.NoBody)
		if err != nil {
			return CacheCounters{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return CacheCounters{}, fmt.Errorf("scrape %s: %w", base, err)
		}
		c, err := parseCacheCounters(resp.Body)
		resp.Body.Close()
		if err != nil {
			return CacheCounters{}, fmt.Errorf("scrape %s: %w", base, err)
		}
		total.Queries += c.Queries
		total.Hits += c.Hits
	}
	return total, nil
}

func parseCacheCounters(r io.Reader) (CacheCounters, error) {
	var c CacheCounters
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		// Compare the exact metric name: companion series such as
		// *_created share the counter's name as a prefix.
		name, _, _ := strings.Cut(line, "{")
		name, _, _ = strings.Cut(name, " ")
		var target *float64
		switch name {
		case "vllm:prefix_cache_queries_total":
			target = &c.Queries
		case "vllm:prefix_cache_hits_total":
			target = &c.Hits
		default:
			continue
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			return CacheCounters{}, fmt.Errorf("parse %q: %w", line, err)
		}
		*target += v
	}
	return c, sc.Err()
}
