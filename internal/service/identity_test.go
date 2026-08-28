package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
	"uuid"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/metadata"
	"github.com/maverickman79/reelix/internal/repository"
	"github.com/maverickman79/reelix/internal/service"
)

// fakeProvider answers from a canned candidate list and counts its calls.
//
// No test in this package reaches the real TMDB. A suite that did would pass
// slowly, unrepeatably, and only while somebody else's API was up.
type fakeProvider struct {
	candidates []metadata.Candidate
	extra      map[string]string
	// altTitles are the alternative titles returned per provider id.
	altTitles map[string][]string

	searchErr error
	idsErr    error
	altErr    error

	searches int
	idCalls  int
	// altCalls counts alternative-title lookups. The count is the assertion in
	// the cost tests: a well-named film must cost zero.
	altCalls int
	// altAsked records which provider ids were asked about, which is how the
	// year window is checked.
	altAsked []string
}

func (f *fakeProvider) Name() string { return "tmdb" }

func (f *fakeProvider) SearchMovie(context.Context, metadata.MovieQuery) ([]metadata.Candidate, error) {
	f.searches++
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.candidates, nil
}

func (f *fakeProvider) ExternalIDs(context.Context, string) (map[string]string, error) {
	f.idCalls++
	if f.idsErr != nil {
		return nil, f.idsErr
	}
	return f.extra, nil
}

func (f *fakeProvider) AlternativeTitles(_ context.Context, id string) ([]string, error) {
	f.altCalls++
	f.altAsked = append(f.altAsked, id)
	if f.altErr != nil {
		return nil, f.altErr
	}
	return f.altTitles[id], nil
}

// identifyFixture is a scanned library plus an identity service over a fake
// provider.
type identifyFixture struct {
	*scanFixture
	provider *fakeProvider
	identity *service.IdentityService
	item     domain.MediaItem
}

func newIdentifyFixture(t *testing.T, provider *fakeProvider) *identifyFixture {
	t.Helper()

	f := newScanFixture(t)
	f.write("Fight Club (1999)/Fight Club (1999).mkv", 1024)
	if job := f.scan(); job.State != domain.JobStateCompleted {
		t.Fatalf("scan did not complete: %s", job.State)
	}

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &identifyFixture{
		scanFixture: f,
		provider:    provider,
		identity:    service.NewIdentityService(f.pool, provider, discard),
		item:        f.onlyItem(t),
	}
}

// runIdentify starts a pass and waits for it to leave the running state.
func (f *identifyFixture) runIdentify(t *testing.T) domain.Job {
	t.Helper()

	job, err := f.identity.Start(context.Background(), f.library)
	if err != nil {
		t.Fatalf("starting identify: %v", err)
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
	t.Fatal("identify pass did not finish")
	return domain.Job{}
}

func (f *identifyFixture) identityOf(t *testing.T) domain.Identity {
	t.Helper()

	got, err := f.identity.Get(context.Background(), f.item.ID)
	if err != nil {
		t.Fatalf("reading identity: %v", err)
	}
	return got
}

// A scanned item must arrive pending, so a pass has something to find and so
// that "no external id" is never the only thing distinguishing states.
func TestScanCreatesPendingIdentity(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{})

	if got := f.identityOf(t); got.Status != domain.IdentityPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

func TestIdentifyMatchesAndStoresBothIDs(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "550"}, Title: "Fight Club", Year: 1999},
		},
		extra: map[string]string{"imdb": "tt0137523"},
	})

	if job := f.runIdentify(t); job.State != domain.JobStateCompleted {
		t.Fatalf("job state = %s: %s", job.State, jobError(job))
	}

	got := f.identityOf(t)
	if got.Status != domain.IdentityMatched {
		t.Fatalf("status = %q, want matched", got.Status)
	}
	if got.Provider == nil || *got.Provider != "tmdb" {
		t.Errorf("provider = %v, want tmdb", got.Provider)
	}
	if got.Confidence == nil || *got.Confidence != string(metadata.ConfidenceExact) {
		t.Errorf("confidence = %v, want exact", got.Confidence)
	}
	if got.ExternalIDs["tmdb"] != "550" {
		t.Errorf("tmdb id = %q, want 550", got.ExternalIDs["tmdb"])
	}
	// The cross-provider id is identity, not a field: the watch-history
	// importer matches exports that may be keyed on either.
	if got.ExternalIDs["imdb"] != "tt0137523" {
		t.Errorf("imdb id = %q, want tt0137523", got.ExternalIDs["imdb"])
	}
	if got.Reason != nil {
		t.Errorf("a matched item carries a reason: %v", *got.Reason)
	}
}

