package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"uuid"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/repository"
)

// seededMovie describes one movie to put in the scratch database.
type seededMovie struct {
	title     string
	year      int
	container string
	seconds   float64
	size      int64
	codec     string
	width     int
	height    int
	channels  int
	subtitles int
}

// library mirrors what a scan of the test media produced on the real server:
// the same six films, containers and shapes, so that a fixture comparison is
// run against data of the kind Reelix actually holds.
var seededLibrary = []seededMovie{
	{"Congo", 1995, "matroska,webm", 6509.5, 5198793748, "h264", 1920, 1080, 6, 12},
	{"Fight Club", 1999, "matroska,webm", 8348.5, 76065184023, "hevc", 3840, 2160, 8, 25},
	{"Gangland", 2025, "mov,mp4,m4a,3gp,3g2,mj2", 6265.2, 2037308026, "h264", 1920, 1080, 2, 0},
	{"Idiocracy", 2006, "matroska,webm", 5050.4, 5255910143, "h264", 1920, 1080, 6, 8},
	{"The Legend of Aang - The Last Airbender", 2026, "matroska,webm", 5931.1, 8123456789, "hevc", 3840, 2160, 6, 3},
	{"The Singers", 2026, "matroska,webm", 1081.3, 1523456789, "hevc", 3840, 2160, 6, 1},
}

// seededMedia is a library and its items, as put into the scratch database.
type seededMedia struct {
	library domain.Library
	items   []domain.MediaItem
	byTitle map[string]domain.MediaItem
}

// seedMedia fills the harness database with one movie library.
func seedMedia(t *testing.T, h *harness) seededMedia {
	t.Helper()

	ctx := context.Background()
	libs := repository.NewLibraryRepository(h.pool)
	media := repository.NewMediaRepository(h.pool)

	library := domain.Library{Name: "Movies", Kind: domain.LibraryKindMovie}
	if err := libs.Create(ctx, &library); err != nil {
		t.Fatalf("seeding library: %v", err)
	}

	out := seededMedia{library: library, byTitle: map[string]domain.MediaItem{}}

	for _, m := range seededLibrary {
		year := m.year
		item := domain.MediaItem{
			LibraryID:  library.ID,
			Kind:       domain.MediaItemKindMovie,
			Title:      m.title,
			Year:       &year,
			SourcePath: "/media/" + m.title + ".mkv",
		}
		if err := media.CreateItem(ctx, &item); err != nil {
			t.Fatalf("seeding %q: %v", m.title, err)
		}

		seconds, container := m.seconds, m.container
		file := domain.MediaFile{
			MediaItemID:     item.ID,
			Path:            item.SourcePath,
			Filename:        m.title + ".mkv",
			SizeBytes:       m.size,
			Container:       &container,
			DurationSeconds: &seconds,
		}
		if err := media.UpsertFile(ctx, &file); err != nil {
			t.Fatalf("seeding file for %q: %v", m.title, err)
		}

		codec, audio, subtitle := m.codec, "eac3", "subrip"
		width, height, channels := m.width, m.height, m.channels

		// The metadata a real ffprobe run returns for these containers. It
		// is seeded because the fixture comparison renders whatever the
		// repository holds, and an unprobed stream would render nulls.
		//
		// Seeding it is NOT what retires the allowances in fixture_test.go.
		// The allowances are retired because the fields are plumbed from
		// ffprobe through the schema to the DTO, which is proved away from
		// this seed: TestParseProbeOutput in internal/media,
		// TestStreamMetadataRoundTrip in internal/repository,
		// TestScanPersistsStreamMetadata in internal/service, and
		// TestStreamMetadataMigrationClearsProbedAt in internal/db.
		eng, profile, pixFmt := "eng", "High", "yuv420p"
		audioTitle, level := "Surround AC3 5.1", 40
		frameRate := 23.976023976023978
		// The qualifier ffprobe reports for a great many real files; the
		// boundary is what strips it.
		layout, sampleRate := "5.1(side)", 48000

		streams := []domain.MediaStream{
			{
				StreamIndex: 0, Kind: domain.StreamKindVideo, Codec: &codec,
				Width: &width, Height: &height,
				Language: &eng, Profile: &profile, Level: &level,
				PixelFormat:      &pixFmt,
				AverageFrameRate: &frameRate, RealFrameRate: &frameRate,
				IsDefault: true,
			},
			{
				StreamIndex: 1, Kind: domain.StreamKindAudio, Codec: &audio,
				Channels: &channels, Language: &eng, Title: &audioTitle,
				ChannelLayout: &layout, SampleRate: &sampleRate,
				IsDefault: true,
			},
		}
		for i := range m.subtitles {
			// The first subtitle is an SDH track, so a seeded library
			// exercises a named, hearing-impaired stream rather than only
			// anonymous ones.
			title := fmt.Sprintf("Subtitle %d", i+1)
			hearingImpaired := false
			if i == 0 {
				title, hearingImpaired = "SDH", true
			}
			streams = append(streams, domain.MediaStream{
				StreamIndex: i + 2, Kind: domain.StreamKindSubtitle, Codec: &subtitle,
				Language: &eng, Title: &title, IsHearingImpaired: hearingImpaired,
			})
		}
		if err := media.ReplaceStreams(ctx, file.ID, streams); err != nil {
			t.Fatalf("seeding streams for %q: %v", m.title, err)
		}

		out.items = append(out.items, item)
		out.byTitle[m.title] = item
	}
	return out
}

