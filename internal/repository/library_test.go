package repository_test

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/repository"
)

func TestLibraryRepositoryCreateAndGet(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	libs := repository.NewLibraryRepository(pool)

	lib := domain.Library{Name: "Movies", Kind: domain.LibraryKindMovie}
	if err := libs.Create(ctx, &lib); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := libs.GetByID(ctx, lib.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Movies" || got.Kind != domain.LibraryKindMovie {
		t.Errorf("GetByID returned %+v", got)
	}

	if _, err := libs.GetByID(ctx, uuid.NewV7()); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("GetByID for an absent library returned %v, want ErrNotFound", err)
	}
}

func TestLibraryRepositoryRejectsUnknownKind(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	libs := repository.NewLibraryRepository(pool)

	// The CHECK constraint permits only 'movie' in 0.0.1. A series library is
	// out of scope, and the database — not just the service layer — should say
	// so.
	lib := domain.Library{Name: "TV", Kind: domain.LibraryKind("series")}
	if err := libs.Create(ctx, &lib); err == nil {
		t.Fatal("Create accepted kind 'series', want a constraint violation")
	}
}

func TestLibraryRepositoryPaths(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	libs := repository.NewLibraryRepository(pool)

	lib := domain.Library{Name: "Movies", Kind: domain.LibraryKindMovie}
	if err := libs.Create(ctx, &lib); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 0.0.1 uses one path, but the schema is built for several and the
	// repository must actually support them.
	for _, p := range []string{"/media/movies", "/media/movies-4k"} {
		lp := domain.LibraryPath{LibraryID: lib.ID, Path: p}
		if err := libs.AddPath(ctx, &lp); err != nil {
			t.Fatalf("AddPath(%q): %v", p, err)
		}
	}

	paths, err := libs.ListPaths(ctx, lib.ID)
	if err != nil {
		t.Fatalf("ListPaths: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("ListPaths returned %d paths, want 2", len(paths))
	}
	if paths[0].Path != "/media/movies" || paths[1].Path != "/media/movies-4k" {
		t.Errorf("paths came back in the wrong order: %q, %q", paths[0].Path, paths[1].Path)
	}

	// The same path twice on one library is a conflict.
	dup := domain.LibraryPath{LibraryID: lib.ID, Path: "/media/movies"}
	if err := libs.AddPath(ctx, &dup); !errors.Is(err, repository.ErrConflict) {
		t.Errorf("duplicate AddPath returned %v, want ErrConflict", err)
	}

	// The same path on a *different* library is allowed: two libraries may
	// legitimately overlap on disk.
	other := domain.Library{Name: "Foreign", Kind: domain.LibraryKindMovie}
	if err := libs.Create(ctx, &other); err != nil {
		t.Fatalf("Create second library: %v", err)
	}
	shared := domain.LibraryPath{LibraryID: other.ID, Path: "/media/movies"}
	if err := libs.AddPath(ctx, &shared); err != nil {
		t.Errorf("AddPath on a second library returned %v, want success", err)
	}
}

func TestLibraryRepositoryList(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	libs := repository.NewLibraryRepository(pool)

	empty, err := libs.List(ctx)
	if err != nil {
		t.Fatalf("List on an empty database: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("List returned %d libraries on a fresh database", len(empty))
	}

	names := []string{"Movies", "Movies 4K", "Foreign"}
	for _, n := range names {
		lib := domain.Library{Name: n, Kind: domain.LibraryKindMovie}
		if err := libs.Create(ctx, &lib); err != nil {
			t.Fatalf("Create(%q): %v", n, err)
		}
	}

	got, err := libs.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != len(names) {
		t.Fatalf("List returned %d libraries, want %d", len(got), len(names))
	}
	// UUIDv7 ordering must equal creation order. This is the property the
	// whole v7 decision rests on, so it gets asserted rather than assumed.
	for i, n := range names {
		if got[i].Name != n {
			t.Errorf("List[%d] is %q, want %q — v7 ids are not in creation order",
				i, got[i].Name, n)
		}
	}
}

func TestLibraryDeleteCascades(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	libs := repository.NewLibraryRepository(pool)
	media := repository.NewMediaRepository(pool)

	lib := domain.Library{Name: "Movies", Kind: domain.LibraryKindMovie}
	if err := libs.Create(ctx, &lib); err != nil {
		t.Fatalf("Create: %v", err)
	}
	lp := domain.LibraryPath{LibraryID: lib.ID, Path: "/media/movies"}
	if err := libs.AddPath(ctx, &lp); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	item := domain.MediaItem{
		LibraryID:  lib.ID,
		Kind:       domain.MediaItemKindMovie,
		Title:      "Idiocracy",
		Year:       ptr(2006),
		SourcePath: "/media/movies/Idiocracy (2006).mkv",
	}
	if err := media.CreateItem(ctx, &item); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	file := domain.MediaFile{
		MediaItemID: item.ID,
		Path:        "/media/movies/Idiocracy (2006).mkv",
		Filename:    "Idiocracy (2006).mkv",
		SizeBytes:   4_294_967_296,
	}
	if err := media.UpsertFile(ctx, &file); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	streams := []domain.MediaStream{{StreamIndex: 0, Kind: domain.StreamKindVideo, Codec: ptr("h264")}}
	if err := media.ReplaceStreams(ctx, file.ID, streams); err != nil {
		t.Fatalf("ReplaceStreams: %v", err)
	}

	if err := libs.Delete(ctx, lib.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Deleting a library must take its paths, items, files, and streams with
	// it. An orphaned media_files row would later be served as a playable item
	// pointing at a library that no longer exists.
	for _, table := range []string{"library_paths", "media_items", "media_files", "media_streams"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s still holds %d rows after the library was deleted", table, n)
		}
	}

	if err := libs.Delete(ctx, lib.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("deleting an absent library returned %v, want ErrNotFound", err)
	}
}

func TestInTxRollsBack(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)

	sentinel := errors.New("deliberate failure")

	// A library and its path must appear together or not at all; this is the
	// composition db.InTx exists for.
	err := db.InTx(ctx, pool, func(q db.Querier) error {
		libs := repository.NewLibraryRepository(q)

		lib := domain.Library{Name: "Movies", Kind: domain.LibraryKindMovie}
		if err := libs.Create(ctx, &lib); err != nil {
			return err
		}
		p := domain.LibraryPath{LibraryID: lib.ID, Path: "/media/movies"}
		if err := libs.AddPath(ctx, &p); err != nil {
			return err
		}
		return sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx returned %v, want the sentinel", err)
	}

	libs := repository.NewLibraryRepository(pool)
	got, err := libs.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("rolled-back transaction left %d libraries behind", len(got))
	}
}

func TestInTxCommits(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)

	err := db.InTx(ctx, pool, func(q db.Querier) error {
		libs := repository.NewLibraryRepository(q)

		lib := domain.Library{Name: "Movies", Kind: domain.LibraryKindMovie}
		if err := libs.Create(ctx, &lib); err != nil {
			return err
		}
		p := domain.LibraryPath{LibraryID: lib.ID, Path: "/media/movies"}
		return libs.AddPath(ctx, &p)
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	libs := repository.NewLibraryRepository(pool)
	got, err := libs.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("committed transaction produced %d libraries, want 1", len(got))
	}

	paths, err := libs.ListPaths(ctx, got[0].ID)
	if err != nil {
		t.Fatalf("ListPaths: %v", err)
	}
	if len(paths) != 1 {
		t.Errorf("committed transaction produced %d paths, want 1", len(paths))
	}
}