// An ambiguous answer is recorded as unmatched WITH a reason, and the reason
// is the whole value of the row: without it an operator learns only that
// something did not work, which is what makes people re-run passes hoping for
// a different answer.
func TestIdentifyRecordsUnmatchedWithAReason(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "a"}, Title: "Fight Club", Year: 1999},
			{IDs: map[string]string{"tmdb": "b"}, Title: "Fight Club", Year: 1999},
		},
	})

	f.runIdentify(t)

	got := f.identityOf(t)
	if got.Status != domain.IdentityUnmatched {
		t.Fatalf("status = %q, want unmatched", got.Status)
	}
	if got.Reason == nil || *got.Reason == "" {
		t.Error("unmatched without a reason")
	}
	if len(got.ExternalIDs) != 0 {
		t.Errorf("an unmatched item carries ids: %v", got.ExternalIDs)
	}
	// No second call: nothing matched, so there was nothing to resolve ids for.
	if f.provider.idCalls != 0 {
		t.Errorf("resolved external ids for an unmatched item (%d calls)", f.provider.idCalls)
	}
}

// A provider failure is NOT a decision. Recording it as unmatched would claim
// a judgement nobody made and would stop the next pass retrying the item.
func TestProviderFailureLeavesTheItemPending(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{searchErr: errors.New("connection refused")})

	if job := f.runIdentify(t); job.State != domain.JobStateCompleted {
		t.Fatalf("one unreachable item failed the whole pass: %s", jobError(job))
	}

	if got := f.identityOf(t); got.Status != domain.IdentityPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

// Rate limiting is the one error where continuing makes things worse.
func TestRateLimitStopsThePass(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{searchErr: metadata.ErrRateLimited})

	if job := f.runIdentify(t); job.State != domain.JobStateFailed {
		t.Errorf("job state = %s, want failed", job.State)
	}
	if got := f.identityOf(t); got.Status != domain.IdentityPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

// A second pass must not re-ask about anything already decided. This is what
// makes running the pass cheap and what stops an unmatched item being quietly
// retried into a guess.
func TestASecondPassAsksAboutNothingAlreadyDecided(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "550"}, Title: "Fight Club", Year: 1999},
		},
	})

	f.runIdentify(t)
	after := f.provider.searches
	if after == 0 {
		t.Fatal("the first pass asked nothing")
	}

	f.runIdentify(t)
	if f.provider.searches != after {
		t.Errorf("the second pass made %d more searches", f.provider.searches-after)
	}
}

// Manual is the highest-authority state. Declining to guess is only workable
// if a correction is easy and STICKS -- an operator who fixes one film must
// not find it gone after the next run.
func TestManualIdentitySurvivesAPass(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "550"}, Title: "Fight Club", Year: 1999},
		},
	})

	ctx := context.Background()
	if err := f.identity.SetManual(ctx, f.item.ID, map[string]string{"tmdb": "999"}); err != nil {
		t.Fatalf("SetManual: %v", err)
	}

	f.runIdentify(t)

	got := f.identityOf(t)
	if got.Status != domain.IdentityManual {
		t.Fatalf("status = %q, want manual", got.Status)
	}
	if got.ExternalIDs["tmdb"] != "999" {
		t.Errorf("tmdb id = %q, want the hand-set 999", got.ExternalIDs["tmdb"])
	}
	if f.provider.searches != 0 {
		t.Errorf("the pass asked about a manually identified item (%d searches)", f.provider.searches)
	}
}

// TestAPassCannotOverwriteAManualIdentitySetMidRun reaches the guard that
// TestManualIdentitySurvivesAPass does not.
//
// That test proves the pass SKIPS manual items, because Pending never returns
// them. It cannot reach the write itself. The dangerous case is the race: a
// pass reads its batch while an item is still pending, somebody corrects that
// item by hand, and the pass then writes its own answer over the correction.
//
// Reproduced here by calling the repository in the order the race produces.
// Found by fault injection -- removing the guard left the whole suite green.
func TestAPassCannotOverwriteAManualIdentitySetMidRun(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{})
	ctx := context.Background()
	repo := repository.NewIdentityRepository(f.pool)

	// The human wins the race to the database.
	if err := f.identity.SetManual(ctx, f.item.ID, map[string]string{"tmdb": "999"}); err != nil {
		t.Fatalf("SetManual: %v", err)
	}

	// The pass, holding a batch it read while the item was still pending,
	// writes its own answer.
	if err := repo.RecordMatch(ctx, f.item.ID, "tmdb", "exact", "primary",
		map[string]string{"tmdb": "550"}); err != nil {
		t.Fatalf("RecordMatch: %v", err)
	}

	got := f.identityOf(t)
	if got.Status != domain.IdentityManual {
		t.Errorf("status = %q, want manual: the pass overwrote a correction", got.Status)
	}
	if got.ExternalIDs["tmdb"] != "999" {
		t.Errorf("tmdb id = %q, want the hand-set 999", got.ExternalIDs["tmdb"])
	}

	// The same guard on the decline path: a pass must not mark a
	// hand-identified film unmatched either.
	if err := repo.RecordUnmatched(ctx, f.item.ID, "ambiguous"); err != nil {
		t.Fatalf("RecordUnmatched: %v", err)
	}
	if got := f.identityOf(t); got.Status != domain.IdentityManual {
		t.Errorf("status = %q, want manual after a decline", got.Status)
	}
}

