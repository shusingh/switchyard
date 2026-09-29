package loadgen

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Run is one benchmark run: its summary and the engines' measured cache hit
// rate, if it was recorded.
type Run struct {
	Label   string
	Summary Summary
	HitRate *float64
}

// trialSuffix matches the trial marker that scripts/bench-gpu.sh puts in run
// labels, so trials of one configuration can be grouped.
var trialSuffix = regexp.MustCompile(`\s*\(trial \d+\)$`)

// Group returns the configuration a run belongs to: its label without any
// trial marker.
func (r Run) Group() string { return trialSuffix.ReplaceAllString(r.Label, "") }

// column is one metric shown in a report table.
type column struct {
	title  string
	value  func(Run) (float64, bool)
	format func(float64) string
}

func columns(slo time.Duration) []column {
	always := func(f func(Summary) float64) func(Run) (float64, bool) {
		return func(r Run) (float64, bool) { return f(r.Summary), true }
	}
	return []column{
		{"Requests", always(func(s Summary) float64 { return float64(s.Requests) }), func(v float64) string { return fmt.Sprintf("%.0f", v) }},
		{"Failed", always(func(s Summary) float64 { return float64(s.Failed) }), func(v float64) string { return fmt.Sprintf("%.0f", v) }},
		{fmt.Sprintf("Goodput (TTFT <= %s)", slo), always(func(s Summary) float64 { return s.Goodput }), percent},
		{"TTFT p50", always(func(s Summary) float64 { return s.TTFT.P50 }), millis},
		{"TTFT p90", always(func(s Summary) float64 { return s.TTFT.P90 }), millis},
		{"TTFT p99", always(func(s Summary) float64 { return s.TTFT.P99 }), millis},
		{"TPOT p50", always(func(s Summary) float64 { return s.TPOT.P50 }), millis},
		{"Output tok/s", always(func(s Summary) float64 { return s.OutputTokensPerSec }), func(v float64) string { return fmt.Sprintf("%.0f", v) }},
		{"Cache hit rate", func(r Run) (float64, bool) {
			if r.HitRate == nil {
				return 0, false
			}
			return *r.HitRate, true
		}, percent},
		{"Router-believed hit rate", func(r Run) (float64, bool) {
			return r.Summary.BelievedHitRate, r.Summary.BelievedHitRate > 0
		}, percent},
		{"TTFT prediction error p50", func(r Run) (float64, bool) {
			return r.Summary.PredictionError.P50, r.Summary.PredictionError.P50 > 0
		}, millis},
	}
}

// Table renders runs as a Markdown table. With grouped set, runs of the same
// configuration (differing only in their trial marker) share one row showing
// the median across trials and, when there are several, the range.
func Table(runs []Run, slo time.Duration, grouped bool) string {
	cols := columns(slo)
	var b strings.Builder
	b.WriteString("| Run |")
	sep := "|---|"
	if grouped {
		b.WriteString(" Trials |")
		sep += "---:|"
	}
	for _, c := range cols {
		b.WriteString(" " + c.title + " |")
		sep += "---:|"
	}
	b.WriteString("\n" + sep + "\n")

	for _, g := range groups(runs, grouped) {
		b.WriteString("| " + g[0].Group())
		if !grouped {
			b.WriteString(g[0].Label[len(g[0].Group()):])
		}
		b.WriteString(" |")
		if grouped {
			fmt.Fprintf(&b, " %d |", len(g))
		}
		for _, c := range cols {
			b.WriteString(" " + cell(g, c) + " |")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// groups partitions runs by configuration, keeping first-seen order. Without
// grouping every run is its own group.
func groups(runs []Run, grouped bool) [][]Run {
	if !grouped {
		out := make([][]Run, len(runs))
		for i, r := range runs {
			out[i] = []Run{r}
		}
		return out
	}
	var order []string
	byGroup := map[string][]Run{}
	for _, r := range runs {
		g := r.Group()
		if _, ok := byGroup[g]; !ok {
			order = append(order, g)
		}
		byGroup[g] = append(byGroup[g], r)
	}
	out := make([][]Run, len(order))
	for i, g := range order {
		out[i] = byGroup[g]
	}
	return out
}

// cell formats one column over a group: the value itself for a single run,
// or the median with the min-to-max range for several.
func cell(g []Run, c column) string {
	var vals []float64
	for _, r := range g {
		if v, ok := c.value(r); ok {
			vals = append(vals, v)
		}
	}
	switch len(vals) {
	case 0:
		return "n/a"
	case 1:
		return c.format(vals[0])
	}
	slices.Sort(vals)
	return fmt.Sprintf("%s (%s to %s)", c.format(quantile(vals, 0.5)), c.format(vals[0]), c.format(vals[len(vals)-1]))
}

func percent(v float64) string { return fmt.Sprintf("%.1f%%", 100*v) }

func millis(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.2f s", v/1000)
	}
	return fmt.Sprintf("%.0f ms", v)
}
