package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maverickman79/reelix/internal/logging"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationDir is the embedded directory holding the .sql files.
const migrationDir = "migrations"

// advisoryLockKey serialises migration runs across processes.
//
// The value is arbitrary but must be stable forever: it is the identity of
// "the Reelix migration lock" within the target database's shared advisory
// lock space. Changing it would let an old and a new binary migrate
// concurrently during a rolling restart.
const advisoryLockKey int64 = 0x7265656C69780001

// Migration is one versioned schema change, loaded from the embedded files.
type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

// appliedMigration is one row of the schema_migrations ledger.
type appliedMigration struct {
	Version  int
	Name     string
	Checksum string
}

// createLedger bootstraps the ledger table itself.
//
// This cannot live in a migration file: the runner needs somewhere to record
// that migration 1 was applied before migration 1 has run. IF NOT EXISTS makes
// the bootstrap idempotent, which is the same property the ledger then gives
// every other migration.
const createLedger = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    integer     PRIMARY KEY,
    name       text        NOT NULL,
    checksum   text        NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
)`

// Migrate applies every pending migration, in version order.
//
// It is safe to call on every startup and safe to call concurrently from more
// than one process. A run against a fully migrated database applies nothing
// and returns nil.
//
// Migrations are forward-only. There are no down migrations: rolling a media
// library's schema backwards unattended is a way to lose data, and the
// constitution requires destructive changes to be deliberate.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	log = logging.Component(log, "db")

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return errors.New("no migrations embedded in the binary")
	}

	// A dedicated connection, held for the whole run: a PostgreSQL session
	// advisory lock belongs to the session that took it, so returning the
	// connection to the pool mid-run would release it.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("acquiring migration lock: %w", err)
	}
	defer func() {
		// Best effort: if unlocking fails the session is broken anyway, and
		// PostgreSQL drops session locks when the connection closes.
		if _, err := conn.Exec(context.WithoutCancel(ctx),
			"SELECT pg_advisory_unlock($1)", advisoryLockKey); err != nil {
			log.Warn("releasing migration lock failed",
				slog.String(logging.KeyOperation, "migrate"),
				slog.String(logging.KeyError, err.Error()))
		}
	}()

	if _, err := conn.Exec(ctx, createLedger); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	applied, err := loadApplied(ctx, conn)
	if err != nil {
		return err
	}

	if err := verifyChecksums(migrations, applied); err != nil {
		return err
	}

	pending := 0
	for _, m := range migrations {
		if _, done := applied[m.Version]; done {
			continue
		}
		if err := applyOne(ctx, conn, m); err != nil {
			return err
		}
		pending++
		log.Info("migration applied",
			slog.String(logging.KeyOperation, "migrate"),
			slog.Int("version", m.Version),
			slog.String("name", m.Name))
	}

	if pending == 0 {
		log.Info("schema up to date",
			slog.String(logging.KeyOperation, "migrate"),
			slog.Int("version", migrations[len(migrations)-1].Version))
	}
	return nil
}

// applyOne runs a single migration and records it, atomically.
//
// The ledger insert shares the migration's transaction on purpose: a failure
// anywhere leaves the database at the previous version rather than in a state
// the ledger describes incorrectly.
func applyOne(ctx context.Context, conn *pgxpool.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migration %04d %s: begin: %w", m.Version, m.Name, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("migration %04d %s: %w", m.Version, m.Name, err)
	}

	const insert = `
		INSERT INTO schema_migrations (version, name, checksum)
		VALUES ($1, $2, $3)`
	if _, err := tx.Exec(ctx, insert, m.Version, m.Name, m.Checksum); err != nil {
		return fmt.Errorf("migration %04d %s: recording: %w", m.Version, m.Name, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migration %04d %s: commit: %w", m.Version, m.Name, err)
	}
	return nil
}

// loadApplied reads the ledger, keyed by version.
func loadApplied(ctx context.Context, q Querier) (map[int]appliedMigration, error) {
	rows, err := q.Query(ctx, "SELECT version, name, checksum FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("reading schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]appliedMigration)
	for rows.Next() {
		var a appliedMigration
		if err := rows.Scan(&a.Version, &a.Name, &a.Checksum); err != nil {
			return nil, fmt.Errorf("reading schema_migrations: %w", err)
		}
		applied[a.Version] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading schema_migrations: %w", err)
	}
	return applied, nil
}

// verifyChecksums refuses to run when an already-applied migration file has
// changed on disk.
//
// Editing a released migration produces environments whose schemas silently
// disagree — the edited version is applied in new deployments and absent in
// existing ones. Failing at startup is far cheaper than diagnosing that later.
func verifyChecksums(migrations []Migration, applied map[int]appliedMigration) error {
	known := make(map[int]struct{}, len(migrations))
	for _, m := range migrations {
		known[m.Version] = struct{}{}

		a, ok := applied[m.Version]
		if !ok {
			continue
		}
		if a.Checksum != m.Checksum {
			return fmt.Errorf(
				"migration %04d %s has changed since it was applied (recorded %s, embedded %s); "+
					"applied migrations are immutable — add a new migration instead",
				m.Version, m.Name, shortSum(a.Checksum), shortSum(m.Checksum))
		}
	}

	// A database migrated by a newer binary than this one. Continuing would
	// run this binary against a schema it does not understand.
	for version, a := range applied {
		if _, ok := known[version]; !ok {
			return fmt.Errorf(
				"database has migration %04d %s applied, which this binary does not contain; "+
					"it was migrated by a newer version of Reelix",
				version, a.Name)
		}
	}
	return nil
}

// loadMigrations reads and validates the embedded migration set.
func loadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, migrationDir)
	if err != nil {
		return nil, fmt.Errorf("reading embedded migrations: %w", err)
	}

	migrations := make([]Migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}

		version, name, err := parseMigrationName(e.Name())
		if err != nil {
			return nil, err
		}

		body, err := migrationFS.ReadFile(path.Join(migrationDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading migration %s: %w", e.Name(), err)
		}

		sum := sha256.Sum256(body)
		migrations = append(migrations, Migration{
			Version:  version,
			Name:     name,
			SQL:      string(body),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}

	slices.SortFunc(migrations, func(a, b Migration) int { return a.Version - b.Version })

	for i, m := range migrations {
		if m.Version < 1 {
			return nil, fmt.Errorf("migration %s: version must be positive", m.Name)
		}
		if i > 0 && migrations[i-1].Version == m.Version {
			return nil, fmt.Errorf("duplicate migration version %04d (%s and %s)",
				m.Version, migrations[i-1].Name, m.Name)
		}
	}
	return migrations, nil
}

// parseMigrationName splits "0001_init.sql" into 1 and "init".
func parseMigrationName(filename string) (int, string, error) {
	base := strings.TrimSuffix(filename, ".sql")

	prefix, name, ok := strings.Cut(base, "_")
	if !ok || name == "" {
		return 0, "", fmt.Errorf("migration %s: name must be NNNN_description.sql", filename)
	}

	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, "", fmt.Errorf("migration %s: %q is not a version number", filename, prefix)
	}
	return version, name, nil
}

// shortSum abbreviates a checksum for an error message.
func shortSum(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}
