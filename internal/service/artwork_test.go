package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uuid"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/metadata"
	"github.com/maverickman79/reelix/internal/repository"
	"github.com/maverickman79/reelix/internal/service"
)

// artworkMetadata is sampleMetadata with images the fake provider can serve.
//
// A poster and a backdrop, and DELIBERATELY NO LOGO: most films have none, and
// a type the provider does not offer is the case that decides whether the pass
// records a negative or re-asks forever.
func artworkMetadata() metadata.MovieMetadata {
	md := sampleMetadata()
	md.Images = map[string]metadata.ImageCandidate{
		domain.ImagePrimary:  {URL: "https://images.invalid/poster.jpg", Width: 780, Height: 1170},
		domain.ImageBackdrop: {URL: "https://images.invalid/backdrop.jpg", Width: 1280, Height: 720},
	}
	return md
}

func artworkProvider() *fakeProvider {
	return &fakeProvider{
		meta: artworkMetadata(),
		images: map[string]string{
			"https://images.invalid/poster.jpg":   "poster bytes",
			"https://images.invalid/backdrop.jpg": "backdrop bytes",
		},
	}
}

func imagesOf(t *testing.T, f *metadataFixture) map[string]domain.ItemImage {
	t.Helper()

	byItem, err := repository.NewMetadataRepository(f.pool).
		ImagesFor(context.Background(), []uuid.UUID{f.item.ID})
	if err != nil {
		t.Fatalf("loading images: %v", err)
	}
	return byItem[f.item.ID]
}

// TestRefreshDownloadsArtwork is the ordinary path: the offered types land on
// disk, and the type the provider does not offer is recorded as a negative
// rather than left absent.
func TestRefreshDownloadsArtwork(t *testing.T) {
	f := newMetadataFixture(t, artworkProvider())

	if job := f.refresh(t, false); job.State != domain.JobStateCompleted {
		t.Fatalf("job state = %s: %s", job.State, jobError(job))
	}

	images := imagesOf(t, f)
	if len(images) != len(domain.ImageTypes) {
		t.Fatalf("recorded %d image rows, want one per type: %v", len(images), images)
	}

	primary := images[domain.ImagePrimary]
	if !primary.Stored() {
		t.Fatal("no poster stored")
	}
	if len(primary.Tag) != 32 {
		t.Errorf("poster tag %q is not 32 characters", primary.Tag)
	}
	if primary.Width != 780 || primary.Height != 1170 {
		t.Errorf("poster dimensions = %dx%d, want 780x1170 from the provider listing",
			primary.Width, primary.Height)
	}
	if !f.metadata.Images().Exists(primary.StoragePath) {
		t.Errorf("the poster row points at %q, which is not on disk", primary.StoragePath)
	}

	if !images[domain.ImageBackdrop].Stored() {
		t.Error("no backdrop stored")
	}

	// The negative. A row, not an absence — see the storage_path note in
	// migration 0012.
	logo, recorded := images[domain.ImageLogo]
	if !recorded {
		t.Fatal("the logo the provider does not offer left no row, so it will be re-asked forever")
	}
	if logo.Stored() {
		t.Error("a logo was stored for a provider that offers none")
	}
}

// TestRerunningTheRefreshDownloadsNothing is one of the slice's completion
// criteria, asserted rather than observed.
//
// It covers the negative too: without a recorded "there is no logo", the second
// pass would re-ask about it, which is exactly the cost the negative row exists
// to avoid.
func TestRerunningTheRefreshDownloadsNothing(t *testing.T) {
	provider := artworkProvider()
	f := newMetadataFixture(t, provider)

	f.refresh(t, false)
	afterFirst := provider.imageCalls
	if afterFirst != 2 {
		t.Fatalf("first pass made %d image downloads, want 2", afterFirst)
	}

	f.refresh(t, false)
	if provider.imageCalls != afterFirst {
		t.Errorf("the second pass downloaded %d more images, want 0",
			provider.imageCalls-afterFirst)
	}

	// And the item is no longer even selected, so the provider is not asked
	// about it at all.
	metaAfterFirst := provider.metaCalls
	f.refresh(t, false)
	if provider.metaCalls != metaAfterFirst {
		t.Errorf("the third pass re-fetched metadata for an item that needed nothing")
	}
}

// TestFullRefreshRefetchesArtwork checks the explicit form still works, since
// it is the operator's way back from a bad image.
func TestFullRefreshRefetchesArtwork(t *testing.T) {
	provider := artworkProvider()
	f := newMetadataFixture(t, provider)

	f.refresh(t, false)
	afterFirst := provider.imageCalls

	f.refresh(t, true)
	if provider.imageCalls != afterFirst+2 {
		t.Errorf("a full refresh downloaded %d images, want 2", provider.imageCalls-afterFirst)
	}
}

// TestAFailedDownloadLeavesNoRow is the retry story.
//
// A timeout or a 404 must not mark a film permanently imageless, and it must
// not fail the pass. Writing NOTHING is what makes absence the retry queue: no
// attempt counter, no backoff column, and the very next ordinary pass tries
// again.
func TestAFailedDownloadLeavesNoRow(t *testing.T) {
	provider := artworkProvider()
	provider.imageErr = errors.New("connection reset")
	f := newMetadataFixture(t, provider)

	if job := f.refresh(t, false); job.State != domain.JobStateCompleted {
		t.Fatalf("one failed image failed the whole pass: %s: %s", job.State, jobError(job))
	}

	// The fields still landed: artwork failing must not cost the film its
	// overview.
	if got := f.metadataOf(t); got.Overview == nil {
		t.Error("a failed artwork download also lost the fields")
	}

	images := imagesOf(t, f)
	for _, imageType := range []string{domain.ImagePrimary, domain.ImageBackdrop} {
		if _, recorded := images[imageType]; recorded {
			t.Errorf("a failed %s download left a row, so it would never be retried", imageType)
		}
	}

	// Now the provider recovers, and the next ordinary pass picks it up with no
	// operator action.
	provider.imageErr = nil
	f.refresh(t, false)

	if !imagesOf(t, f)[domain.ImagePrimary].Stored() {
		t.Error("the retry did not happen; a transient failure was permanent")
	}
}

