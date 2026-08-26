package v1_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	v1 "github.com/maverickman79/reelix/internal/api/v1"
	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/media"
	"github.com/maverickman79/reelix/internal/service"
)

const dsnEnv = "REELIX_TEST_DB_DSN"

const (
	testUser     = "admin"
	testPassword = "a sufficiently long password"
)

// harness is a running API backed by a scratch database.
type harness struct {
	t    *testing.T
	pool *pgxpool.Pool
	srv  *httptest.Server
}

// newHarness builds an API over a fresh, migrated database and serves it.
func newHarness(t *testing.T) *harness {
	t.Helper()

	adminDSN := os.Getenv(dsnEnv)
	if adminDSN == "" {
		t.Skipf("%s not set; skipping database integration test", dsnEnv)
	}

	ctx := context.Background()

	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connecting to %s: %v", dsnEnv, err)
	}
	defer admin.Close()

	name := "reelix_test_" + strings.ReplaceAll(uuid.NewV7().String(), "-", "_")
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		t.Fatalf("creating scratch database: %v", err)
	}

	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parsing %s: %v", dsnEnv, err)
	}
	u.Path = "/" + name

	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatalf("connecting to scratch database: %v", err)
	}

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, pool, discard); err != nil {
		t.Fatalf("migrating scratch database: %v", err)
	}

	// The scan service needs a prober; API tests that do not scan never invoke
	// it, and the scan tests supply their own harness.
	prober := media.NewProber(ffprobePath(), 30*time.Second)
	api := v1.New(
		service.NewAuthService(pool),
		service.NewLibraryService(pool),
		service.NewScanService(pool, prober, discard),
	)

	// StripPrefix mirrors how internal/server mounts the API, so the tests
	// exercise the same paths production does.
	mux := http.NewServeMux()
	mux.Handle(v1.Prefix+"/", http.StripPrefix(v1.Prefix, api.Routes()))

	// A request-scoped logger, as the real middleware installs. Without it the
	// handlers fall back to the default logger and error paths write to stderr
	// during tests.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(logging.WithLogger(r.Context(), discard)))
	}))

	t.Cleanup(func() {
		srv.Close()
		pool.Close()

		cleanup, err := pgxpool.New(context.Background(), adminDSN)
		if err != nil {
			t.Errorf("reconnecting to drop scratch database: %v", err)
			return
		}
		defer cleanup.Close()

		if _, err := cleanup.Exec(context.Background(),
			fmt.Sprintf("DROP DATABASE IF EXISTS %q WITH (FORCE)", name)); err != nil {
			t.Errorf("dropping scratch database %s: %v", name, err)
		}
	})

	return &harness{t: t, pool: pool, srv: srv}
}

// do issues a request against the harness. token may be empty.
func (h *harness) do(method, path, token string, body any) *http.Response {
	h.t.Helper()

	var r io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("encoding request body: %v", err)
		}
		r = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, h.srv.URL+v1.Prefix+path, r)
	if err != nil {
		h.t.Fatalf("building request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// decode reads a JSON response body into dst and closes the body.
func (h *harness) decode(resp *http.Response, dst any) {
	h.t.Helper()
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		h.t.Fatalf("decoding response: %v", err)
	}
}

// body reads a response body as a string and closes it.
func (h *harness) body(resp *http.Response) string {
	h.t.Helper()
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("reading response: %v", err)
	}
	return string(b)
}

// setup creates the first administrator.
func (h *harness) setup() {
	h.t.Helper()

	resp := h.do(http.MethodPost, "/setup", "", map[string]string{
		"username": testUser,
		"password": testPassword,
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		h.t.Fatalf("setup returned %d", resp.StatusCode)
	}
}

// login returns a valid bearer token.
func (h *harness) login() string {
	h.t.Helper()

	resp := h.do(http.MethodPost, "/auth/login", "", map[string]string{
		"username": testUser,
		"password": testPassword,
	})
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		h.t.Fatalf("login returned %d", resp.StatusCode)
	}

	var out struct {
		Token string `json:"token"`
	}
	h.decode(resp, &out)
	return out.Token
}

// errorCode reads the code out of an error response.
func (h *harness) errorCode(resp *http.Response) string {
	h.t.Helper()

	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	h.decode(resp, &out)
	return out.Error.Code
}

// ffprobePath returns the ffprobe binary to test against.
//
// Defaults to whatever is on PATH so a developer with ffmpeg installed gets
// real probing; REELIX_TEST_FFPROBE overrides it, and tests that need a
// working binary skip when neither resolves.
func ffprobePath() string {
	if p := os.Getenv("REELIX_TEST_FFPROBE"); p != "" {
		return p
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		return p
	}
	return "ffprobe"
}

// hasFFprobe reports whether a usable ffprobe exists on this machine.
func hasFFprobe() bool {
	_, err := exec.LookPath(ffprobePath())
	return err == nil
}
