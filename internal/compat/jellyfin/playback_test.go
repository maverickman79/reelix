package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/repository"
)

// playableMedia is a library whose files exist on disk.
type playableMedia struct {
	root    string
	library domain.Library
	item    domain.MediaItem
	path    string
	size    int64
	etag    string
}

// fillPattern writes a deterministic, position-dependent byte pattern.
//
// Position-dependent so that a range test proves the server seeked to the
// right offset: a constant fill would pass whatever offset it read from.
func fillPattern(b []byte, offset int64) {
	for i := range b {
		b[i] = byte((offset + int64(i)) % 251)
	}
}

// seedPlayable creates a library on disk with one movie in it.
//
// size may exceed what the filesystem can hold: the file is created sparse,
// and the caller is skipped if the filesystem materialised it instead.
func seedPlayable(t *testing.T, h *harness, size int64, markers map[int64]int) playableMedia {
	t.Helper()

	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "Idiocracy.2006.1080p.WEB-DL.mkv")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating media file: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatalf("sizing media file to %d: %v", size, err)
	}

	// Known content at the offsets the test will ask for. Everything between
	// them stays a hole and costs no disk.
	for offset, length := range markers {
		chunk := make([]byte, length)
		fillPattern(chunk, offset)
		if _, err := f.WriteAt(chunk, offset); err != nil {
			f.Close()
			t.Fatalf("writing marker at %d: %v", offset, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing media file: %v", err)
	}

	requireSparse(t, path, size)

	library := domain.Library{Name: "Movies", Kind: domain.LibraryKindMovie}
	libs := repository.NewLibraryRepository(h.pool)
	if err := libs.Create(ctx, &library); err != nil {
		t.Fatalf("creating library: %v", err)
	}
	if err := libs.AddPath(ctx, &domain.LibraryPath{LibraryID: library.ID, Path: root}); err != nil {
		t.Fatalf("adding library path: %v", err)
	}

	media := repository.NewMediaRepository(h.pool)
	year := 2006
	item := domain.MediaItem{
		LibraryID: library.ID, Kind: domain.MediaItemKindMovie,
		Title: "Idiocracy", Year: &year, SourcePath: path,
	}
	if err := media.CreateItem(ctx, &item); err != nil {
		t.Fatalf("creating item: %v", err)
	}

	seconds, container := 5050.4, "matroska,webm"
	file := domain.MediaFile{
		MediaItemID: item.ID, Path: path, Filename: filepath.Base(path),
		SizeBytes: size, Container: &container, DurationSeconds: &seconds,
	}
	if err := media.UpsertFile(ctx, &file); err != nil {
		t.Fatalf("creating file row: %v", err)
	}

	video, audio := "h264", "eac3"
	width, height, channels := 1920, 1080, 6
	if err := media.ReplaceStreams(ctx, file.ID, []domain.MediaStream{
		{StreamIndex: 0, Kind: domain.StreamKindVideo, Codec: &video, Width: &width, Height: &height},
		{StreamIndex: 1, Kind: domain.StreamKindAudio, Codec: &audio, Channels: &channels},
	}); err != nil {
		t.Fatalf("creating streams: %v", err)
	}

	// Refetched so the etag is built from the stored timestamps, exactly as
	// the handler builds it.
	stored, err := media.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("refetching item: %v", err)
	}

	return playableMedia{
		root: root, library: library, item: stored, path: path, size: size,
		etag: etagOf(compatID(stored.ID), stored.UpdatedAt.String()),
	}
}

// requireSparse skips the test when the filesystem really allocated the file.
//
// A test that silently writes several gigabytes on the wrong filesystem is
// worse than one that says why it did not run.
func requireSparse(t *testing.T, path string, size int64) {
	t.Helper()

	const sparseThreshold = 64 << 20
	if size < sparseThreshold {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skipf("cannot tell whether %s is sparse on this platform", path)
	}

	// Blocks are 512 bytes by POSIX convention, whatever the block size is.
	allocated := stat.Blocks * 512
	if allocated > sparseThreshold {
		t.Skipf("%s is not sparse on this filesystem: %d bytes allocated for a %d byte file",
			path, allocated, size)
	}
}

// streamURL builds a stream request carrying the capability tag.
func (p playableMedia) streamURL() string {
	return "/Videos/" + compatID(p.item.ID) + "/stream?static=true&tag=" + p.etag +
		"&mediaSourceId=" + compatID(p.item.ID)
}