// Correcting an identity must REMOVE the old id, not leave it beside the new
// one -- otherwise the importer still resolves the wrong film.
func TestSettingAnIdentityReplacesTheOldIDs(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{})
	ctx := context.Background()

	if err := f.identity.SetManual(ctx, f.item.ID, map[string]string{"tmdb": "111", "imdb": "tt111"}); err != nil {
		t.Fatalf("SetManual: %v", err)
	}
	if err := f.identity.SetManual(ctx, f.item.ID, map[string]string{"tmdb": "222"}); err != nil {
		t.Fatalf("SetManual: %v", err)
	}

	got := f.identityOf(t)
	if got.ExternalIDs["tmdb"] != "222" {
		t.Errorf("tmdb id = %q, want 222", got.ExternalIDs["tmdb"])
	}
	if _, ok := got.ExternalIDs["imdb"]; ok {
		t.Errorf("the replaced imdb id survived: %v", got.ExternalIDs)
	}

	// And the reverse lookup, which is the importer's query, must not still
	// resolve the id that was removed.
	if _, err := repository.NewIdentityRepository(f.pool).
		FindByExternalID(ctx, "tmdb", "111"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("the old tmdb id still resolves: %v", err)
	}
}

// Reset is the deliberate act that re-running a pass is not.
func TestResetReturnsAnItemToPending(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "550"}, Title: "Fight Club", Year: 1999},
		},
	})
	ctx := context.Background()

	f.runIdentify(t)
	if got := f.identityOf(t); got.Status != domain.IdentityMatched {
		t.Fatalf("status = %q, want matched", got.Status)
	}

	if err := f.identity.Reset(ctx, f.item.ID); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	got := f.identityOf(t)
	if got.Status != domain.IdentityPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if len(got.ExternalIDs) != 0 {
		t.Errorf("reset kept its ids: %v", got.ExternalIDs)
	}
}

// The importer's lookup, in the direction the importer uses it.
func TestFindByExternalIDResolvesTheItem(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "550"}, Title: "Fight Club", Year: 1999},
		},
		extra: map[string]string{"imdb": "tt0137523"},
	})
	ctx := context.Background()

	f.runIdentify(t)

	repo := repository.NewIdentityRepository(f.pool)
	for _, tc := range []struct{ provider, id string }{
		{"tmdb", "550"},
		{"imdb", "tt0137523"},
	} {
		got, err := repo.FindByExternalID(ctx, tc.provider, tc.id)
		if err != nil {
			t.Fatalf("FindByExternalID(%s, %s): %v", tc.provider, tc.id, err)
		}
		if got != f.item.ID {
			t.Errorf("%s resolved to %s, want %s", tc.provider, got, f.item.ID)
		}
	}
}

// Losing the whole identification because a second request failed would trade
// the thing that matters for the thing that helps.
func TestAFailedIDLookupStillMatches(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "550"}, Title: "Fight Club", Year: 1999},
		},
		idsErr: errors.New("timed out"),
	})

	f.runIdentify(t)

	got := f.identityOf(t)
	if got.Status != domain.IdentityMatched {
		t.Fatalf("status = %q, want matched", got.Status)
	}
	if got.ExternalIDs["tmdb"] != "550" {
		t.Errorf("tmdb id = %q, want 550", got.ExternalIDs["tmdb"])
	}
}

func TestSetManualRejectsAnEmptyIDSet(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{})

	err := f.identity.SetManual(context.Background(), f.item.ID, map[string]string{})
	if !errors.Is(err, service.ErrInvalidArgument) {
		t.Errorf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestIdentifyUnknownLibrary(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{})

	_, err := f.identity.Start(context.Background(), uuid.NewV7())
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// jobError renders a job's failure for a test message.
func jobError(j domain.Job) string {
	if j.Error == nil {
		return "(no error recorded)"
	}
	return *j.Error
}

// TestAlternativeTitlesAreNotFetchedForAMatchedFilm is the cost assertion.
//
// Five of the six films in the real library match on their primary title. If
// those cost an extra request each, a library-wide pass costs one request per
// film for nothing. The second pass must run only on the one decline kind it
// can rescue.
func TestAlternativeTitlesAreNotFetchedForAMatchedFilm(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "550"}, Title: "Fight Club", Year: 1999},
		},
	})

	f.runIdentify(t)

	if got := f.identityOf(t); got.Status != domain.IdentityMatched {
		t.Fatalf("status = %q, want matched", got.Status)
	}
	if f.provider.altCalls != 0 {
		t.Errorf("a film matching on its primary title cost %d alternative-title lookups",
			f.provider.altCalls)
	}
	if got := f.identityOf(t); got.MatchedVia == nil || *got.MatchedVia != "primary" {
		t.Errorf("matched_via = %v, want primary", got.MatchedVia)
	}
}

