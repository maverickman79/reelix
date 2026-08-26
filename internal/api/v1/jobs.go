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

// jobResponse is a background job as this API represents it.
type jobResponse struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	State     string  `json:"state"`
	LibraryID *string `json:"libraryId"`
	Progress  struct {
		Current int `json:"current"`
		Total   int `json:"total"`
	} `json:"progress"`
	CurrentItem *string    `json:"currentItem"`
	Error       *string    `json:"error"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

func newJobResponse(j domain.Job) jobResponse {
	out := jobResponse{
		ID:          j.ID.String(),
		Kind:        string(j.Kind),
		State:       string(j.State),
		CurrentItem: j.CurrentItem,
		Error:       j.Error,
		CreatedAt:   j.CreatedAt,
		StartedAt:   j.StartedAt,
		FinishedAt:  j.FinishedAt,
	}
	out.Progress.Current = j.ProgressCurrent
	out.Progress.Total = j.ProgressTotal

	if j.LibraryID != nil {
		id := j.LibraryID.String()
		out.LibraryID = &id
	}
	return out
}

// handleScanLibrary starts a background scan of one library.
//
// It answers 202 as soon as the job exists rather than waiting for the scan:
// a large library takes minutes, and the constitution forbids blocking an HTTP
// request on that. Poll the returned job for progress.
func (a *API) handleScanLibrary(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, service.InvalidArgumentf("library id is not a UUID"))
		return
	}

	job, err := a.scans.Start(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}

	loggerFrom(r.Context()).Info("scan started",
		slog.String(logging.KeyOperation, "scan_library"),
		slog.String(logging.KeyJobID, job.ID.String()),
		slog.String("library_id", id.String()))

	writeJSON(w, r, http.StatusAccepted, newJobResponse(job))
}

// handleGetJob returns one job's current state.
func (a *API) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, service.InvalidArgumentf("job id is not a UUID"))
		return
	}

	job, err := a.scans.Job(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newJobResponse(job))
}

// listJobsResponse wraps the collection in an object, leaving room for
// pagination without breaking callers.
type listJobsResponse struct {
	Jobs []jobResponse `json:"jobs"`
}

// handleListJobs returns recent jobs, newest first.
func (a *API) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := a.scans.RecentJobs(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}

	out := make([]jobResponse, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, newJobResponse(j))
	}
	writeJSON(w, r, http.StatusOK, listJobsResponse{Jobs: out})
}
