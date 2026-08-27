package repository

import (
	"context"
	"uuid"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
)

// PlaybackRepository persists how far through an item a user is.
type PlaybackRepository struct {
	q db.Querier
}

// NewPlaybackRepository returns a repository reading and writing through q.
func NewPlaybackRepository(q db.Querier) *PlaybackRepository {
	return &PlaybackRepository{q: q}
}

const playbackColumns = `user_id, media_item_id, position_seconds, raw_position_seconds,
                         played, play_count, last_played_at, created_at, updated_at`

// Report records a client's progress through an item.
//
// playCountDelta is 0 or 1: the caller decides whether this report completes a
// viewing, and the increment is applied inside the statement so two devices
// reporting at once cannot both read the same count and write it back.
//
// Played is sticky in SQL for the same reason — `played OR EXCLUDED.played`
// rather than a value the caller computed from a row it read earlier.
//
// A report that changes nothing writes nothing. A paused client keeps sending
// the same position every few seconds; those become no-ops here rather than
// rows rewritten with identical contents.
func (r *PlaybackRepository) Report(ctx context.Context, s domain.PlaybackState, playCountDelta int) error {
	const q = `
		INSERT INTO playback_state (user_id, media_item_id, position_seconds,
		                            raw_position_seconds, played, play_count,
		                            last_played_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		ON CONFLICT (user_id, media_item_id) DO UPDATE SET
			position_seconds     = EXCLUDED.position_seconds,
			raw_position_seconds = EXCLUDED.raw_position_seconds,
			played               = playback_state.played OR EXCLUDED.played,
			play_count           = playback_state.play_count + $6,
			last_played_at       = EXCLUDED.last_played_at,
			updated_at           = EXCLUDED.updated_at
		WHERE playback_state.raw_position_seconds IS DISTINCT FROM EXCLUDED.raw_position_seconds
		   OR playback_state.position_seconds IS DISTINCT FROM EXCLUDED.position_seconds
		   OR (EXCLUDED.played AND NOT playback_state.played)
		   OR $6 <> 0`

	ts := now()
	_, err := r.q.Exec(ctx, q, s.UserID, s.MediaItemID, s.PositionSeconds,
		s.RawPositionSeconds, s.Played, playCountDelta, s.LastPlayedAt, ts)

	return mapError("recording playback progress", err)
}

// Get returns one user's state for one item, or ErrNotFound.
func (r *PlaybackRepository) Get(ctx context.Context, userID, itemID uuid.UUID) (domain.PlaybackState, error) {
	const q = `SELECT ` + playbackColumns + `
		FROM playback_state WHERE user_id = $1 AND media_item_id = $2`

	return scanPlaybackState(r.q.QueryRow(ctx, q, userID, itemID), "getting playback state")
}

// scanPlaybackState reads one row in playbackColumns order.
func scanPlaybackState(row interface{ Scan(...any) error }, op string) (domain.PlaybackState, error) {
	var s domain.PlaybackState

	err := row.Scan(&s.UserID, &s.MediaItemID, &s.PositionSeconds, &s.RawPositionSeconds,
		&s.Played, &s.PlayCount, &s.LastPlayedAt, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return domain.PlaybackState{}, mapError(op, err)
	}
	return s, nil
}
