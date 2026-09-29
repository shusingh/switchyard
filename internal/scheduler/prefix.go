package scheduler

import (
	"math/rand/v2"

	"github.com/shusingh/switchyard/internal/backend"
)

// prefixAffinity sends each request to the backend believed to hold the
// longest prefix of it, breaking ties by fewer in-flight requests and then at
// random. It ignores load otherwise, so it maximizes cache reuse, at the cost
// of pure affinity's failure mode: a popular prefix piles onto one backend.
// It is the cache-only baseline for the load-aware estimated_ttft policy.
type prefixAffinity struct{}

func (prefixAffinity) Name() string { return PolicyPrefixAffinity }

func (prefixAffinity) Pick(req *Request, candidates []*backend.Backend) (*backend.Backend, error) {
	if len(candidates) == 0 {
		return nil, ErrNoCandidates
	}
	var best *backend.Backend
	bestMatch, bestLoad := -1, int64(0)
	ties := 0
	for _, b := range candidates {
		m, load := req.MatchedOn(b), b.InFlight()
		switch {
		case m > bestMatch || (m == bestMatch && load < bestLoad):
			best, bestMatch, bestLoad, ties = b, m, load, 1
		case m == bestMatch && load == bestLoad:
			ties++
			if rand.IntN(ties) == 0 { //nolint:gosec // G404: load balancing, not security
				best = b
			}
		}
	}
	return best, nil
}
