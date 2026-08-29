package jellyfin

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/maverickman79/reelix/internal/repository"
	"github.com/maverickman79/reelix/internal/service"
)

// handleUserViews serves GET /UserViews.
//
// The libraries a user can browse. Wholphin cannot render a home screen
// without it: with this route missing the client restarted its whole startup
// sequence four times in 45 seconds on the SK1, so it is load-bearing rather
// than one row among many.
func (a *API) handleUserViews(w http.ResponseWriter, r *http.Request) {
	settings, err := a.sessions.ServerSettings(r.Context())
	if err != nil {
		a.fail(r, "user_views", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	views, err := a.media.Views(r.Context())
	if err != nil {
		a.fail(r, "user_views", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	items := make([]viewDTO, 0, len(views))
	for _, v := range views {
		items = append(items, newViewDTO(v, settings))
	}

	a.writeJSON(w, r, http.StatusOK, queryResult[viewDTO]{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       0,
	})
}

// handleItems serves GET /Items.
//
// One route, two uses: browsing a library by parentId, and resolving a list of
// ids the client already holds. Both are recorded in the capture.
func (a *API) handleItems(w http.ResponseWriter, r *http.Request) {
	settings, err := a.sessions.ServerSettings(r.Context())
	if err != nil {
		a.fail(r, "items", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	query, empty := browseQuery(r)
	if empty {
		// The client asked for something Reelix demonstrably has none of —
		// episodes, for instance. An empty result is the truthful answer, and
		// unlike a 404 the client accepts it as final.
		a.writeJSON(w, r, http.StatusOK, emptyItemsResult(query.Offset))
		return
	}

	result, err := a.media.Browse(r.Context(), query)
	if err != nil {
		if errors.Is(err, service.ErrInvalidArgument) {
			writeStatus(w, http.StatusBadRequest)
			return
		}
		a.fail(r, "items", err)
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

// handleItem serves GET /Items/{id}.
//
// A library is an item too: Jellyfin models one as a CollectionFolder, and a
// client that follows a view's id here should get the view rather than a 404
// it will keep retrying.
func (a *API) handleItem(w http.ResponseWriter, r *http.Request) {
	settings, err := a.sessions.ServerSettings(r.Context())
	if err != nil {
		a.fail(r, "item", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	id, err := parseCompatID(r.PathValue("id"))
	if err != nil {
		writeStatus(w, http.StatusNotFound)
		return
	}

	detail, err := a.media.Item(r.Context(), id, userFrom(r.Context()).ID)
	switch {
	case err == nil:
		a.writeJSON(w, r, http.StatusOK, newItemDetailDTO(detail, settings))
		return

	case errors.Is(err, service.ErrItemNotFound):
		// Fall through to the library lookup below.

	default:
		a.fail(r, "item", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	views, err := a.media.Views(r.Context())
	if err != nil {
		a.fail(r, "item", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	for _, v := range views {
		if v.Library.ID == id {
			a.writeJSON(w, r, http.StatusOK, newViewDTO(v, settings))
			return
		}
	}

	// Genuinely no such item. 404 is what Jellyfin answers and what the
	// client's own model expects for an id that has gone away.
	writeStatus(w, http.StatusNotFound)
}

// handleLatestItems serves GET /Items/Latest.
//
// A bare array, not a QueryResult — the recorded response is a top-level list
// and the SDK's generated type expects one.
func (a *API) handleLatestItems(w http.ResponseWriter, r *http.Request) {
	settings, err := a.sessions.ServerSettings(r.Context())
	if err != nil {
		a.fail(r, "items_latest", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	query, empty := browseQuery(r)
	if empty {
		a.writeJSON(w, r, http.StatusOK, []itemDTO{})
		return
	}

	// Newest first, whatever the client asked to sort by: "latest" is the
	// ordering, not a preference.
	query.Sort = repository.ItemSortCreatedAt
	query.Descending = true
	query.Offset = 0

	// Reelix reports HidePlayedInLatest in the user configuration, so this
	// row has to actually hide them.
	query.ExcludePlayed = true

	result, err := a.media.Browse(r.Context(), query)
	if err != nil {
		a.fail(r, "items_latest", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	items := make([]itemDTO, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, newItemDTO(row, settings, result.Metadata[row.Item.ID]))
	}

	a.writeJSON(w, r, http.StatusOK, items)
}

// handleItemImage serves GET /Items/{id}/Images/{type}.
//
// The type is canonicalised rather than echoed, so that "primary" and
// "Primary" are one type here and not two. See canonicalImageType: the route
// parameter is not touched by the fold trie, deliberately, so this is the only
// place the spelling can be settled — and it is now load-bearing rather than
// cosmetic, because a lookup keyed on the wrong spelling would 404 an image
// that is on disk. VidHub is the client that lowercases its paths.
//
// UNAUTHENTICATED. See the registration in api.go for what that concedes.
//
// AN UNKNOWN TYPE STILL ANSWERS 404. The reference answers 400 with an ASP.NET
// validation envelope naming the bad value; reproducing that would mean
// inventing a body shape — including a traceId — for a request no observed
// client makes. Recorded rather than reproduced.
func (a *API) handleItemImage(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("type")
	if kind == "" {
		// The reference treats an omitted type as Primary.
		kind = "Primary"
	}
	canonical, known := canonicalImageType(kind)
	if !known {
		a.writeJSON(w, r, http.StatusNotFound, "item does not have an image of type "+kind)
		return
	}

	id, err := parseCompatID(r.PathValue("id"))
	if err != nil {
		a.writeJSON(w, r, http.StatusNotFound, "item does not have an image of type "+canonical)
		return
	}

	// Reelix stores one image per type, so index 0 is the only one that can
	// exist. A client asking for another gets the truth.
	if !firstImageIndex(r) {
		a.writeJSON(w, r, http.StatusNotFound, "item does not have an image of type "+canonical)
		return
	}

	img, err := a.metadata.GetImage(r.Context(), id, strings.ToLower(canonical))
	if err != nil {
		// A recorded negative, an absent row and an unknown item are one
		// answer here: there are no bytes, and the client should draw its
		// placeholder rather than retry.
		a.writeJSON(w, r, http.StatusNotFound, "item does not have an image of type "+canonical)
		return
	}

	file, info, err := a.metadata.Images().Open(img.StoragePath)
	if err != nil {
		// The row said there were bytes and there are not. 404 rather than
		// 500: the client's correct response is identical to any other missing
		// image, and a 500 invites a retry that cannot succeed.
		//
		// Nothing is written to the database from here. The repair belongs to
		// the refresh pass's reconcile sweep, which stats every row and clears
		// the stale ones — a read path that writes would be a second place
		// that decides what a row means.
		a.log.Warn("a stored image is missing from disk",
			slog.String("item_id", id.String()),
			slog.String("image_type", canonical),
			slog.String("path", img.StoragePath))
		a.writeJSON(w, r, http.StatusNotFound, "item does not have an image of type "+canonical)
		return
	}
	defer file.Close()

	// Matching the recorded response headers. Cache-Control and Last-Modified
	// are the pair the reference uses — it sends no ETag — and the recorded
	// capture shows a client returning with If-Modified-Since and being
	// answered 304. ServeContent does that comparison, so conditional requests
	// work without a second implementation of the rule.
	w.Header().Set("Content-Type", img.ContentType)
	w.Header().Set("Cache-Control", "public")
	// Inert for an <img>, which is how every client here loads these, but the
	// reference sends it and matching costs one line.
	w.Header().Set("Content-Disposition", "attachment")

	// The name is only used to guess a content type, which is already set.
	http.ServeContent(w, r, "", info.ModTime(), file)
}

// firstImageIndex reports whether the request is for image index 0.
//
// The index arrives in two spellings — a path segment on the
// /Images/{type}/{index} route, and an imageIndex query parameter, both of
// which appear in the recorded traffic — and an absent index means 0.
func firstImageIndex(r *http.Request) bool {
	index := r.PathValue("index")
	if index == "" {
		index = queryValue(r, "imageIndex")
	}
	return index == "" || index == "0"
}

// RESIZING IS NOT IMPLEMENTED, AND THAT IS A DECISION RATHER THAN A GAP.
//
// Clients ask for specific dimensions: the recorded requests carry quality and
// fillHeight, and the published spec also defines maxWidth, maxHeight,
// fillWidth and others that no observed client sends. Reelix HONOURS NONE OF
// THEM and serves the stored image.
//
// The sizing decision is made once per image at download time instead — see
// posterSize and its neighbours in the TMDB provider — which puts the bytes on
// the wire in roughly the range the reference server returned for the same
// request, for no code here at all.
//
// What serve-time resizing would actually cost is not the resampling library.
// It is the second cache: resized output keyed by dimensions, with its own
// eviction policy, its own key derivation, and its own half-written-file
// problem — the whole of this slice again, to serve a 780px poster into a
// 344px slot.
//
// EVERY UNHONOURED PARAMETER IS ACCEPTED, NEVER REJECTED. Answering 400 for a
// parameter Reelix does not implement would turn a cosmetic inefficiency into
// a missing image, which is much the worse failure. The trigger to revisit is
// evidence: a measured bandwidth problem, or a client that visibly renders
// badly.
//
// THE LONG POSITIONAL IMAGE FORMS ARE STILL NOT ROUTED, for the same
// evidence-first reason they were left out when there was no artwork. They are
// confirmed to exist on the reference — see docs/compat-capture.md — but NO
// CAPTURED REQUEST USES ONE: every recorded image request is
// /Items/{id}/Images/{Type} with the parameters above. Their absence is
// therefore a finding, not an oversight. Watch the access log while a real
// client renders a grid, and add them if one appears.

// handleItemIntros serves GET /Items/{id}/Intros.
func (a *API) handleItemIntros(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, emptyQueryResult())
}

// handleSimilarItems serves GET /Items/{id}/Similar.
//
// Similarity needs metadata Reelix does not collect in 0.0.1.
func (a *API) handleSimilarItems(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, emptyQueryResult())
}

// handleSpecialFeatures serves GET /Items/{id}/SpecialFeatures.
//
// A bare array, like the recorded response.
func (a *API) handleSpecialFeatures(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, emptyList())
}

// handleThemeSongs serves GET /Items/{id}/ThemeSongs.
//
// The envelope carries the id it was asked about, which is what the recorded
// response did.
func (a *API) handleThemeSongs(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("id")
	if id, err := parseCompatID(owner); err == nil {
		owner = compatID(id)
	}

	a.writeJSON(w, r, http.StatusOK, themeMediaResult{
		OwnerID:          owner,
		Items:            emptyList(),
		TotalRecordCount: 0,
		StartIndex:       0,
	})
}

// handleMediaSegments serves GET /MediaSegments/{id}.
//
// Intro and credit detection is excluded from 0.0.1, so there are no segments.
func (a *API) handleMediaSegments(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, emptyQueryResult())
}

// emptyItemsResult is a well-formed empty page of items.
func emptyItemsResult(offset int) queryResult[itemDTO] {
	return queryResult[itemDTO]{Items: []itemDTO{}, StartIndex: offset}
}

// browseQuery translates a client's query parameters into a native browse.
//
// The second return value reports that the request can only match nothing, so
// the caller can answer without a database round trip. That is a real answer
// rather than a shortcut: a client asking for episodes on a server with no
// episodes should be told there are none.
func browseQuery(r *http.Request) (service.BrowseQuery, bool) {
	q := r.URL.Query()

	query := service.BrowseQuery{
		UserID: userFrom(r.Context()).ID,
		Offset: intParam(q.Get("startIndex"), 0),
		Limit:  intParam(q.Get("limit"), 0),
	}

	// 0.0.1 has movie libraries only. Asking for any other type is asking for
	// something Reelix does not have.
	if types := trimmed(q.Get("includeItemTypes")); len(types) > 0 && !contains(types, "Movie") {
		return query, true
	}

	// A client filtering on played state is asking about this user's history.
	switch {
	case strings.EqualFold(q.Get("isPlayed"), "true"):
		query.PlayedOnly = true
	case strings.EqualFold(q.Get("isPlayed"), "false"):
		query.ExcludePlayed = true
	}

	if raw := q.Get("parentId"); raw != "" {
		parent, err := parseCompatID(raw)
		if err != nil {
			return query, true
		}
		query.LibraryIDs = []uuid.UUID{parent}
	}

	if raw := q.Get("ids"); raw != "" {
		ids := make([]uuid.UUID, 0, 4)
		for _, part := range trimmed(raw) {
			id, err := parseCompatID(part)
			if err != nil {
				// An id Reelix cannot parse is an id it does not hold.
				continue
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			return query, true
		}
		query.ItemIDs = ids
	}

	if raw := q.Get("maxPremiereDate"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			// Reelix knows a release year, not a date. Comparing years is
			// coarser than the client asked for, but it keeps genuinely
			// unreleased titles out of the row, which is the point of the
			// filter.
			year := t.Year()
			query.MaxYear = &year
		}
	}

	query.Sort, query.Descending = sortOrder(q)
	return query, false
}

// sortOrder maps a client's sortBy onto an ordering Reelix can serve.
//
// Wholphin asks for DateCreated, CommunityRating, SortName, DatePlayed and
// Random, and all but CommunityRating map onto real columns. There is still no
// metadata behind a rating, so that one falls back to title order: a row in an
// unexpected order is a cosmetic surprise, where an error is an empty screen.
func sortOrder(q map[string][]string) (repository.ItemSort, bool) {
	descending := false
	if v, ok := q["sortOrder"]; ok && len(v) > 0 {
		descending = strings.EqualFold(v[0], "Descending")
	}

	var requested string
	if v, ok := q["sortBy"]; ok && len(v) > 0 {
		// sortBy is a comma-separated list in priority order; Reelix sorts by
		// one column, so the first recognised entry wins.
		requested = v[0]
	}

	for _, name := range trimmed(requested) {
		switch {
		case strings.EqualFold(name, "SortName"), strings.EqualFold(name, "Name"):
			return repository.ItemSortTitle, descending
		case strings.EqualFold(name, "DateCreated"):
			return repository.ItemSortCreatedAt, descending
		case strings.EqualFold(name, "Random"):
			return repository.ItemSortRandom, descending
		case strings.EqualFold(name, "DatePlayed"):
			return repository.ItemSortLastPlayed, descending
		case strings.EqualFold(name, "PremiereDate"), strings.EqualFold(name, "ProductionYear"):
			return repository.ItemSortYear, descending
		}
	}
	return repository.ItemSortTitle, descending
}

// intParam reads a non-negative integer parameter, falling back to def.
func intParam(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// contains reports whether values holds s, ignoring case.
func contains(values []string, s string) bool {
	for _, v := range values {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
