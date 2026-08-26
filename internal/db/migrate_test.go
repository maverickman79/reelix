package db

import (
	"context"
	"strings"
	"sync"
	"testing"
	"uuid"
)

// TestLoadMigrations checks the embedded set is well formed. It needs no
// database, so it runs everywhere.
func TestLoadMigrations(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations embedded")
	}

	for i, m := range migrations {
		if i > 0 && m.Version <= migrations[i-1].Version {
			t.Errorf("migrations out of order at %d: %d after %d",
				i, m.Version, migrations[i-1].Version)
		}
		if m.Name == "" {
			t.Errorf("migration %04d has no name", m.Version)
		}
		if strings.TrimSpace(m.SQL) == "" {
			t.Errorf("migration %04d %s is empty", m.Version, m.Name)
		}
		if len(m.Checksum) != 64 {
			t.Errorf("migration %04d %s: checksum %q is not a sha256 hex digest",
				m.Version, m.Name, m.Checksum)
		}
	}

	if got := migrations[0].Version; got != 1 {
		t.Errorf("first migration is version %d, want 1", got)
	}
}

func TestParseMigrationName(t *testing.T) {
	tests := []struct {
		filename string
		version  int
		name     string
		wantErr  bool
	}{
		{filename: "0001_init.sql", version: 1, name: "init"},
		{filename: "0042_add_jobs.sql", version: 42, name: "add_jobs"},
		{filename: "0002_two_words.sql", version: 2, name: "two_words"},
		{filename: "init.sql", wantErr: true},
		{filename: "0001_.sql", wantErr: true},
		{filename: "abcd_init.sql", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			version, name, err := parseMigrationName(tt.filename)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got version=%d name=%q", version, name)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if version != tt.version || name != tt.name {
				t.Errorf("got (%d, %q), want (%d, %q)", version, name, tt.version, tt.name)
			}
		})
	}
}

// TestMigrateAppliesToEmptyDatabase is the first half of the Step 2 completion
// criterion: migrations apply cleanly to an empty database.
func TestMigrateAppliesToEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	pool := scratchDB(t)

	if err := Migrate(ctx, pool, discardLogger()); err != nil {
		t.Fatalf("Migrate on empty database: %v", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}

	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("counting applied migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Errorf("recorded %d migrations, embedded %d", applied, len(migrations))
	}

	// Every table the schema promises must actually exist.
	for _, table := range []string{
		"schema_migrations", "users", "libraries", "library_paths",
		"media_items", "media_files", "media_streams",
	} {
		var exists bool
		const q = `SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1)`
		if err := pool.QueryRow(ctx, q, table).Scan(&exists); err != nil {
			t.Fatalf("checking for table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s was not created", table)
		}
	}
}

// TestMigrateIsIdempotent is the second half of the completion criterion: a
// re-run applies nothing and succeeds.
func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := scratchDB(t)
	log := discardLogger()

	if err := Migrate(ctx, pool, log); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}

	var firstAppliedAt []string
	rows, err := pool.Query(ctx, `SELECT applied_at::text FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("reading ledger: %v", err)
	}
	for rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err != nil {
			t.Fatalf("reading ledger: %v", err)
		}
		firstAppliedAt = append(firstAppliedAt, ts)
	}
	rows.Close()

	// Three more runs, because "idempotent" should not mean "survives exactly
	// one repeat".
	for i := range 3 {
		if err := Migrate(ctx, pool, log); err != nil {
			t.Fatalf("re-run %d: %v", i+1, err)
		}
	}

	var secondAppliedAt []string
	rows, err = pool.Query(ctx, `SELECT applied_at::text FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("re-reading ledger: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err != nil {
			t.Fatalf("re-reading ledger: %v", err)
		}
		secondAppliedAt = append(secondAppliedAt, ts)
	}

	if len(firstAppliedAt) != len(secondAppliedAt) {
		t.Fatalf("ledger changed size: %d then %d", len(firstAppliedAt), len(secondAppliedAt))
	}
	// Unchanged timestamps prove the rows were not rewritten, which a
	// count-only assertion would miss.
	for i := range firstAppliedAt {
		if firstAppliedAt[i] != secondAppliedAt[i] {
			t.Errorf("migration %d was re-applied: applied_at %s became %s",
				i+1, firstAppliedAt[i], secondAppliedAt[i])
		}
	}
}

