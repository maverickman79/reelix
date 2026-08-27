package jellyfin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// reportBody builds the body a client sends with a playback report.
func reportBody(itemID string, positionTicks int64) map[string]any {
	return map[string]any{
		"ItemId":        itemID,
		"PositionTicks": positionTicks,
		"PlaySessionId": "be507901c9a74c3c97759733a663c50a",
		"PlayMethod":    "DirectPlay",
		"CanSeek":       true,
		"IsPaused":      false,
	}
}

// userDataOf reads one item's UserData through the detail route.
func (h *harness) userDataOf(t *testing.T, token, id string) userDataDTO {
	t.Helper()

	resp := h.do(http.MethodGet, "/Items/"+id, token, nil)
	raw := h.bodyOf(resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /Items/%s: %d: %s", id, resp.StatusCode, raw)
	}

	var got struct {
		UserData userDataDTO
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decoding detail: %v\nbody was: %s", err, raw)
	}
	return got.UserData
}

// resumeList reads the Continue Watching row.
func (h *harness) resumeList(t *testing.T, token string) []struct {
	Name     string
	ID       string `json:"Id"`
	UserData userDataDTO
} {
	t.Helper()

	resp := h.do(http.MethodGet, "/UserItems/Resume?limit=25&mediaTypes=Video", token, nil)
	raw := h.bodyOf(resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /UserItems/Resume: %d: %s", resp.StatusCode, raw)
	}

	var got struct {
		Items []struct {
			Name     string
			ID       string `json:"Id"`
			UserData userDataDTO
		}
		TotalRecordCount int
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decoding resume list: %v\nbody was: %s", err, raw)
	}
	if got.TotalRecordCount != len(got.Items) {
		t.Errorf("TotalRecordCount %d does not match %d items", got.TotalRecordCount, len(got.Items))
	}
	return got.Items
}

