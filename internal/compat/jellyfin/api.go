package jellyfin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/service"
)

// productName and startupWizardCompleted are what clients check before
// offering to connect.
//
// ProductName stays "Jellyfin Server": clients branch on it, and reporting
// something else is a compatibility failure rather than honesty. ServerName,
// which is what a user actually sees, identifies Reelix.
const (
	productName = "Jellyfin Server"

	// jellyfinVersion is the API version Reelix implements, pinned by the
	// project to 10.11.x. Clients gate features on this.
	jellyfinVersion = "10.11.8"
)

// API serves the Jellyfin-compatible surface.
type API struct {
	sessions *service.SessionService
	media    *service.MediaService
	playback *service.PlaybackService
	log      *slog.Logger
}

// New builds the compatibility API.
func New(sessions *service.SessionService, media *service.MediaService,
	playback *service.PlaybackService, log *slog.Logger) *API {
	return &API{
		sessions: sessions,
		media:    media,
		playback: playback,
		log:      logging.Component(log, "compat"),
	}
}

// Routes returns the handler for the compatibility surface.
//
// These mount at the server root, not under a prefix: Jellyfin clients build
// absolute paths like /System/Info/Public and cannot be told otherwise. The
// /emby and /mediabrowser aliases a real server also answers are handled by
// withLegacyPaths ahead of this mux; see routes.go.
//
// Matching is case-insensitive, as a real server's is. The fold trie is built
// from the patterns registered below and rewrites literal segments into these
// spellings while leaving path parameters untouched; see routefold.go.
func (a *API) Routes() http.Handler {
	table := newRouteTable()
	a.registerCompatRoutes(table)

	return withLegacyPaths(buildFoldTrie(table.patterns), table.mux)
}

