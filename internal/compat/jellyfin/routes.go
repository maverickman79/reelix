package jellyfin

import (
	"net/http"
	"strings"
)

// This file holds the route spellings a real Jellyfin server answers for
// endpoints Reelix already implements. Nothing here adds a capability: every
// alias lands on a handler that existed before it.
//
// The set was established by probing a real Jellyfin 10.11.8 — the reference
// server in hack/capture — and NOT by reading the OpenAPI specification, which
// is incomplete on exactly this question. The spec declares 315 paths and not
// one of them is prefixed or user-scoped, yet the server serves both families.
// See docs/compat-capture.md for the method, including how to tell a routing
// 404 from a handler 404.

// legacyPrefixes are the path prefixes a real server also answers under.
//
// Both are inherited from Emby, which Jellyfin was forked from, and
// multi-backend clients use the prefixed form so one code path can address
// either server. VidHub sends /emby on every request and cannot authenticate
// without it.
//
// Only these two. Probing confirmed /jellyfin and /api are not aliases, so
// this is a fixed list rather than a general "strip any first segment" rule,
// which would turn every typo into a 200 for the wrong route.
var legacyPrefixes = []string{"emby", "mediabrowser"}

// normalizeLegacyPath rewrites a request path into its canonical spelling.
//
// Two rewrites, both observed on the reference server:
//
//   - a leading /emby or /mediabrowser segment is removed, matched without
//     regard to case, and removed ONCE. /emby/emby/System/Info/Public is a
//     404 on a real server and must stay one here.
//   - a trailing slash is removed, which a real server tolerates.
//
// Case folding of the rest of the path is deliberately NOT done here. A real
// server is case-insensitive across the whole surface and Reelix is not; that
// gap is real and is being closed separately, because folding literal segments
// while leaving path parameters untouched needs a route trie rather than a
// string operation. Lowercasing the whole path would corrupt item ids and
// container extensions.
func normalizeLegacyPath(path string) string {
	if path == "" || path == "/" {
		return path
	}

	// Only the first segment is considered, so a library genuinely named
	// "emby" deeper in a path is untouched.
	if rest, ok := cutLegacyPrefix(path); ok {
		path = rest
	}

	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "/"
		}
	}
	return normalizeStreamSpelling(path)
}

// normalizeStreamSpelling rewrites /Videos/{id}/stream.mkv onto
// /Videos/{id}/stream.
//
// Some clients append the container to the stream URL, and a real server
// answers any extension — stream.mkv, stream.mp4 and stream.ts were all
// confirmed present on the reference server. It treats a container differing
// from the file's own as a request to remux.
//
// REELIX SERVES THE ORIGINAL FILE WHATEVER EXTENSION IS ASKED FOR, and the
// extension is discarded here rather than carried to the handler. That is
// correct while direct play is the only thing Reelix does: it has no other
// bytes to send, and refusing a request it can satisfy would be worse than
// ignoring a hint. IT BECOMES A REAL DECISION THE MOMENT TRANSCODING LANDS —
// at that point the extension is a client asking for a specific container,
// and answering with a different one is a wrong answer rather than a lenient
// one. Whoever adds transcoding has to stop discarding this.
//
// Done here rather than as a route pattern because net/http's mux requires a
// wildcard to occupy a whole path segment, so "stream.{container}" cannot be
// registered.
func normalizeStreamSpelling(path string) string {
	// Only under /Videos/{id}/. Audio streaming is not implemented, and a
	// blanket rule would silently accept a route Reelix does not serve.
	if !strings.HasPrefix(path, "/Videos/") {
		return path
	}

	slash := strings.LastIndex(path, "/")
	if slash < 0 {
		return path
	}

	name, ext, hasExt := strings.Cut(path[slash+1:], ".")
	if !hasExt || name != "stream" || ext == "" || strings.Contains(ext, ".") {
		return path
	}
	return path[:slash+1] + "stream"
}