// TestAlternativeTitlesRescueARenamedRelease is the Aang case end to end.
func TestAlternativeTitlesRescueARenamedRelease(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "980431"}, Title: "Avatar Aang: The Last Airbender", Year: 1999},
		},
		altTitles: map[string][]string{
			// The scanned fixture film is Fight Club (1999); the shape being
			// tested is a primary title that misses and an alternative that
			// hits, which is what the Aang release does.
			"980431": {"Aang: The Last Airbender", "Fight Club"},
		},
	})

	f.runIdentify(t)

	got := f.identityOf(t)
	if got.Status != domain.IdentityMatched {
		t.Fatalf("status = %q, want matched", got.Status)
	}
	if got.ExternalIDs["tmdb"] != "980431" {
		t.Errorf("tmdb id = %q, want 980431", got.ExternalIDs["tmdb"])
	}
	if got.MatchedVia == nil || *got.MatchedVia != "alternative" {
		t.Errorf("matched_via = %v, want alternative — this is the evidence base", got.MatchedVia)
	}
	if f.provider.altCalls != 1 {
		t.Errorf("alternative-title lookups = %d, want 1", f.provider.altCalls)
	}
}

// TestAlternativeTitlesAreNotFetchedForAnAmbiguousDecline pins the gate.
//
// More titles cannot resolve an ambiguity; they can only deepen it. Asking
// would spend requests to make a decline more certain.
func TestAlternativeTitlesAreNotFetchedForAnAmbiguousDecline(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "a"}, Title: "Fight Club", Year: 1999},
			{IDs: map[string]string{"tmdb": "b"}, Title: "Fight Club", Year: 1999},
		},
	})

	f.runIdentify(t)

	if got := f.identityOf(t); got.Status != domain.IdentityUnmatched {
		t.Fatalf("status = %q, want unmatched", got.Status)
	}
	if f.provider.altCalls != 0 {
		t.Errorf("an ambiguous decline cost %d alternative-title lookups", f.provider.altCalls)
	}
}

// TestAlternativeTitleLookupsRespectTheYearWindow checks the cost control
// against the service rather than against the helper.
//
// Only the candidate within a year is asked about. The others can never reach
// a matching tier whatever they are called, so asking would buy nothing.
func TestAlternativeTitleLookupsRespectTheYearWindow(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "near"}, Title: "Something Else", Year: 2000},
			{IDs: map[string]string{"tmdb": "far"}, Title: "Something Else", Year: 1970},
			{IDs: map[string]string{"tmdb": "alsofar"}, Title: "Something Else", Year: 2020},
		},
	})

	f.runIdentify(t)

	if len(f.provider.altAsked) != 1 || f.provider.altAsked[0] != "near" {
		t.Errorf("asked about %v, want only the candidate within a year", f.provider.altAsked)
	}
}

// A failed alternative-title lookup must not abandon the item: the worst
// outcome is the decline that was already there.
func TestAFailedAlternativeTitleLookupIsNotFatal(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "1"}, Title: "Something Else", Year: 1999},
		},
		altErr: errors.New("timed out"),
	})

	if job := f.runIdentify(t); job.State != domain.JobStateCompleted {
		t.Fatalf("a failed lookup failed the pass: %s", jobError(job))
	}
	if got := f.identityOf(t); got.Status != domain.IdentityUnmatched {
		t.Errorf("status = %q, want unmatched", got.Status)
	}
}

// Rate limiting during the second pass stops the pass, exactly as it does
// during a search: continuing would keep asking a provider that has said no.
func TestRateLimitDuringAlternativeTitlesStopsThePass(t *testing.T) {
	f := newIdentifyFixture(t, &fakeProvider{
		candidates: []metadata.Candidate{
			{IDs: map[string]string{"tmdb": "1"}, Title: "Something Else", Year: 1999},
		},
		altErr: metadata.ErrRateLimited,
	})

	if job := f.runIdentify(t); job.State != domain.JobStateFailed {
		t.Errorf("job state = %s, want failed", job.State)
	}
	if got := f.identityOf(t); got.Status != domain.IdentityPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
}
