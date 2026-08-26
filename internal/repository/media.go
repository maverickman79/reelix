package repository

import (
	"context"
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
	                 channels, bit_rate`
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
		                           width, height, channels, bit_rate)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	for i := range streams {
		s := &streams[i]
		s.ID = newID()
		s.MediaFileID = fileID

		if _, err := r.q.Exec(ctx, q, s.ID, s.MediaFileID, s.StreamIndex, s.Kind,
			s.Codec, s.Width, s.Height, s.Channels, s.BitRate); err != nil {
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
			&s.Width, &s.Height, &s.Channels, &s.BitRate); err != nil {
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
