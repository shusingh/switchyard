// Command loadgen replays benchmark workloads against Switchyard (or any
// OpenAI-compatible endpoint) and reports the results.
//
// Usage:
//
//	loadgen run -url http://localhost:8080 -model M -workload agent -out results/run.jsonl
//	loadgen report -slo 2s results/*.jsonl
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shusingh/switchyard/internal/loadgen"
	"github.com/shusingh/switchyard/internal/workload"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCommand(os.Args[2:])
	case "report":
		err = reportCommand(os.Args[2:], os.Stdout)
	case "-h", "-help", "--help", "help":
		usage(os.Stdout)
		return
	default:
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `usage:
  loadgen run    -url URL -model MODEL -workload agent|prefix|mooncake -out FILE [flags]
  loadgen report [-slo DURATION] [-group] FILE...

Run "loadgen run -h" or "loadgen report -h" for flags.`)
}

// Metadata is written next to a run's records as <out>.meta.json.
type Metadata struct {
	Label      string                  `json:"label"`
	Workload   string                  `json:"workload"`
	Parameters map[string]string       `json:"parameters"`
	Requests   int                     `json:"requests"`
	Started    time.Time               `json:"started"`
	Finished   time.Time               `json:"finished"`
	Before     *loadgen.CacheCounters  `json:"cache_before,omitempty"`
	After      *loadgen.CacheCounters  `json:"cache_after,omitempty"`
	HitRate    *float64                `json:"cache_hit_rate,omitempty"`
	Mooncake   *workload.MooncakeStats `json:"mooncake,omitempty"`
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	url := fs.String("url", "http://localhost:8080", "base URL of the endpoint under test")
	model := fs.String("model", "", "model name to send (required)")
	out := fs.String("out", "", "JSONL file for per-request records (required)")
	label := fs.String("label", "", "label for this run in reports (defaults to the file name)")
	kind := fs.String("workload", "agent", "workload: agent, prefix, or mooncake")
	backends := fs.String("backends", "", "comma-separated backend URLs whose prefix-cache counters are recorded")
	reset := fs.Bool("reset-caches", false, "POST /reset_prefix_cache to every backend before the run")
	maxInFlight := fs.Int("max-in-flight", 4096, "maximum outstanding requests")
	timeout := fs.Duration("timeout", 10*time.Minute, "per-request timeout")

	agent := workload.DefaultAgentConfig()
	prefix := workload.DefaultPrefixRepetitionConfig()
	var mooncake workload.MooncakeConfig
	seed := fs.Uint64("seed", 1, "random seed for synthetic workloads")
	duration := fs.Duration("duration", agent.Duration, "window in which sessions start (agent, prefix)")
	rate := fs.Float64("rate", 0, "sessions per second (agent) or requests per second (prefix); 0 keeps the default")
	outputTokens := fs.Int("output-tokens", agent.OutputTokens, "tokens generated per request (agent, prefix)")
	fs.IntVar(&agent.Apps, "apps", agent.Apps, "agent: number of distinct system prompts")
	fs.Float64Var(&agent.AppSkew, "app-skew", agent.AppSkew, "agent: Zipf exponent of app popularity; 0 is uniform")
	fs.IntVar(&agent.SystemWords, "system-words", agent.SystemWords, "agent: words in each system prompt")
	fs.IntVar(&agent.MinTurns, "min-turns", agent.MinTurns, "agent: minimum turns per session")
	fs.IntVar(&agent.MaxTurns, "max-turns", agent.MaxTurns, "agent: maximum turns per session")
	fs.IntVar(&prefix.Prefixes, "prefixes", prefix.Prefixes, "prefix: number of shared prefixes")
	fs.IntVar(&prefix.PrefixWords, "prefix-words", prefix.PrefixWords, "prefix: words in each shared prefix")
	fs.IntVar(&prefix.SuffixWords, "suffix-words", prefix.SuffixWords, "prefix: words in each unique suffix")
	fs.StringVar(&mooncake.Path, "trace", "bench/traces/toolagent_trace.jsonl", "mooncake: trace file")
	fs.Float64Var(&mooncake.TimeScale, "time-scale", 1, "mooncake: multiply arrival times (2 halves the rate)")
	fs.DurationVar(&mooncake.Start, "trace-start", 0, "mooncake: start of the replayed window, in trace time")
	fs.DurationVar(&mooncake.Duration, "trace-duration", 0, "mooncake: length of the replayed window; 0 means to the end")
	fs.IntVar(&mooncake.MaxPromptTokens, "max-prompt-tokens", 0, "mooncake: drop requests with longer prompts; 0 keeps all")
	fs.IntVar(&mooncake.MaxOutputTokens, "max-output-tokens", 0, "mooncake: cap output tokens; 0 keeps the trace's")
	fs.IntVar(&mooncake.MaxRequests, "max-requests", 0, "mooncake: stop after this many requests; 0 keeps all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *model == "" || *out == "" {
		return errors.New("run: -model and -out are required")
	}

	params := map[string]string{}
	fs.Visit(func(f *flag.Flag) { params[f.Name] = f.Value.String() })
	meta := Metadata{Label: *label, Workload: *kind, Parameters: params}
	if meta.Label == "" {
		meta.Label = strings.TrimSuffix(filepath.Base(*out), filepath.Ext(*out))
	}

	var w *workload.Workload
	var err error
	switch *kind {
	case "agent":
		agent.Seed, agent.Duration, agent.OutputTokens = *seed, *duration, *outputTokens
		if *rate > 0 {
			agent.SessionsPerSecond = *rate
		}
		w, err = workload.Agent(agent)
	case "prefix":
		prefix.Seed, prefix.Duration, prefix.OutputTokens = *seed, *duration, *outputTokens
		if *rate > 0 {
			prefix.RequestsPerSecond = *rate
		}
		w, err = workload.PrefixRepetition(prefix)
	case "mooncake":
		var stats workload.MooncakeStats
		w, stats, err = workload.Mooncake(mooncake)
		meta.Mooncake = &stats
	default:
		return fmt.Errorf("run: unknown workload %q", *kind)
	}
	if err != nil {
		return err
	}
	meta.Requests = w.Requests()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	backendURLs := splitList(*backends)
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	if *reset {
		if err := resetCaches(ctx, client, backendURLs); err != nil {
			return err
		}
	}
	if len(backendURLs) > 0 {
		c, err := loadgen.ScrapeCacheCounters(ctx, client, backendURLs)
		if err != nil {
			return err
		}
		meta.Before = &c
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		return err
	}
	f, err := os.Create(*out) //nolint:gosec // G304: path is the operator's -out flag
	if err != nil {
		return err
	}
	var mu sync.Mutex
	enc := json.NewEncoder(f)
	var writeErr error
	record := func(r loadgen.Record) {
		mu.Lock()
		defer mu.Unlock()
		if err := enc.Encode(r); err != nil && writeErr == nil {
			writeErr = err
		}
	}

	fmt.Fprintf(os.Stderr, "loadgen: %s workload, %d sessions, %d requests -> %s\n", *kind, len(w.Sessions), meta.Requests, *out)
	meta.Started = time.Now()
	runErr := loadgen.Run(ctx, loadgen.Config{BaseURL: *url, Model: *model, MaxInFlight: *maxInFlight, RequestTimeout: *timeout}, w, record)
	meta.Finished = time.Now()
	if err := f.Close(); err != nil && writeErr == nil {
		writeErr = err
	}
	if writeErr != nil {
		return fmt.Errorf("write results: %w", writeErr)
	}

	if len(backendURLs) > 0 {
		c, err := loadgen.ScrapeCacheCounters(context.WithoutCancel(ctx), client, backendURLs)
		if err != nil {
			return err
		}
		meta.After = &c
		hr := loadgen.HitRate(*meta.Before, c)
		meta.HitRate = &hr
	}
	if err := writeJSON(*out+".meta.json", meta); err != nil {
		return err
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return runErr
	}
	return nil
}

func resetCaches(ctx context.Context, client *http.Client, urls []string) error {
	for _, u := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u+"/reset_prefix_cache", http.NoBody)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("reset cache on %s: %w", u, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("reset cache on %s: status %d", u, resp.StatusCode)
		}
	}
	return nil
}

func reportCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	slo := fs.Duration("slo", 2*time.Second, "time-to-first-token objective for goodput")
	group := fs.Bool("group", false, "combine trials of one configuration into a row with the median and range")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("report: give at least one results file")
	}
	runs := make([]loadgen.Result, 0, fs.NArg())
	for _, path := range fs.Args() {
		records, err := loadgen.ReadRecords(path)
		if err != nil {
			return err
		}
		run := loadgen.Result{
			Label:   strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
			Summary: loadgen.Summarize(records, *slo),
		}
		var meta Metadata
		if err := readJSON(path+".meta.json", &meta); err == nil {
			run.Label, run.HitRate = meta.Label, meta.HitRate
		}
		runs = append(runs, run)
	}
	_, err := io.WriteString(out, loadgen.Table(runs, *slo, *group))
	return err
}

func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, strings.TrimSuffix(p, "/"))
		}
	}
	return out
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path derives from an operator-supplied results file
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
