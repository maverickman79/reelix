package v1

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
	"uuid"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/service"
)

// metadataResponse is one item's managed fields with their provenance.
//
// Provenance travels beside the values rather than being a separate call,
// because "where did this come from and is it pinned" is the question anyone
// looking at a field is already asking.
type metadataResponse struct {
	MediaItemID string `json:"media_item_id"`

	Overview        *string    `json:"overview"`
	CommunityRating *float64   `json:"community_rating"`
	OfficialRating  *string    `json:"official_rating"`
	PremiereDate    *time.Time `json:"premiere_date"`
	Genres          []string   `json:"genres"`

	// Fields is keyed by field name. A field with no entry has never been
	// written by anything, which is different from one written and then
	// cleared.
	Fields map[string]fieldProvenanceResponse `json:"fields"`
}

type fieldProvenanceResponse struct {
	Source    string    `json:"source"`
	Locked    bool      `json:"locked"`
	UpdatedAt time.Time `json:"updated_at"`
}

func newMetadataResponse(m domain.ItemMetadata) metadataResponse {
	genres := m.Genres
	if genres == nil {
		genres = []string{}
	}

	fields := map[string]fieldProvenanceResponse{}
	for name, p := range m.Provenance {
		fields[name] = fieldProvenanceResponse{
			Source: p.Source, Locked: p.Locked, UpdatedAt: p.UpdatedAt,
		}
	}

	return metadataResponse{
		MediaItemID:     m.MediaItemID.String(),
		Overview:        m.Overview,
		CommunityRating: m.CommunityRating,
		OfficialRating:  m.OfficialRating,
		PremiereDate:    m.PremiereDate,
		Genres:          genres,
		Fields:          fields,
	}
}

// handleGetMetadata returns one item's managed fields.
func (a *API) handleGetMetadata(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, service.InvalidArgumentf("item id is not a UUID"))
		return
	}

	md, err := a.metadata.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newMetadataResponse(md))
}

// metadataPatch is a hand edit. Every field is optional; only those present
// are changed.
//
// Pointers throughout, so that "absent" and "set to null" are distinguishable:
// omitting overview leaves it alone, sending null clears it.
type metadataPatch struct {
	Overview        *string    `json:"overview"`
	CommunityRating *float64   `json:"community_rating"`
	OfficialRating  *string    `json:"official_rating"`
	PremiereDate    *time.Time `json:"premiere_date"`
	Genres          *[]string  `json:"genres"`
}

// handleSetMetadata applies hand edits, which LOCK the fields they touch.
//
// See MetadataService.Set: the lock is a default for this operation and not a
// merging of Source and Locked, which remain independent. An edit that
// silently reverted on the next refresh is one nobody would make twice.
func (a *API) handleSetMetadata(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, service.InvalidArgumentf("item id is not a UUID"))
		return
	}

	// Decoding into a map first tells us which keys were PRESENT, which a
	// struct alone cannot: a null and an omission both decode to a nil
	// pointer, and they mean different things here.
	var present map[string]any
	if err := decodeJSON(w, r, &present); err != nil {
		writeError(w, r, err)
		return
	}

	var patch metadataPatch
	if err := remarshal(present, &patch); err != nil {
		writeError(w, r, service.InvalidArgumentf("%s", err))
		return
	}

	var edits []service.Edit
	add := func(field string, value any) {
		if _, ok := present[field]; ok {
			edits = append(edits, service.Edit{Field: field, Value: value})
		}
	}
	add(domain.FieldOverview, derefString(patch.Overview))
	add(domain.FieldCommunityRating, derefFloat(patch.CommunityRating))
	add(domain.FieldOfficialRating, derefString(patch.OfficialRating))
	add(domain.FieldPremiereDate, derefTime(patch.PremiereDate))
	if patch.Genres != nil {
		add(domain.FieldGenres, *patch.Genres)
	} else if _, ok := present[domain.FieldGenres]; ok {
		add(domain.FieldGenres, []string{})
	}

	if err := a.metadata.Set(r.Context(), id, edits); err != nil {
		writeError(w, r, err)
		return
	}

	md, err := a.metadata.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}

	loggerFrom(r.Context()).Info("metadata edited by hand",
		slog.String(logging.KeyOperation, "set_metadata"),
		slog.String("item_id", id.String()),
		slog.Int("fields", len(edits)))

	writeJSON(w, r, http.StatusOK, newMetadataResponse(md))
}

// handleSetFieldLock pins or unpins one field without changing its value.
func (a *API) handleSetFieldLock(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, service.InvalidArgumentf("item id is not a UUID"))
		return
	}

	field := strings.ToLower(r.PathValue("field"))
	locked := r.Method == http.MethodPut

	if err := a.metadata.SetLocked(r.Context(), id, field, locked); err != nil {
		writeError(w, r, err)
		return
	}

	md, err := a.metadata.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newMetadataResponse(md))
}

// handleRefreshMetadata starts a background metadata refresh.
//
// ?all=true re-fetches every identified item, which is one provider request
// per film. The default considers only items never fetched, so the expensive
// form is the one somebody asks for rather than the one they discover.
func (a *API) handleRefreshMetadata(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, service.InvalidArgumentf("library id is not a UUID"))
		return
	}

	all := strings.EqualFold(r.URL.Query().Get("all"), "true")

	job, err := a.metadata.StartRefresh(r.Context(), id, all)
	if err != nil {
		writeError(w, r, err)
		return
	}

	loggerFrom(r.Context()).Info("metadata refresh started",
		slog.String(logging.KeyOperation, "refresh_metadata"),
		slog.String(logging.KeyJobID, job.ID.String()),
		slog.String("library_id", id.String()),
		slog.Bool("all", all))

	writeJSON(w, r, http.StatusAccepted, newJobResponse(job))
}

func derefString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func derefFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func derefTime(p *time.Time) any {
	if p == nil {
		return nil
	}
	return *p
}
