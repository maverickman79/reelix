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

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/metadata"
)

// Name is the lowercase internal provider name, stored in the database and
// used as the key in a Candidate's ID map.
const Name = "tmdb"

// Client is a TMDB provider.
type Client struct {
	baseURL      string
	imageBaseURL string
	apiKey       string
	region       string
	http         *http.Client

	// imageHTTP is separate from http because an image download is a different
	// shape of request: megabytes rather than kilobytes, and so a timeout that
	// is right for one abandons the other. Nothing else differs.
	imageHTTP *http.Client
}

// New returns a Client. The key is not verified here; see the note on
// config.Metadata for why reachability is not a startup condition.
//
// region is the ISO 3166-1 country whose certification becomes OfficialRating.
func New(baseURL, imageBaseURL, apiKey, region string, timeout, imageTimeout time.Duration) *Client {
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		imageBaseURL: strings.TrimRight(imageBaseURL, "/"),
		apiKey:       apiKey,
		region:       strings.ToUpper(region),
		http:         &http.Client{Timeout: timeout},
		imageHTTP:    &http.Client{Timeout: imageTimeout},
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
		Images struct {
			Posters   []imageCandidate `json:"posters"`
			Backdrops []imageCandidate `json:"backdrops"`
			Logos     []imageCandidate `json:"logos"`
		} `json:"images"`
	}

	// ARTWORK COSTS NO EXTRA PROVIDER REQUEST. "images" is appended to the
	// request this method already makes, so a library-wide refresh sends the
	// same number of calls to TMDB with artwork as it did without. Only the
	// image BYTES are extra, and those come from a CDN rather than the API.
	//
	// include_image_language=en,null is what makes logos usable: a logo is
	// artwork with the title rendered into it, so it is language-tagged, and
	// the unfiltered listing returns every language TMDB holds. "null" is the
	// language-neutral art — usually the textless poster — and is wanted for
	// its own sake rather than as a fallback.
	err := c.get(ctx, "/movie/"+url.PathEscape(providerID), url.Values{
		"append_to_response":     {"release_dates,images"},
		"include_image_language": {"en,null"},
	}, &body)
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

	out.Images = map[string]metadata.ImageCandidate{}
	c.addImage(out.Images, domain.ImagePrimary, posterSize, body.Images.Posters)
	c.addImage(out.Images, domain.ImageBackdrop, backdropSize, body.Images.Backdrops)
	c.addImage(out.Images, domain.ImageLogo, logoSize, body.Images.Logos)

	return out, nil
}

// The TMDB size buckets Reelix downloads.
//
// THIS IS WHERE THE SIZING DECISION IS MADE, once per image at download time,
// rather than per request at serve time. Clients ask for specific dimensions —
// the recorded requests carry quality and fillHeight — and Reelix honours none
// of them, because serve-time resizing needs a resampling library AND a second
// cache keyed by dimensions, with its own eviction, its own keying and its own
// partial-write problem. The second cache is the expensive half, not the
// library.
//
// Choosing the bucket here gets most of the benefit for none of that: a w780
// poster is roughly the size the reference server returned for the same
// request, so the bytes on the wire are already in the right range. What it
// concedes is that a client asking for a 100px grid thumbnail still receives a
// 780px poster. Wasteful, not wrong, and the trigger to revisit is a measured
// bandwidth problem or a client that renders badly.
const (
	posterSize   = "w780"
	backdropSize = "w1280"
	logoSize     = "w500"
)

// imageCandidate is one entry in TMDB's image listing.
type imageCandidate struct {
	FilePath    string  `json:"file_path"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int     `json:"vote_count"`
}

// addImage picks the best candidate of one type and records it, or records
// nothing when the provider has none.
//
// ABSENT IS THE POINT: a type with no candidates leaves no entry, and the
// refresh pass turns that into a stored negative. Most films have no logo, so
// without a recorded "there is none" every pass would re-ask about every one of
// them forever.
func (c *Client) addImage(
	out map[string]metadata.ImageCandidate, imageType, size string, candidates []imageCandidate,
) {
	best, ok := bestImage(candidates)
	if !ok {
		return
	}
	out[imageType] = metadata.ImageCandidate{
		URL:    c.imageBaseURL + "/" + size + best.FilePath,
		Width:  best.Width,
		Height: best.Height,
	}
}

// bestImage picks one candidate deterministically.
//
// Highest community score, then most votes, then largest. Explicitly ordered
// rather than taking the first entry, because TMDB does not document a sort and
// depending on an undocumented one means the poster silently changes on a day
// the API's ordering does. The tie-breaks matter more than the ranking here:
// they are what make a re-run choose the same image, which is what "re-running
// the pass downloads nothing" rests on.
func bestImage(candidates []imageCandidate) (imageCandidate, bool) {
	var best imageCandidate
	found := false
	for _, c := range candidates {
		if c.FilePath == "" {
			continue
		}
		if !found || betterImage(c, best) {
			best, found = c, true
		}
	}
	return best, found
}

func betterImage(a, b imageCandidate) bool {
	switch {
	case a.VoteAverage != b.VoteAverage:
		return a.VoteAverage > b.VoteAverage
	case a.VoteCount != b.VoteCount:
		return a.VoteCount > b.VoteCount
	case a.Width != b.Width:
		return a.Width > b.Width
	default:
		// Last resort, and it has to be total: without it two identically
		// rated images swap places between runs and the pass re-downloads on
		// every pass.
		return a.FilePath < b.FilePath
	}
}

// FetchImage downloads one image's bytes.
//
// It returns the body unread so the caller can stream it straight to disk while
// hashing it, rather than holding a backdrop in memory. The caller closes it.
//
// The size cap lives in the artwork store rather than here, because the store
// is what writes bytes and a cap enforced anywhere else would be a second
// guard on the same outcome.
func (c *Client) FetchImage(ctx context.Context, imageURL string) (io.ReadCloser, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("tmdb: building image request: %w", err)
	}
	req.Header.Set("Accept", "image/*")

	// No API key: the image CDN does not take one, and sending it would put a
	// credential on a host that has no use for it.
	resp, err := c.imageHTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("tmdb: downloading image: %w", redactedErr(err, c.apiKey))
	}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		resp.Body.Close()
		return nil, "", fmt.Errorf("%w: tmdb image", metadata.ErrRateLimited)
	case resp.StatusCode != http.StatusOK:
		resp.Body.Close()
		return nil, "", fmt.Errorf("tmdb: image returned %d", resp.StatusCode)
	}

	return resp.Body, resp.Header.Get("Content-Type"), nil
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
