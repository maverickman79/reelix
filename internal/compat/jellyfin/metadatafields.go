package jellyfin

import (
	"crypto/sha256"
	"strings"
	"time"
	"uuid"
)

// formatDate renders a date as the timestamp Jellyfin clients parse.
//
// The reference sends PremiereDate as a full RFC3339 instant rather than a
// bare date, so a date-only value is rendered at midnight UTC. Sending
// "1999-10-15" where the recorded shape is "1999-10-15T00:00:00.0000000Z"
// would be a different JSON type to a strict deserialiser.
//
// Nil in, nil out: an unknown premiere date is absent, not the zero instant.
// A fabricated date renders in Wholphin's screensaver as a real one.
func formatDate(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05.0000000Z")
	return &s
}

// productionYear prefers the provider's release year over the parsed one.
//
// media_items.year is whatever the filename parser produced, and it stays that
// way because it is the matcher's input: a provider value written back over it
// would mean re-identification silently changing its own input on every run.
// Choosing between them is therefore a question for this boundary, the same
// division as DisplayTitle and ChannelLayout.
//
// The provider wins because it is measuring the film and the parser is
// measuring a filename. Where there is no provider date the parsed year is
// still better than nothing.
func productionYear(parsed *int, premiere *time.Time) *int {
	if premiere != nil && !premiere.IsZero() {
		year := premiere.UTC().Year()
		return &year
	}
	return parsed
}

// genreNames renders the genre list as Jellyfin's Genres array.
func genreNames(genres []string) []string {
	if len(genres) == 0 {
		return emptyStrings()
	}
	out := make([]string, 0, len(genres))
	out = append(out, genres...)
	return out
}

// genreItems renders the genre list as Jellyfin's GenreItems array.
//
// The reference sends the same genres twice in one response: Genres as plain
// strings and GenreItems as objects carrying an id. Reproduced rather than
// tidied, because a client may read either and sending only one is a shape no
// real server sends.
//
// The id is derived from the name the same way item ids are derived, so it is
// stable across requests and distinct between genres. It is NOT the reference
// server's id for that genre — Reelix has no genre table shared with it, and
// nothing round-trips the value except a client echoing it back.
func genreItems(genres []string) []any {
	if len(genres) == 0 {
		return emptyList()
	}

	type genreItem struct {
		Name string `json:"Name"`
		ID   string `json:"Id"`
	}

	out := make([]any, 0, len(genres))
	for _, g := range genres {
		out = append(out, genreItem{Name: g, ID: genreID(g)})
	}
	return out
}

// genreID derives a stable id for a genre name.
//
// The same construction as displayPreferencesID and for the same reasons:
// stable per name, distinct between names, UUID-shaped because clients parse
// it as one, and namespaced so it can never coincide with an id derived
// elsewhere from the same string.
//
// It is not the reference server's id for that genre. Reelix shares no genre
// table with it, and nothing round-trips the value except a client echoing it
// back — the same reasoning recorded for DisplayPreferences ids, where working
// out the reference's own derivation would have meant reconstructing a
// server-side implementation detail.
func genreID(name string) string {
	sum := sha256.Sum256([]byte("reelix/genre\x00" + strings.ToLower(name)))

	var id uuid.UUID
	copy(id[:], sum[:])
	return compatID(id)
}
