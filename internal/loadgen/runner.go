// Package loadgen replays a workload against an OpenAI-compatible endpoint
// and records what every request experienced.
//
// Sessions start on the workload's schedule regardless of how earlier
// requests fare (open loop), so latency includes queueing delay the way a
// real client would see it. Within a session, each turn waits for the
// previous one plus its think time, as an agent waits for a response before
// acting on it. See docs/adr/0006-open-loop-load-generation.md.
package loadgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shusingh/switchyard/internal/openai"
	"github.com/shusingh/switchyard/internal/workload"
)

// ErrClientOverloaded marks a request the generator did not send because it
// already had MaxInFlight requests outstanding. Delaying the request instead
// would turn the open loop into a closed one and hide the overload.
var ErrClientOverloaded = errors.New("load generator at max in-flight requests")

// headerBackend names the backend that served a request, when the router is
// configured to say.
const headerBackend = "X-Switchyard-Backend"

// Config configures a run.
type Config struct {
	// BaseURL is the endpoint under test, for example
	// "http://localhost:8080".
	BaseURL string
	// Model is sent as the request's model field.
	Model string
	// MaxInFlight bounds outstanding requests; see ErrClientOverloaded.
	MaxInFlight int
	// RequestTimeout bounds one request from send to last byte.
	RequestTimeout time.Duration
}

// Record is the outcome of one request. Times are milliseconds since the
// start of the run, and durations are milliseconds.
type Record struct {
	Session string `json:"session"`
	Turn    int    `json:"turn"`
	// ScheduledMS is when the request should have been sent: the session's
	// start time, or the previous turn's completion plus think time.
	ScheduledMS float64 `json:"scheduled_ms"`
	SentMS      float64 `json:"sent_ms"`
	// TTFTMS is time to the first generated token, measured from SentMS.
	TTFTMS float64 `json:"ttft_ms"`
	// TPOTMS is the mean time per output token after the first.
	TPOTMS float64 `json:"tpot_ms"`
	// E2EMS is the time from SentMS to the end of the response.
	E2EMS            float64 `json:"e2e_ms"`
	PromptWords      int     `json:"prompt_words"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Status           int     `json:"status"`
	Backend          string  `json:"backend,omitempty"`
	Error            string  `json:"error,omitempty"`
}

// OK reports whether the request completed successfully.
func (r *Record) OK() bool { return r.Status == http.StatusOK && r.Error == "" }

// Run replays w against cfg.BaseURL and calls record once per request, from
// multiple goroutines; record must be safe for concurrent use. Run returns
// when every session has finished or ctx is cancelled.
func Run(ctx context.Context, cfg Config, w *workload.Workload, record func(Record)) error {
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = 4096
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10 * time.Minute
	}
	transport := &http.Transport{
		MaxIdleConnsPerHost: cfg.MaxInFlight,
		DisableCompression:  true,
	}
	defer transport.CloseIdleConnections()
	r := &runner{
		cfg:    cfg,
		client: &http.Client{Transport: transport},
		record: record,
		start:  time.Now(),
	}

	var wg sync.WaitGroup
	for i := range w.Sessions {
		s := &w.Sessions[i]
		if !sleepUntil(ctx, r.start.Add(s.Start)) {
			break
		}
		wg.Go(func() { r.runSession(ctx, s) })
	}
	wg.Wait()
	return ctx.Err()
}

type runner struct {
	cfg      Config
	client   *http.Client
	record   func(Record)
	start    time.Time
	inFlight atomic.Int64
}

func (r *runner) sinceStart(t time.Time) float64 { return ms(t.Sub(r.start)) }

func (r *runner) runSession(ctx context.Context, s *workload.Session) {
	scheduled := r.start.Add(s.Start)
	for i := range s.Turns {
		turn := &s.Turns[i]
		if i > 0 {
			scheduled = time.Now().Add(turn.ThinkTime)
			if !sleepUntil(ctx, scheduled) {
				return
			}
		}
		rec := r.send(ctx, turn)
		rec.Session, rec.Turn, rec.ScheduledMS = s.ID, i, r.sinceStart(scheduled)
		r.record(rec)
		if !rec.OK() {
			// An agent cannot continue a conversation whose last turn failed.
			return
		}
	}
}

// send issues one streaming request and measures it.
func (r *runner) send(ctx context.Context, turn *workload.Turn) Record {
	rec := Record{PromptWords: turn.PromptWords()}
	if r.inFlight.Add(1) > int64(r.cfg.MaxInFlight) {
		r.inFlight.Add(-1)
		rec.SentMS = r.sinceStart(time.Now())
		rec.Error = ErrClientOverloaded.Error()
		return rec
	}
	defer r.inFlight.Add(-1)

	body, err := turn.Body(r.cfg.Model)
	if err != nil {
		rec.Error = fmt.Sprintf("render body: %v", err)
		return rec
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.BaseURL+turn.Path(), bytes.NewReader(body))
	if err != nil {
		rec.Error = fmt.Sprintf("build request: %v", err)
		return rec
	}
	req.Header.Set("Content-Type", "application/json")

	sent := time.Now()
	rec.SentMS = r.sinceStart(sent)
	resp, err := r.client.Do(req)
	if err != nil {
		rec.Error = err.Error()
		rec.E2EMS = ms(time.Since(sent))
		return rec
	}
	defer resp.Body.Close()
	rec.Status = resp.StatusCode
	rec.Backend = resp.Header.Get(headerBackend)
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		rec.Error = fmt.Sprintf("status %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
		rec.E2EMS = ms(time.Since(sent))
		return rec
	}
	readStream(resp.Body, sent, &rec)
	return rec
}

// readStream consumes a server-sent event stream and fills in the timing and
// token fields of rec.
func readStream(body io.Reader, sent time.Time, rec *Record) {
	events := openai.NewEventReader(body, 0)
	var first time.Time
	for {
		ev, err := events.Next()
		if len(ev) > 0 {
			data := openai.EventData(ev)
			if first.IsZero() && openai.ChunkHasContent(data) {
				first = time.Now()
			}
			if u, ok := openai.ChunkUsage(data); ok {
				rec.PromptTokens, rec.CompletionTokens = u.PromptTokens, u.CompletionTokens
			}
		}
		if err == nil {
			continue
		}
		end := time.Now()
		rec.E2EMS = ms(end.Sub(sent))
		if !errors.Is(err, io.EOF) {
			rec.Error = fmt.Sprintf("read stream: %v", err)
		}
		break
	}
	if first.IsZero() {
		if rec.Error == "" {
			rec.Error = "stream ended without a token"
		}
		return
	}
	rec.TTFTMS = ms(first.Sub(sent))
	if rec.CompletionTokens > 1 {
		rec.TPOTMS = (rec.E2EMS - rec.TTFTMS) / float64(rec.CompletionTokens-1)
	}
}

// sleepUntil waits until t or ctx ends, and reports whether t was reached.
func sleepUntil(ctx context.Context, t time.Time) bool {
	d := time.Until(t)
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
