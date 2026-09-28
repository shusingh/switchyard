package openai

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// DefaultMaxEventBytes bounds the size of a single server-sent event. A
// streamed chat chunk is typically a few hundred bytes; the final usage event
// is larger but still far below this.
const DefaultMaxEventBytes = 1 << 20

// ErrEventTooLarge is returned when an event exceeds the reader's size limit.
var ErrEventTooLarge = errors.New("server-sent event exceeds size limit")

var (
	dataPrefix = []byte("data:")
	doneData   = []byte("[DONE]")
)

// EventReader splits a server-sent event stream into events.
//
// Events are returned byte-for-byte as they appeared on the wire, including
// the terminating blank line, so a proxy can relay them without re-encoding.
type EventReader struct {
	r        *bufio.Reader
	buf      []byte
	maxBytes int
}

// NewEventReader returns an EventReader that reads from r and rejects events
// larger than maxBytes. A maxBytes of zero or less selects
// DefaultMaxEventBytes.
func NewEventReader(r io.Reader, maxBytes int) *EventReader {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxEventBytes
	}
	return &EventReader{r: bufio.NewReaderSize(r, 32<<10), maxBytes: maxBytes}
}

// Next returns the next complete event. The returned slice is only valid until
// the following call to Next.
//
// At the end of a well-formed stream Next returns io.EOF. If the stream ends
// in the middle of an event, Next returns the partial event together with
// io.ErrUnexpectedEOF, so the caller can decide whether to relay it.
func (er *EventReader) Next() ([]byte, error) {
	er.buf = er.buf[:0]
	for {
		line, err := er.readLine()
		if len(line) > 0 {
			er.buf = append(er.buf, line...)
			if len(er.buf) > er.maxBytes {
				return nil, fmt.Errorf("%w (%d bytes)", ErrEventTooLarge, er.maxBytes)
			}
			if isBlankLine(line) && len(er.buf) > len(line) {
				return er.buf, nil
			}
			if isBlankLine(line) {
				// A blank line with no preceding field lines is padding
				// between events, not an event.
				er.buf = er.buf[:0]
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(er.buf) > 0 {
					return er.buf, io.ErrUnexpectedEOF
				}
				return nil, io.EOF
			}
			return nil, err
		}
	}
}

// readLine returns the next line including its terminator. Lines longer than
// the bufio buffer are assembled across reads, bounded by maxBytes.
func (er *EventReader) readLine() ([]byte, error) {
	line, err := er.r.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return line, err
	}
	long := append([]byte(nil), line...)
	for errors.Is(err, bufio.ErrBufferFull) {
		if len(long) > er.maxBytes {
			return nil, fmt.Errorf("%w (%d bytes)", ErrEventTooLarge, er.maxBytes)
		}
		line, err = er.r.ReadSlice('\n')
		long = append(long, line...)
	}
	return long, err
}

func isBlankLine(line []byte) bool {
	return len(line) == 1 && line[0] == '\n' || len(line) == 2 && line[0] == '\r' && line[1] == '\n'
}

// EventData returns the payload of an event's data fields. Multiple data lines
// are joined with newlines, as the SSE specification requires. It returns nil
// for events without data (comments, keep-alives).
//
// For the common single-line event the result aliases the event buffer; it is
// never written through, so the event itself stays intact for relaying.
func EventData(event []byte) []byte {
	var first, joined []byte
	n := 0
	for len(event) > 0 {
		var line []byte
		line, event, _ = bytes.Cut(event, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		value, ok := bytes.CutPrefix(line, dataPrefix)
		if !ok {
			continue
		}
		value = bytes.TrimPrefix(value, []byte{' '})
		switch n {
		case 0:
			first = value
		case 1:
			joined = make([]byte, 0, len(first)+1+len(value))
			joined = append(joined, first...)
			joined = append(joined, '\n')
			joined = append(joined, value...)
		default:
			joined = append(joined, '\n')
			joined = append(joined, value...)
		}
		n++
	}
	if n > 1 {
		return joined
	}
	return first
}

// IsDone reports whether an event payload is the stream terminator
// "data: [DONE]".
func IsDone(data []byte) bool {
	return bytes.Equal(data, doneData)
}

// WriteEvent writes data as a single server-sent event. The payload must not
// contain newlines.
func WriteEvent(w io.Writer, data []byte) error {
	buf := make([]byte, 0, len(dataPrefix)+1+len(data)+2)
	buf = append(buf, dataPrefix...)
	buf = append(buf, ' ')
	buf = append(buf, data...)
	buf = append(buf, '\n', '\n')
	_, err := w.Write(buf)
	return err
}