// getRange issues a stream request, optionally with a Range header.
func (h *harness) getRange(t *testing.T, url, rangeHeader string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, h.srv.URL+url, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

// The offsets the SK1 actually requested, from the Step 0 capture. The file
// they were served from was 5255910143 bytes; the largest offset is past what
// a 32-bit signed integer holds, which is why the whole path must be int64.
const (
	recordedSize      = int64(5255910143)
	recordedStart     = int64(9471)
	recordedSeek      = int64(95743368)
	recordedFarSeek   = int64(5255045235)
	recordedMarkerLen = 4096
)

// TestStreamRangesAtRecordedOffsets is the seeking test.
//
// Seeking is the part of direct play most likely to be subtly wrong: a server
// that streams from byte zero passes a naive smoke test and fails the moment a
// user drags the scrubber. Every offset here was taken from the capture, and
// every assertion checks the actual bytes returned, not just the headers —
// a server that answers with the right Content-Range and the wrong bytes
// produces a picture that looks like a corrupt file.
func TestStreamRangesAtRecordedOffsets(t *testing.T) {
	h := newHarness(t)
	media := seedPlayable(t, h, recordedSize, map[int64]int{
		0:                                recordedMarkerLen,
		recordedStart:                    recordedMarkerLen,
		recordedSeek:                     recordedMarkerLen,
		recordedFarSeek:                  recordedMarkerLen,
		recordedSize - recordedMarkerLen: recordedMarkerLen,
	})

	url := media.streamURL()

	t.Run("no range serves the whole file", func(t *testing.T) {
		resp := h.getRange(t, url, "")
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200", resp.StatusCode)
		}
		if resp.ContentLength != recordedSize {
			t.Errorf("Content-Length %d, want %d", resp.ContentLength, recordedSize)
		}
		if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
			t.Errorf("Accept-Ranges = %q, want bytes — the client will not seek without it", got)
		}
		// The reference server sent this for a matroska file. Go's own
		// sniffer would call it video/webm.
		if got := resp.Header.Get("Content-Type"); got != "video/x-matroska" {
			t.Errorf("Content-Type = %q, want video/x-matroska", got)
		}

		// Only the first bytes are read: pulling five gigabytes through the
		// test would prove nothing the offsets below do not.
		head := make([]byte, recordedMarkerLen)
		if _, err := io.ReadFull(resp.Body, head); err != nil {
			t.Fatalf("reading the first bytes: %v", err)
		}
		assertPattern(t, head, 0)
	})

	for _, tc := range []struct {
		name   string
		offset int64
	}{
		{"the client's first seek", recordedStart},
		{"a seek forward", recordedSeek},
		{"the farthest recorded seek, past int32", recordedFarSeek},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.getRange(t, url, fmt.Sprintf("bytes=%d-", tc.offset))
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusPartialContent {
				t.Fatalf("status %d, want 206", resp.StatusCode)
			}

			wantLength := recordedSize - tc.offset
			if resp.ContentLength != wantLength {
				t.Errorf("Content-Length %d, want %d", resp.ContentLength, wantLength)
			}

			wantRange := fmt.Sprintf("bytes %d-%d/%d", tc.offset, recordedSize-1, recordedSize)
			if got := resp.Header.Get("Content-Range"); got != wantRange {
				t.Errorf("Content-Range = %q, want %q", got, wantRange)
			}

			// The bytes themselves: the pattern encodes its own position, so
			// this fails if the server seeked anywhere else.
			head := make([]byte, recordedMarkerLen)
			if _, err := io.ReadFull(resp.Body, head); err != nil {
				t.Fatalf("reading from offset %d: %v", tc.offset, err)
			}
			assertPattern(t, head, tc.offset)
		})
	}

	t.Run("the last bytes of the file", func(t *testing.T) {
		offset := recordedSize - recordedMarkerLen

		resp := h.getRange(t, url, fmt.Sprintf("bytes=%d-", offset))
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("status %d, want 206", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading the tail: %v", err)
		}
		if int64(len(body)) != recordedMarkerLen {
			t.Fatalf("read %d bytes, want %d", len(body), recordedMarkerLen)
		}
		assertPattern(t, body, offset)
	})

	t.Run("a closed range", func(t *testing.T) {
		// Wholphin never sent one, but a range that names its end is legal
		// and a player that sends one must not get the whole file.
		start := recordedSeek
		end := start + 1023

		resp := h.getRange(t, url, fmt.Sprintf("bytes=%d-%d", start, end))
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("status %d, want 206", resp.StatusCode)
		}
		if resp.ContentLength != 1024 {
			t.Errorf("Content-Length %d, want 1024", resp.ContentLength)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading: %v", err)
		}
		assertPattern(t, body, start)
	})

	t.Run("a range starting past the end is unsatisfiable", func(t *testing.T) {
		resp := h.getRange(t, url, fmt.Sprintf("bytes=%d-", recordedSize))
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
			t.Fatalf("status %d, want 416", resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Range"); got != fmt.Sprintf("bytes */%d", recordedSize) {
			t.Errorf("Content-Range = %q, want the file size", got)
		}
	})

	t.Run("HEAD reports the size without a body", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodHead, h.srv.URL+url, nil)
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		resp, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatalf("HEAD: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200", resp.StatusCode)
		}
		if resp.ContentLength != recordedSize {
			t.Errorf("Content-Length %d, want %d", resp.ContentLength, recordedSize)
		}

		body, _ := io.ReadAll(resp.Body)
		if len(body) != 0 {
			t.Errorf("HEAD returned %d bytes of body", len(body))
		}
	})
}