// registerCompatRoutes declares the surface. Separate from Routes so the
// pattern list can be built and inspected without standing up a server.
func (a *API) registerCompatRoutes(mux *routeTable) {

	mux.handle("GET /System/Info/Public", a.handlePublicSystemInfo)
	mux.handle("GET /System/Info", a.requireAuth(a.handleSystemInfo))
	mux.handle("GET /Users/Public", a.handlePublicUsers)

	// Read by a login page before it has a token, so unauthenticated, as on
	// the reference server.
	mux.handle("GET /Branding/Configuration", a.handleBranding)

	mux.handle("GET /QuickConnect/Enabled", a.handleQuickConnectEnabled)
	mux.handle("POST /QuickConnect/Initiate", a.handleQuickConnectInitiate)

	mux.handle("POST /Users/AuthenticateByName", a.handleAuthenticateByName)
	mux.handle("GET /Users/Me", a.requireAuth(a.handleUsersMe))

	// Two spellings of the same report. Wholphin uses the bare one with query
	// parameters; jellyfin-web uses /Full with a JSON body. A real server
	// serves both.
	mux.handle("POST /Sessions/Capabilities", a.requireAuth(a.handleSessionCapabilities))
	mux.handle("POST /Sessions/Capabilities/Full", a.requireAuth(a.handleSessionCapabilitiesFull))

	// Held open for the life of the connection; see socket.go.
	mux.handle("GET /socket", a.requireAuth(a.handleSocket))

	// Polled on the home screen. Empty, but never a 404; see polled.go.
	//
	// The preferences key is a parameter, not a fixed set: the reference
	// answers any key, and answers each casing of one as a separate record.
	// Wholphin asks for "default", jellyfin-web for "usersettings", and
	// jellyfin-web will not render a page until it gets an answer.
	mux.handle("GET /DisplayPreferences/{prefsId}", a.requireAuth(a.handleDisplayPreferences))
	mux.handle("GET /UserImage", a.requireAuth(a.handleUserImage))

	// jellyfin-web's bitrate routine. These two are ONE loop, not two routes:
	// it caches /System/Endpoint only on success, so a 404 on either leaves
	// the pair re-requesting forever. See webclient.go.
	mux.handle("GET /System/Endpoint", a.requireAuth(a.handleSystemEndpoint))
	mux.handle("GET /Playback/BitrateTest", a.requireAuth(a.handleBitrateTest))
	mux.handle("GET /UserItems/Resume", a.requireAuth(a.handleResumeItems))
	mux.handle("GET /Items/Latest", a.requireAuth(a.handleLatestItems))
	mux.handle("GET /Shows/NextUp", a.requireAuth(a.handleNextUp))
	mux.handle("GET /LiveTv/Recordings/Folders", a.requireAuth(a.handleRecordingFolders))

	// Browse. /UserViews is what a home screen is built from; without it the
	// client cannot render one and restarts instead.
	mux.handle("GET /UserViews", a.requireAuth(a.handleUserViews))
	mux.handle("GET /Items", a.requireAuth(a.handleItems))
	mux.handle("GET /Items/{id}", a.requireAuth(a.handleItem))

	// Requested when a movie is opened. Empty in the recorded shape rather
	// than absent, for the same reason /UserViews is here.
	mux.handle("GET /Items/{id}/Intros", a.requireAuth(a.handleItemIntros))
	mux.handle("GET /Items/{id}/Similar", a.requireAuth(a.handleSimilarItems))
	mux.handle("GET /Items/{id}/SpecialFeatures", a.requireAuth(a.handleSpecialFeatures))
	mux.handle("GET /Items/{id}/ThemeSongs", a.requireAuth(a.handleThemeSongs))
	mux.handle("GET /MediaSegments/{id}", a.requireAuth(a.handleMediaSegments))

	// No artwork exists yet. Both spellings of the route are registered
	// because clients build image URLs with and without an index.
	//
	// {type} is a PARAMETER, so case folding does not touch it: a request for
	// .../Images/primary reaches the handler as "primary", not "Primary". That
	// is harmless while this route 404s everything. WHOEVER IMPLEMENTS ARTWORK
	// must compare the type case-insensitively, or split this into literal
	// alternatives — otherwise a client that lowercases its paths gets a 404
	// for an image that exists.
	mux.handle("GET /Items/{id}/Images/{type}", a.requireAuth(a.handleItemImage))
	mux.handle("GET /Items/{id}/Images/{type}/{index}", a.requireAuth(a.handleItemImage))

	// Playback. The stream endpoint authenticates itself: the client fetches
	// it from a media player that sends no credentials, so it accepts a
	// capability tag as well as a token. See authorizeStream.
	mux.handle("POST /Items/{id}/PlaybackInfo", a.requireAuth(a.handlePlaybackInfo))
	mux.handle("GET /Videos/{id}/stream", a.handleVideoStream)

	// The stream.{container} spelling is not registered here: net/http's mux
	// requires a wildcard to be a whole path segment, so it is normalised
	// onto this route by withLegacyPaths instead. See normalizeStreamSpelling.

	mux.handle("POST /Sessions/Playing", a.requireAuth(a.handlePlaybackStarted))
	mux.handle("POST /Sessions/Playing/Progress", a.requireAuth(a.handlePlaybackProgress))
	mux.handle("POST /Sessions/Playing/Stopped", a.requireAuth(a.handlePlaybackStopped))

	// The legacy /Users/{userId}/... spellings, which carry the user in the
	// path rather than in the token. Undocumented in the OpenAPI spec and
	// still served by a real 10.11; see routes.go.
	a.registerUserScopedRoutes(mux)
}

// ctxKey is unexported so no other package can collide with it.
type ctxKey int

const (
	ctxKeySession ctxKey = iota
	ctxKeyUser
)

