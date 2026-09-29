package kvevents

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-zeromq/zmq4"
)

// Subscriber receives one replica's KV cache events and passes them to its
// callbacks. It reconnects after failures. Events published while it was
// disconnected are lost, so after every connection, and whenever the
// publisher's sequence numbers skip, it calls OnReset: the consumer must then
// forget what it believed the replica held and rebuild from new events.
type Subscriber struct {
	// Endpoint is the publisher's address, for example "tcp://localhost:5601".
	Endpoint string
	// Topic filters messages; it must match the publisher's topic.
	Topic string
	// OnEvents receives each decoded batch, in publication order.
	OnEvents func([]Event)
	// OnReset is called when the replica's state can no longer be trusted.
	OnReset func()
	// RetryDelay is the pause between reconnection attempts.
	RetryDelay time.Duration
	Logger     *slog.Logger
}

// Run receives events until ctx is cancelled.
func (s *Subscriber) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := s.session(ctx)
		if ctx.Err() != nil {
			return
		}
		s.Logger.Warn("kv event subscription interrupted; reconnecting",
			slog.String("endpoint", s.Endpoint), slog.String("error", err.Error()))
		t := time.NewTimer(s.RetryDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// session runs one connection until it fails or ctx is cancelled.
func (s *Subscriber) session(ctx context.Context) error {
	sub := zmq4.NewSub(ctx)
	defer func() { _ = sub.Close() }() // nothing to do if closing a finished session fails
	if err := sub.Dial(s.Endpoint); err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	if err := sub.SetOption(zmq4.OptionSubscribe, s.Topic); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	s.OnReset() // anything published before this connection is unknown

	var last uint64
	first := true
	for {
		msg, err := sub.Recv()
		if err != nil {
			return fmt.Errorf("receive: %w", err)
		}
		seq, payload, err := parseFrames(msg.Frames)
		if err != nil {
			s.Logger.Warn("skipping malformed kv event message",
				slog.String("endpoint", s.Endpoint), slog.String("error", err.Error()))
			continue
		}
		if !first && seq != last+1 {
			// Lost messages, or a publisher restart that reset the sequence.
			s.Logger.Warn("kv event sequence gap; resetting replica state",
				slog.String("endpoint", s.Endpoint), slog.Uint64("expected", last+1), slog.Uint64("got", seq))
			s.OnReset()
		}
		first, last = false, seq
		events, err := DecodeBatch(payload)
		if err != nil {
			// A batch that cannot be decoded is as good as lost.
			s.Logger.Warn("undecodable kv event batch; resetting replica state",
				slog.String("endpoint", s.Endpoint), slog.String("error", err.Error()))
			s.OnReset()
			continue
		}
		s.OnEvents(events)
	}
}

// parseFrames splits a publisher message into its sequence number and
// payload. vLLM sends three frames: topic, an 8-byte big-endian sequence
// number, and the msgpack payload.
func parseFrames(frames [][]byte) (uint64, []byte, error) {
	if len(frames) != 3 {
		return 0, nil, fmt.Errorf("message has %d frames, want 3", len(frames))
	}
	if len(frames[1]) != 8 {
		return 0, nil, errors.New("sequence frame is not 8 bytes")
	}
	return binary.BigEndian.Uint64(frames[1]), frames[2], nil
}
