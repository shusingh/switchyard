package server

import "sync"

// retryBudget limits retries to a fraction of traffic. Every request deposits
// ratio tokens, up to a cap; every retry withdraws one. When a backend fails
// hard, retries stop once the budget is spent instead of multiplying the load
// on the backends that remain.
type retryBudget struct {
	ratio float64
	cap   float64

	mu     sync.Mutex
	tokens float64 // guarded by mu
}

func newRetryBudget(ratio float64) *retryBudget {
	// The cap lets a short burst of failures retry right after a quiet
	// period, without letting an idle router bank unlimited retries.
	return &retryBudget{ratio: ratio, cap: 10, tokens: 10}
}

// deposit credits one request's share of the budget.
func (b *retryBudget) deposit() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens = min(b.cap, b.tokens+b.ratio)
}

// available reports whether a retry could currently be paid for.
func (b *retryBudget) available() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokens >= 1
}

// withdraw spends one retry and reports whether the budget allowed it.
func (b *retryBudget) withdraw() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
