package v1

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/logging"
)

// setupRequest creates the first administrator.
type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleSetup creates the initial administrator account.
//
// Unauthenticated by necessity — there is no account to authenticate as yet —
// and therefore usable exactly once. The service performs the check and the
// insert under an advisory lock so two simultaneous callers cannot both
// succeed.
func (a *API) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	user, err := a.auth.CreateFirstAdmin(r.Context(), req.Username, req.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}

	loggerFrom(r.Context()).Info("administrator created",
		slog.String(logging.KeyOperation, "setup"),
		slog.String("user_id", user.ID.String()))

	writeJSON(w, r, http.StatusCreated, newUserResponse(user))
}

// loginRequest exchanges credentials for a token.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponse carries the only copy of the plaintext token the caller will
// ever receive.
type loginResponse struct {
	Token     string       `json:"token"`
	ExpiresAt time.Time    `json:"expiresAt"`
	User      userResponse `json:"user"`
}

// handleLogin verifies credentials and issues a bearer token.
func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	user, token, err := a.auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}

	// The user id is safe to log; the token is not, and never appears in any
	// log line here or in the access log.
	loggerFrom(r.Context()).Info("login succeeded",
		slog.String(logging.KeyOperation, "login"),
		slog.String("user_id", user.ID.String()))

	writeJSON(w, r, http.StatusOK, loginResponse{
		Token:     token.Plaintext,
		ExpiresAt: token.ExpiresAt,
		User:      newUserResponse(user),
	})
}

// handleMe returns the authenticated user.
func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, newUserResponse(userFrom(r.Context())))
}

// createLibraryRequest creates a library and its paths.
type createLibraryRequest struct {
	Name  string   `json:"name"`
	Kind  string   `json:"kind"`
	Paths []string `json:"paths"`
}

// handleCreateLibrary creates a library with its filesystem paths.
func (a *API) handleCreateLibrary(w http.ResponseWriter, r *http.Request) {
	var req createLibraryRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	// 0.0.1 has one library kind, so omitting it means the obvious thing
	// rather than being an error.
	kind := domain.LibraryKind(req.Kind)
	if req.Kind == "" {
		kind = domain.LibraryKindMovie
	}

	lib, err := a.libraries.Create(r.Context(), req.Name, kind, req.Paths)
	if err != nil {
		writeError(w, r, err)
		return
	}

	loggerFrom(r.Context()).Info("library created",
		slog.String(logging.KeyOperation, "create_library"),
		slog.String("library_id", lib.Library.ID.String()),
		slog.Int("paths", len(lib.Paths)))

	writeJSON(w, r, http.StatusCreated, newLibraryResponse(lib))
}

// listLibrariesResponse wraps the collection in an object.
//
// A bare top-level array leaves no room to add pagination or totals later
// without breaking every caller.
type listLibrariesResponse struct {
	Libraries []libraryResponse `json:"libraries"`
}

// handleListLibraries returns every library with its paths.
func (a *API) handleListLibraries(w http.ResponseWriter, r *http.Request) {
	libs, err := a.libraries.List(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}

	out := make([]libraryResponse, 0, len(libs))
	for _, l := range libs {
		out = append(out, newLibraryResponse(l))
	}

	writeJSON(w, r, http.StatusOK, listLibrariesResponse{Libraries: out})
}
