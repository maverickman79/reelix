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

// seedUser creates an account to hang playback state off.
func seedUser(t *testing.T, ctx context.Context, users *repository.UserRepository, name string) domain.User {
	t.Helper()

	u := domain.User{Username: name, PasswordHash: "not-a-real-hash"}
	if err := users.Create(ctx, &u); err != nil {
		t.Fatalf("seeding user %q: %v", name, err)
	}
	return u
}

// report is a shorthand for one client report.
func report(userID, itemID uuid.UUID, position, raw float64, played bool) domain.PlaybackState {
	now := time.Now().UTC()
	return domain.PlaybackState{
		UserID:             userID,
		MediaItemID:        itemID,
		PositionSeconds:    position,
		RawPositionSeconds: raw,
		Played:             played,
		LastPlayedAt:       &now,
	}
}

// TestPlaybackReport covers the upsert every progress report goes through.
func TestPlaybackReport(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)

	users := repository.NewUserRepository(pool)
	media := repository.NewMediaRepository(pool)
	playback := repository.NewPlaybackRepository(pool)

	libraryID := seedLibrary(t, ctx, repository.NewLibraryRepository(pool))
	item := seedItem(t, ctx, media, libraryID, "Idiocracy", ptr(2006), 0)
	user := seedUser(t, ctx, users, "viewer")

	t.Run("the first report creates the row", func(t *testing.T) {
		if err := playback.Report(ctx, report(user.ID, item.ID, 600, 600, false), 0); err != nil {
			t.Fatalf("Report: %v", err)
		}

		got, err := playback.Get(ctx, user.ID, item.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.PositionSeconds != 600 || got.RawPositionSeconds != 600 {
			t.Errorf("position %v / raw %v, want 600 / 600", got.PositionSeconds, got.RawPositionSeconds)
		}
		if got.Played || got.PlayCount != 0 {
			t.Errorf("played=%v count=%d, want false / 0", got.Played, got.PlayCount)
		}
		if got.LastPlayedAt == nil {
			t.Error("LastPlayedAt was not set")
		}
	})

	t.Run("a later report moves the position", func(t *testing.T) {
		if err := playback.Report(ctx, report(user.ID, item.ID, 1200, 1200, false), 0); err != nil {
			t.Fatalf("Report: %v", err)
		}

		got, _ := playback.Get(ctx, user.ID, item.ID)
		if got.PositionSeconds != 1200 {
			t.Errorf("position %v, want 1200", got.PositionSeconds)
		}
	})

	t.Run("an unchanged report writes nothing", func(t *testing.T) {
		// A paused client keeps reporting the same position every few
		// seconds. Those must not rewrite the row with identical contents.
		before, _ := playback.Get(ctx, user.ID, item.ID)

		time.Sleep(5 * time.Millisecond)
		if err := playback.Report(ctx, report(user.ID, item.ID, 1200, 1200, false), 0); err != nil {
			t.Fatalf("Report: %v", err)
		}

		after, _ := playback.Get(ctx, user.ID, item.ID)
		if !after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Errorf("updated_at moved from %v to %v on an unchanged report",
				before.UpdatedAt, after.UpdatedAt)
		}
	})

	t.Run("finishing marks it played and counts the viewing", func(t *testing.T) {
		// The position is dropped: a finished film must not offer to resume
		// in its closing credits.
		if err := playback.Report(ctx, report(user.ID, item.ID, 0, 5000, true), 1); err != nil {
			t.Fatalf("Report: %v", err)
		}

		got, _ := playback.Get(ctx, user.ID, item.ID)
		if !got.Played {
			t.Error("played = false after finishing")
		}
		if got.PlayCount != 1 {
			t.Errorf("PlayCount = %d, want 1", got.PlayCount)
		}
		if got.PositionSeconds != 0 {
			t.Errorf("position = %v, want 0", got.PositionSeconds)
		}
		// The raw position is kept whatever the thresholds decided.
		if got.RawPositionSeconds != 5000 {
			t.Errorf("raw position = %v, want 5000", got.RawPositionSeconds)
		}
	})

	t.Run("played is sticky", func(t *testing.T) {
		// Starting a rewatch reports a small position and played=false. The
		// item must stay watched.
		if err := playback.Report(ctx, report(user.ID, item.ID, 900, 900, false), 0); err != nil {
			t.Fatalf("Report: %v", err)
		}

		got, _ := playback.Get(ctx, user.ID, item.ID)
		if !got.Played {
			t.Error("a rewatch un-marked a watched item")
		}
		// And it is in progress again at the same time, which is what a
		// client expects to see.
		if got.PositionSeconds != 900 {
			t.Errorf("position = %v, want 900", got.PositionSeconds)
		}
	})

	t.Run("finishing again counts a second viewing", func(t *testing.T) {
		if err := playback.Report(ctx, report(user.ID, item.ID, 0, 5000, true), 1); err != nil {
			t.Fatalf("Report: %v", err)
		}

		got, _ := playback.Get(ctx, user.ID, item.ID)
		if got.PlayCount != 2 {
			t.Errorf("PlayCount = %d, want 2", got.PlayCount)
		}
	})

	t.Run("state is per user", func(t *testing.T) {
		other := seedUser(t, ctx, users, "someone-else")

		got, err := playback.Get(ctx, other.ID, item.ID)
		if !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("another user's lookup returned %+v, %v", got, err)
		}
	})
}