// assertPattern checks bytes came from the offset they were asked for.
func assertPattern(t *testing.T, got []byte, offset int64) {
	t.Helper()

	want := make([]byte, len(got))
	fillPattern(want, offset)

	if !bytes.Equal(got, want) {
		// Report where it diverged rather than dumping kilobytes.
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("byte %d of the range from offset %d is %d, want %d "+
					"— the server read from the wrong offset",
					i, offset, got[i], want[i])
			}
		}
	}
}

// TestStreamServesRealContent checks a small, fully written file end to end.
//
// The sparse file above proves the offsets; this proves the bytes, with no
// holes involved.
func TestStreamServesRealContent(t *testing.T) {
	h := newHarness(t)

	const size = 512 << 10
	media := seedPlayable(t, h, size, map[int64]int{0: size})

	resp := h.getRange(t, media.streamURL(), "")
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if int64(len(body)) != size {
		t.Fatalf("read %d bytes, want %d", len(body), size)
	}
	assertPattern(t, body, 0)

	onDisk, err := os.ReadFile(media.path)
	if err != nil {
		t.Fatalf("reading the file: %v", err)
	}
	if !bytes.Equal(body, onDisk) {
		t.Error("the streamed bytes differ from the file on disk")
	}
}

// TestStreamAuthorization covers the capability rule.
//
// The recorded client fetches this URL from a media player that sends no
// credentials at all, so the tag it can only have learned from an
// authenticated PlaybackInfo call stands in for one.
func TestStreamAuthorization(t *testing.T) {
	h := newHarness(t)
	media := seedPlayable(t, h, 64<<10, map[int64]int{0: 64 << 10})
	token := h.login()

	base := "/Videos/" + compatID(media.item.ID) + "/stream"

	t.Run("the media source tag is enough", func(t *testing.T) {
		resp := h.getRange(t, base+"?tag="+media.etag, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status %d, want 200", resp.StatusCode)
		}
	})

	t.Run("a session token in the header is enough", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, h.srv.URL+base, nil)
		req.Header.Set(headerAuthorization, authHeader(token))

		resp, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status %d, want 200", resp.StatusCode)
		}
	})

	t.Run("api_key is accepted on this route", func(t *testing.T) {
		// A narrowing of the Step 5 decision to refuse query-string
		// credentials, and only here: a media player cannot set headers, and
		// the access log omits query strings so a token cannot leak into it.
		resp := h.getRange(t, base+"?api_key="+token, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status %d, want 200", resp.StatusCode)
		}
	})

	t.Run("nothing at all is refused", func(t *testing.T) {
		resp := h.getRange(t, base, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status %d, want 401", resp.StatusCode)
		}
	})

	t.Run("a wrong tag is refused", func(t *testing.T) {
		resp := h.getRange(t, base+"?tag=0123456789abcdef0123456789abcdef", "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status %d, want 401", resp.StatusCode)
		}
	})

	t.Run("another item's tag is refused", func(t *testing.T) {
		other := etagOf(compatID(media.library.ID), media.item.UpdatedAt.String())

		resp := h.getRange(t, base+"?tag="+other, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status %d, want 401", resp.StatusCode)
		}
	})

	t.Run("a bogus token is refused", func(t *testing.T) {
		resp := h.getRange(t, base+"?api_key=not-a-real-token", "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status %d, want 401", resp.StatusCode)
		}
	})
}

