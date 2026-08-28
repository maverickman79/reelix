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
	scans     *service.ScanService
	identity  *service.IdentityService
	metadata  *service.MetadataService
}

// New builds the API.
func New(
	authSvc *service.AuthService,
	libraries *service.LibraryService,
	scans *service.ScanService,
	identity *service.IdentityService,
	metadataSvc *service.MetadataService,
) *API {
	return &API{
		auth: authSvc, libraries: libraries, scans: scans,
		identity: identity, metadata: metadataSvc,
	}
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

	// Starting work is an administrative act; watching it is not.
	mux.HandleFunc("POST /libraries/{id}/scan", a.requireAdmin(a.handleScanLibrary))
	mux.HandleFunc("GET /jobs", a.requireAuth(a.handleListJobs))
	mux.HandleFunc("GET /jobs/{id}", a.requireAuth(a.handleGetJob))

	// Identification is its own pass, not a step inside a scan: a scan is
	// local and works offline, this contacts an external provider.
	mux.HandleFunc("POST /libraries/{id}/identify", a.requireAdmin(a.handleIdentifyLibrary))

	// Reading an identity is not administrative; changing one is. A manual
	// identity outranks every automated pass, so setting it is a privileged
	// act rather than a preference.
	mux.HandleFunc("GET /items/{id}/identity", a.requireAuth(a.handleGetIdentity))
	mux.HandleFunc("PUT /items/{id}/identity", a.requireAdmin(a.handleSetIdentity))
	mux.HandleFunc("DELETE /items/{id}/identity", a.requireAdmin(a.handleResetIdentity))

	// Metadata fields. Reading is not administrative; editing, locking and
	// starting a library-wide refresh are.
	//
	// ?all=true on the refresh re-fetches everything identified, which is one
	// provider request per film. The default is items never fetched, so the
	// expensive form is asked for rather than discovered.
	mux.HandleFunc("POST /libraries/{id}/refresh-metadata", a.requireAdmin(a.handleRefreshMetadata))
	mux.HandleFunc("GET /items/{id}/metadata", a.requireAuth(a.handleGetMetadata))
	mux.HandleFunc("PATCH /items/{id}/metadata", a.requireAdmin(a.handleSetMetadata))
	mux.HandleFunc("PUT /items/{id}/metadata/{field}/lock", a.requireAdmin(a.handleSetFieldLock))
	mux.HandleFunc("DELETE /items/{id}/metadata/{field}/lock", a.requireAdmin(a.handleSetFieldLock))

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
