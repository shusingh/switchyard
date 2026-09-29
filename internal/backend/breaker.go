package backend

import (
	"sync"
	"time"
)

// BreakerConfig configures a backend's circuit breaker.
type BreakerConfig struct {
	// FailureThreshold is the number of consecutive failed requests that
	// opens the breaker. Zero disables the breaker.
	FailureThreshold int
	// Cooldown is how long an open breaker rejects traffic before letting a
	// single trial request through.
	Cooldown time.Duration
}

// breaker is a per-backend circuit breaker. Health checks notice a dead
// backend within a few intervals; the breaker reacts to request failures
// immediately, and backs off a backend that answers health checks but fails
// real requests.
//
//	closed    --threshold consecutive failures-->  open
//	open      --cooldown elapsed-->                half-open (one trial request)
//	half-open --trial succeeds-->                  closed
//	half-open --trial fails-->                     open
type breaker struct {
	cfg BreakerConfig
	now func() time.Time

	mu       sync.Mutex
	failures int       // consecutive failures while closed
	openedAt time.Time // zero while closed
	trialOut bool      // a half-open trial request is in flight
}

func (br *breaker) state(now time.Time) string {
	switch {
	case br.openedAt.IsZero():
		return "closed"
	case now.Sub(br.openedAt) < br.cfg.Cooldown:
		return "open"
	default:
		return "half-open"
	}
}

// allow reports whether a request may be sent. In half-open state it admits
// exactly one trial request at a time.
func (br *breaker) allow() bool {
	if br.cfg.FailureThreshold <= 0 {
		return true
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	switch br.state(br.now()) {
	case "closed":
		return true
	case "half-open":
		if br.trialOut {
			return false
		}
		br.trialOut = true
		return true
	default:
		return false
	}
}

// peek reports whether allow would return true, without claiming a trial
// slot. Routing uses it to filter candidates; allow is called once a backend
// is actually chosen.
func (br *breaker) peek() bool {
	if br.cfg.FailureThreshold <= 0 {
		return true
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	switch br.state(br.now()) {
	case "closed":
		return true
	case "half-open":
		return !br.trialOut
	default:
		return false
	}
}

// Outcome is the result of one request, reported to a backend's breaker.
type Outcome int

const (
	// OutcomeSuccess means the backend answered; client errors (4xx) count.
	OutcomeSuccess Outcome = iota
	// OutcomeFailure means the backend could not be reached or answered with
	// a server error.
	OutcomeFailure
	// OutcomeAbandoned means the request ended without telling anything about
	// the backend, for example because the client disconnected. It releases a
	// half-open trial slot without changing state.
	OutcomeAbandoned
)

func (br *breaker) record(o Outcome) {
	if br.cfg.FailureThreshold <= 0 {
		return
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	halfOpen := !br.openedAt.IsZero()
	br.trialOut = false
	switch {
	case o == OutcomeAbandoned:
	case o == OutcomeSuccess:
		br.failures = 0
		br.openedAt = time.Time{}
	case halfOpen:
		br.openedAt = br.now() // the trial failed: stay open for another cooldown
	default:
		br.failures++
		if br.failures >= br.cfg.FailureThreshold {
			br.openedAt = br.now()
			br.failures = 0
		}
	}
}
