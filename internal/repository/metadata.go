package repository

import (
	"context"
	"time"
	"uuid"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
)

// MetadataRepository persists the managed metadata fields and their
// provenance.
type MetadataRepository struct {
	q db.Querier
}

// NewMetadataRepository returns a repository reading and writing through q.
func NewMetadataRepository(q db.Querier) *MetadataRepository {
	return &MetadataRepository{q: q}
}

// Get returns one item's metadata, genres and provenance.
//
// An item with no metadata row yields a zero ItemMetadata rather than
// ErrNotFound: never having been fetched is an ordinary state, not a missing
// record, and every caller would otherwise have to translate the error back
// into that state.
func (r *MetadataRepository) Get(ctx context.Context, itemID uuid.UUID) (domain.ItemMetadata, error) {
	out := domain.ItemMetadata{
		MediaItemID: itemID,
		Provenance:  map[string]domain.FieldProvenance{},
	}

	const q = `
		SELECT overview, community_rating, official_rating, premiere_date
		  FROM media_item_metadata
		 WHERE media_item_id = $1`

	err := r.q.QueryRow(ctx, q, itemID).Scan(
		&out.Overview, &out.CommunityRating, &out.OfficialRating, &out.PremiereDate)
	if err != nil && !isNoRows(err) {
		return domain.ItemMetadata{}, mapError("loading metadata", err)
	}

	if out.Genres, err = r.genres(ctx, itemID); err != nil {
		return domain.ItemMetadata{}, err
	}
	if out.Provenance, err = r.provenance(ctx, itemID); err != nil {
		return domain.ItemMetadata{}, err
	}

	images, err := r.ImagesFor(ctx, []uuid.UUID{itemID})
	if err != nil {
		return domain.ItemMetadata{}, err
	}
	out.Images = images[itemID]

	return out, nil
}

// WriteField stores one field's value and provenance, unless it is locked.
//
// ONE GUARD, DELIBERATELY. An earlier version checked the lock in Go and again
// in the UPDATE's WHERE clause, which read as defence in depth and was not:
// two guards on one outcome mean removing either alone changes nothing
// observable, so neither can be tested. Fault injection removed each in turn
// and the suite stayed green both times. The redundancy did not make the code
// safer, it made the safety unverifiable.
//
// The single guard is claimField: an atomic conditional write on the
// provenance row that succeeds only when the field is unlocked. It is also the
// serialisation point, which is what a read-then-write in Go could never be —
// that loses the race against somebody locking between the read and the write,
// and "silently overwriting a locked field" is exactly what the constitution
// forbids.
//
// Returns whether the write happened. A refused write is not an error; it is
// the guard working, and the caller counts it.
func (r *MetadataRepository) WriteField(
	ctx context.Context, itemID uuid.UUID, field, source string, value any,
) (bool, error) {
	column, ok := metadataColumns[field]
	if !ok {
		return false, mapError("writing metadata field", &unknownFieldError{field: field})
	}

	ts := now()

	// The row has to exist before a field can be written into it.
	if _, err := r.q.Exec(ctx, `
		INSERT INTO media_item_metadata (media_item_id, created_at, updated_at)
		VALUES ($1, $2, $2)
		ON CONFLICT (media_item_id) DO NOTHING`, itemID, ts); err != nil {
		return false, mapError("creating metadata row", err)
	}

	claimed, err := r.claimField(ctx, itemID, field, source, ts)
	if err != nil || !claimed {
		return false, err
	}

	// Per-field UPDATE rather than a whole-row write: fields lock
	// individually, so writing the row would carry unlocked neighbours along
	// with the field being written.
	if _, err := r.q.Exec(ctx,
		`UPDATE media_item_metadata SET `+column+` = $2, updated_at = $3 WHERE media_item_id = $1`,
		itemID, value, ts); err != nil {
		return false, mapError("writing metadata field", err)
	}
	return true, nil
}

// WriteGenres replaces the genre list, unless it is locked.
//
// The list is replaced rather than merged, and locks as a whole. A refresh
// returning a different list means the provider changed its mind about the
// film, not that both lists are true.
//
// Guarded by the same claimField as a scalar field, and for the same reason: a
// lock read in Go and acted on afterwards loses the race against somebody
// taking the lock in between.
func (r *MetadataRepository) WriteGenres(
	ctx context.Context, itemID uuid.UUID, source string, genres []string,
) (bool, error) {
	ts := now()

	claimed, err := r.claimField(ctx, itemID, domain.FieldGenres, source, ts)
	if err != nil || !claimed {
		return false, err
	}

	if _, err := r.q.Exec(ctx,
		`DELETE FROM media_item_genres WHERE media_item_id = $1`, itemID); err != nil {
		return false, mapError("clearing genres", err)
	}

	for i, genre := range genres {
		if genre == "" {
			continue
		}
		if _, err := r.q.Exec(ctx, `
			INSERT INTO media_item_genres (media_item_id, genre, ordinal, created_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (media_item_id, genre) DO NOTHING`,
			itemID, genre, i, ts); err != nil {
			return false, mapError("writing genre", err)
		}
	}
	return true, nil
}

