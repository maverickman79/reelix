package repository

import (
	"context"
	"time"

	"uuid"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
)

// IdentityRepository persists which real film a media item is.
type IdentityRepository struct {
	q db.Querier
}

// NewIdentityRepository returns a repository reading and writing through q.
func NewIdentityRepository(q db.Querier) *IdentityRepository {
	return &IdentityRepository{q: q}
}

// Ensure creates a pending identity row for an item that has none.
//
// Called by the scanner when it discovers an item, so that a later identify
// pass has something to find. Doing nothing on conflict is what makes a
// re-scan safe: an item already matched, or already resolved by hand, must not
// be dragged back to pending because its file was seen again.
func (r *IdentityRepository) Ensure(ctx context.Context, itemID uuid.UUID) error {
	const q = `
		INSERT INTO media_item_identity (media_item_id, status, created_at, updated_at)
		VALUES ($1, 'pending', $2, $2)
		ON CONFLICT (media_item_id) DO NOTHING`

	ts := now()
	_, err := r.q.Exec(ctx, q, itemID, ts)
	return mapError("ensuring identity row", err)
}

// Get returns one item's identity, external ids included.
func (r *IdentityRepository) Get(ctx context.Context, itemID uuid.UUID) (domain.Identity, error) {
	const q = `
		SELECT media_item_id, status, provider, confidence, matched_via, reason,
		       attempted_at, created_at, updated_at
		  FROM media_item_identity
		 WHERE media_item_id = $1`

	identity, err := scanIdentity(r.q.QueryRow(ctx, q, itemID), "loading identity")
	if err != nil {
		return domain.Identity{}, err
	}

	identity.ExternalIDs, err = r.externalIDs(ctx, itemID)
	if err != nil {
		return domain.Identity{}, err
	}
	return identity, nil
}

// Pending returns up to limit items awaiting identification, oldest first.
//
// Only 'pending'. A matched item is settled, an unmatched one was decided
// against and must not be silently retried into a guess, and a manual one is
// somebody's explicit instruction. Re-attempting any of those is a deliberate
// act — Reset — and not a side effect of running the pass again.
func (r *IdentityRepository) Pending(ctx context.Context, libraryID uuid.UUID, limit int) ([]domain.MediaItem, error) {
	const q = `
		SELECT i.id, i.library_id, i.kind, i.title, i.year, i.source_path,
		       i.created_at, i.updated_at
		  FROM media_items i
		  JOIN media_item_identity d ON d.media_item_id = i.id
		 WHERE d.status = 'pending'
		   AND ($1::uuid IS NULL OR i.library_id = $1)
		 ORDER BY i.created_at
		 LIMIT $2`

	// A nil library id means "every library". Passing the zero UUID straight
	// through would filter for a library that cannot exist and return nothing.
	var libraryFilter *uuid.UUID
	if libraryID != (uuid.UUID{}) {
		libraryFilter = &libraryID
	}

	rows, err := r.q.Query(ctx, q, libraryFilter, limit)
	if err != nil {
		return nil, mapError("listing pending items", err)
	}
	defer rows.Close()

	var items []domain.MediaItem
	for rows.Next() {
		var m domain.MediaItem
		if err := rows.Scan(&m.ID, &m.LibraryID, &m.Kind, &m.Title, &m.Year,
			&m.SourcePath, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, mapError("scanning pending item", err)
		}
		items = append(items, m)
	}
	return items, mapError("listing pending items", rows.Err())
}

// RecordMatch stores a successful identification and its external ids.
//
// The two writes are one statement pair inside the caller's transaction: an
// identity claiming a match while its ids are missing would be trusted by the
// importer and would resolve to nothing.
//
// A manual identity is never overwritten. The WHERE clause enforces that in
// SQL rather than in a read-then-write the caller performs, so a pass running
// concurrently with somebody correcting a film by hand cannot lose their
// correction.
func (r *IdentityRepository) RecordMatch(ctx context.Context, itemID uuid.UUID, provider, confidence, matchedVia string, ids map[string]string) error {
	const q = `
		UPDATE media_item_identity
		   SET status = 'matched', provider = $2, confidence = $3,
		       matched_via = $4, reason = NULL, attempted_at = $5, updated_at = $5
		 WHERE media_item_id = $1
		   AND status <> 'manual'`

	ts := now()
	tag, err := r.q.Exec(ctx, q, itemID, provider, confidence, matchedVia, ts)
	if err != nil {
		return mapError("recording identity match", err)
	}

	// The guard above can refuse the row, and if it did, the ids must NOT be
	// written either. Skipping this check leaves an item marked 'manual' while
	// carrying the pass's ids — the status says a human decided and the ids
	// say otherwise, and FindByExternalID believes the ids. That resolves an
	// imported watch history onto the wrong film, which is the exact failure
	// the manual state exists to prevent.
	//
	// Found by fault injection: removing the WHERE clause left the suite green
	// because nothing exercised this write against a manual row.
	if tag.RowsAffected() == 0 {
		return nil
	}
	return r.replaceExternalIDs(ctx, itemID, ids, ts)
}

// RecordUnmatched stores a decline and the reason for it.
//
// The reason is the whole value of this row. An unmatched item with no
// explanation tells an operator only that something did not work, which is
// what makes people re-run passes hoping for a different answer.
func (r *IdentityRepository) RecordUnmatched(ctx context.Context, itemID uuid.UUID, reason string) error {
	const q = `
		UPDATE media_item_identity
		   SET status = 'unmatched', provider = NULL, confidence = NULL,
		       matched_via = NULL, reason = $2, attempted_at = $3, updated_at = $3
		 WHERE media_item_id = $1
		   AND status <> 'manual'`

	_, err := r.q.Exec(ctx, q, itemID, reason, now())
	return mapError("recording unmatched item", err)
}

