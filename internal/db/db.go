// Package db owns the PostgreSQL connection pool and the schema migration
// runner.
//
// Nothing in this package knows anything about Reelix's domain. Repositories
// live in internal/repository and take a Querier from here.
package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maverickman79/reelix/internal/config"
	"github.com/maverickman79/reelix/internal/logging"
)

// Pool sizing. These are deliberately modest: a media server spends most of
// its time streaming bytes, not querying, and an oversized pool mostly serves
// to exhaust PostgreSQL's own connection limit during a scan.
const (
	maxConns          = 10
	maxConnLifetime   = time.Hour
	maxConnIdleTime   = 30 * time.Minute
	healthCheckPeriod = time.Minute
	connectTimeout    = 10 * time.Second
)

// Querier is the subset of pgx used by repositories.
//
// Both *pgxpool.Pool and pgx.Tx satisfy it, so a repository method can run
// either on its own or inside a caller's transaction without a second code
// path.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Open builds a connection pool and verifies it can reach the server.
//
// The connectivity check is deliberate: a bad password or an unreachable host
// should be a startup error naming the database, not a confusing failure on
// the first request minutes later.
func Open(ctx context.Context, cfg config.Database, log *slog.Logger) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		// The DSN carries the password, so it must not reach the error text.
		return nil, fmt.Errorf("parsing database configuration: %w", err)
	}

	poolCfg.MaxConns = maxConns
	poolCfg.MaxConnLifetime = maxConnLifetime
	poolCfg.MaxConnIdleTime = maxConnIdleTime
	poolCfg.HealthCheckPeriod = healthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = connectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to %s: %w", cfg.Redacted(), err)
	}

	logging.Component(log, "db").Info("database connected",
		slog.String(logging.KeyOperation, "open"),
		slog.String("database", cfg.Redacted()),
		slog.Int("max_conns", maxConns))

	return pool, nil
}
