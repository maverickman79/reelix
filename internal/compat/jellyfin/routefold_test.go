package jellyfin

import (
	"net/http"
	"strings"
	"testing"
)

// testPatterns are a representative slice of the real route table: literals,
// parameters, a literal and a parameter competing in the same position, and a
// path whose tail exists only under the parameter.
var testPatterns = []string{
	"GET /System/Info/Public",
	"GET /Users/Public",
	"GET /Users/Me",
	"GET /Users/{userId}",
	"GET /Users/{userId}/Items",
	"GET /Items",
	"GET /Items/Latest",
	"GET /Items/{id}",
	"GET /Items/{id}/Intros",
	"GET /Items/{id}/Images/{type}",
	"GET /Videos/{id}/stream",
	"GET /DisplayPreferences/{prefsId}",
}

func testTrie() *foldNode { return buildFoldTrie(testPatterns) }

func TestFoldRewritesLiteralSegments(t *testing.T) {
	trie := testTrie()

	for _, tc := range []struct{ name, in, want string }{
		{"already canonical", "/System/Info/Public", "/System/Info/Public"},
		{"all lower", "/system/info/public", "/System/Info/Public"},
		{"all upper", "/SYSTEM/INFO/PUBLIC", "/System/Info/Public"},
		{"mixed", "/SyStEm/InFo/pUbLiC", "/System/Info/Public"},

		{"single segment", "/items", "/Items"},
		// The preferences key is a parameter, so only the literal ahead of it
		// folds. Its case MUST survive: the reference server treats
		// "default", "DEFAULT" and "Default" as three separate preference
		// records, so folding the key would merge records it keeps apart.
		{"prefs key keeps its case, literal folds", "/displaypreferences/DEFAULT", "/DisplayPreferences/DEFAULT"},

		// The route VidHub could not reach.
		{"stream", "/videos/abc123/stream", "/Videos/abc123/stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := trie.fold(tc.in); got != tc.want {
				t.Errorf("fold(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFoldLeavesParametersAlone is the reason this is a trie and not
// strings.ToLower. A parameter is a value.
func TestFoldLeavesParametersAlone(t *testing.T) {
	trie := testTrie()

	for _, tc := range []struct{ name, in, want string }{
		{
			name: "uppercase hex id survives",
			in:   "/items/ABCDEF0123456789ABCDEF0123456789",
			want: "/Items/ABCDEF0123456789ABCDEF0123456789",
		},
		{
			name: "dashed uuid survives, including its case",
			in:   "/items/01A03FDF-67BE-703C-920E-2382F227DC67",
			want: "/Items/01A03FDF-67BE-703C-920E-2382F227DC67",
		},
		{
			name: "a mixed-case id is not normalised",
			in:   "/ITEMS/AbCdEf/intros",
			want: "/Items/AbCdEf/Intros",
		},
		{
			// The image type is a parameter, so it is NOT folded. That is
			// correct here and stays correct: folding a parameter would
			// corrupt item ids. The type is canonicalised in the handler
			// instead — see canonicalImageType and TestItemImageTypeFoldsCase.
			name: "image type is a parameter and keeps its casing",
			in:   "/items/abc/images/primary",
			want: "/Items/abc/Images/primary",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := trie.fold(tc.in); got != tc.want {
				t.Errorf("fold(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFoldPrefersLiteralsOverParameters pins net/http's own precedence, which
// the fold has to reproduce or it would route /Users/Me to the user-by-id
// handler with "Me" as the id.
func TestFoldPrefersLiteralsOverParameters(t *testing.T) {
	trie := testTrie()

	for _, tc := range []struct{ in, want string }{
		{"/Users/Me", "/Users/Me"},
		{"/users/me", "/Users/Me"},
		{"/USERS/ME", "/Users/Me"},
		{"/users/public", "/Users/Public"},

		// An actual id goes down the parameter branch, unchanged.
		{"/users/01a03fa407e172bca3fdb0e2c9da6e13", "/Users/01a03fa407e172bca3fdb0e2c9da6e13"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := trie.fold(tc.in); got != tc.want {
				t.Errorf("fold(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFoldBacktracksToTheParameterBranch covers the case a greedy walk gets
// wrong: the leading segments match a literal route, but the tail only exists
// under the parameter.
//
// /Items/Latest is a route. /Items/Latest/Intros is not — but
// /Items/{id}/Intros is, and an item whose id is literally "Latest" is a
// legitimate request shape. A walk that committed to the literal branch would
// abandon the path and 404 it.
func TestFoldBacktracksToTheParameterBranch(t *testing.T) {
	trie := testTrie()

	for _, tc := range []struct{ in, want string }{
		{"/Items/Latest", "/Items/Latest"},
		{"/items/latest", "/Items/Latest"},

		// Only reachable via /Items/{id}/Intros.
		{"/items/latest/intros", "/Items/latest/Intros"},
		{"/Items/Latest/Intros", "/Items/Latest/Intros"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := trie.fold(tc.in); got != tc.want {
				t.Errorf("fold(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFoldLeavesUnknownPathsAlone checks the fold never invents a match. An
// unmatched path must reach the mux unchanged and get an honest 404.
func TestFoldLeavesUnknownPathsAlone(t *testing.T) {
	trie := testTrie()

	for _, in := range []string{
		"/nothing/here",
		"/System/Info/Public/Extra",
		"/Items/abc/NotARoute",
		"/",
		"",
	} {
		t.Run(in, func(t *testing.T) {
			if got := trie.fold(in); got != in {
				t.Errorf("fold(%q) = %q, want it unchanged", in, got)
			}
		})
	}
}

// TestBuildFoldTriePanicsOnACasingConflict checks the failure is loud.
//
// Two patterns whose literals differ only by case have no correct fold: one
// spelling would silently win and route the other somewhere unintended.
// Routes() runs at startup, so this fails at boot.
func TestBuildFoldTriePanicsOnACasingConflict(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a casing conflict was accepted")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "casing conflict") {
			t.Errorf("panic does not name the problem: %v", r)
		}
	}()

	buildFoldTrie([]string{"GET /Items/Latest", "GET /Items/latest"})
}

// TestRealRouteTableFoldsEveryRegisteredRoute checks the actual route table
// against the fold, rather than the representative sample above.
//
// Every registered pattern is lowercased and folded back; each must return to
// exactly the spelling it was registered with. This catches a route added
// later whose literals the fold cannot round-trip, and it is what makes the
// casing-conflict panic a guard rather than a formality.
func TestRealRouteTableFoldsEveryRegisteredRoute(t *testing.T) {
	table := newRouteTable()
	(&API{}).registerCompatRoutes(table)

	trie := buildFoldTrie(table.patterns)

	for _, pattern := range table.patterns {
		_, path, _ := strings.Cut(pattern, " ")

		// The request a client sends: literal segments lowercased, the
		// parameter value in a casing of its own. The expected answer keeps
		// that value byte for byte and restores every literal.
		var sent, want []string
		for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
			if strings.HasPrefix(segment, "{") {
				// Deliberately mixed case: if the fold ever touched a
				// parameter, this is where it would show.
				sent = append(sent, "ZZvalueZZ")
				want = append(want, "ZZvalueZZ")
				continue
			}
			sent = append(sent, strings.ToLower(segment))
			want = append(want, segment)
		}

		in := "/" + strings.Join(sent, "/")
		expected := "/" + strings.Join(want, "/")

		t.Run(pattern, func(t *testing.T) {
			if got := trie.fold(in); got != expected {
				t.Errorf("fold(%q) = %q, want %q", in, got, expected)
			}
		})
	}
}

// TestCaseInsensitiveRoutesEndToEnd exercises the whole pipeline through the
// server, including the prefix and the extension.
func TestCaseInsensitiveRoutesEndToEnd(t *testing.T) {
	h := newHarness(t)

	bare := h.bodyOf(h.do(http.MethodGet, "/System/Info/Public", "", nil))

	for _, path := range []string{
		"/system/info/public",
		"/SYSTEM/INFO/PUBLIC",
		"/emby/system/info/public",
		"/EMBY/System/Info/Public",
		"/mediabrowser/system/info/public",
	} {
		t.Run(path, func(t *testing.T) {
			resp := h.do(http.MethodGet, path, "", nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s returned %d, want 200", path, resp.StatusCode)
			}
			if got := h.bodyOf(resp); string(got) != string(bare) {
				t.Errorf("%s returned a different body than the canonical spelling", path)
			}
		})
	}
}
