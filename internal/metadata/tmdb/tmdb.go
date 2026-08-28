// Package tmdb implements the metadata.Provider interface against The Movie
// Database's v3 API.
//
// net/http and encoding/json only. TMDB publishes Go clients and so do third
// parties; none of them earn a dependency for two GET requests against a
// documented JSON API, and the dependency rules in docs/constitution.md say to
// reach for the standard library first.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/maverickman79/reelix/internal/metadata"
)

// Name is the lowercase internal provider name, stored in the database and
// used as the key in a Candidate's ID map.
const Name = "tmdb"

// Client is a TMDB provider.
type Client struct {
	baseURL string
	apiKey  string
	region  string
	http    *http.Client
}

// New returns a Client. The key is not verified here; see the note on
// config.Metadata for why reachability is not a startup condition.
//
// region is the ISO 3166-1 country whose certification becomes OfficialRating.
func New(baseURL, apiKey, region string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		region:  strings.ToUpper(region),
		http:    &http.Client{Timeout: timeout},
	}
}

// Name identifies the provider.
func (c *Client) Name() string { return Name }

// SearchMovie searches TMDB for films matching the parsed name.
//
// The year is deliberately NOT sent as a filter, even though TMDB accepts
// primary_release_year and it would return a shorter list. Filtering server
// side would hide two things the matcher needs to see: a candidate whose
// release year is out by one, which is a legitimate match, and a second
// candidate sharing the title, which is what makes an answer ambiguous. A
// filter that removes the evidence of ambiguity produces a single confident
// answer out of a situation that did not deserve one.
func (c *Client) SearchMovie(ctx context.Context, q metadata.MovieQuery) ([]metadata.Candidate, error) {
	params := url.Values{
		"query":         {q.Title},
		"include_adult": {"true"},
	}

	var body struct {
		Results []struct {
			ID          int    `json:"id"`
			Title       string `json:"title"`
			ReleaseDate string `json:"release_date"`
		} `json:"results"`
	}
	if err := c.get(ctx, "/search/movie", params, &body); err != nil {
		return nil, err
	}

	candidates := make([]metadata.Candidate, 0, len(body.Results))
	for _, r := range body.Results {
		candidates = append(candidates, metadata.Candidate{
			IDs:   map[string]string{Name: strconv.Itoa(r.ID)},
			Title: r.Title,
			Year:  releaseYear(r.ReleaseDate),
		})
	}
	return candidates, nil
}

// ExternalIDs resolves the other providers' ids for one TMDB film.
//
// Only IMDb is carried through. TMDB also returns wikidata, facebook,
// instagram and twitter handles on this endpoint; none of them identifies a
// film to any client or importer Reelix cares about, and storing them would be
// collecting fields rather than identity.
//
// An empty result is not an error: plenty of films have a TMDB id and no IMDb
// id, and that is a fact about the film rather than a failure to look it up.
func (c *Client) ExternalIDs(ctx context.Context, providerID string) (map[string]string, error) {
	if providerID == "" {
		return nil, errors.New("tmdb: empty provider id")
	}

	var body struct {
		IMDbID string `json:"imdb_id"`
	}
	if err := c.get(ctx, "/movie/"+url.PathEscape(providerID)+"/external_ids", nil, &body); err != nil {
		return nil, err
	}

	ids := map[string]string{}
	if body.IMDbID != "" {
		ids["imdb"] = body.IMDbID
	}
	return ids, nil
}

// AlternativeTitles returns the other titles TMDB publishes for one film.
//
// TMDB files a film under one primary title and any number of regional and
// working titles beneath it. A search response carries the primary only, so a
// file named after a release title TMDB considers alternative cannot match on
// the search result alone — which is exactly the Aang case that had to be
// resolved by hand.
//
// Every title is returned regardless of region. Restricting to a locale would
// need Reelix to know which region a file came from, which it does not, and
// guessing would drop the correct title for a release the operator actually
// has. The matcher compares them all with the same exact equality, so an extra
// title costs a string comparison and cannot invent a match.
func (c *Client) AlternativeTitles(ctx context.Context, providerID string) ([]string, error) {
	if providerID == "" {
		return nil, errors.New("tmdb: empty provider id")
	}

	var body struct {
		Titles []struct {
			Title string `json:"title"`
		} `json:"titles"`
	}
	if err := c.get(ctx, "/movie/"+url.PathEscape(providerID)+"/alternative_titles", nil, &body); err != nil {
		return nil, err
	}

	titles := make([]string, 0, len(body.Titles))
	for _, t := range body.Titles {
		if t.Title != "" {
			titles = append(titles, t.Title)
		}
	}
	return titles, nil
}