// requireAuth rejects requests without a valid session token.
//
// Jellyfin clients also pass the token as an api_key query parameter on some
// routes. That is deliberately not accepted here: the access log records
// paths, and honouring a query-string credential would create a second way in
// that is far easier to leak.
func (a *API) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := ParseAuthorization(r)

		session, user, err := a.sessions.Resolve(r.Context(), auth.Token)
		if err != nil {
			// No detail: a client learning why its token was rejected learns
			// nothing useful, and the log already has the request id.
			writeStatus(w, http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), ctxKeySession, session)
		ctx = context.WithValue(ctx, ctxKeyUser, user)
		next(w, r.WithContext(ctx))
	}
}

// sessionFrom returns the authenticated session. Valid only behind requireAuth.
func sessionFrom(ctx context.Context) domain.Session {
	if s, ok := ctx.Value(ctxKeySession).(domain.Session); ok {
		return s
	}
	return domain.Session{}
}

// userFrom returns the authenticated user. Valid only behind requireAuth.
func userFrom(ctx context.Context) domain.User {
	if u, ok := ctx.Value(ctxKeyUser).(domain.User); ok {
		return u
	}
	return domain.User{}
}

// writeJSON encodes v as the response body.
func (a *API) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(v); err != nil {
		logging.FromContext(r.Context()).Error("encoding compatibility response failed",
			slog.String(logging.KeyOperation, "write_json"),
			slog.String(logging.KeyError, err.Error()))
	}
}

// writeStatus sends a bare status with no body.
//
// A Jellyfin client meeting a 500 usually renders an empty screen with no
// explanation, so error responses here stay minimal and correct rather than
// descriptive.
func writeStatus(w http.ResponseWriter, status int) {
	w.WriteHeader(status)
}

// localAddress reconstructs the URL the client used to reach this server.
//
// Reelix cannot know it otherwise: the listener binds a port, not a hostname,
// and clients reach it by LAN address, tailnet address, or hostname depending
// on where they are. The Host header is what the client itself dialled, which
// makes it the only correct answer to give back.
func localAddress(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	return scheme + "://" + host
}

// remoteAddr returns the client's IP without its port.
func remoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// queryValue reads a query parameter the way the reference server does:
// ignoring case, and ignoring underscores.
//
// Go's url.Values lookup is an exact map hit, and clients do NOT agree on the
// spelling of these names. On the stream route alone, Wholphin sends "tag"
// while jellyfin-web sends "Tag" and its credential as "ApiKey" rather than
// "api_key" — and a real 10.11.8 accepts api_key, API_KEY, ApiKey, apikey,
// APIKEY and Api_Key alike, all probed. Reading an exact name means silently
// not seeing a parameter a client did send, which surfaces as an
// authorization failure with no bad credential anywhere in the request.
//
// The scan is linear over the parameters actually present, which is a handful.
func queryValue(r *http.Request, want string) string {
	want = foldQueryName(want)

	for name, values := range r.URL.Query() {
		if len(values) > 0 && foldQueryName(name) == want {
			return values[0]
		}
	}
	return ""
}

// foldQueryName normalises a query parameter name for comparison.
func foldQueryName(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", ""))
}

// emptyStrings returns a non-nil empty slice.
//
// A nil slice marshals as null, and the SDK's generated types declare these
// arrays non-nullable — a null where a list is expected is a hard client-side
// exception, which is exactly the failure mode the constitution warns about.
func emptyStrings() []string { return []string{} }

// nonNil returns s, or an empty slice when s is nil.
//
// For values going INTO the database rather than out to a client: the session
// capability columns are NOT NULL, and a nil slice binds as SQL NULL.
func nonNil(s []string) []string {
	if s == nil {
		return emptyStrings()
	}
	return s
}

// emptyList returns a non-nil empty slice for heterogeneous arrays, for the
// same reason as emptyStrings.
func emptyList() []any { return []any{} }

// trimmed splits a comma-separated query parameter into its parts.
func trimmed(s string) []string {
	if strings.TrimSpace(s) == "" {
		return emptyStrings()
	}

	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
