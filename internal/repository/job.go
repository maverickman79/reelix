package repository

import (
	"context"
	"uuid"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
)

// JobRepository persists background jobs.
type JobRepository struct {
	q db.Querier
}

// NewJobRepository returns a repository reading and writing through q, which
// may be the pool or an open transaction.
func NewJobRepository(q db.Querier) *JobRepository {
	return &JobRepository{q: q}
}

const jobColumns = `id, kind, state, library_id, progress_current, progress_total,
                    current_item, error, created_at, started_at, finished_at`

// Create enqueues a job in the queued state.
//
// A second active job for the same library violates the partial unique index
// and comes back as ErrConflict; callers should surface that rather than
// retrying.
func (r *JobRepository) Create(ctx context.Context, j *domain.Job) error {
	j.ID = newID()
	j.CreatedAt = now()
	j.State = domain.JobStateQueued

	const q = `
		INSERT INTO jobs (id, kind, state, library_id, progress_current, progress_total, created_at)
		VALUES ($1, $2, $3, $4, 0, 0, $5)`

	_, err := r.q.Exec(ctx, q, j.ID, j.Kind, j.State, j.LibraryID, j.CreatedAt)
	return mapError("creating job", err)
}

// Get returns one job, or ErrNotFound.
func (r *JobRepository) Get(ctx context.Context, id uuid.UUID) (domain.Job, error) {
	const q = `SELECT ` + jobColumns + ` FROM jobs WHERE id = $1`
	return scanJob(r.q.QueryRow(ctx, q, id), "getting job")
}

// List returns recent jobs, newest first.
func (r *JobRepository) List(ctx context.Context, limit int) ([]domain.Job, error) {
	const q = `SELECT ` + jobColumns + ` FROM jobs ORDER BY id DESC LIMIT $1`

	rows, err := r.q.Query(ctx, q, limit)
	if err != nil {
		return nil, mapError("listing jobs", err)
	}
	defer rows.Close()

	var out []domain.Job
	for rows.Next() {
		j, err := scanJob(rows, "listing jobs")
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, mapError("listing jobs", rows.Err())
}

// MarkRunning moves a queued job into the running state.
func (r *JobRepository) MarkRunning(ctx context.Context, id uuid.UUID) error {
	const q = `
		UPDATE jobs
		SET state = 'running', started_at = $2
		WHERE id = $1 AND state = 'queued'`

	tag, err := r.q.Exec(ctx, q, id, now())
	if err != nil {
		return mapError("starting job", err)
	}
	if tag.RowsAffected() == 0 {
		return mapError("starting job", ErrNotFound)
	}
	return nil
}

// UpdateProgress records how far a running job has got.
//
// Progress is advisory: a lost update costs an operator a stale number on a
// status page, so this deliberately does not fail the job when the row has
// already moved to a terminal state.
func (r *JobRepository) UpdateProgress(ctx context.Context, id uuid.UUID, current, total int, item string) error {
	const q = `
		UPDATE jobs
		SET progress_current = $2, progress_total = $3, current_item = $4
		WHERE id = $1 AND state = 'running'`

	_, err := r.q.Exec(ctx, q, id, current, total, nullString(item))
	return mapError("updating job progress", err)
}

// Finish moves a job into a terminal state.
//
// failureReason is stored only when state is failed; it must already be safe
// to show an administrator.
func (r *JobRepository) Finish(ctx context.Context, id uuid.UUID, state domain.JobState, failureReason string) error {
	if !state.Terminal() {
		return mapError("finishing job", ErrInvalidState)
	}

	const q = `
		UPDATE jobs
		SET state = $2, error = $3, finished_at = $4, current_item = NULL
		WHERE id = $1`

	tag, err := r.q.Exec(ctx, q, id, state, nullString(failureReason), now())
	if err != nil {
		return mapError("finishing job", err)
	}
	if tag.RowsAffected() == 0 {
		return mapError("finishing job", ErrNotFound)
	}
	return nil
}

// FailOrphaned marks jobs that were running when the process died.
//
// Jobs execute in-process, so a job still marked running at startup cannot be
// running: its goroutine went with the previous process. Leaving it that way
// would misreport a dead scan as live forever, and — because of the partial
// unique index — would block the library from ever being scanned again.
//
// Returns how many were reaped.
func (r *JobRepository) FailOrphaned(ctx context.Context) (int64, error) {
	const q = `
		UPDATE jobs
		SET state = 'failed',
		    error = 'the server stopped while this job was running',
		    finished_at = $1,
		    current_item = NULL
		WHERE state IN ('queued', 'running')`

	tag, err := r.q.Exec(ctx, q, now())
	if err != nil {
		return 0, mapError("reaping orphaned jobs", err)
	}
	return tag.RowsAffected(), nil
}

// scanJob reads one row in jobColumns order.
func scanJob(row interface{ Scan(...any) error }, op string) (domain.Job, error) {
	var j domain.Job
	err := row.Scan(&j.ID, &j.Kind, &j.State, &j.LibraryID,
		&j.ProgressCurrent, &j.ProgressTotal, &j.CurrentItem, &j.Error,
		&j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if err != nil {
		return domain.Job{}, mapError(op, err)
	}
	return j, nil
}

// nullString maps "" to a SQL NULL, so an absent value is absent rather than
// an empty string that reads as present.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