// FetchMetadata returns the managed fields for one film.
//
// One request. append_to_response folds the certification lookup into the
// detail call, so the whole per-film cost of a metadata refresh is a single
// round trip rather than the two it would otherwise take.
//
// RUNTIME IS DELIBERATELY NOT COLLECTED, and this is the place someone will
// notice the omission: TMDB returns a runtime field right beside the ones
// below, and leaving it looks like something nobody got round to.
//
// Jellyfin's RunTimeTicks drives the SEEK BAR, so it has to describe the file
// actually being played, and Reelix already has that from ffprobe. TMDB's
// runtime describes the WORK. The two agree on an ordinary release and diverge
// on an extended cut, a remux with different framing, or a PAL transfer —
// which are exactly the files most likely to have been misidentified in the
// first place. Taking the provider's number there breaks scrubbing on the
// films where it is hardest to notice.
//
// A provider runtime that nothing renders is a field that exists to be wrong.
// It has one genuine future use — a large gap between the file's duration and
// the work's runtime is evidence that a match is wrong — and that belongs with
// match verification, not with the fields shown to a viewer.
func (c *Client) FetchMetadata(ctx context.Context, providerID string) (metadata.MovieMetadata, error) {
	if providerID == "" {
		return metadata.MovieMetadata{}, errors.New("tmdb: empty provider id")
	}

	var body struct {
		Overview    string  `json:"overview"`
		VoteAverage float64 `json:"vote_average"`
		VoteCount   int     `json:"vote_count"`
		ReleaseDate string  `json:"release_date"`
		Genres      []struct {
			Name string `json:"name"`
		} `json:"genres"`
		ReleaseDates struct {
			Results []struct {
				Region       string `json:"iso_3166_1"`
				ReleaseDates []struct {
					Certification string `json:"certification"`
				} `json:"release_dates"`
			} `json:"results"`
		} `json:"release_dates"`
	}

	err := c.get(ctx, "/movie/"+url.PathEscape(providerID),
		url.Values{"append_to_response": {"release_dates"}}, &body)
	if err != nil {
		return metadata.MovieMetadata{}, err
	}

	out := metadata.MovieMetadata{
		Overview:       body.Overview,
		OfficialRating: officialRating(body.ReleaseDates.Results, c.region),
	}

	// A film nobody has rated is not a film everybody hated. TMDB reports
	// vote_average 0 with vote_count 0 for an unrated film, and passing that
	// through would render as zero stars in every client that shows a rating.
	if body.VoteCount > 0 {
		rating := body.VoteAverage
		out.CommunityRating = &rating
	}

	if t, err := time.Parse("2006-01-02", body.ReleaseDate); err == nil {
		out.ReleaseDate = t
	}

	for _, g := range body.Genres {
		if g.Name != "" {
			out.Genres = append(out.Genres, g.Name)
		}
	}
	return out, nil
}

// officialRating picks the certification for one region.
//
// TMDB returns a certification per release TYPE — premiere, theatrical,
// digital, physical — and several of them are routinely empty strings for the
// same film. The rule is the first non-empty one for the requested region.
//
// A REGION WITH NO CERTIFICATION YIELDS AN EMPTY RATING, and must never fall
// back to another region. An operator who configured GB and is shown "R" has
// no way to tell that it is a US rating: it renders exactly like a real
// answer, so the wrong value is indistinguishable from the right one. An empty
// field is visibly missing, which is the failure that gets noticed and fixed.
func officialRating(results []struct {
	Region       string `json:"iso_3166_1"`
	ReleaseDates []struct {
		Certification string `json:"certification"`
	} `json:"release_dates"`
}, region string) string {
	for _, r := range results {
		if !strings.EqualFold(r.Region, region) {
			continue
		}
		for _, rd := range r.ReleaseDates {
			if rd.Certification != "" {
				return rd.Certification
			}
		}
		return ""
	}
	return ""
}

// get performs one authenticated request and decodes the body into out.
//
// The API key travels as a query parameter, which is how TMDB v3 authenticates.
// That means the key is present in every request URL, so NO ERROR PATH HERE MAY
// CARRY A URL. net/http returns *url.Error, which embeds the full URL including
// the query string, so wrapping a transport error directly would print the key
// into the log the first time TMDB was unreachable. Every error below is
// constructed from the method, the path and the status alone; redactedErr is
// the one place that decision is enforced.
func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	params.Set("api_key", c.apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return fmt.Errorf("tmdb: building request for %s: %w", path, redactedErr(err, c.apiKey))
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tmdb: requesting %s: %w", path, redactedErr(err, c.apiKey))
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: tmdb %s", metadata.ErrRateLimited, path)
	case resp.StatusCode == http.StatusUnauthorized:
		// The one status worth naming precisely: it means the key is wrong,
		// which no amount of retrying fixes.
		return fmt.Errorf("tmdb: %s rejected the API key (401); check REELIX_TMDB_API_KEY", path)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("tmdb: %s not found (404)", path)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("tmdb: %s returned %d", path, resp.StatusCode)
	}

	// Bounded so a misbehaving or misdirected endpoint cannot stream an
	// unbounded body into memory during a library-wide pass.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("tmdb: reading %s: %w", path, redactedErr(err, c.apiKey))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("tmdb: decoding %s: %w", path, err)
	}
	return nil
}

// redactedErr replaces the API key anywhere it appears in an error's text.
//
// The belt to c.get's braces. *url.Error prints the whole request URL, and the
// key is in it; a future edit that wraps a transport error without thinking
// would otherwise put a credential in the log.
func redactedErr(err error, key string) error {
	if key == "" || err == nil {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, key) {
		return err
	}
	return errors.New(strings.ReplaceAll(msg, key, "REDACTED"))
}

// releaseYear takes the year from TMDB's ISO date.
//
// The field is frequently empty for unreleased or poorly catalogued films, and
// an absent year is a normal case: it costs the matcher its strongest signal
// and it does not stop a match.
func releaseYear(date string) int {
	if len(date) < 4 {
		return 0
	}
	year, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return year
}
