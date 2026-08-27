package jellyfin

import "net/http"

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

// handleDisplayPreferences serves GET /DisplayPreferences/default.
//
// Reelix does not persist display preferences in 0.0.1. The recorded fields
// are all present because the SDK's generated type declares them non-nullable,
// and the values are the reference server's defaults — a client reading
// skipForwardLength gets a sane skip button rather than a zero.
func (a *API) handleDisplayPreferences(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, displayPreferences{
		// Stable across calls without storing anything, and opaque: the
		// client only ever echoes it back.
		ID:                 compatID(userFrom(r.Context()).ID),
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
// Nothing is resumable until playback state exists, which arrives with
// Step 7. The userId query parameter is ignored: the user comes from the
// token, and the recorded traffic shows Wholphin omitting userId entirely on
// some calls.
func (a *API) handleResumeItems(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, emptyQueryResult())
}

// handleLatestItems serves GET /Items/Latest.
//
// A bare array, not a QueryResult — the recorded response is a top-level
// list, and the SDK's generated type expects one.
//
// Empty for now. The reference server returned six movies here, and this must
// be revisited once /Items exists: an empty Latest row on a populated server
// is wrong, it is just not wrong in a way that stops the client.
func (a *API) handleLatestItems(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, emptyList())
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
