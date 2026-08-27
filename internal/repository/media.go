package repository

import (
	"context"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
)

// MediaRepository persists media items, the files backing them, and the
// streams within those files.
type MediaRepository struct {
	q db.Querier
}

// NewMediaRepository returns a repository reading and writing through q, which
// may be the pool or an open transaction.
func NewMediaRepository(q db.Querier) *MediaRepository {
	return &MediaRepository{q: q}
}

const (
	itemColumns = `id, library_id, kind, title, year, source_path, created_at, updated_at`
	fileColumns = `id, media_item_id, path, filename, size_bytes, container,
	               duration_seconds, probed_at, created_at, updated_at`
	streamColumns = `id, media_file_id, stream_index, kind, codec, width, height,
	                 channels, bit_rate, language, title, profile, level,
	                 pixel_format, avg_frame_rate, real_frame_rate,
	                 is_default, is_forced, is_hearing_impaired`
)

// CreateItem inserts a media item, assigning its id and timestamps.
//
// SourcePath must be set; it is unique within the library. Use UpsertItem when
// the item may already exist, which is what a re-scan needs.
func (r *MediaRepository) CreateItem(ctx context.Context, m *domain.MediaItem) error {
	m.ID = newID()
	m.CreatedAt = now()
	m.UpdatedAt = m.CreatedAt

	const q = `
		INSERT INTO media_items (id, library_id, kind, title, year, source_path, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	_, err := r.q.Exec(ctx, q, m.ID, m.LibraryID, m.Kind, m.Title, m.Year,
		m.SourcePath, m.CreatedAt, m.UpdatedAt)
	return mapError("creating media item", err)
}

// UpsertItem inserts a media item, or updates the one already recorded for the
// same source path in the same library.
//
// (library_id, source_path) is the item's identity on disk. Preserving the id
// across re-scans is what keeps a client's bookmarks, and eventually playback
// state, pointing at the same movie.
func (r *MediaRepository) UpsertItem(ctx context.Context, m *domain.MediaItem) error {
	if m.ID == (uuid.UUID{}) {
		m.ID = newID()
	}
	ts := now()
	m.CreatedAt = ts
	m.UpdatedAt = ts

	const q = `
		INSERT INTO media_items (id, library_id, kind, title, year, source_path, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (library_id, source_path) DO UPDATE SET
			kind       = EXCLUDED.kind,
			title      = EXCLUDED.title,
			year       = EXCLUDED.year,
			updated_at = EXCLUDED.updated_at
		RETURNING id, created_at`

	err := r.q.QueryRow(ctx, q, m.ID, m.LibraryID, m.Kind, m.Title, m.Year,
		m.SourcePath, m.CreatedAt, m.UpdatedAt).Scan(&m.ID, &m.CreatedAt)

	return mapError("upserting media item", err)
}

// GetItem returns one media item, or ErrNotFound.
func (r *MediaRepository) GetItem(ctx context.Context, id uuid.UUID) (domain.MediaItem, error) {
	const q = `SELECT ` + itemColumns + ` FROM media_items WHERE id = $1`
	return scanItem(r.q.QueryRow(ctx, q, id), "getting media item")
}

// ListItemsByLibrary returns a library's items, oldest first.
func (r *MediaRepository) ListItemsByLibrary(ctx context.Context, libraryID uuid.UUID) ([]domain.MediaItem, error) {
	const q = `SELECT ` + itemColumns + ` FROM media_items WHERE library_id = $1 ORDER BY id`

	rows, err := r.q.Query(ctx, q, libraryID)
	if err != nil {
		return nil, mapError("listing media items", err)
	}
	defer rows.Close()

	var out []domain.MediaItem
	for rows.Next() {
		m, err := scanItem(rows, "listing media items")
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, mapError("listing media items", rows.Err())
}

// scanItem reads one row in itemColumns order.
func scanItem(row interface{ Scan(...any) error }, op string) (domain.MediaItem, error) {
	var m domain.MediaItem
	err := row.Scan(&m.ID, &m.LibraryID, &m.Kind, &m.Title, &m.Year,
		&m.SourcePath, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return domain.MediaItem{}, mapError(op, err)
	}
	return m, nil
}

// UpsertFile inserts a file, or updates the existing row with the same path.
//
// Path is the natural key: a re-scan of an unchanged library must not
// duplicate rows. On conflict the id and created_at of the existing row are
// preserved — anything already referencing this file keeps pointing at it —
// and f is updated to match what is now stored.
func (r *MediaRepository) UpsertFile(ctx context.Context, f *domain.MediaFile) error {
	if f.ID == (uuid.UUID{}) {
		f.ID = newID()
	}
	ts := now()
	f.CreatedAt = ts
	f.UpdatedAt = ts

	const q = `
		INSERT INTO media_files (id, media_item_id, path, filename, size_bytes,
		                         container, duration_seconds, probed_at,
		                         created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (path) DO UPDATE SET
			media_item_id    = EXCLUDED.media_item_id,
			filename         = EXCLUDED.filename,
			size_bytes       = EXCLUDED.size_bytes,
			container        = EXCLUDED.container,
			duration_seconds = EXCLUDED.duration_seconds,
			probed_at        = EXCLUDED.probed_at,
			updated_at       = EXCLUDED.updated_at
		RETURNING id, created_at`

	err := r.q.QueryRow(ctx, q,
		f.ID, f.MediaItemID, f.Path, f.Filename, f.SizeBytes,
		f.Container, f.DurationSeconds, f.ProbedAt, f.CreatedAt, f.UpdatedAt,
	).Scan(&f.ID, &f.CreatedAt)

	return mapError("upserting media file", err)
}

// GetFileByPath returns the file at an absolute path, or ErrNotFound.
func (r *MediaRepository) GetFileByPath(ctx context.Context, path string) (domain.MediaFile, error) {
	const q = `SELECT ` + fileColumns + ` FROM media_files WHERE path = $1`
	return scanFile(r.q.QueryRow(ctx, q, path), "getting media file by path")
}

// ListFilesByItem returns the files backing one media item, oldest first.
func (r *MediaRepository) ListFilesByItem(ctx context.Context, itemID uuid.UUID) ([]domain.MediaFile, error) {
	const q = `SELECT ` + fileColumns + ` FROM media_files WHERE media_item_id = $1 ORDER BY id`

	rows, err := r.q.Query(ctx, q, itemID)
	if err != nil {
		return nil, mapError("listing media files", err)
	}
	defer rows.Close()

	var out []domain.MediaFile
	for rows.Next() {
		f, err := scanFile(rows, "listing media files")
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, mapError("listing media files", rows.Err())
}

// ReplaceStreams makes a file's stream rows exactly those given.
//
// Probing is not incremental: ffprobe reports the whole container, so the
// stored streams are replaced wholesale rather than reconciled.
//
// This issues two statements. Call it inside db.InTx when a partially
// replaced set would be a problem; the scanner does, so an interrupted probe
// leaves the previous streams intact rather than a truncated set.
func (r *MediaRepository) ReplaceStreams(ctx context.Context, fileID uuid.UUID, streams []domain.MediaStream) error {
	_, err := r.q.Exec(ctx, `DELETE FROM media_streams WHERE media_file_id = $1`, fileID)
	if err != nil {
		return mapError("replacing media streams", err)
	}

	const q = `
		INSERT INTO media_streams (id, media_file_id, stream_index, kind, codec,
		                           width, height, channels, bit_rate,
		                           language, title, profile, level, pixel_format,
		                           avg_frame_rate, real_frame_rate,
		                           is_default, is_forced, is_hearing_impaired)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
		        $15, $16, $17, $18, $19)`

	for i := range streams {
		s := &streams[i]
		s.ID = newID()
		s.MediaFileID = fileID

		if _, err := r.q.Exec(ctx, q, s.ID, s.MediaFileID, s.StreamIndex, s.Kind,
			s.Codec, s.Width, s.Height, s.Channels, s.BitRate,
			s.Language, s.Title, s.Profile, s.Level, s.PixelFormat,
			s.AverageFrameRate, s.RealFrameRate,
			s.IsDefault, s.IsForced, s.IsHearingImpaired); err != nil {
			return mapError("replacing media streams", err)
		}
	}
	return nil
}

// ListStreams returns a file's streams in container order.
func (r *MediaRepository) ListStreams(ctx context.Context, fileID uuid.UUID) ([]domain.MediaStream, error) {
	const q = `SELECT ` + streamColumns + `
		FROM media_streams WHERE media_file_id = $1 ORDER BY stream_index`

	rows, err := r.q.Query(ctx, q, fileID)
	if err != nil {
		return nil, mapError("listing media streams", err)
	}
	defer rows.Close()

	var out []domain.MediaStream
	for rows.Next() {
		var s domain.MediaStream
		if err := rows.Scan(&s.ID, &s.MediaFileID, &s.StreamIndex, &s.Kind, &s.Codec,
			&s.Width, &s.Height, &s.Channels, &s.BitRate,
			&s.Language, &s.Title, &s.Profile, &s.Level, &s.PixelFormat,
			&s.AverageFrameRate, &s.RealFrameRate,
			&s.IsDefault, &s.IsForced, &s.IsHearingImpaired); err != nil {
			return nil, mapError("listing media streams", err)
		}
		out = append(out, s)
	}
	return out, mapError("listing media streams", rows.Err())
}

// scanFile reads one row in fileColumns order.
func scanFile(row interface{ Scan(...any) error }, op string) (domain.MediaFile, error) {
	var f domain.MediaFile
	err := row.Scan(&f.ID, &f.MediaItemID, &f.Path, &f.Filename, &f.SizeBytes,
		&f.Container, &f.DurationSeconds, &f.ProbedAt, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return domain.MediaFile{}, mapError(op, err)
	}
	return f, nil
}

// ItemSort names an ordering the browse query supports.
//
// These are Reelix's own orderings, not Jellyfin's. The compatibility layer
// maps a client's sortBy onto one of them, and is responsible for deciding
// what to do with an ordering Reelix cannot serve.
type ItemSort string

const (
	ItemSortTitle      ItemSort = "title"
	ItemSortCreatedAt  ItemSort = "created_at"
	ItemSortYear       ItemSort = "year"
	ItemSortRandom     ItemSort = "random"
	ItemSortLastPlayed ItemSort = "last_played"
)

// ItemQuery selects, orders, and pages media items.
//
// A zero value selects every item in every library, ordered by title. Empty
// id slices mean "no filter" rather than "match nothing", so that a caller
// browsing everything does not have to special-case them.
type ItemQuery struct {
	LibraryIDs []uuid.UUID
	ItemIDs    []uuid.UUID

	// MaxYear excludes items released after it. Clients use this to keep
	// unreleased titles out of a browse row.
	MaxYear *int

	// UserID selects whose playback state travels with the items. A zero
	// value matches no user, so every item comes back with zeroed state —
	// which is the right answer for a caller that has no user in hand.
	UserID uuid.UUID

	// InProgressOnly restricts the answer to items with a resume position,
	// which is what a Continue Watching row is.
	InProgressOnly bool

	// ExcludePlayed drops items the user has finished. The latest-items row
	// uses it, because Reelix tells clients it hides played items there.
	ExcludePlayed bool

	// PlayedOnly keeps only items the user has finished.
	PlayedOnly bool

	Sort       ItemSort
	Descending bool

	Offset int
	// Limit of zero returns every matching row.
	Limit int
}

// ItemWithFile is a media item together with the file that backs it.
//
// The two are fetched in one query because every caller that lists items needs
// the container and duration with them, and fetching files separately would be
// an N+1 across a library.
type ItemWithFile struct {
	Item domain.MediaItem

	// File is nil when an item has no file row, which a partially completed
	// scan can produce.
	File *domain.MediaFile

	// HasSubtitles reports whether the file carries at least one subtitle
	// stream. Computed in SQL rather than by loading every stream.
	HasSubtitles bool

	// State is the requesting user's progress through this item, zero when
	// the query named no user or the user has never played it. Joined here
	// rather than fetched per item: a browse response carries it for every
	// row, and that is an N+1 waiting to happen.
	State domain.PlaybackState
}

// playbackJoin attaches one user's playback state to each item.
//
// $1 is always the user id. A zero uuid matches no row, so a query with no
// user in hand still runs and every item comes back with zeroed state.
const playbackJoin = `
	LEFT JOIN playback_state ps
	       ON ps.media_item_id = m.id AND ps.user_id = $1`

// ListItems returns the items matching q, and the total number of matches
// ignoring Offset and Limit.
//
// The total is what a client needs to render a scrollbar, so it is counted
// even when a page is returned.
func (r *MediaRepository) ListItems(ctx context.Context, q ItemQuery) ([]ItemWithFile, int, error) {
	where, args := itemFilters(q)

	total, err := r.countItems(ctx, where, args)
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}

	// The file is joined laterally rather than with a plain LEFT JOIN: an item
	// may have several files, and this picks exactly one of them without
	// multiplying the item rows.
	query := `
		SELECT ` + prefixed("m", itemColumns) + `,
		       f.id, f.media_item_id, f.path, f.filename, f.size_bytes,
		       f.container, f.duration_seconds, f.probed_at, f.created_at, f.updated_at,
		       COALESCE(f.has_subtitles, false),
		       COALESCE(ps.position_seconds, 0), COALESCE(ps.raw_position_seconds, 0),
		       COALESCE(ps.played, false), COALESCE(ps.play_count, 0), ps.last_played_at
		FROM media_items m
		LEFT JOIN LATERAL (
			SELECT mf.*,
			       EXISTS (
			           SELECT 1 FROM media_streams s
			           WHERE s.media_file_id = mf.id AND s.kind = 'subtitle'
			       ) AS has_subtitles
			FROM media_files mf
			WHERE mf.media_item_id = m.id
			ORDER BY mf.id
			LIMIT 1
		) f ON true` + playbackJoin +
		whereClause(where) + orderClause(q)

	if q.Limit > 0 {
		args = append(args, q.Limit)
		query += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	if q.Offset > 0 {
		args = append(args, q.Offset)
		query += fmt.Sprintf(" OFFSET $%d", len(args))
	}

	rows, err := r.q.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, mapError("listing items", err)
	}
	defer rows.Close()

	var out []ItemWithFile
	for rows.Next() {
		var it ItemWithFile

		// Every file column is scanned through a pointer, including the ones
		// the schema declares NOT NULL: an item with no file row leaves all
		// of them null, which is not a corrupt row but a scan interrupted
		// between writing the item and writing its file.
		var (
			fileID      *uuid.UUID
			mediaItemID *uuid.UUID
			path        *string
			filename    *string
			size        *int64
			container   *string
			duration    *float64
			probedAt    *time.Time
			createdAt   *time.Time
			updatedAt   *time.Time
		)

		err := rows.Scan(&it.Item.ID, &it.Item.LibraryID, &it.Item.Kind, &it.Item.Title,
			&it.Item.Year, &it.Item.SourcePath, &it.Item.CreatedAt, &it.Item.UpdatedAt,
			&fileID, &mediaItemID, &path, &filename, &size,
			&container, &duration, &probedAt, &createdAt, &updatedAt,
			&it.HasSubtitles,
			&it.State.PositionSeconds, &it.State.RawPositionSeconds,
			&it.State.Played, &it.State.PlayCount, &it.State.LastPlayedAt)
		if err != nil {
			return nil, 0, mapError("listing items", err)
		}

		// The state is this user's, for this item, by construction. An item
		// the user has never played joins to nothing and arrives zeroed,
		// which says the same thing as a row full of zeroes.
		it.State.UserID = q.UserID
		it.State.MediaItemID = it.Item.ID

		if fileID != nil {
			it.File = &domain.MediaFile{
				ID:              *fileID,
				MediaItemID:     derefOr(mediaItemID, it.Item.ID),
				Path:            derefOr(path, ""),
				Filename:        derefOr(filename, ""),
				SizeBytes:       derefOr(size, 0),
				Container:       container,
				DurationSeconds: duration,
				ProbedAt:        probedAt,
				CreatedAt:       derefOr(createdAt, time.Time{}),
				UpdatedAt:       derefOr(updatedAt, time.Time{}),
			}
		}
		out = append(out, it)
	}
	return out, total, mapError("listing items", rows.Err())
}

// derefOr reads a pointer scanned from a nullable column.
func derefOr[T any](v *T, fallback T) T {
	if v == nil {
		return fallback
	}
	return *v
}

// countItems returns the number of items matching the filters.
func (r *MediaRepository) countItems(ctx context.Context, where []string, args []any) (int, error) {
	var total int
	err := r.q.QueryRow(ctx,
		`SELECT count(*) FROM media_items m`+playbackJoin+whereClause(where), args...).Scan(&total)
	if err != nil {
		return 0, mapError("counting items", err)
	}
	return total, nil
}

// ItemRuntime returns the runtime of the file behind an item, or ErrNotFound
// when no such item exists.
//
// One round trip, because a progress report asks this every few seconds and
// needs nothing else. A nil result means the item exists but its file has not
// been probed.
func (r *MediaRepository) ItemRuntime(ctx context.Context, itemID uuid.UUID) (*float64, error) {
	const q = `
		SELECT (
			SELECT mf.duration_seconds
			FROM media_files mf
			WHERE mf.media_item_id = m.id
			ORDER BY mf.id
			LIMIT 1
		)
		FROM media_items m
		WHERE m.id = $1`

	var runtime *float64
	if err := r.q.QueryRow(ctx, q, itemID).Scan(&runtime); err != nil {
		return nil, mapError("getting item runtime", err)
	}
	return runtime, nil
}

// CountItemsByLibrary returns the number of items in each of the given
// libraries. Libraries with no items are absent from the map.
func (r *MediaRepository) CountItemsByLibrary(ctx context.Context, libraryIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	counts := make(map[uuid.UUID]int, len(libraryIDs))
	if len(libraryIDs) == 0 {
		return counts, nil
	}

	const q = `
		SELECT library_id, count(*)
		FROM media_items
		WHERE library_id = ANY($1)
		GROUP BY library_id`

	rows, err := r.q.Query(ctx, q, libraryIDs)
	if err != nil {
		return nil, mapError("counting items by library", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id uuid.UUID
			n  int
		)
		if err := rows.Scan(&id, &n); err != nil {
			return nil, mapError("counting items by library", err)
		}
		counts[id] = n
	}
	return counts, mapError("counting items by library", rows.Err())
}

// itemFilters builds the WHERE fragments and their arguments.
//
// The user id is always $1, whether or not the query filters on playback
// state, so that the join above can be a constant.
func itemFilters(q ItemQuery) ([]string, []any) {
	var (
		where []string
		args  = []any{q.UserID}
	)

	add := func(fragment string, arg any) {
		args = append(args, arg)
		where = append(where, fmt.Sprintf(fragment, len(args)))
	}

	if len(q.LibraryIDs) > 0 {
		add("m.library_id = ANY($%d)", q.LibraryIDs)
	}
	if len(q.ItemIDs) > 0 {
		add("m.id = ANY($%d)", q.ItemIDs)
	}
	if q.MaxYear != nil {
		// Items with no year are kept: an unknown release date is not
		// evidence that the item is unreleased.
		add("(m.year IS NULL OR m.year <= $%d)", *q.MaxYear)
	}

	if q.InProgressOnly {
		where = append(where, "ps.position_seconds > 0")
	}
	if q.ExcludePlayed {
		// An item the user has never touched has no row at all, and has
		// certainly not been played.
		where = append(where, "COALESCE(ps.played, false) = false")
	}
	if q.PlayedOnly {
		where = append(where, "COALESCE(ps.played, false) = true")
	}
	return where, args
}

// whereClause renders the fragments, or nothing when there are none.
func whereClause(where []string) string {
	if len(where) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(where, " AND ")
}

// orderClause renders the sort.
//
// Every ordering ends in the item id so that paging is stable: two items with
// the same title must not swap places between page one and page two. Random is
// the deliberate exception — it is reshuffled per query by definition, and a
// client asking for it is not paging through it.
func orderClause(q ItemQuery) string {
	if q.Sort == ItemSortRandom {
		return " ORDER BY random()"
	}

	column := "lower(m.title)"
	switch q.Sort {
	case ItemSortCreatedAt:
		column = "m.created_at"
	case ItemSortYear:
		column = "m.year"
	case ItemSortLastPlayed:
		column = "ps.last_played_at"
	}

	direction := "ASC"
	if q.Descending {
		direction = "DESC"
	}

	// NULLS LAST in both directions: an item with no year belongs at the end
	// of the list, not at the top of a descending one.
	return " ORDER BY " + column + " " + direction + " NULLS LAST, m.id " + direction
}

// prefixed qualifies a column list with a table alias.
func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}
