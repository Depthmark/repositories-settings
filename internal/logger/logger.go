// Package logger constructs the application's slog logger and provides
// helpers for attaching repo and trace context to log records.
package logger

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"strings"
)

func New(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

// WithRepo returns a logger with owner and repo fields attached.
func WithRepo(l *slog.Logger, owner, name string) *slog.Logger {
	return l.With("owner", owner, "repo", name)
}

// TraceID returns a 16-char hex random identifier.
func TraceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Discard returns a logger that drops everything; for tests.
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
