// Package repository persists domain models in PostgreSQL.
//
// Repositories are concrete types, not interfaces. The services that will
// consume them do not exist yet, and in Go an interface belongs to its
// consumer — Step 3 declares the method set it actually needs. Defining
// interfaces here now would be guessing.
//
// Every repository takes a db.Querier, which both the pool and a transaction
// satisfy, so a caller can compose several repository calls into one
// transaction without a parallel set of methods.
package repository

import (
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned when a lookup matches no row. Callers should test it
// with errors.Is rather than comparing pgx.ErrNoRows, which is an
// implementation detail of this package.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a write violates a unique constraint — a
// duplicate username, or a second library path identical to an existing one.
var ErrConflict = errors.New("conflict")

// ErrInvalidState is returned when a caller asks for a transition that does
// not exist, such as finishing a job into a non-terminal state.
var ErrInvalidState = errors.New("invalid state transition")

// uniqueViolation is PostgreSQL's SQLSTATE for a unique constraint breach.
const uniqueViolation = "23505"

// mapError translates driver errors into this package's sentinels.
//
// Callers should not have to import pgx to tell "no such user" from "the
// database is unreachable". Anything unrecognised is returned unchanged, with
// its original context intact.
func mapError(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return fmt.Errorf("%s: %w: %s", op, ErrConflict, pgErr.ConstraintName)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// newID mints an entity identifier.
//
// UUIDv7 rather than v4: v7 embeds a timestamp in its high bits, so ids sort
// by creation time and inserts land at the right-hand edge of the primary key
// index instead of scattering across it. A library scan inserts thousands of
// rows in a burst, which is exactly the case that punishes random ids.
func newID() uuid.UUID { return uuid.NewV7() }

// now returns the timestamp written to created_at and updated_at.
//
// UTC because the column is timestamptz and the server's local zone is not
// interesting; truncating to microseconds matches PostgreSQL's own resolution,
// so a value read back equals the value written.
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// isNoRows reports whether a query matched nothing.
//
// Some callers treat an absent row as a state rather than a failure — an item
// with no metadata has simply never been fetched — and they need to ask
// without first mapping the error into ErrNotFound and back.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
