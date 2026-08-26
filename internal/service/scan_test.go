package service_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/media"
	"github.com/maverickman79/reelix/internal/repository"
	"github.com/maverickman79/reelix/internal/service"
)

const dsnEnv = "REELIX_TEST_DB_DSN"

// fakeProber returns canned probe results without invoking ffprobe.
//
// These tests are about persistence and idempotency, not about ffprobe's
// output; the real binary is exercised by internal/media and by the container
// verification. Using a fake also counts calls, which is how re-probe
// behaviour is asserted.
type fakeProber struct {
	mu     sync.Mutex
	calls  map[string]int
	failOn map[string]error
}

func newFakeProber() *fakeProber {
	return &fakeProber{calls: map[string]int{}, failOn: map[string]error{}}
}

func (f *fakeProber) Probe(_ context.Context, path string) (media.ProbeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls[path]++

	if err, ok := f.failOn[path]; ok {
		return media.ProbeResult{}, err
	}

	duration := 5340.5
	width, height, channels := 1920, 1080, 6
	bitRate := int64(8_000_000)

	return media.ProbeResult{
		Container: "matroska,webm",
		Duration:  &duration,
		Streams: []media.ProbeStream{
			{Index: 0, Kind: "video", Codec: "h264", Width: &width, Height: &height, BitRate: &bitRate},
			{Index: 1, Kind: "audio", Codec: "ac3", Channels: &channels},
			{Index: 2, Kind: "subtitle", Codec: "subrip"},
		},
	}, nil
}

func (f *fakeProber) callCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *fakeProber) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

// scanFixture is a library on disk plus the services that scan it.
type scanFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	root    string
	library uuid.UUID
	scans   *service.ScanService
	prober  *fakeProber
}

func newScanFixture(t *testing.T) *scanFixture {
	t.Helper()

	adminDSN := os.Getenv(dsnEnv)
	if adminDSN == "" {
		t.Skipf("%s not set; skipping database integration test", dsnEnv)
	}

	ctx := context.Background()

	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connecting to %s: %v", dsnEnv, err)
	}
	defer admin.Close()

	name := "reelix_test_" + strings.ReplaceAll(uuid.NewV7().String(), "-", "_")
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		t.Fatalf("creating scratch database: %v", err)
	}

	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parsing %s: %v", dsnEnv, err)
	}
	u.Path = "/" + name

	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatalf("connecting to scratch database: %v", err)
	}

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, pool, discard); err != nil {
		t.Fatalf("migrating scratch database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()

		cleanup, err := pgxpool.New(context.Background(), adminDSN)
		if err != nil {
			t.Errorf("reconnecting to drop scratch database: %v", err)
			return
		}
		defer cleanup.Close()

		if _, err := cleanup.Exec(context.Background(),
			fmt.Sprintf("DROP DATABASE IF EXISTS %q WITH (FORCE)", name)); err != nil {
			t.Errorf("dropping scratch database %s: %v", name, err)
		}
	})

	root := t.TempDir()

	libs := repository.NewLibraryRepository(pool)
	lib := domain.Library{Name: "Movies", Kind: domain.LibraryKindMovie}
	if err := libs.Create(ctx, &lib); err != nil {
		t.Fatalf("creating library: %v", err)
	}
	lp := domain.LibraryPath{LibraryID: lib.ID, Path: root}
	if err := libs.AddPath(ctx, &lp); err != nil {
		t.Fatalf("adding library path: %v", err)
	}

	prober := newFakeProber()

	return &scanFixture{
		t:       t,
		pool:    pool,
		root:    root,
		library: lib.ID,
		scans:   service.NewScanService(pool, prober, discard),
		prober:  prober,
	}
}

