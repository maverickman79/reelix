package db

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dsnEnv names the environment variable pointing at a PostgreSQL server the
// tests may create and drop databases on.
//
// Integration tests are skipped when it is unset, so `go test ./...` passes on
// a machine with no database. To run them:
//
//	docker compose -f docker-compose.yml -f docker-compose.test.yml up -d postgres
//	REELIX_TEST_DB_DSN="postgres://reelix:...@127.0.0.1:5432/reelix?sslmode=disable" go test ./...
const dsnEnv = "REELIX_TEST_DB_DSN"

// discardLogger keeps test output readable. The migration runner logs at info
// on every apply, which would otherwise bury the actual failures.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// scratchDB creates an empty database and returns a pool connected to it.
//
// Each test gets its own, named after a fresh UUID, and drops it on cleanup.
// Tests that assert on migration state need a database nothing else is
// migrating concurrently, and reusing one would make them order-dependent.
func scratchDB(t *testing.T) *pgxpool.Pool {
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

	// Dashes are legal in a quoted identifier but make every manual psql
	// session awkward, so the id becomes underscores.
	name := "reelix_test_" + strings.ReplaceAll(uuid.NewV7().String(), "-", "_")

	// The name is generated here, not taken from input, so interpolating it is
	// safe — CREATE DATABASE cannot take a parameter in any case.
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		t.Fatalf("creating scratch database: %v", err)
	}

	pool, err := pgxpool.New(ctx, replaceDBName(t, adminDSN, name))
	if err != nil {
		t.Fatalf("connecting to scratch database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()

		cleanup, err := pgxpool.New(context.Background(), adminDSN)
		if err != nil {
			t.Errorf("reconnecting to drop scratch database: %v", err)
			return
		}
		defer cleanup.Close()

		// WITH (FORCE) terminates any connection the test left behind; without
		// it a leaked connection makes the drop fail and leaves litter on the
		// developer's server.
		if _, err := cleanup.Exec(context.Background(),
			fmt.Sprintf("DROP DATABASE IF EXISTS %q WITH (FORCE)", name)); err != nil {
			t.Errorf("dropping scratch database %s: %v", name, err)
		}
	})

	return pool
}

// replaceDBName rewrites the database component of a connection URL.
func replaceDBName(t *testing.T, dsn, name string) string {
	t.Helper()

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parsing %s: %v", dsnEnv, err)
	}
	u.Path = "/" + name
	return u.String()
}
