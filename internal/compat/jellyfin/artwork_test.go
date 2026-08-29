package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uuid"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/repository"
)

// decodeInto unmarshals a response body or fails the test.
func decodeInto(t *testing.T, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decoding response: %v: %s", err, raw)
	}
}

// removeCachedImages wipes the artwork store the way losing a cache volume
// does, leaving every database row intact.
func removeCachedImages(h *harness) error {
	return os.RemoveAll(filepath.Join(h.cacheDir, "images"))
}

// posterBytes stands in for a downloaded image. The serving path never decodes
// one, so the content only has to be distinguishable.
const posterBytes = "\xff\xd8\xff\xe0 pretend jpeg"

// seedImage puts bytes in the artwork store and records the row, in that order
// — which is the same order the refresh pass uses and the reason a crash
// between them cannot advertise an image that is not there.
func seedImage(t *testing.T, h *harness, itemID uuid.UUID, imageType, body string) domain.ItemImage {
	t.Helper()

	saved, err := h.metadata.Images().Save(itemID, imageType, "image/jpeg", strings.NewReader(body))
	if err != nil {
		t.Fatalf("saving %s image: %v", imageType, err)
	}

	img := domain.ItemImage{
		MediaItemID: itemID,
		ImageType:   imageType,
		StoragePath: saved.Path,
		Tag:         saved.Tag,
		ContentType: saved.ContentType,
		Width:       600,
		Height:      900,
		SourceURL:   "https://example.invalid/" + imageType + ".jpg",
	}

	written, err := repository.NewMetadataRepository(h.pool).
		WriteImage(context.Background(), itemID, imageType, "tmdb", img)
	if err != nil {
		t.Fatalf("recording %s image: %v", imageType, err)
	}
	if !written {
		t.Fatalf("the %s image was refused by the lock", imageType)
	}
	return img
}

// TestItemImageIsServed is the ordinary path, and it checks the headers that
// were read off the recorded reference responses rather than assumed:
// Cache-Control and Last-Modified, with no ETag.
func TestItemImageIsServed(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	item := seeded.byTitle["Idiocracy"]
	seedImage(t, h, item.ID, domain.ImagePrimary, posterBytes)

	resp := h.do(http.MethodGet, "/Items/"+compatID(item.ID)+"/Images/Primary", h.login(), nil)
	body := h.bodyOf(resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", resp.StatusCode, body)
	}
	if string(body) != posterBytes {
		t.Errorf("served %q, want the stored bytes", body)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "public" {
		t.Errorf("Cache-Control = %q, want public", got)
	}
	if resp.Header.Get("Last-Modified") == "" {
		t.Error("no Last-Modified; the recorded reference sends one and clients revalidate with it")
	}
}

// TestItemImageIsPublic pins the second unauthenticated route on the
// compatibility surface.
//
// Wholphin requests posters from a component that carries no credential, and a
// 401 there is a RETRY where a 404 is final — which is what turns a missing
// poster into a loop. See the registration in api.go for what serving these
// without a token concedes.
func TestItemImageIsPublic(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	item := seeded.byTitle["Congo"]
	seedImage(t, h, item.ID, domain.ImagePrimary, posterBytes)

	// No token, exactly as the media component sends it.
	resp := h.do(http.MethodGet, "/Items/"+compatID(item.ID)+"/Images/Primary", "", nil)
	body := h.bodyOf(resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 without a token: %s", resp.StatusCode, body)
	}
	if string(body) != posterBytes {
		t.Errorf("served %q, want the stored bytes", body)
	}
}

// TestItemImageAnswers304 checks the conditional request the capture recorded:
// a client returns with If-Modified-Since and the reference answers 304.
func TestItemImageAnswers304(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	item := seeded.byTitle["Idiocracy"]
	seedImage(t, h, item.ID, domain.ImagePrimary, posterBytes)

	path := "/Items/" + compatID(item.ID) + "/Images/Primary"
	first := h.do(http.MethodGet, path, "", nil)
	h.bodyOf(first)

	lastModified := first.Header.Get("Last-Modified")
	if lastModified == "" {
		t.Fatal("no Last-Modified to revalidate with")
	}

	req, err := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
	if err != nil {
		t.Fatalf("building the conditional request: %v", err)
	}
	req.Header.Set("If-Modified-Since", lastModified)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("issuing the conditional request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("status %d, want 304", resp.StatusCode)
	}
}

// TestItemImageMissingFromDiskIs404 covers the state a wiped cache directory
// produces: the row says there are bytes and there are not.
//
// 404 rather than 500, because the client's correct response is identical to
// any other missing image and a 500 invites a retry that cannot succeed. The
// read path deliberately writes nothing; the repair belongs to the refresh
// pass's reconcile sweep.
func TestItemImageMissingFromDiskIs404(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	item := seeded.byTitle["Idiocracy"]
	seedImage(t, h, item.ID, domain.ImagePrimary, posterBytes)

	if err := removeCachedImages(h); err != nil {
		t.Fatalf("wiping the cache: %v", err)
	}

	resp := h.do(http.MethodGet, "/Items/"+compatID(item.ID)+"/Images/Primary", "", nil)
	body := h.bodyOf(resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", resp.StatusCode, body)
	}

	// The row must survive the read. Clearing it here would mean the serving
	// path and the reconcile sweep both decide what a row means.
	if _, err := repository.NewMetadataRepository(h.pool).
		GetImage(context.Background(), item.ID, domain.ImagePrimary); err != nil {
		t.Errorf("the read path cleared the row: %v", err)
	}
}

// TestItemImageIndexBeyondTheFirstIs404 checks both spellings of the index,
// each of which appears in the recorded traffic.
func TestItemImageIndexBeyondTheFirstIs404(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	item := seeded.byTitle["Idiocracy"]
	seedImage(t, h, item.ID, domain.ImageBackdrop, posterBytes)

	base := "/Items/" + compatID(item.ID) + "/Images/Backdrop"

	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{"no index means the first", base, http.StatusOK},
		{"index 0 in the path", base + "/0", http.StatusOK},
		{"index 0 in the query", base + "?imageIndex=0", http.StatusOK},
		{"a second image does not exist", base + "/1", http.StatusNotFound},
		{"nor by query", base + "?imageIndex=1", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.do(http.MethodGet, tc.path, "", nil)
			body := h.bodyOf(resp)
			if resp.StatusCode != tc.want {
				t.Errorf("status %d, want %d: %s", resp.StatusCode, tc.want, body)
			}
		})
	}
}

