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
	log      *slog.Logger
}

// New builds the compatibility API.
func New(sessions *service.SessionService, log *slog.Logger) *API {
	return &API{
		sessions: sessions,
		log:      logging.Component(log, "compat"),
	}
}

// Routes returns the handler for the compatibility surface.
//
// These mount at the server root, not under a prefix: Jellyfin clients build
// absolute paths like /System/Info/Public and cannot be told otherwise.
//
// Matching is case-sensitive, which ASP.NET's is not. Wholphin sends the exact
// casing recorded in the capture, so this is correct for the 0.0.1 gate; a
// client that lowercases its paths would 404. Noted as a known gap rather than
// papered over with a normalising layer nothing has yet needed.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /System/Info/Public", a.handlePublicSystemInfo)
	mux.HandleFunc("GET /System/Info", a.requireAuth(a.handleSystemInfo))
	mux.HandleFunc("GET /Users/Public", a.handlePublicUsers)

	mux.HandleFunc("GET /QuickConnect/Enabled", a.handleQuickConnectEnabled)
	mux.HandleFunc("POST /QuickConnect/Initiate", a.handleQuickConnectInitiate)

	mux.HandleFunc("POST /Users/AuthenticateByName", a.handleAuthenticateByName)
	mux.HandleFunc("GET /Users/Me", a.requireAuth(a.handleUsersMe))

	mux.HandleFunc("POST /Sessions/Capabilities", a.requireAuth(a.handleSessionCapabilities))

	// Held open for the life of the connection; see socket.go.
	mux.HandleFunc("GET /socket", a.requireAuth(a.handleSocket))

	// Polled on the home screen. Empty, but never a 404; see polled.go.
	mux.HandleFunc("GET /DisplayPreferences/default", a.requireAuth(a.handleDisplayPreferences))
	mux.HandleFunc("GET /UserImage", a.requireAuth(a.handleUserImage))
	mux.HandleFunc("GET /UserItems/Resume", a.requireAuth(a.handleResumeItems))
	mux.HandleFunc("GET /Items/Latest", a.requireAuth(a.handleLatestItems))
	mux.HandleFunc("GET /Shows/NextUp", a.requireAuth(a.handleNextUp))
	mux.HandleFunc("GET /LiveTv/Recordings/Folders", a.requireAuth(a.handleRecordingFolders))

	return mux
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

// emptyStrings returns a non-nil empty slice.
//
// A nil slice marshals as null, and the SDK's generated types declare these
// arrays non-nullable — a null where a list is expected is a hard client-side
// exception, which is exactly the failure mode the constitution warns about.
func emptyStrings() []string { return []string{} }

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
