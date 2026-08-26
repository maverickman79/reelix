package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maverickman79/reelix/internal/auth"
	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/repository"
)

// setupLockKey serialises first-run setup across processes and requests.
//
// Taken with pg_advisory_xact_lock, so it releases at commit or rollback with
// no unlock path to get wrong. Distinct from the migration lock.
const setupLockKey int64 = 0x7265656C69780002

// AuthService owns first-run setup, login, and token resolution.
type AuthService struct {
	pool *pgxpool.Pool
}

// NewAuthService returns a service backed by pool.
//
// It takes the pool rather than a Querier because it owns transaction
// boundaries: setup and login are each one transaction.
func NewAuthService(pool *pgxpool.Pool) *AuthService {
	return &AuthService{pool: pool}
}

// NeedsSetup reports whether the server has no users yet.
func (s *AuthService) NeedsSetup(ctx context.Context) (bool, error) {
	n, err := repository.NewUserRepository(s.pool).Count(ctx)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// CreateFirstAdmin creates the initial administrator.
//
// It succeeds exactly once. Because the endpoint behind it is necessarily
// unauthenticated — there is no one to authenticate as yet — the "exactly
// once" guarantee is what stops an attacker who reaches a freshly started
// server from racing the operator and claiming the account. The check and the
// insert therefore share a transaction holding an advisory lock, rather than
// being a count followed by an unprotected insert.
func (s *AuthService) CreateFirstAdmin(ctx context.Context, username, password string) (domain.User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return domain.User{}, InvalidArgumentf("username must not be empty")
	}
	if strings.ContainsAny(username, " \t\r\n") {
		return domain.User{}, InvalidArgumentf("username must not contain whitespace")
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		if errors.Is(err, auth.ErrPasswordTooShort) {
			return domain.User{}, InvalidArgumentf("%s", err.Error())
		}
		return domain.User{}, fmt.Errorf("hashing password: %w", err)
	}

	user := domain.User{Username: username, PasswordHash: hash, IsAdmin: true}

	err = db.InTx(ctx, s.pool, func(q db.Querier) error {
		if _, err := q.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", setupLockKey); err != nil {
			return fmt.Errorf("locking for setup: %w", err)
		}

		users := repository.NewUserRepository(q)

		n, err := users.Count(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrAlreadySetUp
		}
		return users.Create(ctx, &user)
	})
	if err != nil {
		return domain.User{}, err
	}
	return user, nil
}

// Login verifies credentials and issues a token.
//
// An unknown username and a wrong password both return ErrInvalidCredentials.
// The unknown-user path still performs a hash comparison against a dummy hash
// so that the two cases take similar time; without it, response latency tells
// an attacker which usernames exist.
func (s *AuthService) Login(ctx context.Context, username, password string) (domain.User, auth.Token, error) {
	users := repository.NewUserRepository(s.pool)

	user, err := users.GetByUsername(ctx, username)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		auth.DummyVerify(password)
		return domain.User{}, auth.Token{}, ErrInvalidCredentials
	case err != nil:
		return domain.User{}, auth.Token{}, err
	}

	if err := auth.VerifyPassword(user.PasswordHash, password); err != nil {
		if errors.Is(err, auth.ErrInvalidHash) {
			// A corrupt stored hash is a server fault, not a failed login, and
			// must not be reported as bad credentials.
			return domain.User{}, auth.Token{}, fmt.Errorf("user %s: %w", user.ID, err)
		}
		return domain.User{}, auth.Token{}, ErrInvalidCredentials
	}

	token, err := auth.NewToken(time.Now().UTC())
	if err != nil {
		return domain.User{}, auth.Token{}, err
	}

	record := domain.APIToken{
		UserID:    user.ID,
		TokenHash: token.Hash,
		ExpiresAt: token.ExpiresAt,
	}
	if err := repository.NewTokenRepository(s.pool).Create(ctx, &record); err != nil {
		return domain.User{}, auth.Token{}, err
	}

	return user, token, nil
}

// Authenticate resolves a bearer token to its user.
//
// Expiry is enforced inside the repository's query, so a token whose row still
// exists but whose expires_at has passed is rejected here exactly as an
// unknown token is.
func (s *AuthService) Authenticate(ctx context.Context, plaintext string) (domain.User, error) {
	if plaintext == "" {
		return domain.User{}, ErrUnauthenticated
	}

	user, err := repository.NewTokenRepository(s.pool).
		UserByTokenHash(ctx, auth.HashToken(plaintext))
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return domain.User{}, ErrUnauthenticated
	case err != nil:
		return domain.User{}, err
	}
	return user, nil
}
