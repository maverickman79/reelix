package v1

import (
	"net/http"
	"time"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/service"
)

// Prefix is where this API is mounted.
const Prefix = "/api/v1"

// API holds the services the handlers call.
//
// It deliberately has no database handle: policy and transactions belong to
// the services, and a handler that could reach the pool directly would
// eventually do so.
type API struct {
	auth      *service.AuthService
	libraries *service.LibraryService
}

// New builds the API.
func New(authSvc *service.AuthService, libraries *service.LibraryService) *API {
	return &API{auth: authSvc, libraries: libraries}
}

// Routes returns the handler for everything under Prefix.
//
// The returned mux is mounted with http.StripPrefix, so patterns here are
// written relative to /api/v1.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated. Setup is usable exactly once; the service enforces that
	// under a lock rather than trusting this route to be unreachable later.
	mux.HandleFunc("POST /setup", a.handleSetup)
	mux.HandleFunc("POST /auth/login", a.handleLogin)

	mux.HandleFunc("GET /me", a.requireAuth(a.handleMe))

	mux.HandleFunc("GET /libraries", a.requireAuth(a.handleListLibraries))
	mux.HandleFunc("POST /libraries", a.requireAdmin(a.handleCreateLibrary))

	return mux
}

// userResponse is a user as this API represents it.
//
// There is no password field of any kind. The hash is not merely omitted from
// the JSON — it never enters this struct, so it cannot be exposed by someone
// later adding a tag or a debug dump.
type userResponse struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	IsAdmin   bool      `json:"isAdmin"`
	CreatedAt time.Time `json:"createdAt"`
}

func newUserResponse(u domain.User) userResponse {
	return userResponse{
		// Dashed UUIDs. The 32-character dashless form is a Jellyfin
		// convention and belongs only to the compatibility layer.
		ID:        u.ID.String(),
		Username:  u.Username,
		IsAdmin:   u.IsAdmin,
		CreatedAt: u.CreatedAt,
	}
}

// libraryResponse is a library and its paths.
type libraryResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Paths     []string  `json:"paths"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func newLibraryResponse(l service.LibraryWithPaths) libraryResponse {
	// Non-nil so the field marshals as [] rather than null. A client that
	// iterates the array should not have to nil-check it first.
	paths := make([]string, 0, len(l.Paths))
	for _, p := range l.Paths {
		paths = append(paths, p.Path)
	}

	return libraryResponse{
		ID:        l.Library.ID.String(),
		Name:      l.Library.Name,
		Kind:      string(l.Library.Kind),
		Paths:     paths,
		CreatedAt: l.Library.CreatedAt,
		UpdatedAt: l.Library.UpdatedAt,
	}
}
