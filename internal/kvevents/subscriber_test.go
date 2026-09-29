package kvevents

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-zeromq/zmq4"
)

// recorder collects what a Subscriber reports.
type recorder struct {
	mu      sync.Mutex
	batches [][]Event
	resets  int
}

func (r *recorder) onEvents(e []Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, e)
}

func (r *recorder) onReset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resets++
}

func (r *recorder) counts() (batches, resets int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.batches), r.resets
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func message(topic string, seq uint64, payload []byte) zmq4.Msg {
	var s [8]byte
	binary.BigEndian.PutUint64(s[:], seq)
	return zmq4.NewMsgFrom([]byte(topic), s[:], payload)
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSubscriberReceivesBatchesAndResetsOnGaps(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpoint := "tcp://127.0.0.1:" + strconv.Itoa(freePort(t))
	pub := zmq4.NewPub(ctx)
	defer func() { _ = pub.Close() }()
	if err := pub.Listen(endpoint); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	sub := &Subscriber{
		Endpoint: endpoint, Topic: "kv-events", RetryDelay: 10 * time.Millisecond,
		OnEvents: rec.onEvents, OnReset: rec.onReset, Logger: slog.New(slog.DiscardHandler),
	}
	done := make(chan struct{})
	go func() {
		sub.Run(ctx)
		close(done)
	}()

	payload := readPayload(t, "stored_removed_cleared.bin")
	// PUB/SUB drops messages sent before the subscription reaches the
	// publisher, so publish until the first one arrives.
	waitUntil(t, "the first batch", func() bool {
		_ = pub.Send(message("kv-events", 0, payload))
		n, _ := rec.counts()
		return n > 0
	})
	_, resetsBefore := rec.counts()
	if resetsBefore < 1 {
		t.Errorf("no reset on connect; state from before the connection must be discarded")
	}

	// Find the sequence number reached, then skip one.
	rec.mu.Lock()
	next := uint64(len(rec.batches))
	rec.mu.Unlock()
	if err := pub.Send(message("kv-events", next+5, payload)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the reset after a sequence gap", func() bool {
		_, resets := rec.counts()
		return resets > resetsBefore
	})

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestParseFrames(t *testing.T) {
	t.Parallel()
	if _, _, err := parseFrames([][]byte{[]byte("t"), {1}}); err == nil {
		t.Error("two frames accepted")
	}
	if _, _, err := parseFrames([][]byte{[]byte("t"), {1, 2}, nil}); err == nil {
		t.Error("short sequence frame accepted")
	}
	seq, payload, err := parseFrames([][]byte{[]byte("t"), {0, 0, 0, 0, 0, 0, 1, 2}, []byte("p")})
	if err != nil || seq != 258 || string(payload) != "p" {
		t.Errorf("parseFrames = %d, %q, %v; want 258, \"p\", nil", seq, payload, err)
	}
}
