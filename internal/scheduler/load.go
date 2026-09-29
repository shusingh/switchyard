package scheduler

import (
	"math/rand/v2"

	"github.com/shusingh/switchyard/internal/backend"
)

// leastLoaded picks the backend with the fewest in-flight requests, breaking
// ties uniformly at random so equally idle backends share traffic. It is the
// strongest cache-blind baseline.
type leastLoaded struct{}

func (leastLoaded) Name() string { return PolicyLeastLoaded }

func (leastLoaded) Pick(_ *Request, candidates []*backend.Backend) (*backend.Backend, error) {
	if len(candidates) == 0 {
		return nil, ErrNoCandidates
	}
	var best *backend.Backend
	var bestLoad int64
	ties := 0
	for _, b := range candidates {
		load := b.InFlight()
		switch {
		case best == nil || load < bestLoad:
			best, bestLoad, ties = b, load, 1
		case load == bestLoad:
			// Reservoir sampling keeps each tied backend with equal
			// probability without a second pass.
			ties++
			if rand.IntN(ties) == 0 { //nolint:gosec // G404: load balancing, not security
				best = b
			}
		}
	}
	return best, nil
}

// p2c samples two distinct backends at random and picks the less loaded one
// ("power of two choices", Mitzenmacher 2001). It avoids the herding that
// strict least-loaded selection can cause when load information is stale.
type p2c struct{}

func (p2c) Name() string { return PolicyP2C }

func (p2c) Pick(_ *Request, candidates []*backend.Backend) (*backend.Backend, error) {
	switch len(candidates) {
	case 0:
		return nil, ErrNoCandidates
	case 1:
		return candidates[0], nil
	}
	i := rand.IntN(len(candidates))     //nolint:gosec // G404: load balancing, not security
	j := rand.IntN(len(candidates) - 1) //nolint:gosec // G404: load balancing, not security
	if j >= i {
		j++ // skip i so the two samples differ
	}
	a, b := candidates[i], candidates[j]
	if b.InFlight() < a.InFlight() {
		return b, nil
	}
	return a, nil
}