// TestUserViewsMatchesFixture validates the route the home screen is built
// from.
//
// Its absence is what crash-looped Wholphin on the SK1: the client repeated
// its entire startup sequence four times in 45 seconds rather than rendering
// a screen without knowing what libraries exist.
func TestUserViewsMatchesFixture(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	for _, name := range fixtureNames(t, "GET_UserViews") {
		t.Run(name, func(t *testing.T) {
			f := loadFixture(t, "GET_UserViews", name)

			resp := h.do(http.MethodGet, "/UserViews"+recordedQuery(f), token, nil)
			raw := h.bodyOf(resp)

			if resp.StatusCode != f.Response.Status {
				t.Fatalf("status %d, recorded %d: %s", resp.StatusCode, f.Response.Status, raw)
			}
			assertSuperset(t, f.recordedJSON(t), decodeBody(t, raw))
		})
	}

	// And the content is the seeded library, not merely a well-formed shape.
	resp := h.do(http.MethodGet, "/UserViews", token, nil)
	raw := h.bodyOf(resp)

	var views struct {
		Items []struct {
			Name           string `json:"Name"`
			ID             string `json:"Id"`
			CollectionType string `json:"CollectionType"`
			ChildCount     int    `json:"ChildCount"`
			IsFolder       bool   `json:"IsFolder"`
			Type           string `json:"Type"`
		}
		TotalRecordCount int
	}
	if err := json.Unmarshal(raw, &views); err != nil {
		t.Fatalf("decoding /UserViews: %v", err)
	}

	if len(views.Items) != 1 || views.TotalRecordCount != 1 {
		t.Fatalf("got %d views (total %d), want the one seeded library: %s",
			len(views.Items), views.TotalRecordCount, raw)
	}

	view := views.Items[0]
	if view.Name != "Movies" || view.CollectionType != "movies" || view.Type != "CollectionFolder" {
		t.Errorf("view = %+v", view)
	}
	if !view.IsFolder {
		t.Error("the view is not marked as a folder")
	}
	if view.ChildCount != len(seededLibrary) {
		t.Errorf("ChildCount = %d, want %d", view.ChildCount, len(seededLibrary))
	}
	if view.ID != compatID(seeded.library.ID) {
		t.Errorf("view id %q, want the library's dashless id %q", view.ID, compatID(seeded.library.ID))
	}
}

// TestItemsMatchesFixture replays every recorded /Items call.
//
// The recorded queries name the reference server's own ids, which mean
// nothing here, so a parentId is rewritten to the seeded library and an ids=
// lookup to a seeded item — but only where the recording actually returned
// movies. Six of the recordings resolved person ids and returned cast lists;
// people are excluded from 0.0.1, so those are left pointing at ids Reelix
// does not hold, and the recording is satisfied by an empty result.
func TestItemsMatchesFixture(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	for _, name := range fixtureNames(t, "GET_Items") {
		t.Run(name, func(t *testing.T) {
			f := loadFixture(t, "GET_Items", name)
			query := rewriteIDs(f, seeded)

			resp := h.do(http.MethodGet, "/Items"+query, token, nil)
			raw := h.bodyOf(resp)

			if resp.StatusCode != f.Response.Status {
				t.Fatalf("status %d, recorded %d: %s", resp.StatusCode, f.Response.Status, raw)
			}
			assertSuperset(t, f.recordedJSON(t), decodeBody(t, raw))
		})
	}
}

// rewriteIDs points a recorded query at the seeded data.
func rewriteIDs(f fixture, seeded seededMedia) string {
	values := url.Values{}
	for k, v := range f.Request.Query {
		switch k {
		case "parentId":
			v = compatID(seeded.library.ID)
		case "ids":
			if recordedReturnedMovies(f) {
				v = compatID(seeded.items[0].ID)
			}
		}
		values.Set(k, v)
	}
	if len(values) == 0 {
		return ""
	}
	return "?" + values.Encode()
}

// recordedReturnedMovies reports whether a recording's items are movies.
func recordedReturnedMovies(f fixture) bool {
	var body struct {
		Items []struct {
			Type string `json:"Type"`
		}
	}
	if err := json.Unmarshal(f.Response.Body.JSON, &body); err != nil || len(body.Items) == 0 {
		return false
	}
	return body.Items[0].Type == "Movie"
}

// TestItemDetailMatchesFixture validates the detail response against every
// recording of it — 55 fields, including the media sources and streams a
// player is built from.
func TestItemDetailMatchesFixture(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	id := compatID(seeded.byTitle["Idiocracy"].ID)

	for _, name := range fixtureNames(t, "GET_Items_{id}") {
		t.Run(name, func(t *testing.T) {
			f := loadFixture(t, "GET_Items_{id}", name)

			resp := h.do(http.MethodGet, "/Items/"+id, token, nil)
			raw := h.bodyOf(resp)

			if resp.StatusCode != f.Response.Status {
				t.Fatalf("status %d, recorded %d: %s", resp.StatusCode, f.Response.Status, raw)
			}
			assertSuperset(t, f.recordedJSON(t), decodeBody(t, raw))
		})
	}
}

