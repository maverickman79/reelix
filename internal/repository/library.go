package repository

import (
	"context"
	"uuid"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
)

// LibraryRepository persists libraries and their filesystem paths.
type LibraryRepository struct {
	q db.Querier
}

// NewLibraryRepository returns a repository reading and writing through q,
// which may be the pool or an open transaction.
func NewLibraryRepository(q db.Querier) *LibraryRepository {
	return &LibraryRepository{q: q}
}

const libraryColumns = `id, name, kind, created_at, updated_at`

// Create inserts a library, assigning its id and timestamps.
//
// Paths are added separately with AddPath. A caller that needs a library and
// its paths to appear atomically should run both inside one transaction —
// which is why this type takes a Querier rather than the pool.
func (r *LibraryRepository) Create(ctx context.Context, l *domain.Library) error {
	l.ID = newID()
	l.CreatedAt = now()
	l.UpdatedAt = l.CreatedAt

	const q = `
		INSERT INTO libraries (id, name, kind, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)`

	_, err := r.q.Exec(ctx, q, l.ID, l.Name, l.Kind, l.CreatedAt, l.UpdatedAt)
	return mapError("creating library", err)
}

// GetByID returns one library, or ErrNotFound.
func (r *LibraryRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.Library, error) {
	const q = `SELECT ` + libraryColumns + ` FROM libraries WHERE id = $1`

	var l domain.Library
	err := r.q.QueryRow(ctx, q, id).Scan(&l.ID, &l.Name, &l.Kind, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return domain.Library{}, mapError("getting library", err)
	}
	return l, nil
}

// List returns every library, oldest first.
//
// Ordering by id rather than created_at is intentional and equivalent: ids are
// UUIDv7, so they are already in creation order, and the primary key index
// supplies that order without a sort.
func (r *LibraryRepository) List(ctx context.Context) ([]domain.Library, error) {
	const q = `SELECT ` + libraryColumns + ` FROM libraries ORDER BY id`

	rows, err := r.q.Query(ctx, q)
	if err != nil {
		return nil, mapError("listing libraries", err)
	}
	defer rows.Close()

	var out []domain.Library
	for rows.Next() {
		var l domain.Library
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, mapError("listing libraries", err)
		}
		out = append(out, l)
	}
	return out, mapError("listing libraries", rows.Err())
}

// AddPath attaches a filesystem location to a library.
//
// A path already registered against the same library returns ErrConflict.
func (r *LibraryRepository) AddPath(ctx context.Context, p *domain.LibraryPath) error {
	p.ID = newID()
	p.CreatedAt = now()

	const q = `
		INSERT INTO library_paths (id, library_id, path, created_at)
		VALUES ($1, $2, $3, $4)`

	_, err := r.q.Exec(ctx, q, p.ID, p.LibraryID, p.Path, p.CreatedAt)
	return mapError("adding library path", err)
}

// ListPaths returns a library's filesystem locations, oldest first.
func (r *LibraryRepository) ListPaths(ctx context.Context, libraryID uuid.UUID) ([]domain.LibraryPath, error) {
	const q = `
		SELECT id, library_id, path, created_at
		FROM library_paths
		WHERE library_id = $1
		ORDER BY id`

	rows, err := r.q.Query(ctx, q, libraryID)
	if err != nil {
		return nil, mapError("listing library paths", err)
	}
	defer rows.Close()

	var out []domain.LibraryPath
	for rows.Next() {
		var p domain.LibraryPath
		if err := rows.Scan(&p.ID, &p.LibraryID, &p.Path, &p.CreatedAt); err != nil {
			return nil, mapError("listing library paths", err)
		}
		out = append(out, p)
	}
	return out, mapError("listing library paths", rows.Err())
}

// Delete removes a library. Its paths, items, files, and streams go with it
// through ON DELETE CASCADE.
func (r *LibraryRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.q.Exec(ctx, `DELETE FROM libraries WHERE id = $1`, id)
	if err != nil {
		return mapError("deleting library", err)
	}
	if tag.RowsAffected() == 0 {
		return mapError("deleting library", ErrNotFound)
	}
	return nil
}
