package service

import (
	"context"
	"path/filepath"
	"strings"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/repository"
)

// LibraryService owns library creation and listing.
type LibraryService struct {
	pool *pgxpool.Pool
}

// NewLibraryService returns a service backed by pool.
func NewLibraryService(pool *pgxpool.Pool) *LibraryService {
	return &LibraryService{pool: pool}
}

// LibraryWithPaths is a library together with its filesystem locations.
//
// The two are always fetched together because a library without its paths is
// not useful to any caller, and returning them separately would invite an
// N+1 query at the API layer.
type LibraryWithPaths struct {
	Library domain.Library
	Paths   []domain.LibraryPath
}

// Create makes a library and attaches its paths, atomically.
//
// A library that exists with no paths would be silently unscannable, so the
// two writes share a transaction rather than being two API calls.
func (s *LibraryService) Create(ctx context.Context, name string, kind domain.LibraryKind, paths []string) (LibraryWithPaths, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return LibraryWithPaths{}, InvalidArgumentf("name must not be empty")
	}

	// 0.0.1 supports movie libraries only. The database enforces this too; the
	// check here exists to produce a useful message instead of a constraint
	// violation.
	if kind != domain.LibraryKindMovie {
		return LibraryWithPaths{}, InvalidArgumentf("kind must be %q", domain.LibraryKindMovie)
	}

	if len(paths) == 0 {
		return LibraryWithPaths{}, InvalidArgumentf("at least one path is required")
	}

	cleaned, err := cleanPaths(paths)
	if err != nil {
		return LibraryWithPaths{}, err
	}

	result := LibraryWithPaths{
		Library: domain.Library{Name: name, Kind: kind},
	}

	err = db.InTx(ctx, s.pool, func(q db.Querier) error {
		libs := repository.NewLibraryRepository(q)

		if err := libs.Create(ctx, &result.Library); err != nil {
			return err
		}

		for _, p := range cleaned {
			lp := domain.LibraryPath{LibraryID: result.Library.ID, Path: p}
			if err := libs.AddPath(ctx, &lp); err != nil {
				return err
			}
			result.Paths = append(result.Paths, lp)
		}
		return nil
	})
	if err != nil {
		return LibraryWithPaths{}, err
	}
	return result, nil
}

// List returns every library with its paths.
//
// Two queries, not one per library: the paths for every library are fetched in
// a single round trip and stitched in memory.
func (s *LibraryService) List(ctx context.Context) ([]LibraryWithPaths, error) {
	libs := repository.NewLibraryRepository(s.pool)

	libraries, err := libs.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(libraries) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(libraries))
	for i, l := range libraries {
		ids[i] = l.ID
	}

	paths, err := libs.ListPathsFor(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]LibraryWithPaths, len(libraries))
	for i, l := range libraries {
		out[i] = LibraryWithPaths{Library: l, Paths: paths[l.ID]}
	}
	return out, nil
}

// cleanPaths normalises and validates filesystem locations.
//
// Paths must be absolute: a relative path would be resolved against the
// server's working directory, which is not something an administrator can
// reason about from the other end of an API call.
func cleanPaths(paths []string) ([]string, error) {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))

	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, InvalidArgumentf("path must not be empty")
		}
		if !filepath.IsAbs(p) {
			return nil, InvalidArgumentf("path %q must be absolute", p)
		}

		p = filepath.Clean(p)
		if _, dup := seen[p]; dup {
			return nil, InvalidArgumentf("path %q is listed twice", p)
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}
