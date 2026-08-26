// Package server owns the HTTP listener, its routes, and its shutdown
// behaviour.
//
// It serves GET /health and mounts the native API under /api/v1. The Jellyfin
// compatibility surface is registered here in a later step; those handlers stay
// behind the internal/compat/jellyfin package boundary and never leak their
// types into this package.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	v1 "github.com/maverickman79/reelix/internal/api/v1"
	"github.com/maverickman79/reelix/internal/config"
	"github.com/maverickman79/reelix/internal/logging"
)

// Timeouts guarding the listener against slow or stuck peers. ReadHeaderTimeout
// in particular is what stops a Slowloris-style client from pinning a
// connection indefinitely.
const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 120 * time.Second
)

// Server wraps the HTTP listener and the routes mounted on it.
type Server struct {
	cfg     config.HTTP
	log     *slog.Logger
	version string
	http    *http.Server
}

// New builds a Server. It does not bind a port; call Run for that.
//
// nativeAPI is mounted under /api/v1. It may be nil, which serves only
// /health — useful in tests that have no database.
func New(cfg config.HTTP, log *slog.Logger, version string, nativeAPI *v1.API) *Server {
	s := &Server{
		cfg:     cfg,
		log:     logging.Component(log, "http"),
		version: version,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth())

	if nativeAPI != nil {
		// StripPrefix so the API package writes its patterns relative to its
		// own root and does not repeat the version in every route.
		mux.Handle(v1.Prefix+"/", http.StripPrefix(v1.Prefix, nativeAPI.Routes()))
	}

	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           requestLogger(s.log)(mux),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	return s
}

// Run binds the listener and serves until ctx is cancelled, then drains
// in-flight requests within the configured shutdown timeout.
//
// Binding happens synchronously so that a port conflict is reported as a
// startup error rather than appearing later as an unexplained exit.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}

	s.log.Info("http listener started",
		slog.String(logging.KeyOperation, "serve"),
		slog.String("addr", ln.Addr().String()))

	errs := make(chan error, 1)
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	s.log.Info("http listener draining",
		slog.String(logging.KeyOperation, "shutdown"),
		slog.Duration("timeout", s.cfg.ShutdownTimeout))

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownTimeout)
	defer cancel()

	if err := s.http.Shutdown(shutdownCtx); err != nil {
		// Shutdown returning an error means the drain deadline passed with
		// connections still open. Close them rather than leaking goroutines.
		_ = s.http.Close()
		return err
	}
	return <-errs
}

// writeJSON encodes v as the response body. An encoding failure after the
// header is written cannot be corrected on the wire, so it is logged rather
// than returned.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(v); err != nil {
		loggerFrom(r.Context()).Error("encoding response body failed",
			slog.String(logging.KeyOperation, "write_json"),
			slog.String(logging.KeyError, err.Error()))
	}
}
