package repository

import (
	"context"
	"uuid"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
)

// SessionRepository persists client sessions and the server's identity.
type SessionRepository struct {
	q db.Querier
}

// NewSessionRepository returns a repository reading and writing through q,
// which may be the pool or an open transaction.
func NewSessionRepository(q db.Querier) *SessionRepository {
	return &SessionRepository{q: q}
}

const sessionColumns = `id, user_id, token_hash, device_id, device_name, client,
                        client_version, playable_media_types, supported_commands,
                        supports_media_control, supports_persistent_identifier,
                        created_at, last_activity_at`

// ServerSettings returns the server's identity, or ErrNotFound.
//
// The row is seeded by migration 0004, so a missing one means the schema is
// older than this binary expects rather than a first-run condition.
func (r *SessionRepository) ServerSettings(ctx context.Context) (domain.ServerSettings, error) {
	const q = `SELECT server_id, server_name FROM server_settings WHERE id = 1`

	var s domain.ServerSettings
	if err := r.q.QueryRow(ctx, q).Scan(&s.ServerID, &s.ServerName); err != nil {
		return domain.ServerSettings{}, mapError("reading server settings", err)
	}
	return s, nil
}

// Upsert creates a session, replacing any existing one for the same user and
// device.
//
// A television client re-authenticates on every app start; without the
// replacement this table would grow a row per launch forever. Replacing also
// means the previous token for that device stops working, which is the
// behaviour a user expects from "log in again".
func (r *SessionRepository) Upsert(ctx context.Context, s *domain.Session) error {
	if s.ID == (uuid.UUID{}) {
		s.ID = newID()
	}
	ts := now()
	s.CreatedAt = ts
	s.LastActivityAt = ts

	const q = `
		INSERT INTO sessions (id, user_id, token_hash, device_id, device_name,
		                      client, client_version, created_at, last_activity_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (user_id, device_id) DO UPDATE SET
			token_hash       = EXCLUDED.token_hash,
			device_name      = EXCLUDED.device_name,
			client           = EXCLUDED.client,
			client_version   = EXCLUDED.client_version,
			last_activity_at = EXCLUDED.last_activity_at
		RETURNING id, created_at`

	err := r.q.QueryRow(ctx, q, s.ID, s.UserID, s.TokenHash, s.DeviceID, s.DeviceName,
		s.Client, s.ClientVersion, s.CreatedAt, s.LastActivityAt).
		Scan(&s.ID, &s.CreatedAt)

	return mapError("creating session", err)
}

// ByTokenHash returns a session and its user, or ErrNotFound.
//
// Sessions do not expire. Jellyfin's own tokens do not either, and a client
// that has to re-authenticate unprompted after a month reads as a broken
// server rather than a security feature.
func (r *SessionRepository) ByTokenHash(ctx context.Context, tokenHash string) (domain.Session, domain.User, error) {
	const q = `
		SELECT s.id, s.user_id, s.token_hash, s.device_id, s.device_name, s.client,
		       s.client_version, s.playable_media_types, s.supported_commands,
		       s.supports_media_control, s.supports_persistent_identifier,
		       s.created_at, s.last_activity_at,
		       u.id, u.username, u.password_hash, u.is_admin, u.created_at, u.updated_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1`

	var s domain.Session
	var u domain.User

	err := r.q.QueryRow(ctx, q, tokenHash).Scan(
		&s.ID, &s.UserID, &s.TokenHash, &s.DeviceID, &s.DeviceName, &s.Client,
		&s.ClientVersion, &s.PlayableMediaTypes, &s.SupportedCommands,
		&s.SupportsMediaControl, &s.SupportsPersistentIdentifier,
		&s.CreatedAt, &s.LastActivityAt,
		&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return domain.Session{}, domain.User{}, mapError("resolving session token", err)
	}
	return s, u, nil
}

// UpdateCapabilities records what a client reported it can do.
func (r *SessionRepository) UpdateCapabilities(ctx context.Context, id uuid.UUID, s domain.Session) error {
	const q = `
		UPDATE sessions
		SET playable_media_types           = $2,
		    supported_commands             = $3,
		    supports_media_control         = $4,
		    supports_persistent_identifier = $5,
		    last_activity_at               = $6
		WHERE id = $1`

	tag, err := r.q.Exec(ctx, q, id, s.PlayableMediaTypes, s.SupportedCommands,
		s.SupportsMediaControl, s.SupportsPersistentIdentifier, now())
	if err != nil {
		return mapError("updating session capabilities", err)
	}
	if tag.RowsAffected() == 0 {
		return mapError("updating session capabilities", ErrNotFound)
	}
	return nil
}

// Touch records activity on a session.
//
// Failures are the caller's to ignore: a stale last_activity_at costs an
// operator an inaccurate timestamp, not a broken request.
func (r *SessionRepository) Touch(ctx context.Context, id uuid.UUID) error {
	_, err := r.q.Exec(ctx, `UPDATE sessions SET last_activity_at = $2 WHERE id = $1`, id, now())
	return mapError("touching session", err)
}
