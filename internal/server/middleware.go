package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"
	"uuid"

	"github.com/maverickman79/reelix/internal/logging"
)

// ctxKey is unexported so no other package can collide with these context keys.
type ctxKey int

const ctxKeyRequestID ctxKey = iota

// requestIDHeader lets a reverse proxy supply a correlation ID. An
// unrecognisable value is replaced rather than propagated, so the field can
// always be trusted in the log.
const requestIDHeader = "X-Request-ID"

// requestLogger assigns each request an ID, puts a logger carrying it into the
// request context, and logs one line per completed request.
//
// /health is logged at debug: the compose healthcheck polls it every few
// seconds, and at info that traffic would bury everything operationally useful.
func requestLogger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := requestID(r)
			log := base.With(slog.String(logging.KeyRequestID, id))

			ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
			ctx = logging.WithLogger(ctx, log)

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			rec.Header().Set(requestIDHeader, id)

			start := time.Now()
			next.ServeHTTP(rec, r.WithContext(ctx))
			elapsed := time.Since(start)

			level := slog.LevelInfo
			switch {
			case r.URL.Path == "/health":
				level = slog.LevelDebug
			case rec.status >= http.StatusInternalServerError:
				level = slog.LevelError
			}

			// The query string is deliberately omitted: Jellyfin clients pass
			// tokens as query parameters, and those must never reach the log.
			log.LogAttrs(r.Context(), level, "request completed",
				slog.String(logging.KeyOperation, "request"),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("bytes", rec.written),
				slog.Int64("duration_ms", elapsed.Milliseconds()),
				slog.String("remote", clientIP(r)))
		})
	}
}

// requestID returns the caller's correlation ID when it supplied a valid one,
// otherwise a fresh UUIDv7. v7 is time-ordered, so IDs sort by arrival.
func requestID(r *http.Request) string {
	if v := r.Header.Get(requestIDHeader); v != "" && len(v) <= 64 && isPrintableASCII(v) {
		return v
	}
	return uuid.NewV7().String()
}

// loggerFrom returns the request-scoped logger. The storage lives in
// internal/logging so the API packages can read it too.
func loggerFrom(ctx context.Context) *slog.Logger { return logging.FromContext(ctx) }

// statusRecorder captures the status code and body size for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}
	r.wrote = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer, which the
// direct-play endpoint will need for flushing and deadline control in Step 7.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func clientIP(r *http.Request) string {
	// Deliberately not honouring X-Forwarded-For: nothing has configured a
	// trusted proxy yet, and an unvalidated forwarded header is spoofable.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isPrintableASCII(s string) bool {
	for _, c := range s {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