// TestPlaybackStateTravelsWithItems checks the join every browse response uses.
func TestPlaybackStateTravelsWithItems(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)

	users := repository.NewUserRepository(pool)
	media := repository.NewMediaRepository(pool)
	playback := repository.NewPlaybackRepository(pool)

	libraryID := seedLibrary(t, ctx, repository.NewLibraryRepository(pool))
	congo := seedItem(t, ctx, media, libraryID, "Congo", ptr(1995), 0)
	fight := seedItem(t, ctx, media, libraryID, "Fight Club", ptr(1999), 0)
	seedItem(t, ctx, media, libraryID, "Avatar", ptr(2026), 0)

	viewer := seedUser(t, ctx, users, "viewer")
	stranger := seedUser(t, ctx, users, "stranger")

	// Congo is part-way through, Fight Club is finished, Avatar untouched.
	older := time.Now().UTC().Add(-time.Hour)
	inProgress := report(viewer.ID, congo.ID, 1800, 1800, false)
	inProgress.LastPlayedAt = &older
	if err := playback.Report(ctx, inProgress, 0); err != nil {
		t.Fatalf("Report: %v", err)
	}
	if err := playback.Report(ctx, report(viewer.ID, fight.ID, 0, 8000, true), 1); err != nil {
		t.Fatalf("Report: %v", err)
	}

	t.Run("state arrives with the items", func(t *testing.T) {
		items, _, err := media.ListItems(ctx, repository.ItemQuery{
			UserID: viewer.ID, Sort: repository.ItemSortTitle,
		})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}

		byTitle := map[string]repository.ItemWithFile{}
		for _, i := range items {
			byTitle[i.Item.Title] = i
		}

		if got := byTitle["Congo"].State.PositionSeconds; got != 1800 {
			t.Errorf("Congo position = %v, want 1800", got)
		}
		if !byTitle["Fight Club"].State.Played {
			t.Error("Fight Club is not marked played")
		}
		if got := byTitle["Fight Club"].State.PlayCount; got != 1 {
			t.Errorf("Fight Club PlayCount = %d, want 1", got)
		}
		// An item never played joins to nothing and arrives zeroed.
		if s := byTitle["Avatar"].State; s.Played || s.PositionSeconds != 0 || s.LastPlayedAt != nil {
			t.Errorf("Avatar carries state it should not: %+v", s)
		}
	})

	t.Run("another user sees none of it", func(t *testing.T) {
		items, _, err := media.ListItems(ctx, repository.ItemQuery{
			UserID: stranger.ID, Sort: repository.ItemSortTitle,
		})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		for _, i := range items {
			if i.State.Played || i.State.PositionSeconds != 0 {
				t.Errorf("%q leaked another user's state: %+v", i.Item.Title, i.State)
			}
		}
	})

	t.Run("in progress only", func(t *testing.T) {
		items, total, err := media.ListItems(ctx, repository.ItemQuery{
			UserID: viewer.ID, InProgressOnly: true,
		})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if total != 1 || len(items) != 1 || items[0].Item.Title != "Congo" {
			t.Fatalf("resume list = %v (total %d), want just Congo", titlesOf(items), total)
		}
	})

	t.Run("excluding played", func(t *testing.T) {
		items, _, err := media.ListItems(ctx, repository.ItemQuery{
			UserID: viewer.ID, ExcludePlayed: true, Sort: repository.ItemSortTitle,
		})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if got := titlesOf(items); !equalStrings(got, []string{"Avatar", "Congo"}) {
			t.Errorf("got %v, want the unplayed items", got)
		}
	})

	t.Run("played only", func(t *testing.T) {
		items, _, err := media.ListItems(ctx, repository.ItemQuery{
			UserID: viewer.ID, PlayedOnly: true,
		})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if got := titlesOf(items); !equalStrings(got, []string{"Fight Club"}) {
			t.Errorf("got %v, want the played item", got)
		}
	})

	t.Run("ordered by when it was last played", func(t *testing.T) {
		// Congo was played an hour ago, Fight Club just now.
		items, _, err := media.ListItems(ctx, repository.ItemQuery{
			UserID: viewer.ID, Sort: repository.ItemSortLastPlayed, Descending: true,
		})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if len(items) != 3 {
			t.Fatalf("got %d items, want 3", len(items))
		}
		if items[0].Item.Title != "Fight Club" || items[1].Item.Title != "Congo" {
			t.Errorf("order = %v, want most recently played first", titlesOf(items))
		}
		// Never played sorts last, not first.
		if items[2].Item.Title != "Avatar" {
			t.Errorf("an unplayed item is not last: %v", titlesOf(items))
		}
	})

	t.Run("a query with no user still runs", func(t *testing.T) {
		items, total, err := media.ListItems(ctx, repository.ItemQuery{Sort: repository.ItemSortTitle})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if total != 3 {
			t.Errorf("total = %d, want 3", total)
		}
		for _, i := range items {
			if i.State.Played || i.State.PositionSeconds != 0 {
				t.Errorf("%q carries state for no user: %+v", i.Item.Title, i.State)
			}
		}
	})
}

