package jellyfin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// newTestAPI builds an API with nothing behind it, for handlers that touch
// neither the database nor the session service. Tests using it run without
// REELIX_TEST_DB_DSN.
func newTestAPI() *API {
	return New(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// polledRoutes are the home-screen routes implemented in the first half of
// Step 6, paired with their recorded fixtures.
var polledRoutes = []struct {
	route string
	path  string
}{
	{"GET_DisplayPreferences_default", "/DisplayPreferences/default"},
	{"GET_UserImage", "/UserImage"},
	{"GET_UserItems_Resume", "/UserItems/Resume"},
	{"GET_Items_Latest", "/Items/Latest"},
	{"GET_Shows_NextUp", "/Shows/NextUp"},
	{"GET_LiveTv_Recordings_Folders", "/LiveTv/Recordings/Folders"},
}

// recordedQuery rebuilds the query string the client sent.
//
// The recorded parameters are replayed rather than omitted so that a route
// reading one of them is exercised with what Wholphin actually sends.
func recordedQuery(f fixture) string {
	if len(f.Request.Query) == 0 {
		return ""
	}

	values := url.Values{}
	for k, v := range f.Request.Query {
		values.Set(k, v)
	}
	return "?" + values.Encode()
}

// TestPolledRoutesMatchFixtures replays every recorded call to the home-screen
// routes and checks the answer against what the reference server sent.
//
// Every fixture is replayed, not just the first: the same route was recorded
// with different parameters — /UserItems/Resume was called once with a
// parentId and no userId at all — and a handler that only works for the first
// shape is a handler that works until the user navigates.
func TestPolledRoutesMatchFixtures(t *testing.T) {
	h := newHarness(t)
	token := h.login()

	for _, rt := range polledRoutes {
		for _, name := range fixtureNames(t, rt.route) {
			t.Run(rt.route+"/"+name, func(t *testing.T) {
				f := loadFixture(t, rt.route, name)

				resp := h.do(http.MethodGet, rt.path+recordedQuery(f), token, nil)
				raw := h.bodyOf(resp)

				if resp.StatusCode != f.Response.Status {
					t.Fatalf("status %d, the recorded server answered %d: %s",
						resp.StatusCode, f.Response.Status, raw)
				}

				assertSuperset(t, f.recordedJSON(t), decodeBody(t, raw))
			})
		}
	}
}

// TestPolledRoutesRequireAToken checks the home-screen routes are
// authenticated like the rest of the surface.
func TestPolledRoutesRequireAToken(t *testing.T) {
	h := newHarness(t)

	for _, rt := range polledRoutes {
		t.Run(rt.path, func(t *testing.T) {
			resp := h.do(http.MethodGet, rt.path, "", nil)
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("without a token: %d, want 401", resp.StatusCode)
			}
		})
	}
}

// TestEmptyListsAreWellFormed pins the shape of the empty responses.
//
// The fixture comparison checks types, so an empty response passes it almost
// by definition. What matters operationally is the rest: that the lists are
// arrays rather than null, that the counts are zero, and that /Items/Latest is
// a bare array while its neighbours are envelopes. Wholphin's SDK deserializes
// a null where it expects a list as a hard exception.
func TestEmptyListsAreWellFormed(t *testing.T) {
	h := newHarness(t)
	token := h.login()

	t.Run("query result envelopes", func(t *testing.T) {
		for _, path := range []string{"/UserItems/Resume", "/Shows/NextUp", "/LiveTv/Recordings/Folders"} {
			t.Run(path, func(t *testing.T) {
				resp := h.do(http.MethodGet, path, token, nil)
				raw := h.bodyOf(resp)

				var got struct {
					Items            *[]any `json:"Items"`
					TotalRecordCount *int   `json:"TotalRecordCount"`
					StartIndex       *int   `json:"StartIndex"`
				}
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatalf("decoding %s: %v\nbody was: %s", path, err, raw)
				}

				switch {
				case got.Items == nil:
					t.Errorf("Items is null or missing: %s", raw)
				case len(*got.Items) != 0:
					t.Errorf("Items is not empty: %s", raw)
				}
				if got.TotalRecordCount == nil || *got.TotalRecordCount != 0 {
					t.Errorf("TotalRecordCount is not 0: %s", raw)
				}
				if got.StartIndex == nil || *got.StartIndex != 0 {
					t.Errorf("StartIndex is not 0: %s", raw)
				}
			})
		}
	})

	// /Items/Latest is a top-level array, not an envelope. Returning the
	// envelope here would be a deserialization failure in the client.
	t.Run("/Items/Latest is a bare array", func(t *testing.T) {
		resp := h.do(http.MethodGet, "/Items/Latest", token, nil)
		raw := h.bodyOf(resp)

		var got []any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decoding /Items/Latest: %v\nbody was: %s", err, raw)
		}
		if len(got) != 0 {
			t.Errorf("expected an empty array, got: %s", raw)
		}
	})
}

// TestDisplayPreferencesEchoesTheClient checks the response belongs to the
// question that was asked, and that its id is stable across calls — a client
// that sees a new id each time it polls has been told its preferences were
// replaced.
func TestDisplayPreferencesEchoesTheClient(t *testing.T) {
	h := newHarness(t)
	token := h.login()

	get := func() (client, id string) {
		t.Helper()

		resp := h.do(http.MethodGet, "/DisplayPreferences/default?userId=x&client=Wholphin", token, nil)
		raw := h.bodyOf(resp)

		var got struct {
			Client string `json:"Client"`
			ID     string `json:"Id"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decoding display preferences: %v\nbody was: %s", err, raw)
		}
		return got.Client, got.ID
	}

	client, first := get()
	if client != "Wholphin" {
		t.Errorf("Client = %q, want the query parameter echoed back", client)
	}
	if first == "" {
		t.Error("Id is empty")
	}

	if _, second := get(); second != first {
		t.Errorf("Id changed between calls: %q then %q", first, second)
	}
}

// TestUserImageIsADeliberate404 pins the one route here that answers 404.
//
// The reference server answered 404 for a user with no avatar, which every
// Reelix user is, and Wholphin re-requests it per screen rather than storming
// it. This test exists so that a future session reading the never-404 rule
// changes the rule or the comment, rather than quietly changing this route.
func TestUserImageIsADeliberate404(t *testing.T) {
	h := newHarness(t)

	resp := h.do(http.MethodGet, "/UserImage?userId=x", h.login(), nil)
	raw := h.bodyOf(resp)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", resp.StatusCode, raw)
	}

	var got problemDetails
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decoding the problem body: %v\nbody was: %s", err, raw)
	}
	if got.Status != http.StatusNotFound || got.Title == "" || got.Type == "" {
		t.Errorf("the problem body is not well formed: %s", raw)
	}
}

// TestUserImageCarriesTheRequestID checks the trace id in the 404 body is
// Reelix's own request id, so a trace a user reports leads to the log line
// that produced it.
//
// The id is set on the response by the HTTP middleware, which the compat
// harness does not run, so this drives the handler directly.
func TestUserImageCarriesTheRequestID(t *testing.T) {
	const id = "0f9a1c2e-request-id"

	req := httptest.NewRequest(http.MethodGet, "/UserImage?userId=x", nil)
	rec := httptest.NewRecorder()
	rec.Header().Set(requestIDHeader, id)

	newTestAPI().handleUserImage(rec, req)

	var got problemDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the problem body: %v\nbody was: %s", err, rec.Body)
	}
	if got.TraceID != id {
		t.Errorf("traceId = %q, want the request id %q", got.TraceID, id)
	}
}
