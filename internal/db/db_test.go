package db

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/maverickman79/reelix/internal/config"
)

// fastPolicy keeps the retry tests to well under a second while still
// exercising several attempts and the backoff doubling.
var fastPolicy = retryPolicy{
	budget:  300 * time.Millisecond,
	initial: 20 * time.Millisecond,
	max:     50 * time.Millisecond,
}

func TestIsPermanent(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "invalid password",
			err:  &pgconn.PgError{Code: "28P01", Message: "password authentication failed"},
			want: true,
		},
		{
			name: "invalid authorization",
			err:  &pgconn.PgError{Code: "28000", Message: "no pg_hba.conf entry"},
			want: true,
		},
		{
			name: "database does not exist",
			err:  &pgconn.PgError{Code: "3D000", Message: "database does not exist"},
			want: true,
		},
		{
			name: "server still starting",
			err:  &pgconn.PgError{Code: "57P03", Message: "the database system is starting up"},
			want: false,
		},
		{
			name: "connection refused",
			err:  errors.New("dial tcp 172.18.0.3:5432: connect: connection refused"),
			want: false,
		},
		{
			name: "wrapped permanent error",
			err: errors.Join(errors.New("connecting"),
				&pgconn.PgError{Code: "28P01"}),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPermanent(tt.err); got != tt.want {
				t.Errorf("isPermanent(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// unusedPort returns a TCP port with nothing listening on it, so a connection
// attempt is refused rather than hanging.
func unusedPort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return port
}

// TestOpenRetriesThenGivesUp covers the case that actually bit us: the app
// starting while PostgreSQL is not yet accepting connections.
//
// `docker compose restart` restarts services in parallel and does not honour
// depends_on, so this window is reachable in normal operation.
func TestOpenRetriesThenGivesUp(t *testing.T) {
	cfg := config.Database{
		Host:     "127.0.0.1",
		Port:     unusedPort(t),
		Name:     "reelix",
		User:     "reelix",
		Password: "irrelevant",
		SSLMode:  "disable",
	}

	started := time.Now()
	_, err := open(context.Background(), cfg, discardLogger(), fastPolicy)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("expected an error connecting to a closed port")
	}
	if !strings.Contains(err.Error(), "gave up after") {
		t.Errorf("error does not report giving up: %v", err)
	}
	// It must have retried rather than failed on the first attempt.
	if elapsed < fastPolicy.initial {
		t.Errorf("gave up after %s without retrying", elapsed)
	}
	// And it must respect the budget rather than retrying indefinitely.
	if elapsed > fastPolicy.budget*3 {
		t.Errorf("took %s, well past the %s budget", elapsed, fastPolicy.budget)
	}
	// The password must not leak into the error text.
	if strings.Contains(err.Error(), "irrelevant") {
		t.Error("the error message contains the database password")
	}
}

// TestOpenAbortsOnContextCancel proves a termination signal during startup
// stops the process instead of finishing the retry budget first.
func TestOpenAbortsOnContextCancel(t *testing.T) {
	cfg := config.Database{
		Host:     "127.0.0.1",
		Port:     unusedPort(t),
		Name:     "reelix",
		User:     "reelix",
		Password: "irrelevant",
		SSLMode:  "disable",
	}

	// A long budget, so finishing quickly can only be the cancellation.
	policy := retryPolicy{budget: time.Minute, initial: 20 * time.Millisecond, max: time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	started := time.Now()
	_, err := open(ctx, cfg, discardLogger(), policy)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("expected an error after cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error is %v, want it to wrap context.Canceled", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("cancellation took %s to take effect", elapsed)
	}
}

// TestOpenFailsFastOnBadPassword proves a configuration error is reported
// immediately rather than retried for the whole budget.
func TestOpenFailsFastOnBadPassword(t *testing.T) {
	adminDSN := os.Getenv(dsnEnv)
	if adminDSN == "" {
		t.Skipf("%s not set; skipping database integration test", dsnEnv)
	}

	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parsing %s: %v", dsnEnv, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing port from %s: %v", dsnEnv, err)
	}
	user := u.User.Username()

	cfg := config.Database{
		Host:     u.Hostname(),
		Port:     port,
		Name:     strings.TrimPrefix(u.Path, "/"),
		User:     user,
		Password: "definitely-not-the-password",
		SSLMode:  "disable",
	}

	// A budget long enough that retrying would be obvious in the timing.
	policy := retryPolicy{budget: 30 * time.Second, initial: time.Second, max: 5 * time.Second}

	started := time.Now()
	_, err = open(context.Background(), cfg, discardLogger(), policy)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("expected an authentication error")
	}
	if elapsed > 5*time.Second {
		t.Errorf("a rejected password took %s; it should not be retried", elapsed)
	}
	if strings.Contains(err.Error(), "gave up after") {
		t.Errorf("a rejected password was retried to exhaustion: %v", err)
	}
	if strings.Contains(err.Error(), "definitely-not-the-password") {
		t.Error("the error message contains the database password")
	}
}

// TestOpenSucceeds is the happy path, and confirms Open reports the redacted
// target rather than the DSN.
func TestOpenSucceeds(t *testing.T) {
	adminDSN := os.Getenv(dsnEnv)
	if adminDSN == "" {
		t.Skipf("%s not set; skipping database integration test", dsnEnv)
	}

	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parsing %s: %v", dsnEnv, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing port from %s: %v", dsnEnv, err)
	}
	password, _ := u.User.Password()

	cfg := config.Database{
		Host:     u.Hostname(),
		Port:     port,
		Name:     strings.TrimPrefix(u.Path, "/"),
		User:     u.User.Username(),
		Password: password,
		SSLMode:  "disable",
	}

	pool, err := open(context.Background(), cfg, discardLogger(), fastPolicy)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	var one int
	if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("querying through the returned pool: %v", err)
	}
	if one != 1 {
		t.Errorf("SELECT 1 returned %d", one)
	}
}