// TestItemRuntime covers the lookup every progress report makes.
func TestItemRuntime(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	media := repository.NewMediaRepository(pool)

	libraryID := seedLibrary(t, ctx, repository.NewLibraryRepository(pool))
	item := seedItem(t, ctx, media, libraryID, "Idiocracy", ptr(2006), 0)

	runtime, err := media.ItemRuntime(ctx, item.ID)
	if err != nil {
		t.Fatalf("ItemRuntime: %v", err)
	}
	if runtime == nil || *runtime != 5400.5 {
		t.Errorf("runtime = %v, want 5400.5", runtime)
	}

	t.Run("an unknown item is not found", func(t *testing.T) {
		if _, err := media.ItemRuntime(ctx, uuid.NewV7()); !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("ItemRuntime for an unknown id returned %v, want ErrNotFound", err)
		}
	})

	t.Run("an item with no file has no runtime", func(t *testing.T) {
		orphan := domain.MediaItem{
			LibraryID: libraryID, Kind: domain.MediaItemKindMovie,
			Title: "Orphan", SourcePath: "/media/Orphan.mkv",
		}
		if err := media.CreateItem(ctx, &orphan); err != nil {
			t.Fatalf("CreateItem: %v", err)
		}

		runtime, err := media.ItemRuntime(ctx, orphan.ID)
		if err != nil {
			t.Fatalf("ItemRuntime: %v", err)
		}
		if runtime != nil {
			t.Errorf("runtime = %v, want nil", *runtime)
		}
	})
}
