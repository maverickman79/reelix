package v1

import (
	"log/slog"
	"net/http"
	"time"
	"uuid"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/service"
)

// identityResponse is one item's identity as this API represents it.
//
// Status is always present and is the field to read first. A caller that
// branches on ExternalIDs being empty cannot tell "never attempted" from
// "attempted and declined", which is the ambiguity the three-state model
// exists to remove.
type identityResponse struct {
	MediaItemID string `json:"media_item_id"`
	Status      string `json:"status"`

	Provider   *string `json:"provider"`
	Confidence *string `json:"confidence"`
	// Reason explains a decline in words meant for a person. It is the field
	// that makes an unmatched item actionable rather than merely disappointing.
	Reason      *string    `json:"reason"`
	AttemptedAt *time.Time `json:"attempted_at"`

	// ExternalIDs are keyed by the lowercase internal provider name. The
	// capitalised spellings Jellyfin clients expect belong to the
	// compatibility layer and are not this API's business.
	ExternalIDs map[string]string `json:"external_ids"`
}

func newIdentityResponse(i domain.Identity) identityResponse {
	ids := i.ExternalIDs
	if ids == nil {
		ids = map[string]string{}
	}
	return identityResponse{
		MediaItemID: i.MediaItemID.String(),
		Status:      string(i.Status),
		Provider:    i.Provider,
		Confidence:  i.Confidence,
		Reason:      i.Reason,
		AttemptedAt: i.AttemptedAt,
		ExternalIDs: ids,
	}
}

// handleIdentifyLibrary starts a background identify pass over one library.
//
// Separate from the scan route on purpose. A scan is filesystem plus ffprobe
// and works offline; this contacts somebody else's API. Folding them together
// would mean a library cannot be re-scanned while TMDB is down.
func (a *API) handleIdentifyLibrary(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, service.InvalidArgumentf("library id is not a UUID"))
		return
	}

	job, err := a.identity.Start(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}

	loggerFrom(r.Context()).Info("identify pass started",
		slog.String(logging.KeyOperation, "identify_library"),
		slog.String(logging.KeyJobID, job.ID.String()),
		slog.String("library_id", id.String()))

	writeJSON(w, r, http.StatusAccepted, newJobResponse(job))
}

// handleGetIdentity returns one item's identity.
func (a *API) handleGetIdentity(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, service.InvalidArgumentf("item id is not a UUID"))
		return
	}

	identity, err := a.identity.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newIdentityResponse(identity))
}

// handleSetIdentity records a human's decision about what an item is.
//
// This is the other half of declining to guess. A policy of leaving ambiguous
// items unmatched is only workable if saying "it is this one" is easy and
// sticks — and manual is the one status no pass overwrites.
func (a *API) handleSetIdentity(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, service.InvalidArgumentf("item id is not a UUID"))
		return
	}

	var body struct {
		ExternalIDs map[string]string `json:"external_ids"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}

	if err := a.identity.SetManual(r.Context(), id, body.ExternalIDs); err != nil {
		writeError(w, r, err)
		return
	}

	identity, err := a.identity.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}

	loggerFrom(r.Context()).Info("identity set by hand",
		slog.String(logging.KeyOperation, "set_identity"),
		slog.String("item_id", id.String()))

	writeJSON(w, r, http.StatusOK, newIdentityResponse(identity))
}

// handleResetIdentity returns an item to pending.
//
// The deliberate act that re-running a pass is not: a pass only ever considers
// pending items, so this is how an operator says "reconsider this one" about
// something already decided.
func (a *API) handleResetIdentity(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, service.InvalidArgumentf("item id is not a UUID"))
		return
	}

	if err := a.identity.Reset(r.Context(), id); err != nil {
		writeError(w, r, err)
		return
	}

	identity, err := a.identity.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newIdentityResponse(identity))
}