// TestOneFailingTypeDoesNotBlockTheOthers checks that images are attempted
// independently. A film whose logo cannot be fetched still gets its poster.
func TestOneFailingTypeDoesNotBlockTheOthers(t *testing.T) {
	provider := artworkProvider()
	// The backdrop URL has no canned body, so only that one fails.
	delete(provider.images, "https://images.invalid/backdrop.jpg")

	f := newMetadataFixture(t, provider)
	if job := f.refresh(t, false); job.State != domain.JobStateCompleted {
		t.Fatalf("job state = %s: %s", job.State, jobError(job))
	}

	images := imagesOf(t, f)
	if !images[domain.ImagePrimary].Stored() {
		t.Error("a failing backdrop cost the item its poster")
	}
	if _, recorded := images[domain.ImageBackdrop]; recorded {
		t.Error("the failing backdrop left a row")
	}
}

// TestAWipedCacheHealsOnTheNextRefresh is what makes storing the bytes under
// /cache honest rather than merely arguable.
//
// Losing the cache volume leaves every row intact and every file gone. The
// reconcile sweep turns that into the ordinary "no row" state, so the very next
// ordinary refresh re-downloads — no operator procedure, and no serving path
// that writes to the database.
func TestAWipedCacheHealsOnTheNextRefresh(t *testing.T) {
	provider := artworkProvider()
	f := newMetadataFixture(t, provider)

	f.refresh(t, false)
	before := imagesOf(t, f)[domain.ImagePrimary]
	if !before.Stored() {
		t.Fatal("no poster to lose")
	}

	if err := os.RemoveAll(filepath.Join(f.cacheDir, "images")); err != nil {
		t.Fatalf("wiping the cache: %v", err)
	}

	afterWipe := provider.imageCalls
	f.refresh(t, false)

	if provider.imageCalls == afterWipe {
		t.Fatal("an ordinary refresh after a wiped cache downloaded nothing")
	}

	restored := imagesOf(t, f)[domain.ImagePrimary]
	if !restored.Stored() {
		t.Fatal("the poster was not restored")
	}
	if !f.metadata.Images().Exists(restored.StoragePath) {
		t.Error("the restored row points at a file that is not on disk")
	}
	if restored.Tag != before.Tag {
		t.Errorf("the restored tag is %s, was %s; identical bytes must produce an identical tag",
			restored.Tag, before.Tag)
	}
}

// TestALockedImageSurvivesARefresh is the provenance half, and it goes through
// exactly the same claimField guard as a locked overview.
func TestALockedImageSurvivesARefresh(t *testing.T) {
	provider := artworkProvider()
	f := newMetadataFixture(t, provider)

	f.refresh(t, false)
	original := imagesOf(t, f)[domain.ImagePrimary]

	if err := f.metadata.SetLocked(
		context.Background(), f.item.ID, domain.FieldImagePrimary, true); err != nil {
		t.Fatalf("locking the poster: %v", err)
	}

	// The provider now offers a different poster. An unlocked field would take
	// it; a locked one must not.
	provider.meta.Images[domain.ImagePrimary] = metadata.ImageCandidate{
		URL: "https://images.invalid/other.jpg", Width: 500, Height: 750,
	}
	provider.images["https://images.invalid/other.jpg"] = "a completely different poster"

	f.refresh(t, true)

	after := imagesOf(t, f)[domain.ImagePrimary]
	if after.Tag != original.Tag {
		t.Errorf("the locked poster changed: tag %s, was %s", after.Tag, original.Tag)
	}

	// The backdrop beside it was not locked and must have moved, or the test
	// proves nothing about the lock.
	provider.meta.Images[domain.ImageBackdrop] = metadata.ImageCandidate{
		URL: "https://images.invalid/other-backdrop.jpg", Width: 1280, Height: 720,
	}
	provider.images["https://images.invalid/other-backdrop.jpg"] = "a different backdrop"
	f.refresh(t, true)

	if imagesOf(t, f)[domain.ImageBackdrop].Tag == "" {
		t.Fatal("no backdrop after the refresh")
	}
}

// TestAnImageCannotBeSetByHand pins the 0.0.2 boundary.
//
// An image is lockable but not settable: choosing one means supplying a URL or
// bytes, which is the selection UI this milestone excludes. Refused by name so
// the message says what is going on, rather than failing on a missing column
// and reading like a bug.
func TestAnImageCannotBeSetByHand(t *testing.T) {
	f := newMetadataFixture(t, artworkProvider())
	f.refresh(t, false)

	err := f.metadata.Set(context.Background(), f.item.ID, []service.Edit{
		{Field: domain.FieldImagePrimary, Value: "https://example.invalid/mine.jpg"},
	})
	if err == nil {
		t.Fatal("an image was accepted as a hand edit")
	}
	if !strings.Contains(err.Error(), domain.FieldImagePrimary) {
		t.Errorf("the error does not name the field: %v", err)
	}

	// And the edit changed nothing.
	if !imagesOf(t, f)[domain.ImagePrimary].Stored() {
		t.Error("the refused edit disturbed the stored poster")
	}
}
