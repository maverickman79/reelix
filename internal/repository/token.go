package repository

import (
	"context"
	"uuid"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
)

// TokenRepository persists native API tokens.
type TokenRepository struct {
	q db.Querier
}

// NewTokenRepository returns a repository reading and writing through q, which
// may be the pool or an open transaction.
func NewTokenRepository(q db.Querier) *TokenRepository {
	return &TokenRepository{q: q}
}

// Create stores a token, assigning its id and creation time.
//
// t.TokenHash must already be the hash; this layer never sees the plaintext.
func (r *TokenRepository) Create(ctx context.Context, t *domain.APIToken) error {
	t.ID = newID()
	t.CreatedAt = now()

	const q = `
		INSERT INTO api_tokens (id, user_id, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)`

	_, err := r.q.Exec(ctx, q, t.ID, t.UserID, t.TokenHash, t.CreatedAt, t.ExpiresAt)
	return mapError("creating api token", err)
}

// UserByTokenHash returns the user a live token belongs to, or ErrNotFound.
//
// The expiry check is part of the query — `expires_at > now()` — not a
// comparison performed after loading the row. Nothing deletes expired tokens,
// so their rows remain in the table indefinitely; a lookup that matched on
// token_hash alone would happily authenticate one. Filtering in SQL means an
// expired token is indistinguishable from an unknown one at every call site,
// with no way for a caller to forget the check.
//
// PostgreSQL's now() is the transaction timestamp, so expiry is evaluated
// against the database's clock rather than the application's.
func (r *TokenRepository) UserByTokenHash(ctx context.Context, tokenHash string) (domain.User, error) {
	const q = `
		SELECT u.id, u.username, u.password_hash, u.is_admin, u.created_at, u.updated_at
		FROM api_tokens t
		JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1
		  AND t.expires_at > now()`

	return scanUser(r.q.QueryRow(ctx, q, tokenHash), "resolving api token")
}

// DeleteExpired removes tokens that are past their expiry.
//
// Authentication does not depend on this having run — UserByTokenHash rejects
// expired tokens whether or not their rows still exist. This exists only to
// keep the table from growing without bound, and is not yet scheduled; Step 5's
// job system is where a periodic sweep would belong.
func (r *TokenRepository) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := r.q.Exec(ctx, `DELETE FROM api_tokens WHERE expires_at <= now()`)
	if err != nil {
		return 0, mapError("deleting expired api tokens", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteForUser removes every token belonging to a user.
func (r *TokenRepository) DeleteForUser(ctx context.Context, userID uuid.UUID) error {
	_, err := r.q.Exec(ctx, `DELETE FROM api_tokens WHERE user_id = $1`, userID)
	return mapError("deleting api tokens for user", err)
}