// TestItemDetailCarriesThePlayableFile checks the parts of the detail Step 7
// will build the direct-play decision on.
func TestItemDetailCarriesThePlayableFile(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	item := seeded.byTitle["Idiocracy"]
	resp := h.do(http.MethodGet, "/Items/"+compatID(item.ID), token, nil)
	raw := h.bodyOf(resp)

	var got struct {
		Name         string
		Container    string
		RunTimeTicks int64
		Width        int
		Height       int
		IsHD         bool
		HasSubtitles bool
		Path         string
		ParentID     string `json:"ParentId"`
		MediaSources []struct {
			ID                      string `json:"Id"`
			Container               string
			Path                    string
			Name                    string
			Size                    int64
			Bitrate                 int
			Protocol                string
			SupportsDirectPlay      bool
			SupportsTranscoding     bool
			DefaultAudioStreamIndex int
			MediaStreams            []struct {
				Type         string
				Codec        string
				DisplayTitle string
			}
		}
		MediaStreams []struct {
			Type                 string
			Index                int
			Codec                string
			DisplayTitle         string
			AspectRatio          string
			IsTextSubtitleStream bool
			VideoRange           string
			ChannelLayout        *string
		}
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decoding detail: %v\nbody was: %s", err, raw)
	}

	if got.Name != "Idiocracy" {
		t.Errorf("Name = %q", got.Name)
	}

	// ffprobe reports "matroska,webm"; the reference server reported "mkv"
	// for the same file, and a client matches this against its direct-play
	// profile.
	if got.Container != "mkv" {
		t.Errorf("Container = %q, want the normalised %q", got.Container, "mkv")
	}
	if got.RunTimeTicks != int64(5050.4*ticksPerSecond) {
		t.Errorf("RunTimeTicks = %d", got.RunTimeTicks)
	}
	if got.Width != 1920 || got.Height != 1080 || !got.IsHD {
		t.Errorf("dimensions %dx%d IsHD=%v", got.Width, got.Height, got.IsHD)
	}
	if !got.HasSubtitles {
		t.Error("HasSubtitles = false, but the file has subtitle streams")
	}
	if got.ParentID != compatID(seeded.library.ID) {
		t.Errorf("ParentId = %q, want the library id", got.ParentID)
	}
	// The constitution forbids returning filesystem layout.
	if got.Path != "" {
		t.Errorf("Path = %q, want it empty", got.Path)
	}

	if len(got.MediaSources) != 1 {
		t.Fatalf("got %d media sources, want 1", len(got.MediaSources))
	}
	source := got.MediaSources[0]

	if source.ID != compatID(item.ID) {
		t.Errorf("source id %q, want the item id", source.ID)
	}
	if source.Container != "mkv" || source.Protocol != "File" {
		t.Errorf("source = %+v", source)
	}
	if source.Path != "" {
		t.Errorf("source Path = %q, want it empty", source.Path)
	}
	// The extension is dropped, as the reference server did.
	if source.Name != "Idiocracy" {
		t.Errorf("source Name = %q, want the filename without its extension", source.Name)
	}
	if source.Size != 5255910143 {
		t.Errorf("source Size = %d", source.Size)
	}
	if !source.SupportsDirectPlay {
		t.Error("SupportsDirectPlay = false")
	}
	// Reelix cannot transcode in 0.0.1; advertising it would invite a request
	// it would then fail.
	if source.SupportsTranscoding {
		t.Error("SupportsTranscoding = true, but Reelix cannot transcode")
	}
	if source.DefaultAudioStreamIndex != 1 {
		t.Errorf("DefaultAudioStreamIndex = %d, want the audio stream's index", source.DefaultAudioStreamIndex)
	}
	// Derived from size and duration rather than probed.
	if source.Bitrate <= 0 {
		t.Errorf("Bitrate = %d, want it derived from size and duration", source.Bitrate)
	}
	if len(source.MediaStreams) != len(got.MediaStreams) {
		t.Errorf("the source carries %d streams, the item %d",
			len(source.MediaStreams), len(got.MediaStreams))
	}

	// 1 video + 1 audio + 8 subtitles, as seeded.
	if len(got.MediaStreams) != 10 {
		t.Fatalf("got %d streams, want 10", len(got.MediaStreams))
	}

	video := got.MediaStreams[0]
	if video.Type != "Video" || video.Codec != "h264" {
		t.Errorf("first stream = %+v, want the video", video)
	}
	if video.DisplayTitle != "1080p H264" {
		t.Errorf("video DisplayTitle = %q", video.DisplayTitle)
	}
	if video.AspectRatio != "16:9" {
		t.Errorf("video AspectRatio = %q", video.AspectRatio)
	}
	// An enum member rather than null, even though the scanner does not read
	// colour metadata: the SDK deserializes this as an enum.
	if video.VideoRange != "Unknown" {
		t.Errorf("video VideoRange = %q, want the Unknown member", video.VideoRange)
	}

	// The seeded track is tagged "eng" and titled "Surround AC3 5.1", so the
	// label leads with the title, names the language, and drops the channel
	// layout the title already carries.
	audio := got.MediaStreams[1]
	if audio.Type != "Audio" ||
		audio.DisplayTitle != "Surround AC3 5.1 - English - Dolby Digital+ - Default" {
		t.Errorf("second stream = %+v, want the audio", audio)
	}

	// The stored layout is "5.1(side)" and the wire must carry "5.1".
	// Asserted on the response rather than on displayChannelLayout alone,
	// because a DTO that stopped calling the normaliser would keep that
	// unit test green while sending a string Findroid classifies as stereo.
	switch {
	case audio.ChannelLayout == nil:
		t.Error("audio ChannelLayout is null — the stored \"5.1(side)\" must reach the wire as \"5.1\"")
	case *audio.ChannelLayout != "5.1":
		t.Errorf("audio ChannelLayout = %q, want \"5.1\" — the stored \"5.1(side)\" "+
			"must be normalised at the boundary", *audio.ChannelLayout)
	}
	if sub := got.MediaStreams[2]; sub.Type != "Subtitle" || !sub.IsTextSubtitleStream {
		t.Errorf("third stream = %+v, want a text subtitle", sub)
	}
}

