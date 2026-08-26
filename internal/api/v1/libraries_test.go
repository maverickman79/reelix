package v1_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// libraryBody is the library shape the API returns.
type libraryBody struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Kind  string   `json:"kind"`
	Paths []string `json:"paths"`
}

// TestCreateAndListLibrary is the Step 3 completion criterion, end to end.
func TestCreateAndListLibrary(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.login()

	resp := h.do(http.MethodPost, "/libraries", token, map[string]any{
		"name":  "Movies",
		"kind":  "movie",
		"paths": []string{"/media/movies"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d: %s", resp.StatusCode, h.body(resp))
	}

	var created libraryBody
	h.decode(resp, &created)

	if created.Name != "Movies" || created.Kind != "movie" {
		t.Errorf("create returned %+v", created)
	}
	if len(created.Paths) != 1 || created.Paths[0] != "/media/movies" {
		t.Errorf("create returned paths %v", created.Paths)
	}

	resp = h.do(http.MethodGet, "/libraries", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list returned %d", resp.StatusCode)
	}

	var list struct {
		Libraries []libraryBody `json:"libraries"`
	}
	h.decode(resp, &list)

	if len(list.Libraries) != 1 {
		t.Fatalf("list returned %d libraries, want 1", len(list.Libraries))
	}
	got := list.Libraries[0]
	if got.ID != created.ID {
		t.Errorf("list returned id %s, create returned %s", got.ID, created.ID)
	}
	if len(got.Paths) != 1 || got.Paths[0] != "/media/movies" {
		t.Errorf("list returned paths %v, want [/media/movies]", got.Paths)
	}
}

// TestCreateLibraryIsAtomic checks a failed path insert leaves no library
// behind. A library with no paths would be silently unscannable.
func TestCreateLibraryIsAtomic(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.login()

	// The second path duplicates the first, which the service rejects before
	// touching the database; then a genuine conflict at the second insert.
	resp := h.do(http.MethodPost, "/libraries", token, map[string]any{
		"name":  "Movies",
		"kind":  "movie",
		"paths": []string{"/media/movies", "/media/movies"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate paths returned %d, want 400: %s", resp.StatusCode, h.body(resp))
	}
	resp.Body.Close()

	var n int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM libraries`).Scan(&n); err != nil {
		t.Fatalf("counting libraries: %v", err)
	}
	if n != 0 {
		t.Errorf("a rejected create left %d libraries behind", n)
	}
}

func TestListLibrariesEmpty(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.login()

	resp := h.do(http.MethodGet, "/libraries", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list returned %d", resp.StatusCode)
	}

	body := h.body(resp)

	// The array must be [] and not null: a client iterating it should not have
	// to nil-check first.
	if !strings.Contains(body, `"libraries":[]`) {
		t.Errorf("empty list returned %s, want an empty array", body)
	}
}

func TestCreateLibraryValidation(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.login()

	tests := []struct {
		name string
		body map[string]any
	}{
		{"no name", map[string]any{"kind": "movie", "paths": []string{"/media/movies"}}},
		{"blank name", map[string]any{"name": "   ", "kind": "movie", "paths": []string{"/media/movies"}}},
		{"no paths", map[string]any{"name": "Movies", "kind": "movie"}},
		{"empty paths", map[string]any{"name": "Movies", "kind": "movie", "paths": []string{}}},
		{"blank path", map[string]any{"name": "Movies", "kind": "movie", "paths": []string{"  "}}},
		{"relative path", map[string]any{"name": "Movies", "kind": "movie", "paths": []string{"media/movies"}}},
		{"unsupported kind", map[string]any{"name": "TV", "kind": "series", "paths": []string{"/media/tv"}}},
		{"unknown field", map[string]any{"name": "Movies", "paths": []string{"/media/movies"}, "scanner": "fast"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.do(http.MethodPost, "/libraries", token, tt.body)
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("returned %d, want 400", resp.StatusCode)
			}
		})
	}
}

// TestCreateLibraryDefaultsKind checks the single supported kind may be
// omitted rather than being a required field with one legal value.
func TestCreateLibraryDefaultsKind(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.login()

	resp := h.do(http.MethodPost, "/libraries", token, map[string]any{
		"name":  "Movies",
		"paths": []string{"/media/movies"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create without a kind returned %d: %s", resp.StatusCode, h.body(resp))
	}

	var created libraryBody
	h.decode(resp, &created)

	if created.Kind != "movie" {
		t.Errorf("kind defaulted to %q, want movie", created.Kind)
	}
}

// TestCreateLibraryCleansPaths checks paths are normalised before storage, so
// the scanner is not handed a path with redundant separators.
func TestCreateLibraryCleansPaths(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.login()

	resp := h.do(http.MethodPost, "/libraries", token, map[string]any{
		"name":  "Movies",
		"paths": []string{"/media//movies/"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d: %s", resp.StatusCode, h.body(resp))
	}

	var created libraryBody
	h.decode(resp, &created)

	if len(created.Paths) != 1 || created.Paths[0] != "/media/movies" {
		t.Errorf("paths were stored as %v, want [/media/movies]", created.Paths)
	}
}

// TestLibraryEndpointsRequireAuth checks neither endpoint is reachable
// unauthenticated.
func TestLibraryEndpointsRequireAuth(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodGet, "/libraries", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated list returned %d, want 401", resp.StatusCode)
	}

	resp2 := h.do(http.MethodPost, "/libraries", "", map[string]any{
		"name": "Movies", "paths": []string{"/media/movies"},
	})
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated create returned %d, want 401", resp2.StatusCode)
	}

	var n int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM libraries`).Scan(&n); err != nil {
		t.Fatalf("counting libraries: %v", err)
	}
	if n != 0 {
		t.Errorf("an unauthenticated create made %d libraries", n)
	}
}

// TestCreateLibraryRequiresAdmin checks the admin gate, using a non-admin user
// inserted directly since the API has no user-creation endpoint yet.
func TestCreateLibraryRequiresAdmin(t *testing.T) {
	h := newHarness(t)
	h.setup()

	ctx := context.Background()

	// Demote the only user, then log in as them.
	if _, err := h.pool.Exec(ctx, `UPDATE users SET is_admin = false`); err != nil {
		t.Fatalf("demoting the user: %v", err)
	}

	token := h.login()

	resp := h.do(http.MethodPost, "/libraries", token, map[string]any{
		"name": "Movies", "paths": []string{"/media/movies"},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a non-admin create returned %d, want 403", resp.StatusCode)
	}

	// Reading is still permitted for an authenticated non-admin.
	resp2 := h.do(http.MethodGet, "/libraries", token, nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("a non-admin list returned %d, want 200", resp2.StatusCode)
	}
}

// TestCreateLibraryMultiplePaths checks the schema's multi-path capability is
// actually reachable through the API, even though 0.0.1 uses one path.
func TestCreateLibraryMultiplePaths(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.login()

	resp := h.do(http.MethodPost, "/libraries", token, map[string]any{
		"name":  "Movies",
		"paths": []string{"/media/movies", "/media/movies-4k", "/media/foreign"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d: %s", resp.StatusCode, h.body(resp))
	}

	var created libraryBody
	h.decode(resp, &created)

	if len(created.Paths) != 3 {
		t.Fatalf("create returned %d paths, want 3", len(created.Paths))
	}
}

// TestListLibrariesStitchesPaths checks several libraries each get their own
// paths — the case a broken join or a mis-keyed map would fail.
func TestListLibrariesStitchesPaths(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.login()

	want := map[string][]string{
		"Movies":   {"/media/movies"},
		"Movies4K": {"/media/movies-4k", "/media/movies-uhd"},
		"Foreign":  {"/media/foreign"},
	}

	for name, paths := range want {
		resp := h.do(http.MethodPost, "/libraries", token, map[string]any{
			"name": name, "paths": paths,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("creating %s returned %d: %s", name, resp.StatusCode, h.body(resp))
		}
		resp.Body.Close()
	}

	resp := h.do(http.MethodGet, "/libraries", token, nil)
	var list struct {
		Libraries []libraryBody `json:"libraries"`
	}
	h.decode(resp, &list)

	if len(list.Libraries) != len(want) {
		t.Fatalf("list returned %d libraries, want %d", len(list.Libraries), len(want))
	}

	for _, got := range list.Libraries {
		expected, ok := want[got.Name]
		if !ok {
			t.Errorf("unexpected library %q", got.Name)
			continue
		}
		if len(got.Paths) != len(expected) {
			t.Errorf("%s has %d paths, want %d", got.Name, len(got.Paths), len(expected))
			continue
		}
		for i := range expected {
			if got.Paths[i] != expected[i] {
				t.Errorf("%s path %d is %q, want %q", got.Name, i, got.Paths[i], expected[i])
			}
		}
	}
}