// write creates a media file of the given size.
func (f *scanFixture) write(rel string, size int) string {
	f.t.Helper()

	path := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		f.t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// scan runs a scan to completion and returns the finished job.
func (f *scanFixture) scan() domain.Job {
	f.t.Helper()

	ctx := context.Background()

	job, err := f.scans.Start(ctx, f.library)
	if err != nil {
		f.t.Fatalf("starting scan: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, err := f.scans.Job(ctx, job.ID)
		if err != nil {
			f.t.Fatalf("polling job: %v", err)
		}
		if got.State.Terminal() {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}

	f.t.Fatal("scan did not finish within 30s")
	return domain.Job{}
}

// counts returns row counts for the three media tables.
func (f *scanFixture) counts() (items, files, streams int) {
	f.t.Helper()

	ctx := context.Background()
	for _, q := range []struct {
		sql string
		dst *int
	}{
		{`SELECT count(*) FROM media_items`, &items},
		{`SELECT count(*) FROM media_files`, &files},
		{`SELECT count(*) FROM media_streams`, &streams},
	} {
		if err := f.pool.QueryRow(ctx, q.sql).Scan(q.dst); err != nil {
			f.t.Fatalf("counting: %v", err)
		}
	}
	return items, files, streams
}

func TestScanPopulatesDatabase(t *testing.T) {
	f := newScanFixture(t)

	f.write("Idiocracy.2006.1080p.WEB-DL.mkv", 1024)
	f.write("Gangland 2025 1080p WEB-DL.mp4", 1024)
	f.write("Congo.1995.BluRay/Congo.1995.BluRay.mkv", 1024)

	job := f.scan()
	if job.State != domain.JobStateCompleted {
		t.Fatalf("job state = %s, error = %v", job.State, job.Error)
	}

	items, files, streams := f.counts()
	if items != 3 || files != 3 {
		t.Errorf("got %d items and %d files, want 3 and 3", items, files)
	}
	// Three streams per file from the fake prober.
	if streams != 9 {
		t.Errorf("got %d streams, want 9", streams)
	}

	// Progress must end at the full count.
	if job.ProgressTotal != 3 || job.ProgressCurrent != 3 {
		t.Errorf("progress = %d/%d, want 3/3", job.ProgressCurrent, job.ProgressTotal)
	}

	ctx := context.Background()
	repo := repository.NewMediaRepository(f.pool)

	got, err := repo.ListItemsByLibrary(ctx, f.library)
	if err != nil {
		t.Fatalf("listing items: %v", err)
	}

	titles := map[string]int{}
	for _, it := range got {
		if it.Year != nil {
			titles[it.Title] = *it.Year
		} else {
			titles[it.Title] = 0
		}
	}

	for title, year := range map[string]int{"Idiocracy": 2006, "Gangland": 2025, "Congo": 1995} {
		if titles[title] != year {
			t.Errorf("title %q parsed with year %d, want %d", title, titles[title], year)
		}
	}

	// Durations must be persisted, not just probed.
	files2, err := repo.ListFilesByItem(ctx, got[0].ID)
	if err != nil {
		t.Fatalf("listing files: %v", err)
	}
	if len(files2) != 1 {
		t.Fatalf("item has %d files, want 1", len(files2))
	}
	if files2[0].DurationSeconds == nil || *files2[0].DurationSeconds != 5340.5 {
		t.Errorf("duration = %v, want 5340.5", files2[0].DurationSeconds)
	}
	if files2[0].ProbedAt == nil {
		t.Error("probed_at was not set")
	}
	if files2[0].Container == nil || *files2[0].Container != "matroska,webm" {
		t.Errorf("container = %v", files2[0].Container)
	}
}

// TestScanIsIdempotent is the Step 4 completion criterion: a second scan
// updates rather than duplicates.
func TestScanIsIdempotent(t *testing.T) {
	f := newScanFixture(t)

	f.write("Idiocracy.2006.1080p.WEB-DL.mkv", 1024)
	f.write("Congo.1995.BluRay/Congo.1995.BluRay.mkv", 1024)
	f.write("Congo.1995.BluRay/Congo.1995.BluRay-part2.mkv", 1024)

	if job := f.scan(); job.State != domain.JobStateCompleted {
		t.Fatalf("first scan: %s %v", job.State, job.Error)
	}

	items1, files1, streams1 := f.counts()
	// Two movies: one loose file, one release folder holding two files.
	if items1 != 2 || files1 != 3 {
		t.Fatalf("first scan produced %d items and %d files, want 2 and 3", items1, files1)
	}

	ctx := context.Background()
	repo := repository.NewMediaRepository(f.pool)

	before, err := repo.ListItemsByLibrary(ctx, f.library)
	if err != nil {
		t.Fatalf("listing items: %v", err)
	}

	probesAfterFirst := f.prober.totalCalls()

	// Second scan, nothing changed on disk.
	if job := f.scan(); job.State != domain.JobStateCompleted {
		t.Fatalf("second scan: %s %v", job.State, job.Error)
	}

	items2, files2, streams2 := f.counts()
	if items2 != items1 || files2 != files1 || streams2 != streams1 {
		t.Errorf("re-scan changed counts: items %d→%d, files %d→%d, streams %d→%d",
			items1, items2, files1, files2, streams1, streams2)
	}

	after, err := repo.ListItemsByLibrary(ctx, f.library)
	if err != nil {
		t.Fatalf("listing items: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("item count changed: %d then %d", len(before), len(after))
	}

	// Ids must survive. A client that bookmarked an item, and eventually its
	// playback position, must still be pointing at the same movie.
	for i := range before {
		if before[i].ID != after[i].ID {
			t.Errorf("item %d changed id from %s to %s", i, before[i].ID, after[i].ID)
		}
		if !before[i].CreatedAt.Equal(after[i].CreatedAt) {
			t.Errorf("item %d created_at was rewritten", i)
		}
	}

	// Nothing was re-probed: probed_at is set and the sizes are unchanged.
	if got := f.prober.totalCalls(); got != probesAfterFirst {
		t.Errorf("re-scan performed %d extra probes, want 0", got-probesAfterFirst)
	}
}

// TestScanReprobesChangedFile checks the size-based change signal works.
//
// Size is weaker than modification time — media_files has no mtime column — so
// a file edited in place without changing length would not be re-probed. This
// covers the case that is detected.
func TestScanReprobesChangedFile(t *testing.T) {
	f := newScanFixture(t)

	path := f.write("Idiocracy.2006.1080p.WEB-DL.mkv", 1024)

	if job := f.scan(); job.State != domain.JobStateCompleted {
		t.Fatalf("first scan: %s %v", job.State, job.Error)
	}
	if got := f.prober.callCount(path); got != 1 {
		t.Fatalf("first scan probed %d times, want 1", got)
	}

	// Unchanged: no re-probe.
	if job := f.scan(); job.State != domain.JobStateCompleted {
		t.Fatalf("second scan: %s %v", job.State, job.Error)
	}
	if got := f.prober.callCount(path); got != 1 {
		t.Errorf("unchanged file was probed %d times, want 1", got)
	}

	// Grown on disk: re-probe.
	f.write("Idiocracy.2006.1080p.WEB-DL.mkv", 4096)

	if job := f.scan(); job.State != domain.JobStateCompleted {
		t.Fatalf("third scan: %s %v", job.State, job.Error)
	}
	if got := f.prober.callCount(path); got != 2 {
		t.Errorf("resized file was probed %d times, want 2", got)
	}

	items, files, _ := f.counts()
	if items != 1 || files != 1 {
		t.Errorf("re-probe duplicated rows: %d items, %d files", items, files)
	}
}

// TestScanSkipsSampleDirectories is the same Radarr convention covered in
// internal/media, verified end to end through persistence.
func TestScanSkipsSampleDirectories(t *testing.T) {
	f := newScanFixture(t)

	f.write("Congo.1995.BluRay/Congo.1995.BluRay.mkv", 2048)
	f.write("Congo.1995.BluRay/Sample/congo-sample.mkv", 512)
	f.write("Idiocracy.2006.WEB-DL/Idiocracy.2006.WEB-DL.mkv", 2048)
	f.write("Idiocracy.2006.WEB-DL/sample/sample.mkv", 512)

	if job := f.scan(); job.State != domain.JobStateCompleted {
		t.Fatalf("scan: %s %v", job.State, job.Error)
	}

	items, files, _ := f.counts()
	if items != 2 || files != 2 {
		t.Errorf("got %d items and %d files, want 2 and 2 — samples were indexed", items, files)
	}

	var indexed []string
	rows, err := f.pool.Query(context.Background(), `SELECT path FROM media_files`)
	if err != nil {
		t.Fatalf("listing paths: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scanning path: %v", err)
		}
		rel, _ := filepath.Rel(f.root, p)
		indexed = append(indexed, rel)
	}

	for _, rel := range indexed {
		if strings.Contains(strings.ToLower(rel), "sample") {
			t.Errorf("a sample file reached the database: %s", rel)
		}
	}
}

// TestScanContinuesPastProbeFailure checks one bad file does not cost the rest
// of the library.
func TestScanContinuesPastProbeFailure(t *testing.T) {
	f := newScanFixture(t)

	good1 := f.write("Good One (2020).mkv", 1024)
	bad := f.write("Corrupt (2021).mkv", 1024)
	good2 := f.write("Good Two (2022).mkv", 1024)

	f.prober.failOn[bad] = errors.New("ffprobe: Invalid data found when processing input")

	job := f.scan()
	if job.State != domain.JobStateCompleted {
		t.Fatalf("a single bad file failed the whole scan: %s %v", job.State, job.Error)
	}

	items, files, _ := f.counts()
	if items != 2 || files != 2 {
		t.Errorf("got %d items and %d files, want 2 and 2", items, files)
	}

	repo := repository.NewMediaRepository(f.pool)
	for _, path := range []string{good1, good2} {
		if _, err := repo.GetFileByPath(context.Background(), path); err != nil {
			t.Errorf("good file %s was not indexed: %v", filepath.Base(path), err)
		}
	}

	// The bad file is absent, so the next scan retries it rather than
	// treating it as done.
	_, err := repo.GetFileByPath(context.Background(), bad)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("the unprobeable file was recorded anyway: %v", err)
	}
}

// TestScanRejectsConcurrentScans checks the partial unique index prevents two
// scans of one library.
func TestScanRejectsConcurrentScans(t *testing.T) {
	f := newScanFixture(t)

	for i := range 30 {
		f.write(fmt.Sprintf("Movie %d (2020).mkv", i), 1024)
	}

	ctx := context.Background()

	first, err := f.scans.Start(ctx, f.library)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// The first scan may already have finished; only assert the conflict when
	// it is genuinely still in flight.
	current, err := f.scans.Job(ctx, first.ID)
	if err != nil {
		t.Fatalf("reading job: %v", err)
	}
	if current.State.Terminal() {
		t.Skip("the first scan finished before a second could be attempted")
	}

	if _, err := f.scans.Start(ctx, f.library); !errors.Is(err, repository.ErrConflict) {
		t.Errorf("a second concurrent scan returned %v, want ErrConflict", err)
	}
}

// TestScanUnknownLibrary checks scanning a library that does not exist is a
// clean not-found rather than a job that fails later.
func TestScanUnknownLibrary(t *testing.T) {
	f := newScanFixture(t)

	if _, err := f.scans.Start(context.Background(), uuid.NewV7()); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("scanning an unknown library returned %v, want ErrNotFound", err)
	}
}

// TestReapOrphanedJobs checks a job left running by a dead process is failed
// at startup rather than blocking the library forever.
func TestReapOrphanedJobs(t *testing.T) {
	f := newScanFixture(t)
	ctx := context.Background()

	jobs := repository.NewJobRepository(f.pool)

	job := domain.Job{Kind: domain.JobKindLibraryScan, LibraryID: &f.library}
	if err := jobs.Create(ctx, &job); err != nil {
		t.Fatalf("creating job: %v", err)
	}
	if err := jobs.MarkRunning(ctx, job.ID); err != nil {
		t.Fatalf("marking running: %v", err)
	}

	// While it looks running, the library cannot be scanned again.
	if _, err := f.scans.Start(ctx, f.library); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("expected ErrConflict while a job is running, got %v", err)
	}

	if err := f.scans.ReapOrphanedJobs(ctx); err != nil {
		t.Fatalf("ReapOrphanedJobs: %v", err)
	}

	reaped, err := jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("reading reaped job: %v", err)
	}
	if reaped.State != domain.JobStateFailed {
		t.Errorf("reaped job state = %s, want failed", reaped.State)
	}
	if reaped.Error == nil || *reaped.Error == "" {
		t.Error("reaped job carries no explanation")
	}
	if reaped.FinishedAt == nil {
		t.Error("reaped job has no finished_at")
	}

	// And the library is scannable again.
	if _, err := f.scans.Start(ctx, f.library); err != nil {
		t.Errorf("scanning after a reap returned %v", err)
	}
}

// TestScanEmptyLibrary checks a library with no media completes rather than
// failing.
func TestScanEmptyLibrary(t *testing.T) {
	f := newScanFixture(t)

	job := f.scan()
	if job.State != domain.JobStateCompleted {
		t.Fatalf("scanning an empty library: %s %v", job.State, job.Error)
	}

	items, files, streams := f.counts()
	if items != 0 || files != 0 || streams != 0 {
		t.Errorf("an empty library produced %d/%d/%d rows", items, files, streams)
	}
}

// TestScanMissingPathFailsJob checks a library pointing at a path that is not
// there fails visibly rather than silently reporting success over nothing.
func TestScanMissingPathFailsJob(t *testing.T) {
	f := newScanFixture(t)

	if err := os.RemoveAll(f.root); err != nil {
		t.Fatalf("removing the library root: %v", err)
	}

	job := f.scan()
	if job.State != domain.JobStateFailed {
		t.Fatalf("job state = %s, want failed", job.State)
	}
	if job.Error == nil || *job.Error == "" {
		t.Error("the failed job carries no explanation")
	}
}