// TestItemImageIgnoresSizingParameters pins the resizing decision.
//
// Reelix honours none of them and must never answer 400 for one: rejecting an
// unhonoured parameter turns a cosmetic inefficiency into a missing image,
// which is much the worse failure. quality and fillHeight are what the recorded
// clients actually send; maxWidth and maxHeight are in the published spec and
// were sent by nothing in the capture.
func TestItemImageIgnoresSizingParameters(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	item := seeded.byTitle["Idiocracy"]
	seedImage(t, h, item.ID, domain.ImagePrimary, posterBytes)

	base := "/Items/" + compatID(item.ID) + "/Images/Primary"

	for _, query := range []string{
		"?quality=96&fillHeight=344",
		"?maxWidth=200",
		"?maxHeight=200&quality=10",
		"?fillWidth=100&fillHeight=100",
		"?tag=00000000000000000000000000000000",
		"?nonsense=1",
	} {
		t.Run(query, func(t *testing.T) {
			resp := h.do(http.MethodGet, base+query, "", nil)
			body := h.bodyOf(resp)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d, want 200: %s", resp.StatusCode, body)
			}
			if string(body) != posterBytes {
				t.Errorf("the source image was not served unchanged")
			}
		})
	}
}

// TestImageTagsAreServable is the assertion the ImageTags allowance cannot
// make.
//
// A dataObjects allowance constrains the TYPE only, so the fixture comparison
// is satisfied by an empty map and could never notice a tag that does not
// resolve. This builds the URL a client builds, from the tag the item
// advertises, and fetches it.
func TestImageTagsAreServable(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	item := seeded.byTitle["Idiocracy"]

	primary := seedImage(t, h, item.ID, domain.ImagePrimary, posterBytes)
	backdrop := seedImage(t, h, item.ID, domain.ImageBackdrop, "backdrop bytes")
	logo := seedImage(t, h, item.ID, domain.ImageLogo, "logo bytes")

	token := h.login()
	resp := h.do(http.MethodGet, "/Items/"+compatID(item.ID), token, nil)
	body := h.bodyOf(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}

	var dto struct {
		ImageTags               map[string]string `json:"ImageTags"`
		BackdropImageTags       []string          `json:"BackdropImageTags"`
		PrimaryImageAspectRatio *float64          `json:"PrimaryImageAspectRatio"`
	}
	decodeInto(t, body, &dto)

	// Keyed by image TYPE, capitalised, as the recorded responses are. Not by
	// image id, which is what the allowance's reason wrongly claimed for as
	// long as nothing tested it.
	if dto.ImageTags["Primary"] != primary.Tag {
		t.Errorf("ImageTags[Primary] = %q, want %q", dto.ImageTags["Primary"], primary.Tag)
	}
	if dto.ImageTags["Logo"] != logo.Tag {
		t.Errorf("ImageTags[Logo] = %q, want %q", dto.ImageTags["Logo"], logo.Tag)
	}
	if _, present := dto.ImageTags["Backdrop"]; present {
		t.Error("Backdrop is in ImageTags; it is indexed, and belongs in BackdropImageTags")
	}
	if len(dto.BackdropImageTags) != 1 || dto.BackdropImageTags[0] != backdrop.Tag {
		t.Errorf("BackdropImageTags = %v, want [%s]", dto.BackdropImageTags, backdrop.Tag)
	}

	// The tag retires no allowance on its own. What matters is that the URL a
	// client builds from it resolves.
	for _, tc := range []struct{ imageType, tag string }{
		{"Primary", dto.ImageTags["Primary"]},
		{"Logo", dto.ImageTags["Logo"]},
		{"Backdrop", dto.BackdropImageTags[0]},
	} {
		t.Run(tc.imageType, func(t *testing.T) {
			url := "/Items/" + compatID(item.ID) + "/Images/" + tc.imageType + "?tag=" + tc.tag
			resp := h.do(http.MethodGet, url, "", nil)
			body := h.bodyOf(resp)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("the advertised tag does not resolve: status %d: %s", resp.StatusCode, body)
			}
		})
	}

	// PrimaryImageAspectRatio retires its allowance: the dimensions come from
	// the provider's own listing, so no image is decoded to answer it.
	if dto.PrimaryImageAspectRatio == nil {
		t.Fatal("PrimaryImageAspectRatio is null for an item with a poster")
	}
	if want := 600.0 / 900.0; *dto.PrimaryImageAspectRatio != want {
		t.Errorf("PrimaryImageAspectRatio = %v, want %v", *dto.PrimaryImageAspectRatio, want)
	}
}