// cutLegacyPrefix removes one leading alias segment, reporting whether it did.
func cutLegacyPrefix(path string) (string, bool) {
	trimmed := strings.TrimPrefix(path, "/")

	first, rest, hasRest := strings.Cut(trimmed, "/")
	for _, prefix := range legacyPrefixes {
		if !strings.EqualFold(first, prefix) {
			continue
		}
		if !hasRest {
			// "/emby" alone addresses nothing below it.
			return "/", true
		}
		return "/" + rest, true
	}
	return path, false
}

// withLegacyPaths normalises the request path before the mux matches on it.
//
// A middleware rather than a second set of registrations: the alias applies to
// every route including ones added later, and registering each pattern twice
// would leave the next person to add a route wondering why theirs is the only
// one VidHub cannot reach.
//
// r.URL is rewritten rather than only r.URL.Path, so anything downstream that
// reconstructs a URL sees the canonical form. The original arrives in the
// access log, which is where a question about what a client actually sent
// gets answered.
func withLegacyPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		normalized := normalizeLegacyPath(r.URL.Path)
		if normalized == r.URL.Path {
			next.ServeHTTP(w, r)
			return
		}

		// The URL is copied rather than mutated in place: r.URL is shared
		// with whatever built the request, and rewriting it under them is
		// the kind of aliasing bug that surfaces months later.
		clone := *r.URL
		clone.Path = normalized
		clone.RawPath = ""

		outer := r.Clone(r.Context())
		outer.URL = &clone
		next.ServeHTTP(w, outer)
	})
}

// requireUserPath rejects a user-scoped request for somebody else's data.
//
// The legacy routes carry the user in the path where the modern ones take it
// from the token. Trusting the path would mean any authenticated client could
// read any user's playback state by editing a URL, so the two must agree.
//
// There is deliberately no administrator override. Reelix has one account and
// no sharing model; an override would be a permissions system implemented
// ahead of the permissions system. It goes in when groups and roles do.
//
// Valid only behind requireAuth, which is what puts the user in the context.
func (a *API) requireUserPath(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requested, err := parseCompatID(r.PathValue("userId"))
		if err != nil {
			writeStatus(w, http.StatusBadRequest)
			return
		}

		if requested != userFrom(r.Context()).ID {
			// 403, not 404: the route exists and the caller is authenticated.
			// Answering 404 would tell a client its token had gone stale and
			// send it round the login loop for a request that will never
			// succeed.
			writeStatus(w, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// registerUserScopedRoutes adds the legacy /Users/{userId}/... spellings.
//
// Written out one by one rather than generated from the modern routes,
// because the family is NOT uniform: a real server has
// /Items/{id}/ThemeSongs and no user-scoped twin for it. Generating the set
// mechanically would invent a route the reference server answers 404 for.
// TestThemeSongsHasNoUserScopedTwin pins that.
//
// Each alias delegates to the handler the modern spelling uses. The path's
// userId is validated and then discarded: the handler reads the authenticated
// user from the context, which requireUserPath has just proved is the same
// person.
func (a *API) registerUserScopedRoutes(mux *http.ServeMux) {
	scoped := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, a.requireAuth(a.requireUserPath(handler)))
	}

	scoped("GET /Users/{userId}/Items", a.handleItems)
	scoped("GET /Users/{userId}/Items/Resume", a.handleResumeItems)
	scoped("GET /Users/{userId}/Items/Latest", a.handleLatestItems)
	scoped("GET /Users/{userId}/Views", a.handleUserViews)

	// {id} keeps the name the modern route uses, so the handler's
	// r.PathValue("id") resolves without either spelling knowing about the
	// other.
	scoped("GET /Users/{userId}/Items/{id}", a.handleItem)
	scoped("GET /Users/{userId}/Items/{id}/Intros", a.handleItemIntros)
	scoped("GET /Users/{userId}/Items/{id}/SpecialFeatures", a.handleSpecialFeatures)
}