// claimField stamps a field's provenance if and only if it is unlocked.
//
// THE ONE PLACE A LOCK IS ENFORCED. Every write goes through it, so there is
// exactly one path to test and exactly one line to remove to make the tests
// fail. The conditional ON CONFLICT is what makes it atomic: a caller cannot
// observe "unlocked" and then write, because observing and claiming are the
// same statement.
func (r *MetadataRepository) claimField(
	ctx context.Context, itemID uuid.UUID, field, source string, ts time.Time,
) (bool, error) {
	tag, err := r.q.Exec(ctx, `
		INSERT INTO media_item_field_provenance (media_item_id, field, source, locked, updated_at)
		VALUES ($1, $2, $3, false, $4)
		ON CONFLICT (media_item_id, field) DO UPDATE SET
			source = EXCLUDED.source, updated_at = EXCLUDED.updated_at
		 WHERE NOT media_item_field_provenance.locked`,
		itemID, field, source, ts)
	if err != nil {
		return false, mapError("claiming metadata field", err)
	}
	return tag.RowsAffected() > 0, nil
}

// SetLocked pins or unpins one field.
func (r *MetadataRepository) SetLocked(ctx context.Context, itemID uuid.UUID, field string, locked bool) error {
	ts := now()
	_, err := r.q.Exec(ctx, `
		INSERT INTO media_item_field_provenance (media_item_id, field, source, locked, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (media_item_id, field) DO UPDATE SET
			locked = EXCLUDED.locked, updated_at = EXCLUDED.updated_at`,
		itemID, field, domain.MetadataSourceManual, locked, ts)
	return mapError("setting field lock", err)
}

// ItemsNeedingMetadata returns identified items for the refresh pass.
//
// onlyMissing selects items that have never been fetched, which is the default
// because a full re-fetch is one provider request per film in the library. On
// a large library that is a cost somebody should choose rather than discover.
func (r *MetadataRepository) ItemsNeedingMetadata(
	ctx context.Context, libraryID uuid.UUID, onlyMissing bool, limit int,
) ([]domain.MediaItem, error) {
	// Only identified items: a metadata fetch needs a provider id, and an
	// unidentified film has none. A manual identity counts — somebody said
	// which film it is, and that is exactly as usable as a matched one.
	q := `
		SELECT i.id, i.library_id, i.kind, i.title, i.year, i.source_path,
		       i.created_at, i.updated_at
		  FROM media_items i
		  JOIN media_item_identity d ON d.media_item_id = i.id
		 WHERE d.status IN ('matched', 'manual')
		   AND ($1::uuid IS NULL OR i.library_id = $1)`

	if onlyMissing {
		// "Never fetched" is now two questions, because fields and artwork
		// arrive in the same pass and can be missing independently: an item
		// identified before artwork existed has fields and no images, and must
		// still be selected.
		//
		// An item is done when it has a metadata row AND a row for every image
		// type — INCLUDING the negatives, since "the provider has no logo" is
		// a stored answer rather than an absent one. That is what makes
		// re-running the pass fetch nothing.
		//
		// A row whose FILE has gone cannot be seen from here. The reconcile
		// sweep deletes those before this query runs, which turns a wiped
		// cache directory back into the ordinary "no row" case rather than
		// needing a predicate that can stat.
		q += `
		   AND (NOT EXISTS (SELECT 1 FROM media_item_metadata m
		                     WHERE m.media_item_id = i.id)
		        OR (SELECT count(*) FROM media_item_images g
		             WHERE g.media_item_id = i.id) < $3)`
	}
	q += `
		 ORDER BY i.created_at
		 LIMIT $2`

	var libraryFilter *uuid.UUID
	if libraryID != (uuid.UUID{}) {
		libraryFilter = &libraryID
	}

	args := []any{libraryFilter, limit}
	if onlyMissing {
		args = append(args, len(domain.ImageTypes))
	}

	rows, err := r.q.Query(ctx, q, args...)
	if err != nil {
		return nil, mapError("listing items needing metadata", err)
	}
	defer rows.Close()

	var items []domain.MediaItem
	for rows.Next() {
		var m domain.MediaItem
		if err := rows.Scan(&m.ID, &m.LibraryID, &m.Kind, &m.Title, &m.Year,
			&m.SourcePath, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, mapError("scanning item", err)
		}
		items = append(items, m)
	}
	return items, mapError("listing items needing metadata", rows.Err())
}

