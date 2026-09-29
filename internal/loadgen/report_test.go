package loadgen

import (
	"strings"
	"testing"
	"time"
)

func runWith(label string, ttftP50, hit float64) Run {
	return Run{Label: label, Summary: Summary{Requests: 10, TTFT: Percentiles{P50: ttftP50}}, HitRate: &hit}
}

func TestTableUngrouped(t *testing.T) {
	t.Parallel()
	table := Table([]Run{runWith("a (trial 1)", 100, 0.5), runWith("b", 2500, 0.25)}, time.Second, false)
	for _, want := range []string{"| a (trial 1) |", "| b |", "100 ms", "2.50 s", "50.0%", "25.0%"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
	if strings.Contains(table, "Trials") {
		t.Error("ungrouped table has a Trials column")
	}
}

func TestTableGroupsTrialsWithMedianAndRange(t *testing.T) {
	t.Parallel()
	runs := []Run{
		runWith("rr (trial 1)", 300, 0.40),
		runWith("ttft (trial 1)", 100, 0.90),
		runWith("rr (trial 2)", 320, 0.44),
		runWith("rr (trial 3)", 310, 0.42),
	}
	table := Table(runs, time.Second, true)
	lines := strings.Split(strings.TrimSpace(table), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want header, separator, and two groups:\n%s", len(lines), table)
	}
	rr := lines[2]
	for _, want := range []string{"| rr |", "| 3 |", "310 ms (300 ms to 320 ms)", "42.0% (40.0% to 44.0%)"} {
		if !strings.Contains(rr, want) {
			t.Errorf("round-robin row lacks %q: %s", want, rr)
		}
	}
	if !strings.Contains(lines[3], "| ttft | 1 |") || !strings.Contains(lines[3], "| 100 ms |") {
		t.Errorf("single-trial group should show its plain value: %s", lines[3])
	}
}

func TestRunGroup(t *testing.T) {
	t.Parallel()
	for label, want := range map[string]string{
		"estimated_ttft (trial 12)":                "estimated_ttft",
		"p2c (4 replicas, simulated)":              "p2c (4 replicas, simulated)",
		"random (4 replicas, simulated) (trial 2)": "random (4 replicas, simulated)",
	} {
		if got := (Run{Label: label}).Group(); got != want {
			t.Errorf("Group(%q) = %q, want %q", label, got, want)
		}
	}
}
