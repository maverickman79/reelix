package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maverickman79/reelix/internal/auth"
	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/repository"
)

// ClientInfo describes the device asking for a session.
//
// It comes from a client's authorization header, so every field is untrusted
// input and may be empty.
type ClientInfo struct {
	Client     string
	Device     string
	DeviceID   string
	Version    string
	RemoteAddr string
}

// SessionService issues and resolves client sessions.
//
// This is the native counterpart to the compatibility layer's authentication.
// It is deliberately separate from AuthService: the constitution requires the
// native API's scheme and the Jellyfin-compatible one to stay independent, so
// they share no tokens, no table, and no code path beyond password
// verification itself.
type SessionService struct {
	pool *pgxpool.Pool
}

// NewSessionService returns a service backed by pool.
func NewSessionService(pool *pgxpool.Pool) *SessionService {
	return &SessionService{pool: pool}
}

// ServerSettings returns the server's persistent identity.
func (s *SessionService) ServerSettings(ctx context.Context) (domain.ServerSettings, error) {
	return repository.NewSessionRepository(s.pool).ServerSettings(ctx)
}

// sessionTokenBytes is the entropy behind a session token.
//
// Sixteen bytes renders as the 32-character hexadecimal string Jellyfin
// clients expect to receive and echo back.
const sessionTokenBytes = 16

// Authenticate verifies credentials and opens a session for a device.
//
// Returns the plaintext token exactly once; only its hash is stored.
//
// As with the native login, an unknown username and a wrong password are
// indistinguishable to the caller and cost similar time.
func (s *SessionService) Authenticate(ctx context.Context, username, password string, info ClientInfo) (domain.Session, domain.User, string, error) {
	users := repository.NewUserRepository(s.pool)

	user, err := users.GetByUsername(ctx, username)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		auth.DummyVerify(password)
		return domain.Session{}, domain.User{}, "", ErrInvalidCredentials
	case err != nil:
		return domain.Session{}, domain.User{}, "", err
	}

	if err := auth.VerifyPassword(user.PasswordHash, password); err != nil {
		if errors.Is(err, auth.ErrInvalidHash) {
			return domain.Session{}, domain.User{}, "", fmt.Errorf("user %s: %w", user.ID, err)
		}
		return domain.Session{}, domain.User{}, "", ErrInvalidCredentials
	}

	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return domain.Session{}, domain.User{}, "", fmt.Errorf("generating session token: %w", err)
	}
	token := hex.EncodeToString(raw)

	// A client that reports no device id still needs a session. Falling back to
	// the token's own identity keeps the (user_id, device_id) uniqueness
	// meaningful instead of collapsing every anonymous device onto one row.
	deviceID := info.DeviceID
	if deviceID == "" {
		deviceID = "unknown-" + token[:8]
	}

	session := domain.Session{
		UserID:        user.ID,
		TokenHash:     auth.HashToken(token),
		DeviceID:      deviceID,
		DeviceName:    info.Device,
		Client:        info.Client,
		ClientVersion: info.Version,
	}

	if err := repository.NewSessionRepository(s.pool).Upsert(ctx, &session); err != nil {
		return domain.Session{}, domain.User{}, "", err
	}

	return session, user, token, nil
}

// Resolve returns the session and user behind a token.
func (s *SessionService) Resolve(ctx context.Context, token string) (domain.Session, domain.User, error) {
	if token == "" {
		return domain.Session{}, domain.User{}, ErrUnauthenticated
	}

	session, user, err := repository.NewSessionRepository(s.pool).
		ByTokenHash(ctx, auth.HashToken(token))
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return domain.Session{}, domain.User{}, ErrUnauthenticated
	case err != nil:
		return domain.Session{}, domain.User{}, err
	}
	return session, user, nil
}

// SetCapabilities records what a client reported it can do.
func (s *SessionService) SetCapabilities(ctx context.Context, id uuid.UUID, caps domain.Session) error {
	return repository.NewSessionRepository(s.pool).UpdateCapabilities(ctx, id, caps)
}