// TestItemsBrowse covers what the client actually asks /Items for.
func TestItemsBrowse(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()
	parent := compatID(seeded.library.ID)

	get := func(t *testing.T, query string) (names []string, total, start int) {
		t.Helper()

		resp := h.do(http.MethodGet, "/Items"+query, token, nil)
		raw := h.bodyOf(resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}

		var body struct {
			Items            []struct{ Name string }
			TotalRecordCount int
			StartIndex       int
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decoding: %v\nbody was: %s", err, raw)
		}
		for _, i := range body.Items {
			names = append(names, i.Name)
		}
		return names, body.TotalRecordCount, body.StartIndex
	}

	t.Run("a library holds its six movies", func(t *testing.T) {
		names, total, _ := get(t, "?parentId="+parent+"&sortBy=SortName&includeItemTypes=Movie&recursive=true")
		if total != 6 || len(names) != 6 {
			t.Fatalf("got %d of %d items: %v", len(names), total, names)
		}
		if names[0] != "Congo" || names[5] != "The Singers" {
			t.Errorf("titles not in name order: %v", names)
		}
	})

	t.Run("descending", func(t *testing.T) {
		names, _, _ := get(t, "?parentId="+parent+"&sortBy=SortName&sortOrder=Descending")
		if names[0] != "The Singers" {
			t.Errorf("first title %q, want the last alphabetically", names[0])
		}
	})

	t.Run("paging", func(t *testing.T) {
		names, total, start := get(t, "?parentId="+parent+"&sortBy=SortName&startIndex=2&limit=2")
		if total != 6 {
			t.Errorf("total %d, want every match counted, not the page", total)
		}
		if start != 2 {
			t.Errorf("StartIndex %d, want the requested offset echoed", start)
		}
		if len(names) != 2 || names[0] != "Gangland" {
			t.Errorf("page = %v", names)
		}
	})

	t.Run("an unsupported sort falls back rather than failing", func(t *testing.T) {
		// No metadata means no community rating to sort by. A row in an
		// unexpected order is a cosmetic surprise; an error is a blank screen.
		names, total, _ := get(t, "?parentId="+parent+"&sortBy=CommunityRating&sortOrder=Descending")
		if total != 6 || len(names) != 6 {
			t.Errorf("got %d of %d items", len(names), total)
		}
	})

	t.Run("by ids, dashed and dashless", func(t *testing.T) {
		item := seeded.byTitle["Congo"]

		dashless, total, _ := get(t, "?ids="+compatID(item.ID))
		if total != 1 || len(dashless) != 1 || dashless[0] != "Congo" {
			t.Errorf("dashless lookup returned %v (total %d)", dashless, total)
		}

		// A client echoing back an id it read elsewhere may dash it.
		dashed, total, _ := get(t, "?ids="+item.ID.String())
		if total != 1 || len(dashed) != 1 || dashed[0] != "Congo" {
			t.Errorf("dashed lookup returned %v (total %d)", dashed, total)
		}
	})

	t.Run("several ids at once", func(t *testing.T) {
		ids := compatID(seeded.byTitle["Congo"].ID) + "," + compatID(seeded.byTitle["Gangland"].ID)
		names, total, _ := get(t, "?ids="+ids+"&sortBy=SortName")
		if total != 2 || len(names) != 2 {
			t.Fatalf("got %v (total %d)", names, total)
		}
	})

	t.Run("an unknown id is an empty result, not an error", func(t *testing.T) {
		names, total, _ := get(t, "?ids="+compatID(uuid.NewV7()))
		if total != 0 || len(names) != 0 {
			t.Errorf("got %v (total %d)", names, total)
		}
	})

	t.Run("a type Reelix does not have is empty", func(t *testing.T) {
		// Series libraries are excluded from 0.0.1, so there are no episodes
		// and saying so is the truthful answer.
		names, total, _ := get(t, "?parentId="+parent+"&includeItemTypes=Episode")
		if total != 0 || len(names) != 0 {
			t.Errorf("episodes returned %v (total %d)", names, total)
		}
	})

	t.Run("played items are empty until playback state exists", func(t *testing.T) {
		names, total, _ := get(t, "?parentId="+parent+"&isPlayed=true")
		if total != 0 || len(names) != 0 {
			t.Errorf("played items returned %v (total %d)", names, total)
		}

		// And unplayed is everything.
		names, total, _ = get(t, "?parentId="+parent+"&isPlayed=false")
		if total != 6 || len(names) != 6 {
			t.Errorf("unplayed items returned %d of %d", len(names), total)
		}
	})

	t.Run("maxPremiereDate keeps out the unreleased", func(t *testing.T) {
		names, _, _ := get(t, "?parentId="+parent+"&maxPremiereDate=2010-01-01T00:00:00.000-04:00")
		if len(names) != 3 {
			t.Errorf("got %v, want the three films from 2010 or earlier", names)
		}
	})

	t.Run("an unknown parent is empty", func(t *testing.T) {
		names, total, _ := get(t, "?parentId="+compatID(uuid.NewV7()))
		if total != 0 || len(names) != 0 {
			t.Errorf("got %v (total %d)", names, total)
		}
	})
}