// TestNoArtworkAdvertisesNoTags checks the other half: an item with no images
// must advertise none, so a client draws its placeholder rather than issuing a
// request that cannot succeed.
func TestNoArtworkAdvertisesNoTags(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	item := seeded.byTitle["Congo"]

	resp := h.do(http.MethodGet, "/Items/"+compatID(item.ID), h.login(), nil)
	body := h.bodyOf(resp)

	var dto struct {
		ImageTags               map[string]string `json:"ImageTags"`
		BackdropImageTags       []string          `json:"BackdropImageTags"`
		PrimaryImageAspectRatio *float64          `json:"PrimaryImageAspectRatio"`
	}
	decodeInto(t, body, &dto)

	if len(dto.ImageTags) != 0 {
		t.Errorf("ImageTags = %v, want empty", dto.ImageTags)
	}
	if len(dto.BackdropImageTags) != 0 {
		t.Errorf("BackdropImageTags = %v, want empty", dto.BackdropImageTags)
	}
	if dto.PrimaryImageAspectRatio != nil {
		t.Errorf("PrimaryImageAspectRatio = %v, want null", *dto.PrimaryImageAspectRatio)
	}
}

// TestARecordedNegativeServesNothing checks that "the provider has no logo" and
// "we have a logo" are not confused. A negative is a stored answer that stops
// the pass re-asking; it is not an image.
func TestARecordedNegativeServesNothing(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	item := seeded.byTitle["Idiocracy"]

	written, err := repository.NewMetadataRepository(h.pool).
		WriteImage(context.Background(), item.ID, domain.ImageLogo, "tmdb", domain.ItemImage{})
	if err != nil {
		t.Fatalf("recording the negative: %v", err)
	}
	if !written {
		t.Fatal("the negative was refused")
	}

	resp := h.do(http.MethodGet, "/Items/"+compatID(item.ID)+"/Images/Logo", "", nil)
	body := h.bodyOf(resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404: %s", resp.StatusCode, body)
	}

	detail := h.bodyOf(h.do(http.MethodGet, "/Items/"+compatID(item.ID), h.login(), nil))
	var dto struct {
		ImageTags map[string]string `json:"ImageTags"`
	}
	decodeInto(t, detail, &dto)
	if _, present := dto.ImageTags["Logo"]; present {
		t.Error("a recorded negative was advertised as an image tag")
	}
}
