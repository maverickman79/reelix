package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maverickman79/reelix/internal/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(config.HTTP{Addr: "127.0.0.1:0"}, log, "0.0.1-test")
}

func TestHealthOK(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
	}

	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v (body %q)", err, rec.Body.String())
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
	if body.Version != "0.0.1-test" {
		t.Errorf("version = %q, want 0.0.1-test", body.Version)
	}
}

// The route is registered with a method pattern, so anything else must be
// rejected rather than silently treated as a GET.
func TestHealthRejectsNonGET(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// Step 1 mounts exactly one route. An unknown path must 404, not surface some
// accidentally-registered handler.
func TestUnknownRouteIs404(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/System/Info", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// Every response carries a correlation ID, generated when the caller did not
// supply one and echoed back when they did.
func TestRequestIDHeader(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if got := rec.Header().Get(requestIDHeader); got == "" {
		t.Error("generated request ID header is missing")
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set(requestIDHeader, "caller-supplied-id")
	rec = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if got := rec.Header().Get(requestIDHeader); got != "caller-supplied-id" {
		t.Errorf("request ID = %q, want the caller's value echoed back", got)
	}
}