// TestLatestItemsReturnsNewestFirst checks the row that was deliberately left
// empty in the first half of Step 6.
func TestLatestItemsReturnsNewestFirst(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	for _, name := range fixtureNames(t, "GET_Items_Latest") {
		t.Run("fixture "+name, func(t *testing.T) {
			f := loadFixture(t, "GET_Items_Latest", name)

			resp := h.do(http.MethodGet, "/Items/Latest"+rewriteIDs(f, seeded), token, nil)
			raw := h.bodyOf(resp)

			if resp.StatusCode != f.Response.Status {
				t.Fatalf("status %d, recorded %d: %s", resp.StatusCode, f.Response.Status, raw)
			}
			assertSuperset(t, f.recordedJSON(t), decodeBody(t, raw))
		})
	}

	t.Run("newest first", func(t *testing.T) {
		resp := h.do(http.MethodGet, "/Items/Latest?parentId="+compatID(seeded.library.ID), token, nil)
		raw := h.bodyOf(resp)

		var items []struct{ Name string }
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatalf("decoding: %v\nbody was: %s", err, raw)
		}
		if len(items) != len(seededLibrary) {
			t.Fatalf("got %d items, want %d", len(items), len(seededLibrary))
		}

		// Seeded in order, so the last one in is the first one out.
		last := seededLibrary[len(seededLibrary)-1].title
		if items[0].Name != last {
			t.Errorf("first item %q, want the most recently added %q", items[0].Name, last)
		}
	})

	t.Run("a limit is honoured", func(t *testing.T) {
		resp := h.do(http.MethodGet, "/Items/Latest?limit=2", token, nil)
		var items []struct{ Name string }
		if err := json.Unmarshal(h.bodyOf(resp), &items); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		if len(items) != 2 {
			t.Errorf("got %d items, want 2", len(items))
		}
	})
}

// TestItemLookupEdgeCases covers the ids a client can arrive with.
func TestItemLookupEdgeCases(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	t.Run("a dashed id resolves", func(t *testing.T) {
		item := seeded.byTitle["Congo"]
		resp := h.do(http.MethodGet, "/Items/"+item.ID.String(), token, nil)
		raw := h.bodyOf(resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}

		var got struct{ Name, Id string }
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		if got.Name != "Congo" {
			t.Errorf("Name = %q", got.Name)
		}
		// The id comes back dashless whichever form was sent.
		if got.Id != compatID(item.ID) {
			t.Errorf("Id = %q, want the dashless form", got.Id)
		}
	})

	t.Run("a library id resolves to its view", func(t *testing.T) {
		// A client following a view's id here should get the view rather
		// than a 404 it will keep retrying.
		resp := h.do(http.MethodGet, "/Items/"+compatID(seeded.library.ID), token, nil)
		raw := h.bodyOf(resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}

		var got struct{ Name, Type, CollectionType string }
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		if got.Type != "CollectionFolder" || got.Name != "Movies" {
			t.Errorf("got %+v, want the library as a collection folder", got)
		}
	})

	t.Run("an unknown id is 404", func(t *testing.T) {
		// Jellyfin's own answer, and what the client's model expects for an
		// item that has gone away.
		resp := h.do(http.MethodGet, "/Items/"+compatID(uuid.NewV7()), token, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status %d, want 404", resp.StatusCode)
		}
	})

	t.Run("an unparseable id is 404", func(t *testing.T) {
		resp := h.do(http.MethodGet, "/Items/not-an-id", token, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status %d, want 404", resp.StatusCode)
		}
	})
}

// itemSubRoutes are the routes requested when a movie is opened.
var itemSubRoutes = []struct {
	route string
	path  string
}{
	{"GET_Items_{id}_Intros", "/Items/%s/Intros"},
	{"GET_Items_{id}_Similar", "/Items/%s/Similar"},
	{"GET_Items_{id}_SpecialFeatures", "/Items/%s/SpecialFeatures"},
	{"GET_Items_{id}_ThemeSongs", "/Items/%s/ThemeSongs"},
	{"GET_MediaSegments_{id}", "/MediaSegments/%s"},
}

