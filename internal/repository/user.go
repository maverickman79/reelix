package repository

import (
	"context"
	"uuid"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
)

// UserRepository persists accounts.
type UserRepository struct {
	q db.Querier
}

// NewUserRepository returns a repository reading and writing through q, which
// may be the pool or an open transaction.
func NewUserRepository(q db.Querier) *UserRepository {
	return &UserRepository{q: q}
}

const userColumns = `id, username, password_hash, is_admin, created_at, updated_at`

// Create inserts a user, assigning its id and timestamps.
//
// It mutates u so the caller holds the generated id without a second query.
// A duplicate username — compared case-insensitively — returns ErrConflict.
func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	u.ID = newID()
	u.CreatedAt = now()
	u.UpdatedAt = u.CreatedAt

	const q = `
		INSERT INTO users (id, username, password_hash, is_admin, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	_, err := r.q.Exec(ctx, q, u.ID, u.Username, u.PasswordHash, u.IsAdmin, u.CreatedAt, u.UpdatedAt)
	return mapError("creating user", err)
}

// GetByID returns one user, or ErrNotFound.
func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE id = $1`
	return scanUser(r.q.QueryRow(ctx, q, id), "getting user by id")
}

// GetByUsername returns one user by name, case-insensitively, or ErrNotFound.
//
// The lower() call matches the functional unique index on the table, so the
// lookup uses it rather than scanning.
func (r *UserRepository) GetByUsername(ctx context.Context, username string) (domain.User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE lower(username) = lower($1)`
	return scanUser(r.q.QueryRow(ctx, q, username), "getting user by username")
}

// Count returns the number of accounts.
//
// First-run setup uses this to decide whether an administrator still needs to
// be created.
func (r *UserRepository) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.q.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, mapError("counting users", err)
	}
	return n, nil
}

// scanUser reads one row in userColumns order.
func scanUser(row interface{ Scan(...any) error }, op string) (domain.User, error) {
	var u domain.User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return domain.User{}, mapError(op, err)
	}
	return u, nil
}
