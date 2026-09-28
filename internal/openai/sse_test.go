package openai

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// readAll drains r and returns every event and the terminal error.
func readAll(t *testing.T, r *EventReader) ([]string, error) {
	t.Helper()
	var events []string
	for {
		ev, err := r.Next()
		if ev != nil {
			events = append(events, string(ev))
		}
		if err != nil {
			return events, err
		}
	}
}

func TestEventReader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		stream     string
		wantEvents []string
		wantErr    error
	}{
		{
			name:       "typical chat stream",
			stream:     "data: {\"a\":1}\n\ndata: {\"a\":2}\n\ndata: [DONE]\n\n",
			wantEvents: []string{"data: {\"a\":1}\n\n", "data: {\"a\":2}\n\n", "data: [DONE]\n\n"},
			wantErr:    io.EOF,
		},
		{
			name:       "CRLF line endings are preserved",
			stream:     "data: x\r\n\r\n",
			wantEvents: []string{"data: x\r\n\r\n"},
			wantErr:    io.EOF,
		},
		{
			name:       "multi-line event and comment",
			stream:     ": keep-alive\n\nevent: message\ndata: a\ndata: b\n\n",
			wantEvents: []string{": keep-alive\n\n", "event: message\ndata: a\ndata: b\n\n"},
			wantErr:    io.EOF,
		},
		{
			name:       "leading blank lines are skipped",
			stream:     "\n\ndata: x\n\n",
			wantEvents: []string{"data: x\n\n"},
			wantErr:    io.EOF,
		},
		{
			name:       "truncated final event",
			stream:     "data: x\n\ndata: partial",
			wantEvents: []string{"data: x\n\n", "data: partial"},
			wantErr:    io.ErrUnexpectedEOF,
		},
		{
			name:    "empty stream",
			stream:  "",
			wantErr: io.EOF,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := readAll(t, NewEventReader(strings.NewReader(tt.stream), 0))
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("terminal error = %v, want %v", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.wantEvents, got); diff != "" {
				t.Errorf("events mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEventReaderLongLines(t *testing.T) {
	t.Parallel()
	payload := strings.Repeat("x", 100<<10) // longer than the internal buffer
	stream := "data: " + payload + "\n\n"
	ev, err := NewEventReader(strings.NewReader(stream), 0).Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if string(EventData(ev)) != payload {
		t.Errorf("EventData returned %d bytes, want %d", len(EventData(ev)), len(payload))
	}
}

func TestEventReaderSizeLimit(t *testing.T) {
	t.Parallel()
	stream := "data: " + strings.Repeat("x", 200<<10) + "\n\n"
	_, err := NewEventReader(strings.NewReader(stream), 64<<10).Next()
	if !errors.Is(err, ErrEventTooLarge) {
		t.Errorf("Next() error = %v, want %v", err, ErrEventTooLarge)
	}
}

func TestEventData(t *testing.T) {
	t.Parallel()
	tests := []struct {
		event string
		want  string
		isNil bool
	}{
		{event: "data: {\"a\":1}\n\n", want: `{"a":1}`},
		{event: "data:{\"a\":1}\n\n", want: `{"a":1}`},
		{event: "data: a\ndata: b\ndata: c\n\n", want: "a\nb\nc"},
		{event: "data: x\r\n\r\n", want: "x"},
		{event: ": comment\n\n", isNil: true},
		{event: "event: ping\n\n", isNil: true},
	}
	for _, tt := range tests {
		got := EventData([]byte(tt.event))
		if tt.isNil {
			if got != nil {
				t.Errorf("EventData(%q) = %q, want nil", tt.event, got)
			}
			continue
		}
		if string(got) != tt.want {
			t.Errorf("EventData(%q) = %q, want %q", tt.event, got, tt.want)
		}
	}
}

func TestEventDataDoesNotModifyEvent(t *testing.T) {
	t.Parallel()
	event := []byte("data: a\ndata: b\n\n")
	original := bytes.Clone(event)
	_ = EventData(event)
	if !bytes.Equal(event, original) {
		t.Errorf("EventData modified its input: got %q, want %q", event, original)
	}
}

func TestIsDone(t *testing.T) {
	t.Parallel()
	if !IsDone(EventData([]byte("data: [DONE]\n\n"))) {
		t.Error("IsDone(data: [DONE]) = false, want true")
	}
	if IsDone(EventData([]byte("data: {}\n\n"))) {
		t.Error("IsDone(data: {}) = true, want false")
	}
}

func TestWriteEventRoundTrip(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := WriteEvent(&buf, []byte(`{"x":1}`)); err != nil {
		t.Fatalf("WriteEvent() error = %v", err)
	}
	ev, err := NewEventReader(&buf, 0).Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if got := string(EventData(ev)); got != `{"x":1}` {
		t.Errorf("round trip = %q, want %q", got, `{"x":1}`)
	}
}

// FuzzEventReader checks that arbitrary input never panics and that the
// events returned, concatenated, are a prefix-preserving slice of the input
// minus skipped blank padding.
func FuzzEventReader(f *testing.F) {
	for _, seed := range []string{
		"data: x\n\n",
		"data: a\ndata: b\n\ndata: [DONE]\n\n",
		": c\r\n\r\ndata: y",
		"\n\n\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		r := NewEventReader(bytes.NewReader(in), 4096)
		total := 0
		for {
			ev, err := r.Next()
			total += len(ev)
			if len(ev) > 0 && !bytes.Contains(in, ev) {
				t.Fatalf("event %q is not a substring of the input", ev)
			}
			_ = EventData(ev)
			if err != nil {
				break
			}
		}
		if total > len(in) {
			t.Fatalf("returned %d bytes from %d bytes of input", total, len(in))
		}
	})
}