// TestStreamRefusesFilesOutsideTheLibrary is the containment check.
//
// The scanner wrote these rows, but an endpoint that opens whatever a database
// row names is one bad row away from serving anything on the host.
func TestStreamRefusesFilesOutsideTheLibrary(t *testing.T) {
	h := newHarness(t)
	media := seedPlayable(t, h, 4096, map[int64]int{0: 4096})

	ctx := context.Background()
	outside := filepath.Join(t.TempDir(), "secrets.mkv")
	if err := os.WriteFile(outside, []byte("not a movie"), 0o600); err != nil {
		t.Fatalf("writing the outside file: %v", err)
	}

	repo := repository.NewMediaRepository(h.pool)

	t.Run("a path outside every library root", func(t *testing.T) {
		item := domain.MediaItem{
			LibraryID: media.library.ID, Kind: domain.MediaItemKindMovie,
			Title: "Elsewhere", SourcePath: outside,
		}
		if err := repo.CreateItem(ctx, &item); err != nil {
			t.Fatalf("creating item: %v", err)
		}
		file := domain.MediaFile{
			MediaItemID: item.ID, Path: outside,
			Filename: filepath.Base(outside), SizeBytes: 11,
		}
		if err := repo.UpsertFile(ctx, &file); err != nil {
			t.Fatalf("creating file row: %v", err)
		}

		resp := h.getRange(t, "/Videos/"+compatID(item.ID)+"/stream?api_key="+h.login(), "")
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status %d, want 403", resp.StatusCode)
		}
	})

	t.Run("a symlink inside the library pointing out of it", func(t *testing.T) {
		// The case a lexical prefix check would wave through.
		link := filepath.Join(media.root, "innocent.mkv")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}

		item := domain.MediaItem{
			LibraryID: media.library.ID, Kind: domain.MediaItemKindMovie,
			Title: "Innocent", SourcePath: link,
		}
		if err := repo.CreateItem(ctx, &item); err != nil {
			t.Fatalf("creating item: %v", err)
		}
		file := domain.MediaFile{
			MediaItemID: item.ID, Path: link,
			Filename: filepath.Base(link), SizeBytes: 11,
		}
		if err := repo.UpsertFile(ctx, &file); err != nil {
			t.Fatalf("creating file row: %v", err)
		}

		resp := h.getRange(t, "/Videos/"+compatID(item.ID)+"/stream?api_key="+h.login(), "")
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status %d, want 403 — a symlink escaped the library", resp.StatusCode)
		}
	})
}

