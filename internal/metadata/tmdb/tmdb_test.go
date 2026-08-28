package tmdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maverickman79/reelix/internal/metadata"
)

// The suite never reaches the real TMDB. Every test here points the client at
// an httptest server, which is why config carries REELIX_TMDB_BASE_URL.
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, "test-key", 5*time.Second)
}

// searchBody is a trimmed real /search/movie response: the three fields the
// client reads, with the shapes TMDB actually sends. Recorded rather than
// invented — release_date is an ISO date, id is a JSON number, and an
// unreleased film carries an empty release_date.
const searchBody = `{
  "page": 1,
  "results": [
    {"id": 550, "title": "Fight Club", "release_date": "1999-10-15"},
    {"id": 1147610, "title": "Gangland", "release_date": "2025-01-17"},
    {"id": 99, "title": "Unreleased Thing", "release_date": ""}
  ],
  "total_results": 3
}`

func TestSearchMovieParsesTheResponse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(searchBody))
	})

	got, err := c.SearchMovie(context.Background(), metadata.MovieQuery{Title: "Fight Club", Year: 1999})
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}
	if got[0].IDs[Name] != "550" || got[0].Title != "Fight Club" || got[0].Year != 1999 {
		t.Errorf("first candidate = %+v", got[0])
	}
	// An absent release date is a normal case, not an error.
	if got[2].Year != 0 {
		t.Errorf("empty release_date gave year %d, want 0", got[2].Year)
	}
}

// TestSearchMovieDoesNotFilterByYear pins the decision that the matcher, not
// TMDB, applies the year rule.
//
// Filtering server side would return a shorter list that hides both a
// legitimate off-by-one release year and a second candidate sharing the title
// — and hiding the evidence of ambiguity manufactures a confident answer out
// of a situation that did not deserve one.
func TestSearchMovieDoesNotFilterByYear(t *testing.T) {
	var gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(searchBody))
	})

	if _, err := c.SearchMovie(context.Background(), metadata.MovieQuery{Title: "Congo", Year: 1995}); err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	for _, banned := range []string{"year=", "primary_release_year="} {
		if strings.Contains(gotQuery, banned) {
			t.Errorf("the request filters by year (%s): %s", banned, gotQuery)
		}
	}
	if !strings.Contains(gotQuery, "query=Congo") {
		t.Errorf("the request does not carry the title: %s", gotQuery)
	}
}

func TestExternalIDsCarriesIMDbOnly(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/movie/1147610/external_ids") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
		  "id": 1147610,
		  "imdb_id": "tt28263483",
		  "wikidata_id": "Q1",
		  "facebook_id": "fb",
		  "twitter_id": "tw"
		}`))
	})

	got, err := c.ExternalIDs(context.Background(), "1147610")
	if err != nil {
		t.Fatalf("ExternalIDs: %v", err)
	}
	if got["imdb"] != "tt28263483" {
		t.Errorf("imdb = %q, want tt28263483", got["imdb"])
	}
	// Social handles identify a film to nobody. Carrying them would be
	// collecting fields rather than identity.
	if len(got) != 1 {
		t.Errorf("carried more than the IMDb id: %v", got)
	}
}

// A film with no IMDb id is a fact about the film, not a lookup failure.
func TestExternalIDsAcceptsAnAbsentIMDbID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id": 99, "imdb_id": null}`))
	})

	got, err := c.ExternalIDs(context.Background(), "99")
	if err != nil {
		t.Fatalf("ExternalIDs returned an error for a film with no IMDb id: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want no ids", got)
	}
}

func TestRateLimitIsItsOwnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})

	_, err := c.SearchMovie(context.Background(), metadata.MovieQuery{Title: "Fight Club"})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
}

func TestBadKeyIsNamedPrecisely(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	_, err := c.SearchMovie(context.Background(), metadata.MovieQuery{Title: "Fight Club"})
	if err == nil || !strings.Contains(err.Error(), "REELIX_TMDB_API_KEY") {
		t.Errorf("err = %v, want it to name the variable to fix", err)
	}
}

// TestErrorsNeverCarryTheAPIKey is a security regression test.
//
// TMDB v3 authenticates with a query parameter, so the key is in every request
// URL. net/http returns *url.Error, which prints the whole URL — so wrapping a
// transport error without thinking would put a live credential into the log
// the first time TMDB was unreachable.
func TestErrorsNeverCarryTheAPIKey(t *testing.T) {
	const key = "super-secret-key"

	t.Run("transport failure", func(t *testing.T) {
		// Nothing listens here, so the client fails inside http.Do and gets a
		// *url.Error carrying the full URL.
		c := New("http://127.0.0.1:1", key, time.Second)
		_, err := c.SearchMovie(context.Background(), metadata.MovieQuery{Title: "Fight Club"})
		if err == nil {
			t.Fatal("expected a transport error")
		}
		if strings.Contains(err.Error(), key) {
			t.Errorf("the API key leaked into an error: %v", err)
		}
	})

	t.Run("undecodable body", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not json"))
		})
		c.apiKey = key
		_, err := c.SearchMovie(context.Background(), metadata.MovieQuery{Title: "Fight Club"})
		if err == nil {
			t.Fatal("expected a decode error")
		}
		if strings.Contains(err.Error(), key) {
			t.Errorf("the API key leaked into an error: %v", err)
		}
	})
}

// The client must satisfy the interface the service depends on.
var _ metadata.Provider = (*Client)(nil)
