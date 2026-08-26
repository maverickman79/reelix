// Package db owns the PostgreSQL connection pool and the schema migration
// runner.
//
// Nothing in this package knows anything about Reelix's domain. Repositories
// live in internal/repository and take a Querier from here.
package db

import (
	"context"
	"errors"
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

// retryPolicy bounds how long Open waits for a database that is not yet
// accepting connections.
type retryPolicy struct {
	// budget is the total time to keep trying before giving up.
	budget time.Duration
	// initial is the first pause between attempts; it doubles up to max.
	initial time.Duration
	max     time.Duration
}

// defaultRetryPolicy covers the cases compose's depends_on does not.
//
// `depends_on: service_healthy` orders `docker compose up`, and nothing else.
// It does not apply to `docker compose restart`, which restarts services in
// parallel, so the app can reach db.Open while PostgreSQL is still binding its
// socket. The same window opens during a PostgreSQL upgrade or a host reboot.
//
// Sixty seconds is chosen against PostgreSQL's own startup: an unclean
// shutdown means WAL recovery before it accepts connections, which on a large
// database is measured in tens of seconds.
var defaultRetryPolicy = retryPolicy{
	budget:  60 * time.Second,
	initial: 250 * time.Millisecond,
	max:     5 * time.Second,
}

// Open builds a connection pool and waits for the server to accept it.
//
// The connectivity check is deliberate: an unreachable host should be a
// startup error naming the database, not a confusing failure on the first
// request minutes later. A server that is merely not ready yet is waited for;
// a rejected password is not, since no amount of waiting fixes it.
func Open(ctx context.Context, cfg config.Database, log *slog.Logger) (*pgxpool.Pool, error) {
	return open(ctx, cfg, log, defaultRetryPolicy)
}

// open is Open with an injectable retry policy so tests need not wait a
// minute to observe the give-up path.
func open(ctx context.Context, cfg config.Database, log *slog.Logger, policy retryPolicy) (*pgxpool.Pool, error) {
	log = logging.Component(log, "db")

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

	if err := waitForDatabase(ctx, pool, cfg, log, policy); err != nil {
		pool.Close()
		return nil, err
	}

	log.Info("database connected",
		slog.String(logging.KeyOperation, "open"),
		slog.String("database", cfg.Redacted()),
		slog.Int("max_conns", maxConns))

	return pool, nil
}

// waitForDatabase pings until the server answers, the error proves waiting is
// pointless, the budget runs out, or ctx is cancelled.
func waitForDatabase(
	ctx context.Context,
	pool *pgxpool.Pool,
	cfg config.Database,
	log *slog.Logger,
	policy retryPolicy,
) error {
	started := time.Now()
	deadline := started.Add(policy.budget)
	delay := policy.initial

	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
		err := pool.Ping(pingCtx)
		cancel()

		if err == nil {
			if attempt > 1 {
				log.Info("database became reachable",
					slog.String(logging.KeyOperation, "open"),
					slog.Int("attempts", attempt),
					slog.Duration("waited", time.Since(started).Truncate(time.Millisecond)))
			}
			return nil
		}

		// A rejected password or a missing database is a configuration
		// problem. Retrying for a minute turns a clear error into a slow one.
		if isPermanent(err) {
			return fmt.Errorf("connecting to %s: %w", cfg.Redacted(), err)
		}

		// Stop before sleeping past the budget rather than after.
		if time.Now().Add(delay).After(deadline) {
			return fmt.Errorf("connecting to %s: gave up after %s and %d attempts: %w",
				cfg.Redacted(), time.Since(started).Truncate(time.Millisecond), attempt, err)
		}

		log.Warn("database not ready, retrying",
			slog.String(logging.KeyOperation, "open"),
			slog.Int("attempt", attempt),
			slog.Duration("retry_in", delay),
			slog.String(logging.KeyError, err.Error()))

		select {
		case <-ctx.Done():
			// A termination signal during startup should stop the process
			// promptly, not finish the retry budget first.
			return fmt.Errorf("connecting to %s: %w", cfg.Redacted(), ctx.Err())
		case <-time.After(delay):
		}

		delay = min(delay*2, policy.max)
	}
}

// isPermanent reports whether an error will still be an error after waiting.
//
// Everything else — refused connections, DNS failures, a server in recovery —
// is treated as transient, because the common case for all of them is a
// database that has not finished starting.
func isPermanent(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}

	switch pgErr.Code {
	case "28P01", // invalid_password
		"28000", // invalid_authorization_specification
		"3D000": // invalid_catalog_name — the database does not exist
		return true
	}
	return false
}