// MetadataFor loads metadata for many items at once, for a listing.
func (r *MetadataRepository) MetadataFor(
	ctx context.Context, itemIDs []uuid.UUID,
) (map[uuid.UUID]domain.ItemMetadata, error) {
	out := map[uuid.UUID]domain.ItemMetadata{}
	if len(itemIDs) == 0 {
		return out, nil
	}

	rows, err := r.q.Query(ctx, `
		SELECT media_item_id, overview, community_rating, official_rating, premiere_date
		  FROM media_item_metadata
		 WHERE media_item_id = ANY($1)`, itemIDs)
	if err != nil {
		return nil, mapError("loading metadata", err)
	}
	defer rows.Close()

	for rows.Next() {
		var m domain.ItemMetadata
		if err := rows.Scan(&m.MediaItemID, &m.Overview, &m.CommunityRating,
			&m.OfficialRating, &m.PremiereDate); err != nil {
			return nil, mapError("scanning metadata", err)
		}
		out[m.MediaItemID] = m
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("loading metadata", err)
	}

	genres, err := r.genresFor(ctx, itemIDs)
	if err != nil {
		return nil, err
	}
	for id, list := range genres {
		m := out[id]
		m.MediaItemID, m.Genres = id, list
		out[id] = m
	}

	images, err := r.ImagesFor(ctx, itemIDs)
	if err != nil {
		return nil, err
	}
	for id, byType := range images {
		m := out[id]
		m.MediaItemID, m.Images = id, byType
		out[id] = m
	}
	return out, nil
}

func (r *MetadataRepository) genres(ctx context.Context, itemID uuid.UUID) ([]string, error) {
	rows, err := r.q.Query(ctx,
		`SELECT genre FROM media_item_genres WHERE media_item_id = $1 ORDER BY ordinal`, itemID)
	if err != nil {
		return nil, mapError("loading genres", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, mapError("scanning genre", err)
		}
		out = append(out, g)
	}
	return out, mapError("loading genres", rows.Err())
}

func (r *MetadataRepository) genresFor(
	ctx context.Context, itemIDs []uuid.UUID,
) (map[uuid.UUID][]string, error) {
	rows, err := r.q.Query(ctx, `
		SELECT media_item_id, genre FROM media_item_genres
		 WHERE media_item_id = ANY($1) ORDER BY media_item_id, ordinal`, itemIDs)
	if err != nil {
		return nil, mapError("loading genres", err)
	}
	defer rows.Close()

	out := map[uuid.UUID][]string{}
	for rows.Next() {
		var id uuid.UUID
		var g string
		if err := rows.Scan(&id, &g); err != nil {
			return nil, mapError("scanning genre", err)
		}
		out[id] = append(out[id], g)
	}
	return out, mapError("loading genres", rows.Err())
}

func (r *MetadataRepository) provenance(
	ctx context.Context, itemID uuid.UUID,
) (map[string]domain.FieldProvenance, error) {
	rows, err := r.q.Query(ctx, `
		SELECT field, source, locked, updated_at
		  FROM media_item_field_provenance WHERE media_item_id = $1`, itemID)
	if err != nil {
		return nil, mapError("loading field provenance", err)
	}
	defer rows.Close()

	out := map[string]domain.FieldProvenance{}
	for rows.Next() {
		var p domain.FieldProvenance
		if err := rows.Scan(&p.Field, &p.Source, &p.Locked, &p.UpdatedAt); err != nil {
			return nil, mapError("scanning field provenance", err)
		}
		out[p.Field] = p
	}
	return out, mapError("loading field provenance", rows.Err())
}

// metadataColumns maps a field name onto its column.
//
// A allow-list, not a format string built from the caller's input: the column
// name is interpolated into SQL, so anything not on this map must never reach
// it. Values are still bound as parameters.
var metadataColumns = map[string]string{
	domain.FieldOverview:        "overview",
	domain.FieldCommunityRating: "community_rating",
	domain.FieldOfficialRating:  "official_rating",
	domain.FieldPremiereDate:    "premiere_date",
}

type unknownFieldError struct{ field string }

func (e *unknownFieldError) Error() string { return "unknown metadata field: " + e.field }
