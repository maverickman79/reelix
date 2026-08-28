package jellyfin

import (
	"crypto/sha256"
	"net/http"

	"uuid"

	"github.com/maverickman79/reelix/internal/repository"
)

// The routes in this file are polled by Wholphin on its home screen. None of
// them carries data in 0.0.1 — the libraries themselves arrive with
// /UserViews and /Items — but all of them must answer in the recorded shape.
//
// A 404 here is worse than an empty response: Wholphin retries a 404 rather
// than treating it as final, so an unimplemented route produces a retry storm
// where a well-formed empty one settles. /UserImage is the single deliberate
// exception, explained on its handler.

// requestIDHeader is set on the response by the HTTP middleware. Declared
// here rather than imported: internal/server imports this package, and the
// dependency must not run the other way for the sake of one header name.
const requestIDHeader = "X-Request-ID"

// handleDisplayPreferences serves GET /DisplayPreferences/{prefsId}.
//
// Reelix does not persist display preferences in 0.0.1. The recorded fields
// are all present because the SDK's generated type declares them non-nullable,
// and the values are the reference server's defaults — a client reading
// skipForwardLength gets a sane skip button rather than a zero.
//
// The key is a PATH PARAMETER, not a fixed set. Probing the reference server
// showed it answers 200 for any key at all, and — this is the part that
// matters — that the key is CASE-SENSITIVE: /DisplayPreferences/default,
// /DEFAULT and /Default are three different preference records with three
// different ids. It is therefore registered as a parameter rather than as
// literal alternatives, so the fold trie leaves its case alone. Registering
// "default" as a literal would fold /displaypreferences/DEFAULT onto it and
// silently merge records the reference keeps apart.
//
// Wholphin asks for "default"; jellyfin-web asks for "usersettings" and
// blocks its entire post-login chain on the answer. Both arrive here.
//
// userId and client are not required, though every observed client sends
// both and the reference answers 400 without them. Reelix is deliberately the
// more lenient of the two: accepting a request the reference would reject
// cannot break a client, and rejecting one it accepts can.
func (a *API) handleDisplayPreferences(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, displayPreferences{
		ID:                 displayPreferencesID(r.PathValue("prefsId")),
		SortBy:             "SortName",
		RememberIndexing:   false,
		PrimaryImageHeight: 250,
		PrimaryImageWidth:  250,
		CustomPrefs: map[string]any{
			"chromecastVersion":          "stable",
			"skipForwardLength":          "30000",
			"skipBackLength":             "10000",
			"enableNextVideoInfoOverlay": "False",
			"tvhome":                     nil,
			"dashboardTheme":             nil,
		},
		ScrollDirection: "Horizontal",
		ShowBackdrop:    true,
		RememberSorting: false,
		SortOrder:       "Ascending",
		ShowSidebar:     false,
		// Echoed back so a client that keeps preferences per client sees the
		// answer belong to the question it asked.
		Client: r.URL.Query().Get("client"),
	})
}

// displayPreferencesID derives the opaque id a preferences record is returned
// under.
//
// The reference server derives this from the key by some hash that is NOT a
// plain MD5 of it — that was tested and ruled out. It is deliberately NOT
// reproduced here. Working out the exact algorithm would mean reconstructing a
// server-side implementation detail, which is the wrong side of the clean-room
// rule in CLAUDE.md, and nothing needs the exact bytes: the client treats this
// value as opaque and only ever echoes it back.
//
// What IS reproduced is every observable property the probe established:
//
//   - stable across calls, so a polling client is not told each time that its
//     preferences were replaced
//   - distinct per key, so "default" and "usersettings" cannot collide
//   - identical across users, which the reference confirms — the same key
//     returns the same id for two different userIds
//   - shaped like a UUID, because clients parse it as one
//
// The namespace makes the digest specific to this use, so an id here can
// never coincide with one derived elsewhere from the same key.
func displayPreferencesID(key string) string {
	sum := sha256.Sum256([]byte("reelix/displaypreferences\x00" + key))

	var id uuid.UUID
	copy(id[:], sum[:])
	return compatID(id)
}

// handleUserImage serves GET /UserImage.
//
// This is the one route in this file that answers 404, and it is deliberate.
// The reference server 404s here for a user with no avatar, which every
// Reelix user is: the project stores no user images. Wholphin called it ten
// times across the recorded session — call orders 10, 31, 45, through 196 —
// which is a per-screen re-request as the user moves around, not the backoff
// storm a 404 provokes on a route the client actually needs. Answering 200
// with an empty body would instead hand the client a zero-byte image to fail
// on. Do not "fix" this into an empty response.
func (a *API) handleUserImage(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusNotFound, problemDetails{
		Type:   "https://tools.ietf.org/html/rfc9110#section-15.5.5",
		Title:  "Not Found",
		Status: http.StatusNotFound,
		// Reelix's own request id, so a trace a client reports greps straight
		// to the log line that produced it.
		TraceID: w.Header().Get(requestIDHeader),
	})
}

// handleResumeItems serves GET /UserItems/Resume.
//
// Continue Watching: the items this user is part-way through, most recently
// played first. The userId query parameter is ignored — the user comes from
// the token, and the recorded traffic shows Wholphin omitting userId entirely
// on some calls.
func (a *API) handleResumeItems(w http.ResponseWriter, r *http.Request) {
	settings, err := a.sessions.ServerSettings(r.Context())
	if err != nil {
		a.fail(r, "resume_items", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	query, empty := browseQuery(r)
	if empty {
		a.writeJSON(w, r, http.StatusOK, emptyItemsResult(query.Offset))
		return
	}

	query.InProgressOnly = true
	query.Sort = repository.ItemSortLastPlayed
	query.Descending = true

	result, err := a.media.Browse(r.Context(), query)
	if err != nil {
		a.fail(r, "resume_items", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	items := make([]itemDTO, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, newItemDTO(row, settings, result.Metadata[row.Item.ID]))
	}

	a.writeJSON(w, r, http.StatusOK, queryResult[itemDTO]{
		Items:            items,
		TotalRecordCount: result.Total,
		StartIndex:       query.Offset,
	})
}

// handleNextUp serves GET /Shows/NextUp.
//
// Permanently empty in this milestone rather than unimplemented: TV series
// libraries are excluded from 0.0.1, so there is never a next episode.
func (a *API) handleNextUp(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, emptyQueryResult())
}

// handleRecordingFolders serves GET /LiveTv/Recordings/Folders.
//
// Also permanently empty: Live TV and DVR are excluded from 0.0.1. The
// reference server answered the same way, having no tuner configured.
func (a *API) handleRecordingFolders(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, emptyQueryResult())
}
