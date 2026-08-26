package repository_test

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

	"github.com/maverickman79/reelix/internal/db"
)

// dsnEnv mirrors internal/db: integration tests are skipped when it is unset.
const dsnEnv = "REELIX_TEST_DB_DSN"

// migratedDB returns a pool on a fresh, fully migrated database.
//
// Repository tests run against the real schema rather than a hand-written one,
// so a constraint that exists only in the test would be caught here.
func migratedDB(t *testing.T) *pgxpool.Pool {
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

	t.Cleanup(func() {
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

	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrating scratch database: %v", err)
	}
	return pool
}

func ptr[T any](v T) *T { return &v }
