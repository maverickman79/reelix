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
	http    *http.Client
}

// New returns a Client. The key is not verified here; see the note on
// config.Metadata for why reachability is not a startup condition.
func New(baseURL, apiKey string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
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
