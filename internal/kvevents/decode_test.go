package kvevents

import (
	"errors"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The payloads in testdata were encoded by vLLM 0.30.0's own event classes
// and msgspec encoder (testdata/gen_event_batches.py), exactly as its ZMQ
// publisher sends them.

func readPayload(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeBatchFromVLLM(t *testing.T) {
	t.Parallel()
	events, err := DecodeBatch(readPayload(t, "stored_removed_cleared.bin"))
	if err != nil {
		t.Fatal(err)
	}
	// Hashes are 0x00..0x1f and 0x20..0x3f; keys are their last 8 bytes.
	const h1, h2 = 0x18191a1b1c1d1e1f, 0x38393a3b3c3d3e3f
	want := []Event{
		{Kind: BlockStored, Hashes: []uint64{h1, h2}, Medium: "GPU"},
		{Kind: BlockRemoved, Hashes: []uint64{h1}, Medium: "GPU"},
		{Kind: AllBlocksCleared},
	}
	if diff := cmp.Diff(want, events); diff != "" {
		t.Errorf("events mismatch (-want +got):\n%s", diff)
	}
}

func TestDecodeBatchWithIntegerHashes(t *testing.T) {
	t.Parallel()
	events, err := DecodeBatch(readPayload(t, "int_hashes.bin"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{{Kind: BlockStored, Hashes: []uint64{7, 1<<63 + 5}}}
	if diff := cmp.Diff(want, events); diff != "" {
		t.Errorf("events mismatch (-want +got):\n%s", diff)
	}
}

func TestDecodeBatchRejectsMalformedPayloads(t *testing.T) {
	t.Parallel()
	for name, payload := range map[string][]byte{
		"not msgpack":      []byte("{}"),
		"too few fields":   {0x91, 0x01},             // [1]
		"events not array": {0x92, 0x01, 0x02},       // [1, 2]
		"event not map":    {0x92, 0x01, 0x91, 0x07}, // [1, [7]]
		"hashes not array": append([]byte{0x92, 0x01, 0x91, 0x82,
			0xa4, 't', 'y', 'p', 'e', 0xab, 'B', 'l', 'o', 'c', 'k', 'S', 't', 'o', 'r', 'e', 'd',
			0xac, 'b', 'l', 'o', 'c', 'k', '_', 'h', 'a', 's', 'h', 'e', 's'}, 0x07),
	} {
		if _, err := DecodeBatch(payload); err == nil {
			t.Errorf("%s: DecodeBatch error = nil, want an error", name)
		}
	}
}

// Not parallel: testing.AllocsPerRun must not run alongside other tests.
func TestDecodeBatchRejectsLengthsBeyondThePayload(t *testing.T) {
	// Declared lengths the payload cannot hold are rejected before anything
	// is allocated for them.
	for name, payload := range map[string][]byte{
		"huge events array": {0x92, 0x01, 0xdd, 0x7f, 0xff, 0xff, 0xff},
		"huge field name":   {0x92, 0x01, 0x91, 0x81, 0xdb, 0x7f, 0xff, 0xff, 0xff},
	} {
		if _, err := DecodeBatch(payload); !errors.Is(err, errLength) {
			t.Errorf("%s: error = %v, want %v", name, err, errLength)
		}
	}

	// Found by fuzzing: a map header claiming about 1.9 billion entries in
	// the timestamp slot of a 10-byte payload. Decoding into generic values
	// allocated enormously. The decoder skips that field without allocating,
	// so it fails at the end of the payload instead.
	huge := []byte{0x93, 0xdf, 0x71, 0xe3, 0x4a, 0xb2, 0xa4, 0x67, 0x32, 0x69}
	allocs := testing.AllocsPerRun(10, func() { _, _ = DecodeBatch(huge) })
	if _, err := DecodeBatch(huge); err == nil || allocs > 50 {
		t.Errorf("fuzzed payload: error = %v with %.0f allocations; want an error and no large allocation", err, allocs)
	}
}

func TestDecodeBatchSkipsUnknownEventTypes(t *testing.T) {
	t.Parallel()
	// [1.0, [{"type": "SomethingNew"}]]
	payload := []byte{0x92, 0x01, 0x91, 0x81, 0xa4, 't', 'y', 'p', 'e', 0xac,
		'S', 'o', 'm', 'e', 't', 'h', 'i', 'n', 'g', 'N', 'e', 'w'}
	events, err := DecodeBatch(payload)
	if err != nil || len(events) != 0 {
		t.Errorf("DecodeBatch = %v, %v; want no events and no error", events, err)
	}
}

func FuzzDecodeBatch(f *testing.F) {
	for _, name := range []string{"stored_removed_cleared.bin", "int_hashes.bin"} {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(_ *testing.T, payload []byte) {
		_, _ = DecodeBatch(payload) // must not panic
	})
}
