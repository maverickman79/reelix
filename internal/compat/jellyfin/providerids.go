package jellyfin

import "sort"

// providerSpellings maps Reelix's internal lowercase provider names onto the
// three different spellings a Jellyfin client expects.
//
// Established by probing a real 10.11.8 rather than assumed, because nothing
// in the recorded capture pins it: every fixture carries "ProviderIds": {},
// since the captured library had no metadata. A film identified on a live
// reference instance answers
//
//	"ProviderIds":  {"Imdb": "tt28263483", "Tmdb": "1147610"}
//	"ExternalUrls": [{"Name": "IMDb",  "Url": "https://www.imdb.com/title/tt28263483"},
//	                 {"Name": "TMDB",  "Url": "https://www.themoviedb.org/movie/1147610"}]
//
// Note the three spellings of two providers in one response: the ProviderIds
// KEY is "Tmdb"/"Imdb", the ExternalUrls display NAME is "TMDB"/"IMDb", and
// Reelix stores "tmdb"/"imdb". Guessing any of them would have been wrong, and
// the key is what a client matches on.
//
// The values are STRINGS on the wire even when the id is numeric — TMDB's
// 1147610 travels as "1147610". That is why external_id is a text column.
var providerSpellings = map[string]struct {
	// Key is the ProviderIds key.
	Key string
	// Display is the ExternalUrls name.
	Display string
	// URL formats a browsable link, with %s replaced by the id.
	URL string
}{
	"tmdb": {Key: "Tmdb", Display: "TMDB", URL: "https://www.themoviedb.org/movie/%s"},
	"imdb": {Key: "Imdb", Display: "IMDb", URL: "https://www.imdb.com/title/%s"},
}

// providerIDs renders stored identity ids as the map a Jellyfin client reads.
//
// A provider Reelix stores but has no recorded spelling for is OMITTED rather
// than guessed into some capitalisation. A key a client does not recognise is
// ignored at best; a key that collides with a real one under different casing
// is worse. The empty map is the correct answer for an unidentified item, and
// is what every fixture records.
func providerIDs(ids map[string]string) map[string]any {
	out := map[string]any{}
	for provider, value := range ids {
		spelling, ok := providerSpellings[provider]
		if !ok || value == "" {
			continue
		}
		out[spelling.Key] = value
	}
	return out
}

// externalURLs renders stored identity ids as the browsable links a client
// offers.
//
// Sorted by display name so the order is stable between requests. The
// reference server's own order is not something the traffic reveals, and an
// order that changes per request would make two identical responses differ.
func externalURLs(ids map[string]string) []any {
	type link struct {
		Name string `json:"Name"`
		URL  string `json:"Url"`
	}

	names := make([]string, 0, len(ids))
	for provider := range ids {
		names = append(names, provider)
	}
	sort.Strings(names)

	out := make([]any, 0, len(ids))
	for _, provider := range names {
		spelling, ok := providerSpellings[provider]
		if !ok || ids[provider] == "" {
			continue
		}
		out = append(out, link{
			Name: spelling.Display,
			URL:  formatProviderURL(spelling.URL, ids[provider]),
		})
	}
	return out
}

// formatProviderURL substitutes the id into a provider's URL template.
//
// fmt.Sprintf would do this in one line; it is spelled out so that an id
// containing a percent sign cannot turn into a format verb and corrupt the
// link. External ids come from a remote API, so they are not ours to trust.
func formatProviderURL(template, id string) string {
	const verb = "%s"
	for i := 0; i+len(verb) <= len(template); i++ {
		if template[i:i+len(verb)] == verb {
			return template[:i] + id + template[i+len(verb):]
		}
	}
	return template
}