// SetManual records a human's decision, which no pass may overwrite.
func (r *IdentityRepository) SetManual(ctx context.Context, itemID uuid.UUID, ids map[string]string) error {
	const q = `
		UPDATE media_item_identity
		   SET status = 'manual', provider = NULL, confidence = NULL,
		       matched_via = NULL, reason = NULL, attempted_at = $2, updated_at = $2
		 WHERE media_item_id = $1`

	ts := now()
	if _, err := r.q.Exec(ctx, q, itemID, ts); err != nil {
		return mapError("setting manual identity", err)
	}
	return r.replaceExternalIDs(ctx, itemID, ids, ts)
}

// Reset returns an item to pending and drops its external ids.
//
// The deliberate act that a re-run is not. It is how an operator says "try
// this one again" about an item the pass has already decided, including one
// they had previously fixed by hand.
func (r *IdentityRepository) Reset(ctx context.Context, itemID uuid.UUID) error {
	const q = `
		UPDATE media_item_identity
		   SET status = 'pending', provider = NULL, confidence = NULL,
		       matched_via = NULL, reason = NULL, attempted_at = NULL, updated_at = $2
		 WHERE media_item_id = $1`

	ts := now()
	if _, err := r.q.Exec(ctx, q, itemID, ts); err != nil {
		return mapError("resetting identity", err)
	}
	return r.replaceExternalIDs(ctx, itemID, nil, ts)
}

// FindByExternalID resolves a provider's id back to a local item.
//
// This is the watch-history importer's query, and the reason
// media_item_external_ids carries an index on (provider, external_id): an
// import walks thousands of exported rows, and without it each one is a
// sequential scan.
func (r *IdentityRepository) FindByExternalID(ctx context.Context, provider, externalID string) (uuid.UUID, error) {
	const q = `
		SELECT media_item_id
		  FROM media_item_external_ids
		 WHERE provider = $1 AND external_id = $2`

	var id uuid.UUID
	if err := r.q.QueryRow(ctx, q, provider, externalID).Scan(&id); err != nil {
		return uuid.UUID{}, mapError("finding item by external id", err)
	}
	return id, nil
}

// replaceExternalIDs makes the stored id set exactly ids.
//
// Delete-then-insert rather than an upsert, because an id that has been
// REMOVED must disappear. A correction that replaces a wrong TMDB id would
// otherwise leave the wrong one in place beside the right one, and
// FindByExternalID would still resolve it.
func (r *IdentityRepository) replaceExternalIDs(ctx context.Context, itemID uuid.UUID, ids map[string]string, ts time.Time) error {
	if _, err := r.q.Exec(ctx,
		`DELETE FROM media_item_external_ids WHERE media_item_id = $1`, itemID); err != nil {
		return mapError("clearing external ids", err)
	}

	for provider, value := range ids {
		if value == "" {
			continue
		}
		if _, err := r.q.Exec(ctx,
			`INSERT INTO media_item_external_ids (media_item_id, provider, external_id, created_at)
			 VALUES ($1, $2, $3, $4)`, itemID, provider, value, ts); err != nil {
			return mapError("writing external id", err)
		}
	}
	return nil
}

func (r *IdentityRepository) externalIDs(ctx context.Context, itemID uuid.UUID) (map[string]string, error) {
	rows, err := r.q.Query(ctx,
		`SELECT provider, external_id FROM media_item_external_ids WHERE media_item_id = $1`, itemID)
	if err != nil {
		return nil, mapError("loading external ids", err)
	}
	defer rows.Close()

	ids := map[string]string{}
	for rows.Next() {
		var provider, value string
		if err := rows.Scan(&provider, &value); err != nil {
			return nil, mapError("scanning external id", err)
		}
		ids[provider] = value
	}
	return ids, mapError("loading external ids", rows.Err())
}

// ExternalIDsFor loads the ids for many items at once.
//
// The browse and detail paths render whole pages of items, and asking per item
// would put a query per row behind every listing.
func (r *IdentityRepository) ExternalIDsFor(ctx context.Context, itemIDs []uuid.UUID) (map[uuid.UUID]map[string]string, error) {
	out := map[uuid.UUID]map[string]string{}
	if len(itemIDs) == 0 {
		return out, nil
	}

	rows, err := r.q.Query(ctx,
		`SELECT media_item_id, provider, external_id
		   FROM media_item_external_ids
		  WHERE media_item_id = ANY($1)`, itemIDs)
	if err != nil {
		return nil, mapError("loading external ids", err)
	}
	defer rows.Close()

	for rows.Next() {
		var itemID uuid.UUID
		var provider, value string
		if err := rows.Scan(&itemID, &provider, &value); err != nil {
			return nil, mapError("scanning external id", err)
		}
		if out[itemID] == nil {
			out[itemID] = map[string]string{}
		}
		out[itemID][provider] = value
	}
	return out, mapError("loading external ids", rows.Err())
}

func scanIdentity(row interface{ Scan(...any) error }, op string) (domain.Identity, error) {
	var i domain.Identity
	err := row.Scan(&i.MediaItemID, &i.Status, &i.Provider, &i.Confidence,
		&i.MatchedVia, &i.Reason, &i.AttemptedAt, &i.CreatedAt, &i.UpdatedAt)
	if err != nil {
		return domain.Identity{}, mapError(op, err)
	}
	return i, nil
}