// TestStreamUnknownItem checks an id that names nothing.
func TestStreamUnknownItem(t *testing.T) {
	h := newHarness(t)
	token := h.login()

	resp := h.getRange(t, "/Videos/ffffffffffffffffffffffffffffffff/stream?api_key="+token, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}

// TestPlaybackInfoMatchesFixture replays the recorded PlaybackInfo exchange,
// device profile and all.
func TestPlaybackInfoMatchesFixture(t *testing.T) {
	h := newHarness(t)
	media := seedPlayable(t, h, 64<<10, map[int64]int{0: 64 << 10})
	token := h.login()

	for _, name := range fixtureNames(t, "POST_Items_{id}_PlaybackInfo") {
		t.Run(name, func(t *testing.T) {
			f := loadFixture(t, "POST_Items_{id}_PlaybackInfo", name)

			var body any
			if err := json.Unmarshal(f.Request.Body.JSON, &body); err != nil {
				t.Fatalf("decoding the recorded request: %v", err)
			}

			resp := h.do(http.MethodPost,
				"/Items/"+compatID(media.item.ID)+"/PlaybackInfo", token, body)
			raw := h.bodyOf(resp)

			if resp.StatusCode != f.Response.Status {
				t.Fatalf("status %d, recorded %d: %s", resp.StatusCode, f.Response.Status, raw)
			}
			assertSuperset(t, f.recordedJSON(t), decodeBody(t, raw))
		})
	}
}

// TestPlaybackInfoDecidesDirectPlay checks the answer the client acts on.
func TestPlaybackInfoDecidesDirectPlay(t *testing.T) {
	h := newHarness(t)
	media := seedPlayable(t, h, 64<<10, map[int64]int{0: 64 << 10})
	token := h.login()

	post := func(t *testing.T, body any) (directPlay bool, sessionID, sourceID, etag string) {
		t.Helper()

		resp := h.do(http.MethodPost, "/Items/"+compatID(media.item.ID)+"/PlaybackInfo", token, body)
		raw := h.bodyOf(resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}

		var got struct {
			MediaSources []struct {
				ID                  string `json:"Id"`
				ETag                string
				Container           string
				SupportsDirectPlay  bool
				SupportsTranscoding bool
			}
			PlaySessionID string `json:"PlaySessionId"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decoding: %v\nbody was: %s", err, raw)
		}
		if len(got.MediaSources) != 1 {
			t.Fatalf("got %d media sources, want 1", len(got.MediaSources))
		}
		if got.MediaSources[0].SupportsTranscoding {
			t.Error("SupportsTranscoding = true, but Reelix cannot transcode")
		}
		return got.MediaSources[0].SupportsDirectPlay, got.PlaySessionID,
			got.MediaSources[0].ID, got.MediaSources[0].ETag
	}

	t.Run("the real device profile plays directly", func(t *testing.T) {
		f := loadFixture(t, "POST_Items_{id}_PlaybackInfo", "00")

		var body any
		if err := json.Unmarshal(f.Request.Body.JSON, &body); err != nil {
			t.Fatalf("decoding the recorded request: %v", err)
		}

		directPlay, sessionID, sourceID, etag := post(t, body)
		if !directPlay {
			t.Error("the SK1's own profile was refused direct play")
		}
		if len(sessionID) != 32 {
			t.Errorf("PlaySessionId = %q, want 32 hex characters", sessionID)
		}
		if sourceID != compatID(media.item.ID) {
			t.Errorf("media source id %q, want the item id", sourceID)
		}
		// The tag the client will carry back on the stream request.
		if etag != media.etag {
			t.Errorf("ETag = %q, want %q", etag, media.etag)
		}
	})

	t.Run("a device that cannot play matroska is told so", func(t *testing.T) {
		directPlay, _, _, _ := post(t, map[string]any{
			"DeviceProfile": map[string]any{
				"DirectPlayProfiles": []any{
					map[string]any{"Type": "Video", "Container": "mp4", "VideoCodec": "h264", "AudioCodec": "aac"},
				},
			},
		})
		if directPlay {
			t.Error("a device listing only mp4 was told it could direct-play matroska")
		}
	})

	t.Run("no profile is permissive", func(t *testing.T) {
		if directPlay, _, _, _ := post(t, map[string]any{}); !directPlay {
			t.Error("a client that stated no profile was refused")
		}
	})

	t.Run("each call mints a new play session", func(t *testing.T) {
		_, first, _, _ := post(t, map[string]any{})
		_, second, _, _ := post(t, map[string]any{})
		if first == second {
			t.Error("two playbacks were given the same session id")
		}
	})
}

// TestPlaybackInfoUnknownItem checks an id that names nothing.
func TestPlaybackInfoUnknownItem(t *testing.T) {
	h := newHarness(t)
	token := h.login()

	resp := h.do(http.MethodPost,
		"/Items/ffffffffffffffffffffffffffffffff/PlaybackInfo", token, map[string]any{})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}

// TestPlaybackReportsAreRecorded covers the session routes and the milestone's
// requirement that a playback session is visible in the log.
func TestPlaybackReportsAreRecorded(t *testing.T) {
	h := newHarness(t)
	media := seedPlayable(t, h, 64<<10, map[int64]int{0: 64 << 10})
	token := h.login()

	id := media.item.ID.String()
	const session = "be507901c9a74c3c97759733a663c50a"

	for _, route := range []struct {
		route, path string
		fixture     string
	}{
		{"POST_Sessions_Playing", "/Sessions/Playing", "00"},
		{"POST_Sessions_Playing_Progress", "/Sessions/Playing/Progress", "00"},
		{"POST_Sessions_Playing_Stopped", "/Sessions/Playing/Stopped", "00"},
	} {
		t.Run(route.path, func(t *testing.T) {
			f := loadFixture(t, route.route, route.fixture)

			// The recorded body with its item id replaced by a real one.
			var body map[string]any
			if err := json.Unmarshal(f.Request.Body.JSON, &body); err != nil {
				t.Fatalf("decoding the recorded request: %v", err)
			}
			body["ItemId"] = id

			resp := h.do(http.MethodPost, route.path, token, body)
			defer resp.Body.Close()

			if resp.StatusCode != f.Response.Status {
				t.Fatalf("status %d, recorded %d", resp.StatusCode, f.Response.Status)
			}
			if resp.ContentLength > 0 {
				t.Errorf("a %d carried %d bytes of body", resp.StatusCode, resp.ContentLength)
			}
		})
	}

	logs := h.logs.String()

	// Criterion 11: the session is visible in the log.
	for _, want := range []string{
		"playback started", "playback stopped",
		compatID(media.item.ID), session,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log does not mention %q", want)
		}
	}

	// The position is logged in a form a human can read.
	if !strings.Contains(logs, "0:02:06") {
		t.Errorf("the stopped position was not logged as a timestamp:\n%s", logs)
	}

	// And no credential reached it.
	if strings.Contains(logs, token) {
		t.Error("the access token reached the log")
	}
}

// TestPlaybackRoutesRequireAToken checks the reporting routes are
// authenticated, unlike the stream itself.
func TestPlaybackRoutesRequireAToken(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{
		"/Sessions/Playing", "/Sessions/Playing/Progress", "/Sessions/Playing/Stopped",
		"/Items/ffffffffffffffffffffffffffffffff/PlaybackInfo",
	} {
		t.Run(path, func(t *testing.T) {
			resp := h.do(http.MethodPost, path, "", map[string]any{})
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("without a token: %d, want 401", resp.StatusCode)
			}
		})
	}
}

// TestContentTypeForContainer pins the mapping.
func TestContentTypeForContainer(t *testing.T) {
	for _, tc := range []struct{ filename, want string }{
		// Go's sniffer knows the EBML header only as WebM, so a Matroska file
		// would be served as video/webm if this were left to it.
		{"movie.mkv", "video/x-matroska"},
		{"movie.MKV", "video/x-matroska"},
		{"movie.mp4", "video/mp4"},
		{"movie.m2ts", "video/mp2t"},
		{"movie.webm", "video/webm"},
		{"movie.unknown-container", "application/octet-stream"},
	} {
		if got := contentTypeFor(tc.filename); got != tc.want {
			t.Errorf("contentTypeFor(%q) = %q, want %q", tc.filename, got, tc.want)
		}
	}
}

// TestFormatTicks checks the log's position format.
func TestFormatTicks(t *testing.T) {
	for _, tc := range []struct {
		ticks int64
		want  string
	}{
		{0, "0:00:00"},
		{52030000, "0:00:05"},
		{1263890000, "0:02:06"},
		{50503790000, "1:24:10"},
		{-1, "0:00:00"},
	} {
		if got := formatTicks(tc.ticks); got != tc.want {
			t.Errorf("formatTicks(%d) = %q, want %q", tc.ticks, got, tc.want)
		}
	}
}

// TestPlaySessionIDIsUnique checks the identifier that correlates a playback
// across PlaybackInfo, the stream and every progress report.
func TestPlaySessionIDIsUnique(t *testing.T) {
	seen := make(map[string]bool, 64)

	for range 64 {
		id, err := newPlaySessionID()
		if err != nil {
			t.Fatalf("newPlaySessionID: %v", err)
		}
		if len(id) != 32 {
			t.Fatalf("id %q is %d characters, want 32", id, len(id))
		}
		if _, err := strconv.ParseUint(id[:16], 16, 64); err != nil {
			t.Fatalf("id %q is not hexadecimal", id)
		}
		if seen[id] {
			t.Fatalf("id %q was minted twice", id)
		}
		seen[id] = true
	}
}

// TestStreamRefusalDoesNotOpenTheFile checks the ordering: a request that is
// not allowed must be refused before the file is touched.
func TestStreamRefusalDoesNotOpenTheFile(t *testing.T) {
	h := newHarness(t)
	media := seedPlayable(t, h, 4096, map[int64]int{0: 4096})

	// The file is removed from under the row. A refusal must still be a 401 —
	// if it were a 404 or a 500, the handler had already gone to the disk.
	if err := os.Remove(media.path); err != nil {
		t.Fatalf("removing the file: %v", err)
	}

	resp := h.getRange(t, "/Videos/"+compatID(media.item.ID)+"/stream", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401 — the file was opened before the request was authorized",
			resp.StatusCode)
	}
}
