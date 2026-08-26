package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/repository"
)

// seedLibrary returns a library id to hang media off.
func seedLibrary(t *testing.T, ctx context.Context, libs *repository.LibraryRepository) uuid.UUID {
	t.Helper()

	lib := domain.Library{Name: "Movies", Kind: domain.LibraryKindMovie}
	if err := libs.Create(ctx, &lib); err != nil {
		t.Fatalf("seeding library: %v", err)
	}
	return lib.ID
}

func TestMediaRepositoryItems(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	media := repository.NewMediaRepository(pool)
	libraryID := seedLibrary(t, ctx, repository.NewLibraryRepository(pool))

	item := domain.MediaItem{
		LibraryID: libraryID,
		Kind:      domain.MediaItemKindMovie,
		Title:     "Idiocracy",
		Year:      ptr(2006),
	}
	if err := media.CreateItem(ctx, &item); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	got, err := media.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.Title != "Idiocracy" || got.Year == nil || *got.Year != 2006 {
		t.Errorf("GetItem returned %+v", got)
	}

	// A year the parser could not find stays null rather than becoming zero.
	// 0.0.1's parser is minimal and expected to fail on some filenames.
	unparsed := domain.MediaItem{
		LibraryID: libraryID,
		Kind:      domain.MediaItemKindMovie,
		Title:     "some.scene.release.name",
	}
	if err := media.CreateItem(ctx, &unparsed); err != nil {
		t.Fatalf("CreateItem without a year: %v", err)
	}
	back, err := media.GetItem(ctx, unparsed.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if back.Year != nil {
		t.Errorf("absent year came back as %v, want nil", *back.Year)
	}

	items, err := media.ListItemsByLibrary(ctx, libraryID)
	if err != nil {
		t.Fatalf("ListItemsByLibrary: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("ListItemsByLibrary returned %d items, want 2", len(items))
	}

	if _, err := media.GetItem(ctx, uuid.NewV7()); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("GetItem for an absent item returned %v, want ErrNotFound", err)
	}
}

// TestMediaFileLargeSize is the schema half of the Step 0 finding that range
// offsets exceed int32. A 70GB remux must survive a round trip exactly.
func TestMediaFileLargeSize(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	media := repository.NewMediaRepository(pool)
	libraryID := seedLibrary(t, ctx, repository.NewLibraryRepository(pool))

	item := domain.MediaItem{LibraryID: libraryID, Kind: domain.MediaItemKindMovie, Title: "Remux"}
	if err := media.CreateItem(ctx, &item); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	// 70GB, comfortably past int32's 2147483647.
	const size int64 = 75_161_927_680

	file := domain.MediaFile{
		MediaItemID: item.ID,
		Path:        "/media/movies/Remux (2019)/Remux (2019) - 2160p.mkv",
		Filename:    "Remux (2019) - 2160p.mkv",
		SizeBytes:   size,
	}
	if err := media.UpsertFile(ctx, &file); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	got, err := media.GetFileByPath(ctx, file.Path)
	if err != nil {
		t.Fatalf("GetFileByPath: %v", err)
	}
	if got.SizeBytes != size {
		t.Errorf("size_bytes came back as %d, want %d — the column truncated", got.SizeBytes, size)
	}

	// The largest offset the capture actually recorded, as a bit_rate, to
	// prove that column is 64-bit too.
	const observedOffset int64 = 5_255_045_235
	streams := []domain.MediaStream{{
		StreamIndex: 0,
		Kind:        domain.StreamKindVideo,
		Codec:       ptr("hevc"),
		Width:       ptr(3840),
		Height:      ptr(2160),
		BitRate:     ptr(observedOffset),
	}}
	if err := media.ReplaceStreams(ctx, got.ID, streams); err != nil {
		t.Fatalf("ReplaceStreams: %v", err)
	}

	back, err := media.ListStreams(ctx, got.ID)
	if err != nil {
		t.Fatalf("ListStreams: %v", err)
	}
	if len(back) != 1 || back[0].BitRate == nil || *back[0].BitRate != observedOffset {
		t.Errorf("bit_rate did not survive: %+v", back)
	}
}

// TestMediaFilenameWithSpacesAndBrackets covers one of the six test-library
// files chosen precisely because it breaks naive path handling.
func TestMediaFilenameWithSpacesAndBrackets(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	media := repository.NewMediaRepository(pool)
	libraryID := seedLibrary(t, ctx, repository.NewLibraryRepository(pool))

	item := domain.MediaItem{LibraryID: libraryID, Kind: domain.MediaItemKindMovie, Title: "Bracketed"}
	if err := media.CreateItem(ctx, &item); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	const name = "Some Movie (2011) [1080p] [x264] {edition-Director's Cut}.mkv"
	file := domain.MediaFile{
		MediaItemID: item.ID,
		Path:        "/media/movies/" + name,
		Filename:    name,
		SizeBytes:   1234,
	}
	if err := media.UpsertFile(ctx, &file); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	got, err := media.GetFileByPath(ctx, file.Path)
	if err != nil {
		t.Fatalf("GetFileByPath: %v", err)
	}
	if got.Filename != name {
		t.Errorf("filename came back as %q, want %q", got.Filename, name)
	}
}

// TestUpsertFileIsStableAcrossRescans is the property that keeps a repeated
// library scan from duplicating every file.
func TestUpsertFileIsStableAcrossRescans(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	media := repository.NewMediaRepository(pool)
	libraryID := seedLibrary(t, ctx, repository.NewLibraryRepository(pool))

	item := domain.MediaItem{LibraryID: libraryID, Kind: domain.MediaItemKindMovie, Title: "Idiocracy"}
	if err := media.CreateItem(ctx, &item); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	const path = "/media/movies/Idiocracy (2006).mkv"

	// First scan: discovered, not yet probed.
	first := domain.MediaFile{
		MediaItemID: item.ID,
		Path:        path,
		Filename:    "Idiocracy (2006).mkv",
		SizeBytes:   4_294_967_296,
	}
	if err := media.UpsertFile(ctx, &first); err != nil {
		t.Fatalf("first UpsertFile: %v", err)
	}
	if first.ProbedAt != nil {
		t.Error("a freshly discovered file should not be marked probed")
	}

	// Second pass: ffprobe has run.
	probedAt := time.Now().UTC().Truncate(time.Microsecond)
	second := domain.MediaFile{
		MediaItemID:     item.ID,
		Path:            path,
		Filename:        "Idiocracy (2006).mkv",
		SizeBytes:       4_294_967_296,
		Container:       ptr("matroska,webm"),
		DurationSeconds: ptr(5340.5),
		ProbedAt:        &probedAt,
	}
	if err := media.UpsertFile(ctx, &second); err != nil {
		t.Fatalf("second UpsertFile: %v", err)
	}

	// The id must survive: anything already referencing this file keeps
	// pointing at it.
	if second.ID != first.ID {
		t.Errorf("re-scan changed the file id from %s to %s", first.ID, second.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("re-scan rewrote created_at: %s became %s", first.CreatedAt, second.CreatedAt)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_files`).Scan(&n); err != nil {
		t.Fatalf("counting files: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-scan produced %d rows for one file", n)
	}

	got, err := media.GetFileByPath(ctx, path)
	if err != nil {
		t.Fatalf("GetFileByPath: %v", err)
	}
	if got.Container == nil || *got.Container != "matroska,webm" {
		t.Errorf("container is %v, want matroska,webm", got.Container)
	}
	if got.DurationSeconds == nil || *got.DurationSeconds != 5340.5 {
		t.Errorf("duration is %v, want 5340.5", got.DurationSeconds)
	}
	if got.ProbedAt == nil || !got.ProbedAt.Equal(probedAt) {
		t.Errorf("probed_at is %v, want %s", got.ProbedAt, probedAt)
	}
	if !got.UpdatedAt.After(got.CreatedAt) && !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("updated_at %s precedes created_at %s", got.UpdatedAt, got.CreatedAt)
	}
}

func TestReplaceStreams(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	media := repository.NewMediaRepository(pool)
	libraryID := seedLibrary(t, ctx, repository.NewLibraryRepository(pool))

	item := domain.MediaItem{LibraryID: libraryID, Kind: domain.MediaItemKindMovie, Title: "Idiocracy"}
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

	// A realistic MKV: video, two audio tracks, an embedded subtitle. The
	// subtitle is the case the CHECK constraint has to accept — ffprobe
	// reports it and the scanner must be able to record it.
	streams := []domain.MediaStream{
		{StreamIndex: 0, Kind: domain.StreamKindVideo, Codec: ptr("h264"),
			Width: ptr(1920), Height: ptr(1080), BitRate: ptr(int64(8_000_000))},
		{StreamIndex: 1, Kind: domain.StreamKindAudio, Codec: ptr("ac3"), Channels: ptr(6)},
		{StreamIndex: 2, Kind: domain.StreamKindAudio, Codec: ptr("aac"), Channels: ptr(2)},
		{StreamIndex: 3, Kind: domain.StreamKindSubtitle, Codec: ptr("subrip")},
	}
	if err := media.ReplaceStreams(ctx, file.ID, streams); err != nil {
		t.Fatalf("ReplaceStreams: %v", err)
	}

	got, err := media.ListStreams(ctx, file.ID)
	if err != nil {
		t.Fatalf("ListStreams: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("ListStreams returned %d streams, want 4", len(got))
	}
	if got[3].Kind != domain.StreamKindSubtitle {
		t.Errorf("stream 3 is %q, want subtitle", got[3].Kind)
	}
	if got[0].Width == nil || *got[0].Width != 1920 {
		t.Errorf("video width is %v, want 1920", got[0].Width)
	}
	// Video streams have no channel count and audio streams have no
	// dimensions; those columns must stay null rather than defaulting to zero.
	if got[0].Channels != nil {
		t.Errorf("video stream has channels=%v, want nil", *got[0].Channels)
	}
	if got[1].Width != nil {
		t.Errorf("audio stream has width=%v, want nil", *got[1].Width)
	}

	// A re-probe replaces the set wholesale rather than accumulating.
	reprobed := []domain.MediaStream{
		{StreamIndex: 0, Kind: domain.StreamKindVideo, Codec: ptr("hevc"),
			Width: ptr(3840), Height: ptr(2160)},
	}
	if err := media.ReplaceStreams(ctx, file.ID, reprobed); err != nil {
		t.Fatalf("second ReplaceStreams: %v", err)
	}

	got, err = media.ListStreams(ctx, file.ID)
	if err != nil {
		t.Fatalf("ListStreams: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("re-probe left %d streams, want 1", len(got))
	}
	if got[0].Codec == nil || *got[0].Codec != "hevc" {
		t.Errorf("codec is %v, want hevc", got[0].Codec)
	}
}

func TestMediaStreamRejectsUnknownKind(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	media := repository.NewMediaRepository(pool)
	libraryID := seedLibrary(t, ctx, repository.NewLibraryRepository(pool))

	item := domain.MediaItem{LibraryID: libraryID, Kind: domain.MediaItemKindMovie, Title: "X"}
	if err := media.CreateItem(ctx, &item); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	file := domain.MediaFile{MediaItemID: item.ID, Path: "/media/movies/x.mkv", Filename: "x.mkv"}
	if err := media.UpsertFile(ctx, &file); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	// ffprobe also reports 'data' and 'attachment' streams. Those are out of
	// scope for 0.0.1, and the constraint should reject them rather than let
	// an unrecognised kind reach the playback layer later.
	bad := []domain.MediaStream{{StreamIndex: 0, Kind: domain.StreamKind("attachment")}}
	if err := media.ReplaceStreams(ctx, file.ID, bad); err == nil {
		t.Error("ReplaceStreams accepted kind 'attachment', want a constraint violation")
	}
}

func TestMediaFilePathIsUnique(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	media := repository.NewMediaRepository(pool)
	libraryID := seedLibrary(t, ctx, repository.NewLibraryRepository(pool))

	first := domain.MediaItem{LibraryID: libraryID, Kind: domain.MediaItemKindMovie, Title: "A"}
	second := domain.MediaItem{LibraryID: libraryID, Kind: domain.MediaItemKindMovie, Title: "B"}
	for _, it := range []*domain.MediaItem{&first, &second} {
		if err := media.CreateItem(ctx, it); err != nil {
			t.Fatalf("CreateItem: %v", err)
		}
	}

	const path = "/media/movies/shared.mkv"

	f1 := domain.MediaFile{MediaItemID: first.ID, Path: path, Filename: "shared.mkv"}
	if err := media.UpsertFile(ctx, &f1); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	// Upserting the same path under a different item re-points the existing
	// row rather than creating a second one. One file on disk is one row.
	f2 := domain.MediaFile{MediaItemID: second.ID, Path: path, Filename: "shared.mkv"}
	if err := media.UpsertFile(ctx, &f2); err != nil {
		t.Fatalf("second UpsertFile: %v", err)
	}
	if f2.ID != f1.ID {
		t.Errorf("re-pointing the file changed its id from %s to %s", f1.ID, f2.ID)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_files`).Scan(&n); err != nil {
		t.Fatalf("counting files: %v", err)
	}
	if n != 1 {
		t.Errorf("media_files holds %d rows for one path, want 1", n)
	}

	files, err := media.ListFilesByItem(ctx, second.ID)
	if err != nil {
		t.Fatalf("ListFilesByItem: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("the file did not move to the second item: %d files", len(files))
	}
}

func TestGetFileByPathNotFound(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	media := repository.NewMediaRepository(pool)

	_, err := media.GetFileByPath(ctx, "/media/movies/does-not-exist.mkv")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("GetFileByPath for an absent file returned %v, want ErrNotFound", err)
	}
}