// TestItemSubRoutesMatchFixtures checks the routes a detail screen polls.
//
// Each is empty, and each is present: an unimplemented route on a screen the
// client must render is load-bearing, which is the lesson /UserViews taught.
func TestItemSubRoutesMatchFixtures(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	id := compatID(seeded.byTitle["Idiocracy"].ID)

	for _, sub := range itemSubRoutes {
		for _, name := range fixtureNames(t, sub.route) {
			t.Run(sub.route+"/"+name, func(t *testing.T) {
				f := loadFixture(t, sub.route, name)
				path := strings.Replace(sub.path, "%s", id, 1) + recordedQuery(f)

				resp := h.do(http.MethodGet, path, token, nil)
				raw := h.bodyOf(resp)

				if resp.StatusCode != f.Response.Status {
					t.Fatalf("status %d, recorded %d: %s", resp.StatusCode, f.Response.Status, raw)
				}
				assertSuperset(t, f.recordedJSON(t), decodeBody(t, raw))
			})
		}
	}

	// ThemeSongs carries the id it was asked about.
	t.Run("ThemeSongs names its owner", func(t *testing.T) {
		resp := h.do(http.MethodGet, "/Items/"+id+"/ThemeSongs", token, nil)
		var got struct{ OwnerId string }
		if err := json.Unmarshal(h.bodyOf(resp), &got); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		if got.OwnerId != id {
			t.Errorf("OwnerId = %q, want %q", got.OwnerId, id)
		}
	})
}