// TestContinueWatching walks the milestone's completion criteria end to end:
// play part-way, appear in Continue Watching at the right position, then watch
// to the end and leave it.
func TestContinueWatching(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	item := seeded.byTitle["Idiocracy"]
	id := compatID(item.ID)
	dashed := item.ID.String()

	// Idiocracy is seeded at 5050.4 seconds.
	const runtime = 5050.4
	ticks := func(seconds float64) int64 { return int64(seconds * ticksPerSecond) }

	t.Run("nothing is in progress to begin with", func(t *testing.T) {
		if items := h.resumeList(t, token); len(items) != 0 {
			t.Fatalf("resume list starts with %d items", len(items))
		}

		data := h.userDataOf(t, token, id)
		if data.PlaybackPositionTicks != 0 || data.Played || data.PlayCount != 0 {
			t.Errorf("untouched item carries %+v", data)
		}
		if data.LastPlayedDate != nil {
			t.Error("an untouched item has a LastPlayedDate")
		}
	})

	t.Run("starting playback records nothing", func(t *testing.T) {
		// The start report carries no position, and storing it would clear a
		// resume point rather than establish one.
		resp := h.do(http.MethodPost, "/Sessions/Playing", token, reportBody(dashed, 0))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status %d, want 204", resp.StatusCode)
		}

		if items := h.resumeList(t, token); len(items) != 0 {
			t.Errorf("pressing play put %d items in the resume list", len(items))
		}
	})

	t.Run("a couple of minutes in is not yet in progress", func(t *testing.T) {
		// 2.5% — the position the capture's own reference server discarded.
		resp := h.do(http.MethodPost, "/Sessions/Playing/Progress", token,
			reportBody(dashed, ticks(126.4)))
		defer resp.Body.Close()

		if items := h.resumeList(t, token); len(items) != 0 {
			t.Errorf("two minutes in created a resume entry: %+v", items)
		}
		if data := h.userDataOf(t, token, id); data.PlaybackPositionTicks != 0 {
			t.Errorf("position = %d, want 0", data.PlaybackPositionTicks)
		}
	})

	t.Run("half an hour in appears in Continue Watching", func(t *testing.T) {
		resp := h.do(http.MethodPost, "/Sessions/Playing/Progress", token,
			reportBody(dashed, ticks(1800)))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status %d, want 204", resp.StatusCode)
		}

		items := h.resumeList(t, token)
		if len(items) != 1 {
			t.Fatalf("resume list has %d items, want 1", len(items))
		}
		if items[0].Name != "Idiocracy" || items[0].ID != id {
			t.Errorf("resume list holds %+v", items[0])
		}
		if got := items[0].UserData.PlaybackPositionTicks; got != ticks(1800) {
			t.Errorf("resume position = %d ticks, want %d", got, ticks(1800))
		}

		// And the detail route agrees, which is what the player reads before
		// it decides where to start.
		data := h.userDataOf(t, token, id)
		if data.PlaybackPositionTicks != ticks(1800) {
			t.Errorf("detail position = %d, want %d", data.PlaybackPositionTicks, ticks(1800))
		}
		if data.Played {
			t.Error("a part-watched item is marked played")
		}
		if data.LastPlayedDate == nil {
			t.Error("LastPlayedDate is still null after watching")
		}
	})

	t.Run("pressing play again does not wipe the position", func(t *testing.T) {
		// The start report carries no position. Storing it would judge zero
		// against the thresholds and clear the resume point the client is
		// about to seek to — so resuming a film would lose the place it was
		// resuming from.
		resp := h.do(http.MethodPost, "/Sessions/Playing", token, reportBody(dashed, 0))
		defer resp.Body.Close()

		data := h.userDataOf(t, token, id)
		if data.PlaybackPositionTicks != ticks(1800) {
			t.Errorf("position = %d after pressing play, want %d kept",
				data.PlaybackPositionTicks, ticks(1800))
		}

		items := h.resumeList(t, token)
		if len(items) != 1 {
			t.Errorf("resume list has %d items after pressing play, want 1", len(items))
		}
	})

	t.Run("stopping keeps the position", func(t *testing.T) {
		resp := h.do(http.MethodPost, "/Sessions/Playing/Stopped", token,
			reportBody(dashed, ticks(2400)))
		defer resp.Body.Close()

		items := h.resumeList(t, token)
		if len(items) != 1 {
			t.Fatalf("resume list has %d items, want 1", len(items))
		}
		if got := items[0].UserData.PlaybackPositionTicks; got != ticks(2400) {
			t.Errorf("resume position = %d, want %d", got, ticks(2400))
		}
		// Stopping part-way is not a viewing.
		if items[0].UserData.PlayCount != 0 {
			t.Errorf("PlayCount = %d after stopping part-way", items[0].UserData.PlayCount)
		}
	})

	t.Run("watching to the end marks it played and clears the list", func(t *testing.T) {
		resp := h.do(http.MethodPost, "/Sessions/Playing/Stopped", token,
			reportBody(dashed, ticks(runtime*0.98)))
		defer resp.Body.Close()

		if items := h.resumeList(t, token); len(items) != 0 {
			t.Errorf("a finished film is still in Continue Watching: %+v", items)
		}

		data := h.userDataOf(t, token, id)
		if !data.Played {
			t.Error("Played = false after watching to the end")
		}
		if data.PlayCount != 1 {
			t.Errorf("PlayCount = %d, want 1", data.PlayCount)
		}
		// No resume point in the closing credits.
		if data.PlaybackPositionTicks != 0 {
			t.Errorf("position = %d after finishing, want 0", data.PlaybackPositionTicks)
		}
	})

	t.Run("a played film drops out of the latest row", func(t *testing.T) {
		// Reelix reports HidePlayedInLatest in the user configuration, so it
		// has to actually hide them.
		resp := h.do(http.MethodGet, "/Items/Latest", token, nil)
		raw := h.bodyOf(resp)

		var latest []struct{ Name string }
		if err := json.Unmarshal(raw, &latest); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		for _, i := range latest {
			if i.Name == "Idiocracy" {
				t.Errorf("a played film is still in the latest row: %s", raw)
			}
		}
		if len(latest) != len(seededLibrary)-1 {
			t.Errorf("latest has %d items, want %d", len(latest), len(seededLibrary)-1)
		}
	})

	t.Run("rewatching resumes without un-marking it", func(t *testing.T) {
		resp := h.do(http.MethodPost, "/Sessions/Playing/Progress", token,
			reportBody(dashed, ticks(900)))
		defer resp.Body.Close()

		data := h.userDataOf(t, token, id)
		if !data.Played {
			t.Error("a rewatch un-marked a watched film")
		}
		if data.PlaybackPositionTicks != ticks(900) {
			t.Errorf("position = %d, want %d", data.PlaybackPositionTicks, ticks(900))
		}
		if items := h.resumeList(t, token); len(items) != 1 {
			t.Errorf("a rewatch in progress is not in Continue Watching")
		}
	})

	t.Run("a failed playback records nothing", func(t *testing.T) {
		before := h.userDataOf(t, token, id)

		body := reportBody(dashed, ticks(4000))
		body["Failed"] = true

		resp := h.do(http.MethodPost, "/Sessions/Playing/Stopped", token, body)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status %d, want 204", resp.StatusCode)
		}

		if after := h.userDataOf(t, token, id); after.PlaybackPositionTicks != before.PlaybackPositionTicks {
			t.Errorf("a failed playback moved the position from %d to %d",
				before.PlaybackPositionTicks, after.PlaybackPositionTicks)
		}
	})
}

