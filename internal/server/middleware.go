package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/shusingh/switchyard/internal/openai"
)

// HeaderRequestID carries the request ID. A client-supplied value is kept if
// it is well formed, so IDs can be traced across services; otherwise the
// server generates one. The ID is forwarded to the backend and echoed in the
// response.
const HeaderRequestID = "X-Request-ID"

// maxRequestIDLength bounds client-supplied IDs, which are logged verbatim.
const maxRequestIDLength = 64

type requestIDKey struct{}

// RequestID returns the request ID stored in ctx, or "" if there is none.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if !validRequestID(id) {
			id = newRequestID()
			r.Header.Set(HeaderRequestID, id)
		}
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// validRequestID accepts short IDs made of characters that are safe to log
// and to echo in a header.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLength {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	return hex.EncodeToString(b[:])
}

// withRecovery turns a panic in a handler into a logged error and, if nothing
// has been written yet, a 500 response. The panic still ends the request, but
// never the process.
func withRecovery(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &trackingWriter{ResponseWriter: w}
		defer recoverPanic(r.Context(), logger, tw)
		next.ServeHTTP(tw, r)
	})
}

// recoverPanic must be deferred directly, so that recover sees the panic.
func recoverPanic(ctx context.Context, logger *slog.Logger, tw *trackingWriter) {
	v := recover()
	if v == nil {
		return
	}
	if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
		panic(v) // net/http's documented way to abort a response
	}
	logger.ErrorContext(ctx, "handler panic",
		slog.String("request_id", RequestID(ctx)),
		slog.String("panic", fmt.Sprint(v)),
		slog.String("stack", string(debug.Stack())))
	if !tw.wroteHeader {
		openai.WriteError(tw, http.StatusInternalServerError, openai.ErrTypeServer, "internal error")
	}
}

// trackingWriter records whether the response status has been sent.
// Unwrap lets http.ResponseController reach the underlying writer's Flush.
type trackingWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (tw *trackingWriter) WriteHeader(status int) {
	tw.wroteHeader = true
	tw.ResponseWriter.WriteHeader(status)
}

func (tw *trackingWriter) Write(p []byte) (int, error) {
	tw.wroteHeader = true
	return tw.ResponseWriter.Write(p)
}

func (tw *trackingWriter) Unwrap() http.ResponseWriter { return tw.ResponseWriter }

// formatBytes renders a byte count for error messages.
func formatBytes(n int64) string {
	const unit = 1 << 10
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