// TestMigrateRejectsChangedChecksum proves an edited, already-applied
// migration is a startup error rather than a silent skip.
func TestMigrateRejectsChangedChecksum(t *testing.T) {
	ctx := context.Background()
	pool := scratchDB(t)

	if err := Migrate(ctx, pool, discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Simulate the file having been edited after release by corrupting the
	// recorded checksum, which is equivalent and does not require writing to
	// the embedded filesystem.
	if _, err := pool.Exec(ctx,
		`UPDATE schema_migrations SET checksum = 'deadbeef' WHERE version = 1`); err != nil {
		t.Fatalf("corrupting ledger: %v", err)
	}

	err := Migrate(ctx, pool, discardLogger())
	if err == nil {
		t.Fatal("expected an error for a changed migration, got nil")
	}
	if !strings.Contains(err.Error(), "has changed since it was applied") {
		t.Errorf("error does not explain the problem: %v", err)
	}
}

// TestMigrateRejectsUnknownVersion covers the downgrade case: a database
// migrated by a newer binary must not be served by an older one.
func TestMigrateRejectsUnknownVersion(t *testing.T) {
	ctx := context.Background()
	pool := scratchDB(t)

	if err := Migrate(ctx, pool, discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO schema_migrations (version, name, checksum) VALUES (9999, 'from_the_future', 'x')`,
	); err != nil {
		t.Fatalf("inserting future migration: %v", err)
	}

	err := Migrate(ctx, pool, discardLogger())
	if err == nil {
		t.Fatal("expected an error for an unknown applied migration, got nil")
	}
	if !strings.Contains(err.Error(), "newer version of Reelix") {
		t.Errorf("error does not explain the problem: %v", err)
	}
}

// TestMigrateConcurrent proves the advisory lock serialises simultaneous
// runners, as happens when compose starts several replicas at once.
//
// Without the lock this races on CREATE TABLE and at least one runner fails
// with "relation already exists".
func TestMigrateConcurrent(t *testing.T) {
	ctx := context.Background()
	pool := scratchDB(t)
	log := discardLogger()

	const runners = 4

	var wg sync.WaitGroup
	errs := make([]error, runners)
	start := make(chan struct{})

	for i := range runners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = Migrate(ctx, pool, log)
		}()
	}

	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("runner %d: %v", i, err)
		}
	}

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}

	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("counting applied migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Errorf("recorded %d migrations, embedded %d — a migration ran twice",
			applied, len(migrations))
	}
}

// TestStdlibUUIDRoundTrip pins pgx's handling of the standard library's uuid
// type.
//
// uuid.UUID is [16]byte and implements none of pgx's own interfaces; pgx
// v5.10.0 resolves it through underlying-type wrapping, which is behaviour
// this project depends on but does not control. If a pgx upgrade breaks it,
// every repository breaks at once, and this test says so directly rather than
// leaving it to be diagnosed through a scan error somewhere else.
func TestStdlibUUIDRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := scratchDB(t)

	in := uuid.NewV7()

	var out uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT $1::uuid`, in).Scan(&out); err != nil {
		t.Fatalf("round-tripping a uuid through PostgreSQL: %v", err)
	}
	if in != out {
		t.Errorf("uuid changed in transit: sent %s, read back %s", in, out)
	}

	// Confirm PostgreSQL agrees it is version 7, so an encoding that happened
	// to survive a round trip byte-reversed would still be caught.
	var text string
	if err := pool.QueryRow(ctx, `SELECT ($1::uuid)::text`, in).Scan(&text); err != nil {
		t.Fatalf("reading uuid as text: %v", err)
	}
	if text != in.String() {
		t.Errorf("PostgreSQL renders the uuid as %s, Go renders it as %s", text, in.String())
	}
	if text[14] != '7' {
		t.Errorf("uuid %s is not version 7", text)
	}
}
