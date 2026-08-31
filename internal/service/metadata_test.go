package service_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/metadata"
	"github.com/maverickman79/reelix/internal/repository"
	"github.com/maverickman79/reelix/internal/service"
)

// metadataFixture is an identified library plus a metadata service.
type metadataFixture struct {
	*identifyFixture
	metadata *service.MetadataService

	// cacheDir roots the artwork store, so a test can wipe it the way losing a
	// cache volume does.
	cacheDir string
}

func newMetadataFixture(t *testing.T, provider *fakeProvider) *metadataFixture {
	t.Helper()

	// The film has to be identified before its metadata can be fetched, so the
	// identify fixture does that first with a candidate that matches.
	provider.candidates = []metadata.Candidate{
		{IDs: map[string]string{"tmdb": "550"}, Title: "Fight Club", Year: 1999},
	}

	f := newIdentifyFixture(t, provider)
	f.runIdentify(t)

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	cacheDir := t.TempDir()
	return &metadataFixture{
		identifyFixture: f,
		metadata:        service.NewMetadataService(f.pool, provider, cacheDir, discard),
		cacheDir:        cacheDir,
	}
}

func (f *metadataFixture) refresh(t *testing.T, all bool) domain.Job {
	t.Helper()

	job, err := f.metadata.StartRefresh(context.Background(), f.library, all)
	if err != nil {
		t.Fatalf("starting refresh: %v", err)
	}

	jobs := repository.NewJobRepository(f.pool)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		got, err := jobs.Get(context.Background(), job.ID)
		if err != nil {
			t.Fatalf("reading job: %v", err)
		}
		if got.State.Terminal() {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("refresh did not finish")
	return domain.Job{}
}

func (f *metadataFixture) metadataOf(t *testing.T) domain.ItemMetadata {
	t.Helper()

	got, err := f.metadata.Get(context.Background(), f.item.ID)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	return got
}

func sampleMetadata() metadata.MovieMetadata {
	rating := 8.4
	return metadata.MovieMetadata{
		Overview:        "A ticking-time-bomb insomniac.",
		CommunityRating: &rating,
		OfficialRating:  "R",
		ReleaseDate:     time.Date(1999, 10, 15, 0, 0, 0, 0, time.UTC),
		Genres:          []string{"Drama", "Thriller"},
	}
}

func TestRefreshStoresProviderFields(t *testing.T) {
	f := newMetadataFixture(t, &fakeProvider{meta: sampleMetadata()})

	if job := f.refresh(t, false); job.State != domain.JobStateCompleted {
		t.Fatalf("job state = %s: %s", job.State, jobError(job))
	}

	got := f.metadataOf(t)
	if got.Overview == nil || *got.Overview == "" {
		t.Error("no overview stored")
	}
	if got.CommunityRating == nil || *got.CommunityRating != 8.4 {
		t.Errorf("community rating = %v, want 8.4", got.CommunityRating)
	}
	if got.OfficialRating == nil || *got.OfficialRating != "R" {
		t.Errorf("official rating = %v, want R", got.OfficialRating)
	}
	if got.PremiereDate == nil || got.PremiereDate.Year() != 1999 {
		t.Errorf("premiere date = %v, want 1999", got.PremiereDate)
	}
	if len(got.Genres) != 2 || got.Genres[0] != "Drama" {
		t.Errorf("genres = %v, want [Drama Thriller] in provider order", got.Genres)
	}

	// Provenance travels with every field written.
	for _, field := range []string{
		domain.FieldOverview, domain.FieldCommunityRating,
		domain.FieldOfficialRating, domain.FieldPremiereDate, domain.FieldGenres,
	} {
		p, ok := got.Provenance[field]
		if !ok {
			t.Errorf("%s has no provenance", field)
			continue
		}
		if p.Source != "tmdb" {
			t.Errorf("%s source = %q, want tmdb", field, p.Source)
		}
		if p.Locked {
			t.Errorf("%s arrived locked; only a person locks a field", field)
		}
	}
}

// TestALockedFieldSurvivesARefresh is the constitution's rule, end to end.
func TestALockedFieldSurvivesARefresh(t *testing.T) {
	f := newMetadataFixture(t, &fakeProvider{meta: sampleMetadata()})
	ctx := context.Background()

	f.refresh(t, false)

	// A person corrects the overview, which locks it.
	if err := f.metadata.Set(ctx, f.item.ID, []service.Edit{
		{Field: domain.FieldOverview, Value: "The corrected overview."},
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The provider changes its mind about everything.
	newRating := 1.0
	f.provider.meta = metadata.MovieMetadata{
		Overview:        "A different overview.",
		CommunityRating: &newRating,
		OfficialRating:  "PG",
		ReleaseDate:     time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC),
		Genres:          []string{"Comedy"},
	}
	f.refresh(t, true)

	got := f.metadataOf(t)
	if got.Overview == nil || *got.Overview != "The corrected overview." {
		t.Errorf("overview = %v, want the locked correction", got.Overview)
	}
	if got.Provenance[domain.FieldOverview].Source != domain.MetadataSourceManual {
		t.Errorf("overview source = %q, want manual",
			got.Provenance[domain.FieldOverview].Source)
	}

	// Its unlocked neighbours DID update, which is what shows the lock is
	// per-field rather than the refresh having skipped the item entirely.
	if got.CommunityRating == nil || *got.CommunityRating != 1.0 {
		t.Errorf("community rating = %v, want the refreshed 1.0", got.CommunityRating)
	}
	if got.OfficialRating == nil || *got.OfficialRating != "PG" {
		t.Errorf("official rating = %v, want the refreshed PG", got.OfficialRating)
	}
	if len(got.Genres) != 1 || got.Genres[0] != "Comedy" {
		t.Errorf("genres = %v, want the refreshed [Comedy]", got.Genres)
	}
}

// TestARefreshCannotOverwriteALockTakenMidRun reaches the guarded write.
//
// TestALockedFieldSurvivesARefresh proves the value survives, but a refresh
// could reach that outcome by skipping the write for any reason — the same way
// TestManualIdentitySurvivesAPass passed while the identity guard was absent,
// because the item was filtered out before the guarded line ran.
//
// This calls the repository in the order the race produces: the refresh has
// already decided to write, and the lock is taken before the write lands. The
// only thing that can refuse it is the guard in the statement itself.
func TestARefreshCannotOverwriteALockTakenMidRun(t *testing.T) {
	f := newMetadataFixture(t, &fakeProvider{meta: sampleMetadata()})
	ctx := context.Background()
	repo := repository.NewMetadataRepository(f.pool)

	if _, err := repo.WriteField(ctx, f.item.ID, domain.FieldOverview, "tmdb", "original"); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// The person wins the race to the lock.
	if err := f.metadata.Set(ctx, f.item.ID, []service.Edit{
		{Field: domain.FieldOverview, Value: "the correction"},
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The refresh, already past its own checks, writes.
	written, err := repo.WriteField(ctx, f.item.ID, domain.FieldOverview, "tmdb", "the overwrite")
	if err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	if written {
		t.Error("the write was accepted against a locked field")
	}

	got := f.metadataOf(t)
	if got.Overview == nil || *got.Overview != "the correction" {
		t.Errorf("overview = %v, want the correction", got.Overview)
	}
	if got.Provenance[domain.FieldOverview].Source != domain.MetadataSourceManual {
		t.Errorf("source = %q, want manual: the provider stamped a field it could not write",
			got.Provenance[domain.FieldOverview].Source)
	}

	// The same guard on the list.
	if _, err := repo.WriteGenres(ctx, f.item.ID, "tmdb", []string{"Drama"}); err != nil {
		t.Fatalf("WriteGenres: %v", err)
	}
	if err := f.metadata.Set(ctx, f.item.ID, []service.Edit{
		{Field: domain.FieldGenres, Value: []string{"Documentary"}},
	}); err != nil {
		t.Fatalf("Set genres: %v", err)
	}
	written, err = repo.WriteGenres(ctx, f.item.ID, "tmdb", []string{"Comedy"})
	if err != nil {
		t.Fatalf("WriteGenres: %v", err)
	}
	if written {
		t.Error("the genre write was accepted against a locked list")
	}
	if got := f.metadataOf(t); len(got.Genres) != 1 || got.Genres[0] != "Documentary" {
		t.Errorf("genres = %v, want the locked [Documentary]", got.Genres)
	}
}

// An edit implies a lock, so that a correction does not silently revert. That
// is a default for the edit operation, not a merging of Source and Locked.
func TestAnEditLocksTheFieldItTouches(t *testing.T) {
	f := newMetadataFixture(t, &fakeProvider{meta: sampleMetadata()})
	ctx := context.Background()

	if err := f.metadata.Set(ctx, f.item.ID, []service.Edit{
		{Field: domain.FieldOfficialRating, Value: "15"},
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got := f.metadataOf(t)
	if !got.Locked(domain.FieldOfficialRating) {
		t.Error("an edited field is not locked; the next refresh would revert it")
	}
	// And only the field that was edited.
	if got.Locked(domain.FieldOverview) {
		t.Error("editing one field locked another")
	}
}

// Unlocking is explicit, and lets the provider win again.
func TestUnlockingLetsARefreshWriteAgain(t *testing.T) {
	f := newMetadataFixture(t, &fakeProvider{meta: sampleMetadata()})
	ctx := context.Background()

	if err := f.metadata.Set(ctx, f.item.ID, []service.Edit{
		{Field: domain.FieldOverview, Value: "mine"},
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.metadata.SetLocked(ctx, f.item.ID, domain.FieldOverview, false); err != nil {
		t.Fatalf("SetLocked: %v", err)
	}

	f.refresh(t, true)

	got := f.metadataOf(t)
	if got.Overview == nil || *got.Overview != sampleMetadata().Overview {
		t.Errorf("overview = %v, want the provider's after unlocking", got.Overview)
	}
	if got.Provenance[domain.FieldOverview].Source != "tmdb" {
		t.Errorf("source = %q, want tmdb", got.Provenance[domain.FieldOverview].Source)
	}
}

// The default refresh considers only items never fetched. A full re-fetch is
// one provider request per film, which is a cost somebody chooses.
func TestTheDefaultRefreshSkipsItemsAlreadyFetched(t *testing.T) {
	f := newMetadataFixture(t, &fakeProvider{meta: sampleMetadata()})

	f.refresh(t, false)
	after := f.provider.metaCalls
	if after == 0 {
		t.Fatal("the first refresh fetched nothing")
	}

	f.refresh(t, false)
	if f.provider.metaCalls != after {
		t.Errorf("the second default refresh made %d more fetches",
			f.provider.metaCalls-after)
	}

	f.refresh(t, true)
	if f.provider.metaCalls != after+1 {
		t.Errorf("all=true made %d fetches, want 1 more", f.provider.metaCalls-after)
	}
}

// A field the provider does not know is skipped, not written as null:
// overwriting a good value with "the provider has nothing" loses it to a bad
// fetch.
func TestAnAbsentProviderFieldDoesNotClearAStoredOne(t *testing.T) {
	f := newMetadataFixture(t, &fakeProvider{meta: sampleMetadata()})

	f.refresh(t, false)

	f.provider.meta = metadata.MovieMetadata{Overview: "still here"}
	f.refresh(t, true)

	got := f.metadataOf(t)
	if got.OfficialRating == nil || *got.OfficialRating != "R" {
		t.Errorf("official rating = %v, want the previously stored R", got.OfficialRating)
	}
	if got.CommunityRating == nil {
		t.Error("community rating was cleared by a fetch that did not mention it")
	}
}

// An unidentified film has no provider id, so there is nothing to fetch.
func TestRefreshSkipsUnidentifiedItems(t *testing.T) {
	f := newMetadataFixture(t, &fakeProvider{meta: sampleMetadata()})
	ctx := context.Background()

	if err := f.identity.Reset(ctx, f.item.ID); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	before := f.provider.metaCalls

	f.refresh(t, true)

	if f.provider.metaCalls != before {
		t.Errorf("fetched metadata for an unidentified item (%d calls)",
			f.provider.metaCalls-before)
	}
}

// One film that cannot be fetched must not abandon the pass.
func TestAFailedFetchIsNotFatal(t *testing.T) {
	f := newMetadataFixture(t, &fakeProvider{metaErr: errors.New("timed out")})

	if job := f.refresh(t, true); job.State != domain.JobStateCompleted {
		t.Errorf("job state = %s, want completed: %s", job.State, jobError(job))
	}
}

// Rate limiting stops the pass, as it does everywhere else.
func TestRateLimitStopsTheRefresh(t *testing.T) {
	f := newMetadataFixture(t, &fakeProvider{metaErr: metadata.ErrRateLimited})

	if job := f.refresh(t, true); job.State != domain.JobStateFailed {
		t.Errorf("job state = %s, want failed", job.State)
	}
}

func TestSetRejectsAnUnknownField(t *testing.T) {
	f := newMetadataFixture(t, &fakeProvider{})

	err := f.metadata.SetLocked(context.Background(), f.item.ID, "runtime", true)
	if !errors.Is(err, service.ErrInvalidArgument) {
		t.Errorf("err = %v, want ErrInvalidArgument — runtime is deliberately not managed", err)
	}
}

// addIdentifiedFilms writes n more films, scans them in, and gives each a
// manual identity so the refresh pass will consider it.
//
// Identity is set directly rather than run through the matcher because this
// test is about how far the refresh pass gets, not about what the matcher
// decides. Manual and matched are equally usable to ItemsNeedingMetadata, and
// going through SetManual means the test does not have to arrange n distinct
// films that all match one canned candidate.
func (f *metadataFixture) addIdentifiedFilms(t *testing.T, n int) []domain.MediaItem {
	t.Helper()

	for i := range n {
		name := fmt.Sprintf("Extra Film %02d (20%02d)", i, i)
		f.write(filepath.Join(name, name+".mkv"), 1024)
	}
	if job := f.scan(); job.State != domain.JobStateCompleted {
		t.Fatalf("scan did not complete: %s", job.State)
	}

	items, err := repository.NewMediaRepository(f.pool).
		ListItemsByLibrary(context.Background(), f.library)
	if err != nil {
		t.Fatalf("listing items: %v", err)
	}
	if len(items) != n+1 {
		t.Fatalf("library holds %d items, want %d", len(items), n+1)
	}

	for _, item := range items {
		if item.ID == f.item.ID {
			continue // already identified by the fixture
		}
		if err := f.identity.SetManual(context.Background(), item.ID,
			map[string]string{"tmdb": "550"}); err != nil {
			t.Fatalf("setting identity for %s: %v", item.Title, err)
		}
	}
	return items
}

// itemsWithMetadata counts how many items in the library hold a metadata row.
func (f *metadataFixture) itemsWithMetadata(t *testing.T) int {
	t.Helper()

	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(DISTINCT media_item_id) FROM media_item_metadata`).Scan(&n); err != nil {
		t.Fatalf("counting items with metadata: %v", err)
	}
	return n
}

// TestAFullRefreshReachesItemsBeyondOneBatch is the regression test for a
// refresh that could never see past its first batch.
//
// ItemsNeedingMetadata was a LIMIT over a fixed ORDER BY with no offset and no
// cursor. With ?all=true there is no "needs work" predicate, so fetching an
// item does not remove it from the selection set — every run re-selected the
// same first batch, and the item after it was unreachable by any number of
// runs. Correcting a field on a film past that boundary was impossible, and a
// full refresh was useless as a measurement of a library larger than a batch.
//
// THE BATCH IS SHRUNK RATHER THAN THE LIBRARY GROWN. At the real batch size of
// two hundred this test would need two hundred films to say anything at all;
// below that size it passes against the broken code, which is why the bug
// survived a six-film library. Five films and a batch of two crosses the same
// boundary three times over.
func TestAFullRefreshReachesItemsBeyondOneBatch(t *testing.T) {
	provider := &fakeProvider{meta: sampleMetadata()}
	f := newMetadataFixture(t, provider)

	const films = 5
	f.addIdentifiedFilms(t, films-1)
	f.metadata.SetBatchForTest(2)

	provider.metaCalls = 0
	if job := f.refresh(t, true); job.State != domain.JobStateCompleted {
		t.Fatalf("refresh state = %s, want completed (error: %v)", job.State, job.Error)
	}

	if provider.metaCalls != films {
		t.Errorf("provider was asked about %d films, want %d — the pass stopped at a batch boundary",
			provider.metaCalls, films)
	}
	if got := f.itemsWithMetadata(t); got != films {
		t.Errorf("%d films hold metadata, want %d", got, films)
	}
}

// TestTheDefaultRefreshReachesItemsBeyondOneBatch pins the same boundary for
// the default pass.
//
// The default pass concealed the bug rather than escaping it: fetching an item
// removes it from the "never fetched" set, so a SECOND run moved on by itself
// and the library eventually finished across several runs. It still has to
// finish in ONE, and the cursor is what makes that true — so this asserts the
// whole library lands from a single pass.
func TestTheDefaultRefreshReachesItemsBeyondOneBatch(t *testing.T) {
	provider := &fakeProvider{meta: sampleMetadata()}
	f := newMetadataFixture(t, provider)

	const films = 5
	f.addIdentifiedFilms(t, films-1)
	f.metadata.SetBatchForTest(2)

	provider.metaCalls = 0
	if job := f.refresh(t, false); job.State != domain.JobStateCompleted {
		t.Fatalf("refresh state = %s, want completed (error: %v)", job.State, job.Error)
	}

	if got := f.itemsWithMetadata(t); got != films {
		t.Errorf("one default pass reached %d films, want %d", got, films)
	}
}

// TestAFullRefreshReportsTheWholeLibraryAsItsTotal pins job progress against
// the batch size.
//
// Progress is what an operator watches during the library-wide passes this
// milestone is about to run for real, so a total that describes the current
// batch rather than the whole pass would read as a job finishing five times
// over. The count is taken once, before the first batch.
func TestAFullRefreshReportsTheWholeLibraryAsItsTotal(t *testing.T) {
	provider := &fakeProvider{meta: sampleMetadata()}
	f := newMetadataFixture(t, provider)

	const films = 5
	f.addIdentifiedFilms(t, films-1)
	f.metadata.SetBatchForTest(2)

	job := f.refresh(t, true)
	if job.State != domain.JobStateCompleted {
		t.Fatalf("refresh state = %s, want completed (error: %v)", job.State, job.Error)
	}
	if job.ProgressTotal != films {
		t.Errorf("job total = %d, want %d — the total describes a batch, not the pass",
			job.ProgressTotal, films)
	}
}
