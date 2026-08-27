package jellyfin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// userID returns the authenticated user's id in the dashless form clients
// build URLs from, which is what the legacy routes carry in the path.
func (h *harness) userID(t *testing.T, token string) string {
	t.Helper()

	resp := h.do(http.MethodGet, "/Users/Me", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/Users/Me returned %d", resp.StatusCode)
	}

	var out struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(h.bodyOf(resp), &out); err != nil {
		t.Fatalf("decoding /Users/Me: %v", err)
	}
	if out.ID == "" {
		t.Fatal("/Users/Me returned no id")
	}
	return out.ID
}

// TestNormalizeLegacyPath pins the rewrites, and their limits.
//
// Every "want" here was confirmed against a real Jellyfin 10.11.8: the alias
// cases return 200 there and the rejected cases return a routing 404. This is
// reproduced behaviour, not a guess about what a prefix ought to mean.
func TestNormalizeLegacyPath(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"untouched", "/System/Info/Public", "/System/Info/Public"},
		{"emby prefix", "/emby/System/Info/Public", "/System/Info/Public"},
		{"mediabrowser prefix", "/mediabrowser/System/Info/Public", "/System/Info/Public"},

		// A real server matches the prefix without regard to case, even
		// though it is otherwise irrelevant to Reelix's own matching.
		{"capitalised prefix", "/Emby/System/Info/Public", "/System/Info/Public"},
		{"shouting prefix", "/EMBY/System/Info/Public", "/System/Info/Public"},
		{"mixed case mediabrowser", "/MediaBrowser/Users/Public", "/Users/Public"},

		// Stripped once. /emby/emby/... is a 404 on a real server, so the
		// second prefix must survive and fail to match.
		{"stripped once only", "/emby/emby/System/Info/Public", "/emby/System/Info/Public"},

		// Not aliases. Probing confirmed both are routing 404s.
		{"jellyfin is not a prefix", "/jellyfin/System/Info/Public", "/jellyfin/System/Info/Public"},
		{"api is not a prefix", "/api/System/Info/Public", "/api/System/Info/Public"},

		// Only the first segment. A path that merely contains the word is
		// left alone.
		{"prefix word deeper in the path", "/Items/emby/Images", "/Items/emby/Images"},

		{"trailing slash", "/System/Info/Public/", "/System/Info/Public"},
		{"prefix and trailing slash", "/emby/System/Info/Public/", "/System/Info/Public"},
		{"root", "/", "/"},
		{"empty", "", ""},
		{"prefix alone", "/emby", "/"},
		{"prefix alone with slash", "/emby/", "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeLegacyPath(tc.in); got != tc.want {
				t.Errorf("normalizeLegacyPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestLegacyPrefixReachesTheSameHandler checks the middleware end to end, on
// the route VidHub cannot log in without.
func TestLegacyPrefixReachesTheSameHandler(t *testing.T) {
	h := newHarness(t)

	bare := h.bodyOf(h.do(http.MethodGet, "/System/Info/Public", "", nil))

	for _, path := range []string{
		"/emby/System/Info/Public",
		"/mediabrowser/System/Info/Public",
		"/Emby/System/Info/Public",
		"/emby/System/Info/Public/",
	} {
		t.Run(path, func(t *testing.T) {
			resp := h.do(http.MethodGet, path, "", nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s returned %d, want 200", path, resp.StatusCode)
			}
			if got := h.bodyOf(resp); string(got) != string(bare) {
				t.Errorf("%s returned a different body than the bare path:\n got: %s\nwant: %s",
					path, got, bare)
			}
		})
	}
}

// TestUnknownPrefixIsStillA404 checks the alias list is a list and not a rule
// that strips whatever it finds. A general "drop the first segment" would turn
// every typo into a 200 for the wrong route.
func TestUnknownPrefixIsStillA404(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{
		"/jellyfin/System/Info/Public",
		"/api/System/Info/Public",
		"/emby/emby/System/Info/Public",
		"/notaprefix/System/Info/Public",
	} {
		t.Run(path, func(t *testing.T) {
			if resp := h.do(http.MethodGet, path, "", nil); resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s returned %d, want 404", path, resp.StatusCode)
			}
		})
	}
}

// TestAuthenticateByNameUnderPrefix is the specific failure VidHub reported:
// it prefixes every request, so login itself 404s and it never gets further.
func TestAuthenticateByNameUnderPrefix(t *testing.T) {
	h := newHarness(t)

	body := map[string]string{"Username": testUser, "Pw": testPassword}
	resp := h.do(http.MethodPost, "/emby/Users/AuthenticateByName", "", body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prefixed login returned %d, want 200", resp.StatusCode)
	}
	if got := h.bodyOf(resp); !strings.Contains(string(got), "AccessToken") {
		t.Errorf("prefixed login returned no token: %s", got)
	}
}

// TestStreamSpellingNormalisation pins which paths get the extension stripped.
func TestStreamSpellingNormalisation(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"mkv", "/Videos/abc/stream.mkv", "/Videos/abc/stream"},
		{"mp4", "/Videos/abc/stream.mp4", "/Videos/abc/stream"},
		{"ts", "/Videos/abc/stream.ts", "/Videos/abc/stream"},
		{"bare is untouched", "/Videos/abc/stream", "/Videos/abc/stream"},
		{"under a prefix", "/emby/Videos/abc/stream.mkv", "/Videos/abc/stream"},

		// Not the stream route. Rewriting these would route a request to a
		// handler that was never asked for.
		{"audio is not implemented", "/Audio/abc/stream.mp3", "/Audio/abc/stream.mp3"},
		{"hls is not this route", "/Videos/abc/master.m3u8", "/Videos/abc/master.m3u8"},
		{"a different verb", "/Videos/abc/notstream.mkv", "/Videos/abc/notstream.mkv"},
		{"empty extension", "/Videos/abc/stream.", "/Videos/abc/stream."},
		{"not under Videos", "/Items/abc/stream.mkv", "/Items/abc/stream.mkv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeLegacyPath(tc.in); got != tc.want {
				t.Errorf("normalizeLegacyPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// userScopedRoutes are the legacy spellings and the modern route each is an
// alias for. Confirmed present on a real Jellyfin 10.11.8.
var userScopedRoutes = []struct{ name, legacy, modern string }{
	{"items", "/Users/%s/Items", "/Items"},
	{"resume", "/Users/%s/Items/Resume", "/UserItems/Resume"},
	{"latest", "/Users/%s/Items/Latest", "/Items/Latest"},
	{"views", "/Users/%s/Views", "/UserViews"},
}

// TestUserScopedAliasesMatchTheModernRoutes checks each legacy spelling
// returns exactly what its modern twin returns.
func TestUserScopedAliasesMatchTheModernRoutes(t *testing.T) {
	h := newHarness(t)
	seedMedia(t, h)
	token := h.login()
	userID := h.userID(t, token)

	for _, rt := range userScopedRoutes {
		t.Run(rt.name, func(t *testing.T) {
			legacy := fmt.Sprintf(rt.legacy, userID)

			modernResp := h.do(http.MethodGet, rt.modern, token, nil)
			if modernResp.StatusCode != http.StatusOK {
				t.Fatalf("%s returned %d", rt.modern, modernResp.StatusCode)
			}
			modernBody := h.bodyOf(modernResp)

			legacyResp := h.do(http.MethodGet, legacy, token, nil)
			if legacyResp.StatusCode != http.StatusOK {
				t.Fatalf("%s returned %d, want 200", legacy, legacyResp.StatusCode)
			}
			if got := h.bodyOf(legacyResp); string(got) != string(modernBody) {
				t.Errorf("%s and %s differ:\n legacy: %s\n modern: %s",
					legacy, rt.modern, got, modernBody)
			}
		})
	}
}

// TestUserScopedItemAliases covers the per-item spellings, which carry both a
// user id and an item id.
func TestUserScopedItemAliases(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()
	userID := h.userID(t, token)
	itemID := compatID(seeded.items[0].ID)

	for _, suffix := range []string{"", "/Intros", "/SpecialFeatures"} {
		t.Run("item"+suffix, func(t *testing.T) {
			modern := "/Items/" + itemID + suffix
			legacy := "/Users/" + userID + "/Items/" + itemID + suffix

			modernResp := h.do(http.MethodGet, modern, token, nil)
			if modernResp.StatusCode != http.StatusOK {
				t.Fatalf("%s returned %d", modern, modernResp.StatusCode)
			}
			modernBody := h.bodyOf(modernResp)

			legacyResp := h.do(http.MethodGet, legacy, token, nil)
			if legacyResp.StatusCode != http.StatusOK {
				t.Fatalf("%s returned %d, want 200", legacy, legacyResp.StatusCode)
			}
			if got := h.bodyOf(legacyResp); string(got) != string(modernBody) {
				t.Errorf("%s and %s differ", legacy, modern)
			}
		})
	}
}

// TestThemeSongsHasNoUserScopedTwin pins an ABSENCE.
//
// A real Jellyfin 10.11.8 serves /Items/{id}/ThemeSongs and answers a routing
// 404 for /Users/{userId}/Items/{id}/ThemeSongs. The user-scoped family looks
// mechanical and is not, so this test exists to fail if someone later
// "completes" it by generating twins for every modern route. Matching the
// reference server means matching what it does not serve.
func TestThemeSongsHasNoUserScopedTwin(t *testing.T) {
	h := newHarness(t)
	seeded := seedMedia(t, h)
	token := h.login()
	userID := h.userID(t, token)
	itemID := compatID(seeded.items[0].ID)

	if resp := h.do(http.MethodGet, "/Items/"+itemID+"/ThemeSongs", token, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the modern spelling returned %d, want 200", resp.StatusCode)
	}

	legacy := "/Users/" + userID + "/Items/" + itemID + "/ThemeSongs"
	if resp := h.do(http.MethodGet, legacy, token, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("%s returned %d, want 404 — a real server has no user-scoped ThemeSongs, "+
			"and inventing one is not compatibility", legacy, resp.StatusCode)
	}
}

// TestUserScopedRouteRejectsAnotherUser is the reason the path id is checked
// rather than trusted: without it, any authenticated client could read any
// user's library and playback state by editing a URL.
func TestUserScopedRouteRejectsAnotherUser(t *testing.T) {
	h := newHarness(t)
	seedMedia(t, h)

	token := h.login()
	otherToken := h.loginAs(t, "someone-else")
	otherID := h.userID(t, otherToken)

	resp := h.do(http.MethodGet, "/Users/"+otherID+"/Items", token, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("reading another user's items returned %d, want 403", resp.StatusCode)
	}
}

// TestUserScopedRouteRejectsAMalformedID checks a path id that is not an id
// is a 400 rather than a panic or a 403 that suggests the id was real.
func TestUserScopedRouteRejectsAMalformedID(t *testing.T) {
	h := newHarness(t)
	token := h.login()

	if resp := h.do(http.MethodGet, "/Users/not-an-id/Items", token, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a malformed user id returned %d, want 400", resp.StatusCode)
	}
}

// TestUserScopedRouteStillRequiresAToken checks the alias did not become a way
// in without one.
func TestUserScopedRouteStillRequiresAToken(t *testing.T) {
	h := newHarness(t)

	if resp := h.do(http.MethodGet, "/Users/"+strings.Repeat("0", 32)+"/Items", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unauthenticated user-scoped request returned %d, want 401", resp.StatusCode)
	}
}

// TestStreamWithContainerExtensionServesTheFile checks the spelling clients
// append actually plays, range requests included.
//
// The extension is discarded: Reelix direct-plays the original file and has no
// other bytes to send. When transcoding lands, a container that differs from
// the file's own becomes a request this cannot answer by ignoring.
func TestStreamWithContainerExtensionServesTheFile(t *testing.T) {
	h := newHarness(t)
	media := seedPlayable(t, h, recordedSize, map[int64]int{
		recordedSeek: recordedMarkerLen,
	})

	bare := media.streamURL()

	for _, ext := range []string{".mkv", ".mp4", ".ts"} {
		t.Run(ext, func(t *testing.T) {
			url := strings.Replace(bare, "/stream", "/stream"+ext, 1)

			resp := h.getRange(t, url, fmt.Sprintf("bytes=%d-%d",
				recordedSeek, recordedSeek+recordedMarkerLen-1))
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusPartialContent {
				t.Fatalf("stream%s returned %d, want 206", ext, resp.StatusCode)
			}

			wantRange := fmt.Sprintf("bytes %d-%d/%d",
				recordedSeek, recordedSeek+recordedMarkerLen-1, recordedSize)
			if got := resp.Header.Get("Content-Range"); got != wantRange {
				t.Errorf("stream%s Content-Range = %q, want %q", ext, got, wantRange)
			}
		})
	}
}

// TestVidHubStreamRequest is the literal request VidHub retries indefinitely:
// lowercase "videos", the /emby prefix, and a container extension, all at once.
//
// It is a regression test for a PIPELINE ORDER bug as much as for case
// folding. The extension has to come off before the fold runs, because
// "stream.mkv" is not a literal in any registered pattern. Adding
// case-insensitive matching without fixing normalizeStreamSpelling's own
// case-sensitive guard would have left this request a 404 and looked like a
// fix.
func TestVidHubStreamRequest(t *testing.T) {
	h := newHarness(t)
	media := seedPlayable(t, h, recordedSize, map[int64]int{
		recordedSeek: recordedMarkerLen,
	})

	// The query string is split off and reattached. Keeping it joined puts
	// the container extension into the QUERY rather than the path, which
	// silently turns every case below into a request for the bare /stream
	// route — a test that passes without exercising anything.
	canonicalPath, query, _ := strings.Cut(media.streamURL(), "?")
	id := strings.TrimSuffix(strings.TrimPrefix(canonicalPath, "/Videos/"), "/stream")

	// Every spelling that must reach the same bytes.
	for _, tc := range []struct{ name, path string }{
		{"lowercase videos with prefix and extension", "/emby/videos/%s/stream.mkv"},
		{"lowercase videos, no prefix", "/videos/%s/stream.mkv"},
		{"lowercase everything", "/emby/videos/%s/stream"},
		{"uppercase path", "/EMBY/VIDEOS/%s/STREAM.MKV"},
		{"mediabrowser prefix, lowercase", "/mediabrowser/videos/%s/stream.mkv"},
		{"canonical", "/Videos/%s/stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := strings.Replace(tc.path, "%s", id, 1) + "?" + query

			resp := h.getRange(t, path, fmt.Sprintf("bytes=%d-%d",
				recordedSeek, recordedSeek+recordedMarkerLen-1))
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusPartialContent {
				t.Fatalf("%s returned %d, want 206", path, resp.StatusCode)
			}

			wantRange := fmt.Sprintf("bytes %d-%d/%d",
				recordedSeek, recordedSeek+recordedMarkerLen-1, recordedSize)
			if got := resp.Header.Get("Content-Range"); got != wantRange {
				t.Errorf("%s Content-Range = %q, want %q", path, got, wantRange)
			}
		})
	}
}

// TestUserObjectRoute covers /Users/{userId}, and the precedence it depends
// on: /Users/Me and /Users/Public are literals and must keep beating it.
func TestUserObjectRoute(t *testing.T) {
	h := newHarness(t)
	token := h.login()
	userID := h.userID(t, token)

	me := h.bodyOf(h.do(http.MethodGet, "/Users/Me", token, nil))

	t.Run("returns the same object as /Users/Me", func(t *testing.T) {
		resp := h.do(http.MethodGet, "/Users/"+userID, token, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("/Users/{id} returned %d, want 200", resp.StatusCode)
		}
		if got := h.bodyOf(resp); string(got) != string(me) {
			t.Errorf("/Users/{id} and /Users/Me differ:\n got: %s\nwant: %s", got, me)
		}
	})

	t.Run("under a prefix and lowercased", func(t *testing.T) {
		resp := h.do(http.MethodGet, "/emby/users/"+userID, token, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("prefixed lowercase returned %d, want 200", resp.StatusCode)
		}
	})

	t.Run("another user is forbidden", func(t *testing.T) {
		otherID := h.userID(t, h.loginAs(t, "other-person"))
		if resp := h.do(http.MethodGet, "/Users/"+otherID, token, nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("another user's object returned %d, want 403", resp.StatusCode)
		}
	})

	// The precedence the fold has to reproduce. If /Users/{userId} ever won
	// over these, "Me" and "Public" would be parsed as user ids.
	t.Run("literals still beat the parameter", func(t *testing.T) {
		if resp := h.do(http.MethodGet, "/users/me", token, nil); resp.StatusCode != http.StatusOK {
			t.Errorf("/users/me returned %d, want 200", resp.StatusCode)
		}
		if resp := h.do(http.MethodGet, "/users/public", "", nil); resp.StatusCode != http.StatusOK {
			t.Errorf("/users/public returned %d, want 200", resp.StatusCode)
		}
	})
}

// TestJellyfinWebStreamRequest is the literal request that failed on Gangland,
// reproduced whole rather than in pieces.
//
// jellyfin-web builds its direct-play URL as
//
//	Videos/{id}/stream.{container}?Static=true&mediaSourceId=…&ApiKey=…&Tag=…
//
// taking {container} from the MediaSource's Container field and capitalising
// both credential parameters. Reelix answered 401 to this, which read like a
// credential problem and was not one: the route matched, and authorizeStream
// simply could not see a "Tag" it was looking for as "tag".
//
// Both halves are exercised together on purpose. Fixing only the container
// leaves a well-formed URL that still 401s; fixing only the lookup leaves a
// URL built from a raw ffprobe list. The bug needed both.
func TestJellyfinWebStreamRequest(t *testing.T) {
	h := newHarness(t)
	media := seedPlayable(t, h, recordedSize, map[int64]int{
		recordedSeek: recordedMarkerLen,
	})

	id := compatID(media.item.ID)

	for _, tc := range []struct{ name, url string }{
		{
			// Exactly what the bundle assembles, capitalisation included.
			name: "jellyfin-web spelling",
			url: "/Videos/" + id + "/stream.mkv?Static=true&mediaSourceId=" + id +
				"&Tag=" + media.etag,
		},
		{
			// The other credential jellyfin-web uses, as ApiKey rather than
			// api_key. A real 10.11.8 accepts both.
			name: "ApiKey rather than api_key",
			url:  "/Videos/" + id + "/stream.mkv?Static=true&ApiKey=" + h.login(),
		},
		{
			// Wholphin's spelling must keep working.
			name: "wholphin spelling",
			url: "/Videos/" + id + "/stream.mkv?static=true&tag=" + media.etag +
				"&mediaSourceId=" + id,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.getRange(t, tc.url, fmt.Sprintf("bytes=%d-%d",
				recordedSeek, recordedSeek+recordedMarkerLen-1))
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusPartialContent {
				t.Fatalf("returned %d, want 206", resp.StatusCode)
			}
		})
	}
}

// TestStreamStillRefusesABadCredential guards the fix above from having
// widened the capability check rather than only its spelling.
//
// This is the one place Reelix is deliberately STRICTER than the reference,
// which was probed serving these bytes to a request carrying no credential at
// all — and even to one carrying a wrong tag. The capability model is Reelix's
// own decision; making the lookup case-insensitive must not have quietly
// turned it off.
func TestStreamStillRefusesABadCredential(t *testing.T) {
	h := newHarness(t)
	media := seedPlayable(t, h, recordedSize, map[int64]int{
		recordedSeek: recordedMarkerLen,
	})

	id := compatID(media.item.ID)

	for _, tc := range []struct{ name, url string }{
		{"no credential", "/Videos/" + id + "/stream.mkv?Static=true"},
		{"wrong Tag", "/Videos/" + id + "/stream.mkv?Tag=deadbeef"},
		{"wrong ApiKey", "/Videos/" + id + "/stream.mkv?ApiKey=deadbeef"},
		{"empty Tag", "/Videos/" + id + "/stream.mkv?Tag="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.getRange(t, tc.url, "")
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("returned %d, want 401", resp.StatusCode)
			}
		})
	}
}
