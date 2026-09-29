package backend

import (
	"testing"
	"time"
)

const testCooldown = time.Minute

// newTestBreaker returns a breaker on a clock the test advances by writing
// through the returned pointer.
func newTestBreaker(threshold int) (*breaker, *time.Time) {
	now := time.Unix(0, 0)
	return &breaker{cfg: BreakerConfig{FailureThreshold: threshold, Cooldown: testCooldown}, now: func() time.Time { return now }}, &now
}

func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	t.Parallel()
	br, _ := newTestBreaker(3)
	for i := range 2 {
		br.allow()
		br.record(OutcomeFailure)
		if !br.peek() {
			t.Fatalf("breaker opened after %d failures; threshold is 3", i+1)
		}
	}
	br.allow()
	br.record(OutcomeSuccess) // a success resets the streak
	for range 2 {
		br.allow()
		br.record(OutcomeFailure)
	}
	if !br.peek() {
		t.Fatal("breaker opened although failures were not consecutive")
	}
	br.allow()
	br.record(OutcomeFailure)
	if br.peek() || br.allow() {
		t.Error("breaker did not open after 3 consecutive failures")
	}
}

func TestBreakerHalfOpenAllowsOneTrial(t *testing.T) {
	t.Parallel()
	br, now := newTestBreaker(1)
	br.allow()
	br.record(OutcomeFailure) // open
	*now = now.Add(testCooldown)

	if !br.allow() {
		t.Fatal("half-open breaker refused the trial request")
	}
	if br.allow() || br.peek() {
		t.Error("half-open breaker allowed a second request while the trial is out")
	}

	br.record(OutcomeFailure) // the trial failed: open for another cooldown
	if br.allow() {
		t.Error("breaker allowed traffic right after a failed trial")
	}
	*now = now.Add(testCooldown)
	br.allow()
	br.record(OutcomeSuccess) // the trial succeeded: closed
	for range 2 {
		if !br.allow() {
			t.Fatal("breaker did not close after a successful trial")
		}
	}
}

func TestBreakerAbandonedTrialFreesTheSlot(t *testing.T) {
	t.Parallel()
	br, now := newTestBreaker(1)
	br.allow()
	br.record(OutcomeFailure)
	*now = now.Add(testCooldown)
	br.allow()
	br.record(OutcomeAbandoned) // client left; says nothing about the backend
	if !br.allow() {
		t.Error("an abandoned trial left the breaker stuck with its trial slot taken")
	}
}

func TestBreakerDisabled(t *testing.T) {
	t.Parallel()
	br, _ := newTestBreaker(0)
	for range 100 {
		br.allow()
		br.record(OutcomeFailure)
	}
	if !br.allow() {
		t.Error("a disabled breaker refused traffic")
	}
}
