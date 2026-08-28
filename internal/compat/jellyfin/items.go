package jellyfin

import (
	"errors"
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
// 404, always. Reelix downloads no artwork yet, so no item has an image of any
// type — and this is the reference server's own answer in exactly that
// situation: the recorded Chapter request came back 404 with this body, and a
// live probe with a well-formed but nonexistent id answers 404 too.
//
// The items Reelix serves advertise no image tags, so a client should not ask
// at all; a client that asks anyway gets the truth and renders its placeholder.
//
// The type is canonicalised rather than echoed, so that "primary" and
// "Primary" are one type here and not two. See canonicalImageType: the route
// parameter is not touched by the fold trie, so this is the only place the
// spelling can be settled.
//
// An unknown type still answers 404. The reference answers 400 with an ASP.NET
// validation envelope naming the bad value; reproducing that would mean
// inventing a body shape — including a traceId — for a request no observed
// client makes. Recorded rather than reproduced.
func (a *API) handleItemImage(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("type")
	if kind == "" {
		kind = "Primary"
	}
	if canonical, ok := canonicalImageType(kind); ok {
		kind = canonical
	}
	a.writeJSON(w, r, http.StatusNotFound, "item does not have an image of type "+kind)
}

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
