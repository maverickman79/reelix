package jellyfin

import (
	"encoding/json"
	"testing"
)

// TestProviderIDsUseTheReferenceSpellings is the test the probe was run for.
//
// No fixture pins this: every recording carries "ProviderIds": {}, because the
// captured library had no metadata. The spellings below come from identifying
// a film on a live 10.11.8 and reading the response — and they are three
// different spellings of two providers in one document, which is exactly the
// shape that gets guessed wrong.
func TestProviderIDsUseTheReferenceSpellings(t *testing.T) {
	ids := map[string]string{"tmdb": "1147610", "imdb": "tt28263483"}

	got := providerIDs(ids)
	if got["Tmdb"] != "1147610" {
		t.Errorf(`ProviderIds["Tmdb"] = %v, want "1147610"`, got["Tmdb"])
	}
	if got["Imdb"] != "tt28263483" {
		t.Errorf(`ProviderIds["Imdb"] = %v, want "tt28263483"`, got["Imdb"])
	}
	if len(got) != 2 {
		t.Errorf("got %d keys, want 2: %v", len(got), got)
	}

	// The reference sends the numeric TMDB id as a JSON STRING. That is why
	// external_id is a text column: emitting 1147610 unquoted would be a
	// different type from the one every client parses.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if string(raw) != `{"Imdb":"tt28263483","Tmdb":"1147610"}` {
		t.Errorf("on the wire: %s", raw)
	}
}

func TestExternalURLsUseTheReferenceDisplayNames(t *testing.T) {
	got := externalURLs(map[string]string{"tmdb": "1147610", "imdb": "tt28263483"})

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	// Display names are "IMDb" and "TMDB" — NOT the ProviderIds keys, which
	// are "Imdb" and "Tmdb". Sorted by internal name, so the order is stable
	// between two identical requests.
	const want = `[{"Name":"IMDb","Url":"https://www.imdb.com/title/tt28263483"},` +
		`{"Name":"TMDB","Url":"https://www.themoviedb.org/movie/1147610"}]`
	if string(raw) != want {
		t.Errorf("got  %s\nwant %s", raw, want)
	}
}

// An unidentified item must send the empty map every fixture records, not null.
func TestNoIdentityRendersTheRecordedEmptyShapes(t *testing.T) {
	raw, err := json.Marshal(providerIDs(nil))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if string(raw) != `{}` {
		t.Errorf("ProviderIds with no identity = %s, want {}", raw)
	}

	raw, err = json.Marshal(externalURLs(nil))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if string(raw) != `[]` {
		t.Errorf("ExternalUrls with no identity = %s, want []", raw)
	}
}

// A provider with no recorded spelling is omitted rather than guessed into
// some capitalisation. A key a client does not recognise is ignored at best;
// one that collides with a real key under different casing is worse.
func TestUnknownProvidersAreOmittedNotGuessed(t *testing.T) {
	ids := map[string]string{"tmdb": "550", "tvdb": "1234", "anidb": "77"}

	got := providerIDs(ids)
	if len(got) != 1 || got["Tmdb"] != "550" {
		t.Errorf("got %v, want only Tmdb", got)
	}
	if urls := externalURLs(ids); len(urls) != 1 {
		t.Errorf("got %d urls, want 1: %v", len(urls), urls)
	}
}

// An empty id is not an identity. Emitting the key with an empty value would
// tell a client the film has a TMDB page and then send it to a broken link.
func TestEmptyIDsAreDropped(t *testing.T) {
	got := providerIDs(map[string]string{"tmdb": "", "imdb": "tt1"})
	if _, ok := got["Tmdb"]; ok {
		t.Errorf("an empty id was emitted: %v", got)
	}
	if got["Imdb"] != "tt1" {
		t.Errorf("got %v, want the non-empty id kept", got)
	}
}

// External ids come from a remote API, so they are not ours to trust. A
// percent sign in one must not be interpreted as a format verb.
func TestAPercentInAnIDDoesNotCorruptTheURL(t *testing.T) {
	got := externalURLs(map[string]string{"tmdb": "12%s34"})
	if len(got) != 1 {
		t.Fatalf("got %d urls, want 1", len(got))
	}
	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	const want = `{"Name":"TMDB","Url":"https://www.themoviedb.org/movie/12%s34"}`
	if string(raw) != want {
		t.Errorf("got  %s\nwant %s", raw, want)
	}
}
