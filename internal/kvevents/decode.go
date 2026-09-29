// Package kvevents receives the KV cache events vLLM publishes over ZeroMQ
// when started with --kv-events-config: which blocks each replica stored and
// removed. The router's precise mode uses them to know, rather than guess,
// what each replica holds. See docs/engineering/design.md section 7.4.
package kvevents

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/vmihailenco/msgpack/v5/msgpcode"
)

// Kind identifies a KV cache event.
type Kind uint8

// Event kinds, named after vLLM's event classes.
const (
	BlockStored Kind = iota + 1
	BlockRemoved
	AllBlocksCleared
)

// Event is one KV cache change on a replica.
type Event struct {
	Kind Kind
	// Hashes are the affected blocks' keys: the low 64 bits of vLLM's block
	// hashes, matching prefix.VLLMBlockHash.Low64. Empty for
	// AllBlocksCleared.
	Hashes []uint64
	// Medium is where the blocks live, such as "GPU"; empty if unspecified.
	Medium string
}

// errLength reports a container or string whose declared length cannot fit in
// the payload. Checking every length before allocating means a corrupt or
// hostile message cannot make the decoder allocate more than its own size.
var errLength = errors.New("declared length exceeds payload")

// DecodeBatch decodes one event batch payload. vLLM encodes a batch as a
// msgpack array [timestamp, events, data_parallel_rank?], and each event as a
// map tagged with its class name under "type" (msgspec's tagged structs).
// Events of unknown types, and fields the router does not use, are skipped,
// so newer vLLM versions that add them do not break the subscriber.
func DecodeBatch(payload []byte) ([]Event, error) {
	d := &decoder{Decoder: msgpack.NewDecoder(bytes.NewReader(payload)), limit: len(payload)}
	fields, err := d.length(d.DecodeArrayLen())
	if err != nil {
		return nil, fmt.Errorf("decode event batch: %w", err)
	}
	if fields < 2 {
		return nil, fmt.Errorf("event batch has %d fields, want at least 2", fields)
	}
	if err := d.Skip(); err != nil { // timestamp
		return nil, fmt.Errorf("decode event batch timestamp: %w", err)
	}
	n, err := d.length(d.DecodeArrayLen())
	if err != nil {
		return nil, fmt.Errorf("decode event batch events: %w", err)
	}
	events := make([]Event, 0, n)
	for i := range n {
		ev, known, err := d.event()
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}
		if known {
			events = append(events, ev)
		}
	}
	return events, nil
}

// decoder wraps a msgpack decoder with length checks against the payload.
type decoder struct {
	*msgpack.Decoder
	limit int
}

// length validates a declared element count. Every element takes at least
// one byte, so a count above the payload size is corrupt.
func (d *decoder) length(n int, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	if n < 0 || n > d.limit {
		return 0, fmt.Errorf("%w: %d elements", errLength, n)
	}
	return n, nil
}

// raw reads a string or binary value into a buffer sized after checking its
// declared length. It returns nil for a msgpack nil.
func (d *decoder) raw() ([]byte, error) {
	n, err := d.DecodeBytesLen()
	if err != nil {
		return nil, err
	}
	if n == -1 {
		return nil, nil
	}
	if n > d.limit {
		return nil, fmt.Errorf("%w: %d bytes", errLength, n)
	}
	b := make([]byte, n)
	return b, d.ReadFull(b)
}

func (d *decoder) event() (Event, bool, error) {
	n, err := d.length(d.DecodeMapLen())
	if err != nil {
		return Event{}, false, err
	}
	var ev Event
	var hashes []uint64
	sawHashes := false
	for range n {
		key, err := d.raw()
		if err != nil {
			return Event{}, false, fmt.Errorf("field name: %w", err)
		}
		switch string(key) {
		case "type":
			v, err := d.raw()
			if err != nil {
				return Event{}, false, fmt.Errorf("type: %w", err)
			}
			switch string(v) {
			case "BlockStored":
				ev.Kind = BlockStored
			case "BlockRemoved":
				ev.Kind = BlockRemoved
			case "AllBlocksCleared":
				ev.Kind = AllBlocksCleared
			}
		case "medium":
			v, err := d.raw()
			if err != nil {
				return Event{}, false, fmt.Errorf("medium: %w", err)
			}
			ev.Medium = string(v)
		case "block_hashes":
			if hashes, err = d.hashes(); err != nil {
				return Event{}, false, fmt.Errorf("block_hashes: %w", err)
			}
			sawHashes = true
		default:
			if err := d.Skip(); err != nil {
				return Event{}, false, fmt.Errorf("field %q: %w", key, err)
			}
		}
	}
	switch ev.Kind {
	case BlockStored, BlockRemoved:
		if !sawHashes {
			return Event{}, false, errors.New("block_hashes missing")
		}
		ev.Hashes = hashes
		return ev, true, nil
	case AllBlocksCleared:
		return Event{Kind: AllBlocksCleared}, true, nil
	default:
		return Event{}, false, nil
	}
}

// hashes decodes a list of block hashes into their low 64 bits. By default
// vLLM sends each hash as 32 bytes; with VLLM_KV_EVENTS_USE_INT_BLOCK_HASHES
// it sends the low 64 bits as an integer.
func (d *decoder) hashes() ([]uint64, error) {
	n, err := d.length(d.DecodeArrayLen())
	if err != nil {
		return nil, err
	}
	out := make([]uint64, n)
	for i := range out {
		c, err := d.PeekCode()
		if err != nil {
			return nil, err
		}
		if isBytesOrString(c) {
			b, err := d.raw()
			if err != nil {
				return nil, err
			}
			if len(b) < 8 {
				return nil, fmt.Errorf("hash %d has %d bytes, want at least 8", i, len(b))
			}
			out[i] = binary.BigEndian.Uint64(b[len(b)-8:])
			continue
		}
		if out[i], err = d.DecodeUint64(); err != nil {
			return nil, fmt.Errorf("hash %d: %w", i, err)
		}
	}
	return out, nil
}

func isBytesOrString(c byte) bool {
	return msgpcode.IsFixedString(c) || msgpcode.IsString(c) || msgpcode.IsBin(c)
}
