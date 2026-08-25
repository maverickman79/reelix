// Package logging constructs the process-wide structured logger.
//
// Reelix logs with log/slog. The field vocabulary is fixed by the project
// constitution: component, operation, request_id, and where applicable user_id,
// job_id, and error.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// Attribute keys. Using constants keeps the vocabulary consistent across
// packages and makes a typo a compile error rather than an unqueryable log.
const (
	KeyComponent = "component"
	KeyOperation = "operation"
	KeyRequestID = "request_id"
	KeyError     = "error"
)

// New builds a logger writing to w.
//
// format is "json" or "text"; level is one of debug, info, warn, error. Both
// have already been validated by the config package, so an unrecognised value
// here falls back to the safe default rather than failing at runtime.
func New(w io.Writer, format, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

// Component returns a child logger tagged with the component field. Every
// subsystem should log through one of these rather than through the root.
func Component(l *slog.Logger, name string) *slog.Logger {
	return l.With(slog.String(KeyComponent, name))
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
