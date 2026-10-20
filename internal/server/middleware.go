package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Depthmark/repositories-settings/internal/logger"
)

type ctxKey int

const traceIDKey ctxKey = 1

func TraceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

func chain(mw ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		for i := len(mw) - 1; i >= 0; i-- {
			h = mw[i](h)
		}
		return h
	}
}

func traceMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A caller-supplied trace ID is echoed into a response
			// header and into every log line for the request, so it is
			// only honoured in a shape that cannot forge either: no
			// newlines to inject a log record, nothing to break out of
			// the header.
			id := sanitizeTraceID(r.Header.Get("X-Request-ID"))
			if id == "" {
				id = logger.TraceID()
			}
			w.Header().Set("X-Request-ID", id)
			ctx := context.WithValue(r.Context(), traceIDKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// maxTraceIDLen bounds a caller-supplied trace ID.
const maxTraceIDLen = 64

// sanitizeTraceID returns id when it is a short run of characters safe
// in a header and a log line, and "" otherwise. Rejecting rather than
// stripping keeps a mangled ID from being correlated with the real one.
func sanitizeTraceID(id string) string {
	if id == "" || len(id) > maxTraceIDLen {
		return ""
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_':
		default:
			return ""
		}
	}
	return id
}

func accessLogMiddleware(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rec, r)
			// Suppress healthz/readyz to avoid log spam.
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
				return
			}
			level := slog.LevelInfo
			switch {
			case rec.status >= 500:
				level = slog.LevelError
			case rec.status >= 400:
				level = slog.LevelWarn
			}
			log.LogAttrs(r.Context(), level, "http",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.String("trace_id", TraceIDFromContext(r.Context())),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.String("remote_addr", clientIP(r)),
				slog.String("user_agent", r.UserAgent()),
				slog.String("query", r.URL.RawQuery),
				slog.Int64("bytes_in", r.ContentLength),
			)
		})
	}
}

// clientIP returns the best-effort caller IP, preferring X-Forwarded-For when
// present (the service typically sits behind an ingress).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return xrip
	}
	return r.RemoteAddr
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(s int) {
	r.status = s
	r.ResponseWriter.WriteHeader(s)
}

// limitBody caps how much a handler can read from the request body.
// http.MaxBytesReader makes the read fail once the cap is exceeded and
// signals the client with a 413 rather than letting a handler buffer an
// arbitrarily large payload. Applied per-route because the webhook and
// the API endpoints have different sized legitimate payloads.
func limitBody(n int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next.ServeHTTP(w, r)
	})
}