// TestItemDetailEmitsIdentity proves the DTO CALLS the provider-id helpers.
//
// providerids_test.go already tests those helpers directly, and that is not
// the same thing. Every other seeded item here has no identity, so ProviderIds
// is correctly {} whether the DTO consults the identity or ignores it — the
// assertion cannot tell the two apart. Removing the wiring from the DTO left
// the whole compat suite green, which is how this gap was found.
//
// The item is therefore given an identity first, so that {} becomes a WRONG
// answer rather than merely an unexercised one.
func TestItemDetailEmitsIdentity(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	item := seeded.byTitle["Gangland"]
	ctx := context.Background()

	// The ids a real 10.11.8 reports for this film, and the ones Reelix's own
	// pass produced against TMDB.
	identities := repository.NewIdentityRepository(h.pool)
	if err := identities.SetManual(ctx, item.ID, map[string]string{
		"tmdb": "1147610",
		"imdb": "tt28263483",
	}); err != nil {
		t.Fatalf("seeding identity: %v", err)
	}

	resp := h.do(http.MethodGet, "/Items/"+compatID(item.ID), token, nil)
	var got struct {
		ProviderIds  map[string]string
		ExternalUrls []struct{ Name, Url string }
	}
	if err := json.Unmarshal(h.bodyOf(resp), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	// The keys are the reference's, probed from a live 10.11.8: "Tmdb" and
	// "Imdb", not "TMDB", "tmdb" or "IMDB".
	if got.ProviderIds["Tmdb"] != "1147610" {
		t.Errorf(`ProviderIds["Tmdb"] = %q, want "1147610" (got %v)`,
			got.ProviderIds["Tmdb"], got.ProviderIds)
	}
	if got.ProviderIds["Imdb"] != "tt28263483" {
		t.Errorf(`ProviderIds["Imdb"] = %q, want "tt28263483"`, got.ProviderIds["Imdb"])
	}

	// And the display names differ from the keys, which is the detail nothing
	// in the capture pinned.
	names := map[string]string{}
	for _, u := range got.ExternalUrls {
		names[u.Name] = u.Url
	}
	if names["TMDB"] != "https://www.themoviedb.org/movie/1147610" {
		t.Errorf("TMDB url = %q", names["TMDB"])
	}
	if names["IMDb"] != "https://www.imdb.com/title/tt28263483" {
		t.Errorf("IMDb url = %q", names["IMDb"])
	}
}

// TestItemDetailWithoutIdentityEmitsTheRecordedEmptyShapes is the control.
//
// It is the assertion every other test in this file was already making by
// accident. Kept deliberately, and paired with the test above so the pair can
// discriminate: this one alone cannot.
func TestItemDetailWithoutIdentityEmitsTheRecordedEmptyShapes(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	resp := h.do(http.MethodGet, "/Items/"+compatID(seeded.byTitle["Congo"].ID), token, nil)
	raw := h.bodyOf(resp)

	var got struct {
		ProviderIds  map[string]string
		ExternalUrls []any
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(got.ProviderIds) != 0 {
		t.Errorf("an unidentified item advertises ids: %v", got.ProviderIds)
	}
	if len(got.ExternalUrls) != 0 {
		t.Errorf("an unidentified item advertises urls: %v", got.ExternalUrls)
	}
	// Absent, not null: a client deserialising these strictly must find the
	// shapes every fixture recorded.
	for _, want := range []string{`"ProviderIds":{}`, `"ExternalUrls":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("body does not contain %s", want)
		}
	}
}

// TestItemImagesAre404 pins the artwork routes.
//
// Reelix downloads no artwork in 0.0.1, so no item has an image of any type.
// The recorded server answered exactly this way for an image it did not have
// — the Chapter recording is a 404 carrying a message — while its 200s are
// for images a metadata provider had fetched. The items Reelix serves
// advertise no image tags, so a client should not ask at all.
func TestItemImagesAre404(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	id := compatID(seeded.byTitle["Idiocracy"].ID)
	chapter := loadFixture(t, "GET_Items_{id}_Images_Chapter", "00")

	for _, kind := range []string{"Primary", "Backdrop", "Logo", "Thumb", "Chapter"} {
		t.Run(kind, func(t *testing.T) {
			resp := h.do(http.MethodGet, "/Items/"+id+"/Images/"+kind+"?quality=96", token, nil)
			raw := h.bodyOf(resp)

			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status %d, want 404: %s", resp.StatusCode, raw)
			}

			// Same shape as the recorded no-image answer: a JSON string.
			assertSuperset(t, chapter.recordedJSON(t), decodeBody(t, raw))
			if !strings.Contains(string(raw), kind) {
				t.Errorf("the message does not name the image type: %s", raw)
			}
		})
	}

	t.Run("with an index in the path", func(t *testing.T) {
		resp := h.do(http.MethodGet, "/Items/"+id+"/Images/Primary/0", token, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status %d, want 404", resp.StatusCode)
		}
	})

	// An item with no artwork must advertise none: a tag is what a client
	// builds an image URL from, so a fabricated one would produce a broken
	// request instead of the placeholder it shows for an item with no images.
	t.Run("items advertise no image tags", func(t *testing.T) {
		resp := h.do(http.MethodGet, "/Items/"+id, token, nil)
		var got struct {
			ImageTags         map[string]string
			BackdropImageTags []string
		}
		if err := json.Unmarshal(h.bodyOf(resp), &got); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		if len(got.ImageTags) != 0 || len(got.BackdropImageTags) != 0 {
			t.Errorf("item advertises artwork it does not have: %+v", got)
		}
	})
}

// TestItemImageDoesNotRequireAToken pins the artwork exception.
//
// This is the fault that motivated it: a browse grid requested six posters
// with a token and got six 404s, and five seconds later the playback screen
// requested one without a token and got a 401. A 401 is a retry where a 404
// is final, so once artwork exists that disagreement is a loop rather than a
// placeholder.
//
// The reference was probed unauthenticated and answers 400 for a malformed id
// and 404 for an absent image, never 401, while /Items/{id} beside it answers
// 401. So this matches the reference rather than relaxing it.
func TestItemImageDoesNotRequireAToken(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	id := compatID(seeded.byTitle["Idiocracy"].ID)

	for _, path := range []string{
		"/Items/" + id + "/Images/Primary",
		"/Items/" + id + "/Images/primary",
		"/Items/" + id + "/Images/Primary/0",
	} {
		t.Run(path, func(t *testing.T) {
			resp := h.do(http.MethodGet, path, "", nil)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("without a token: %d, want 404", resp.StatusCode)
			}
		})
	}

	// The control. If this ever answers anything but 401, the change above
	// leaked past the two routes it was meant for.
	t.Run("the item itself still requires a token", func(t *testing.T) {
		resp := h.do(http.MethodGet, "/Items/"+id, "", nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status %d, want 401", resp.StatusCode)
		}
	})
}

// TestItemImageTypeFoldsCase pins the other half of the artwork fix.
//
// The type is a route parameter, so routefold.go leaves its casing alone by
// design. A client that lowercases its paths therefore delivers "primary"
// here. While nothing has artwork both spellings 404 either way, so the
// assertion that carries the meaning is the canonical NAME in the body: it is
// what shows the two spellings arrived at one type rather than two.
func TestItemImageTypeFoldsCase(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	id := compatID(seeded.byTitle["Idiocracy"].ID)

	for _, spelling := range []string{"Primary", "primary", "PRIMARY", "pRiMaRy"} {
		t.Run(spelling, func(t *testing.T) {
			resp := h.do(http.MethodGet, "/Items/"+id+"/Images/"+spelling, "", nil)
			raw := h.bodyOf(resp)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status %d, want 404: %s", resp.StatusCode, raw)
			}
			if !strings.Contains(string(raw), "Primary") {
				t.Errorf("body does not name the canonical type: %s", raw)
			}
		})
	}

	// An unknown type is echoed as sent and still 404s. The reference answers
	// 400 here; see handleItemImage for why that is recorded not reproduced.
	t.Run("an unknown type is not invented into a real one", func(t *testing.T) {
		resp := h.do(http.MethodGet, "/Items/"+id+"/Images/Poster", "", nil)
		raw := h.bodyOf(resp)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status %d, want 404: %s", resp.StatusCode, raw)
		}
		if !strings.Contains(string(raw), "Poster") {
			t.Errorf("body does not name the type as sent: %s", raw)
		}
	})
}

// TestBrowseRoutesRequireAToken checks the new surface is authenticated.
func TestBrowseRoutesRequireAToken(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	id := compatID(seeded.byTitle["Congo"].ID)

	for _, path := range []string{
		"/UserViews",
		"/Items",
		"/Items/" + id,
		"/Items/" + id + "/Similar",
		"/Items/" + id + "/Intros",
		"/Items/" + id + "/SpecialFeatures",
		"/Items/" + id + "/ThemeSongs",
		"/MediaSegments/" + id,
	} {
		t.Run(path, func(t *testing.T) {
			resp := h.do(http.MethodGet, path, "", nil)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("without a token: %d, want 401", resp.StatusCode)
			}
		})
	}
}

// TestNoStreamFieldSerialisesAsTheStringNull is the regression test for the
// fault that started this.
//
// Wholphin composes its own track label from individual fields rather than
// from DisplayTitle, and builds it with a list that does not drop nulls — so a
// field Reelix answers null for arrives at a user as the literal four
// characters "null". The Singers rendered as "English DD+ null (null)" on real
// hardware while our DisplayTitle for the same stream read
// "English - Dolby Digital+ - 5.1 - Default".
//
// This walks every stream field a client is known to concatenate without a
// null check and fails if any is null. It cannot cover fields no client reads
// today; what it does cover is the ones a real client was observed to print.
func TestNoStreamFieldSerialisesAsTheStringNull(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	raw := h.bodyOf(h.do(http.MethodGet,
		"/Items/"+compatID(seeded.items[0].ID), token, nil))

	var body struct {
		MediaStreams []map[string]any `json:"MediaStreams"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding item: %v", err)
	}
	if len(body.MediaStreams) == 0 {
		t.Fatal("no streams to check")
	}

	// Fields Wholphin concatenates directly. ChannelLayout is audio-only;
	// the Localized* strings are read whenever the matching flag is set, and
	// Reelix sets IsDefault from the container, so they must always be there.
	concatenated := map[string][]string{
		"Audio": {
			"ChannelLayout",
			"LocalizedDefault", "LocalizedForced", "LocalizedExternal",
		},
		"Subtitle": {
			"LocalizedDefault", "LocalizedForced", "LocalizedExternal",
			"LocalizedHearingImpaired",
		},
		"Video": {
			"LocalizedDefault", "LocalizedForced", "LocalizedExternal",
		},
	}

	for i, stream := range body.MediaStreams {
		kind, _ := stream["Type"].(string)

		for _, field := range concatenated[kind] {
			value, present := stream[field]
			if !present {
				t.Errorf("stream %d (%s): %s is missing entirely", i, kind, field)
				continue
			}
			if value == nil {
				t.Errorf("stream %d (%s): %s is null — a client that concatenates it "+
					"without a null check prints the string \"null\" to a user", i, kind, field)
			}
		}
	}
}

// TestMediaSourceContainerIsASingleToken pins the split between the item's
// container and the media source's.
//
// The expectations are what a real 10.11.8 returned for one file per
// extension, all four probed directly; see mediaSourceContainer. A client
// builds /Videos/{id}/stream.{container} from the media source field, so a
// raw ffprobe list here is a broken URL rather than a cosmetic difference.
func TestMediaSourceContainerIsASingleToken(t *testing.T) {
	const mp4Family = "mov,mp4,m4a,3gp,3g2,mj2"

	for _, tc := range []struct {
		name     string
		probed   string
		filename string
		want     string
	}{
		{"mp4 matches a token", mp4Family, "Gangland (2025).mp4", "mp4"},
		{"mov matches a token", mp4Family, "Probe (2020).mov", "mov"},
		// The case that rules out "just use the extension": m4v is not in the
		// list, and the reference answers with the first token.
		{"m4v falls back to the first token", mp4Family, "Probe (2020).m4v", "mov"},
		{"matroska resolves to mkv", "matroska,webm", "Film (1999).mkv", "mkv"},
		{"a single token passes through", "avi", "Film (1999).avi", "avi"},
		{"no extension falls back", mp4Family, "Film", "mov"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probed := tc.probed
			if got := mediaSourceContainer(&probed, tc.filename); got != tc.want {
				t.Errorf("mediaSourceContainer(%q, %q) = %q, want %q",
					tc.probed, tc.filename, got, tc.want)
			}
		})
	}

	if got := mediaSourceContainer(nil, "Film (1999).mp4"); got != "" {
		t.Errorf("an unprobed file reported %q, want an empty string", got)
	}
}

// TestItemContainerKeepsTheRawProbeList is the other half, and is why
// mediaSourceContainer exists separately rather than replacing containerName.
//
// The reference server reports ffprobe's raw list at the ITEM level for the
// mp4 family — verified against a real server and visible in the Step 0
// capture, where GET_Items carries an item whose Container is the full list.
// Collapsing this to "mp4" for tidiness would diverge from the recorded
// server on a field the fixtures pin.
func TestItemContainerKeepsTheRawProbeList(t *testing.T) {
	raw := "mov,mp4,m4a,3gp,3g2,mj2"
	if got := containerName(&raw); got != raw {
		t.Errorf("containerName(%q) = %q, want it left alone", raw, got)
	}

	matroska := "matroska,webm"
	if got := containerName(&matroska); got != "mkv" {
		t.Errorf("containerName(%q) = %q, want %q", matroska, got, "mkv")
	}
}
