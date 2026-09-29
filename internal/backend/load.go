package backend

import "sync/atomic"

// Load is a snapshot of the work the router has sent to a backend. The
// counts cover only traffic that flows through this router.
type Load struct {
	// InFlight is the number of requests being served.
	InFlight int64
	// PendingPrefillTokens is the estimated number of uncached prompt tokens
	// of requests that have not yet produced their first token: prefill work
	// queued or running on the backend.
	PendingPrefillTokens int64
	// Decoding is the number of requests generating output.
	Decoding int64
}

// Load returns the backend's current load.
func (b *Backend) Load() Load {
	return Load{
		InFlight:             b.inFlight.Load(),
		PendingPrefillTokens: b.pendingPrefill.Load(),
		Decoding:             b.decoding.Load(),
	}
}

// InFlight returns the number of requests being served.
func (b *Backend) InFlight() int64 { return b.inFlight.Load() }

// Ticket tracks one request's contribution to a backend's load through its
// lifecycle: admitted with its prefill work pending, then decoding after its
// first token, then done. Its methods are idempotent and safe to call in any
// order, so every exit path can simply call Done.
type Ticket struct {
	b             *Backend
	prefillTokens int64
	// QueueTokensAtAdmit is the backend's pending prefill work just before
	// this request was admitted. With the request's own prefill tokens it is
	// the work that stood between the request and its first token.
	QueueTokensAtAdmit int64

	state atomic.Int32
}

const (
	ticketPrefilling int32 = iota
	ticketDecoding
	ticketDone
)

// Admit records a request with the given estimated uncached prompt tokens as
// in flight on b. The caller must call Done on the returned ticket.
func (b *Backend) Admit(prefillTokens int64) *Ticket {
	queue := b.pendingPrefill.Add(prefillTokens) - prefillTokens
	b.inFlight.Add(1)
	return &Ticket{b: b, prefillTokens: prefillTokens, QueueTokensAtAdmit: queue}
}

// PrefillTokens returns the uncached prompt tokens the ticket was admitted
// with.
func (t *Ticket) PrefillTokens() int64 { return t.prefillTokens }

// FirstToken moves the request from prefilling to decoding. Calls after the
// first, or after Done, have no effect.
func (t *Ticket) FirstToken() {
	if t.state.CompareAndSwap(ticketPrefilling, ticketDecoding) {
		t.b.pendingPrefill.Add(-t.prefillTokens)
		t.b.decoding.Add(1)
	}
}

// Done removes the request from the backend's load. Calls after the first
// have no effect.
func (t *Ticket) Done() {
	switch prev := t.state.Swap(ticketDone); prev {
	case ticketPrefilling:
		t.b.pendingPrefill.Add(-t.prefillTokens)
	case ticketDecoding:
		t.b.decoding.Add(-1)
	default:
		return // already done
	}
	t.b.inFlight.Add(-1)
}