// TestResumeListOrdering checks Continue Watching leads with the film you were
// watching most recently.
func TestResumeListOrdering(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	// A third of the way into each, rather than a fixed number of seconds:
	// five minutes is under the resume threshold for a two-hour film and well
	// over it for a short one, which would put only some of them in the list.
	partWay := func(title string) int64 {
		for _, m := range seededLibrary {
			if m.title == title {
				return int64(m.seconds / 3 * ticksPerSecond)
			}
		}
		t.Fatalf("no seeded film called %q", title)
		return 0
	}

	// Watched in this order; the list must come back reversed.
	for _, title := range []string{"Congo", "Gangland", "The Singers"} {
		resp := h.do(http.MethodPost, "/Sessions/Playing/Progress", token,
			reportBody(seeded.byTitle[title].ID.String(), partWay(title)))
		resp.Body.Close()
	}

	items := h.resumeList(t, token)
	if len(items) != 3 {
		t.Fatalf("resume list has %d items, want 3", len(items))
	}

	want := []string{"The Singers", "Gangland", "Congo"}
	for i, name := range want {
		if items[i].Name != name {
			t.Errorf("position %d is %q, want %q (most recent first)", i, items[i].Name, name)
		}
	}
}

// TestPlaybackStateIsPerUser checks one account's history stays its own.
func TestPlaybackStateIsPerUser(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()

	item := seeded.byTitle["Congo"]
	resp := h.do(http.MethodPost, "/Sessions/Playing/Progress", token,
		reportBody(item.ID.String(), int64(1800*ticksPerSecond)))
	resp.Body.Close()

	if items := h.resumeList(t, token); len(items) != 1 {
		t.Fatalf("the watching user has %d resume items, want 1", len(items))
	}

	// A second account on the same server sees an empty list.
	other := h.loginAs(t, "second-viewer")
	if items := h.resumeList(t, other); len(items) != 0 {
		t.Errorf("another user sees %d resume items: %+v", len(items), items)
	}
	if data := h.userDataOf(t, other, compatID(item.ID)); data.PlaybackPositionTicks != 0 {
		t.Errorf("another user sees a position of %d", data.PlaybackPositionTicks)
	}
}

// TestUnknownItemReportIsAccepted checks a report about something no longer in
// the library does not fail the client.
//
// A client that reports progress on an item removed from the library is not
// making an error it can correct; answering 204 and recording nothing is the
// only useful response.
func TestUnknownItemReportIsAccepted(t *testing.T) {
	h := newHarness(t)
	seedMedia(t, h)
	token := h.login()

	for _, path := range []string{"/Sessions/Playing/Progress", "/Sessions/Playing/Stopped"} {
		t.Run(path, func(t *testing.T) {
			resp := h.do(http.MethodPost, path, token,
				reportBody("ffffffffffffffffffffffffffffffff", 1000))
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusNoContent {
				t.Errorf("status %d, want 204", resp.StatusCode)
			}
		})
	}

	if !strings.Contains(h.logs.String(), "unknown_item") {
		t.Error("the log does not record that the item was unknown")
	}
}
