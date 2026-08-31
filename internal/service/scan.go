package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/media"
	"github.com/maverickman79/reelix/internal/repository"
)

// progressInterval is how often a running scan writes its progress.
//
// Every file would mean one UPDATE per probe; a status page does not need that
// resolution, and the probes themselves are what take the time.
const progressInterval = 2 * time.Second

// recentJobLimit bounds the job listing.
const recentJobLimit = 50

// Prober is the part of media.Prober a scan needs.
//
// Declared here rather than in the media package because the interface belongs
// to its consumer. It also lets the persistence and idempotency tests run
// without ffprobe installed, which keeps them useful on a machine that only
// has the container.
type Prober interface {
	Probe(ctx context.Context, path string) (media.ProbeResult, error)
}

// ScanService runs library scans as background jobs.
type ScanService struct {
	pool   *pgxpool.Pool
	prober Prober
	log    *slog.Logger
}

// NewScanService returns a service backed by pool.
func NewScanService(pool *pgxpool.Pool, prober Prober, log *slog.Logger) *ScanService {
	return &ScanService{
		pool:   pool,
		prober: prober,
		log:    logging.Component(log, "scanner"),
	}
}

// Start enqueues a scan for a library and begins running it.
//
// It returns as soon as the job row exists, so the HTTP handler answers
// immediately: the constitution forbids blocking a request on a large library
// operation. ErrConflict comes back when a scan for this library is already in
// flight, enforced by a partial unique index rather than by a check here.
func (s *ScanService) Start(ctx context.Context, libraryID uuid.UUID) (domain.Job, error) {
	libs := repository.NewLibraryRepository(s.pool)

	if _, err := libs.GetByID(ctx, libraryID); err != nil {
		return domain.Job{}, err
	}

	job := domain.Job{Kind: domain.JobKindLibraryScan, LibraryID: &libraryID}
	if err := repository.NewJobRepository(s.pool).Create(ctx, &job); err != nil {
		return domain.Job{}, err
	}

	// Deliberately not the request's context: the scan must outlive the HTTP
	// request that asked for it. It is bounded instead by the process
	// lifetime, and a job still running when the process dies is reaped at the
	// next startup.
	go s.run(context.WithoutCancel(ctx), job.ID, libraryID)

	return job, nil
}

// Job returns one job.
func (s *ScanService) Job(ctx context.Context, id uuid.UUID) (domain.Job, error) {
	return repository.NewJobRepository(s.pool).Get(ctx, id)
}

// RecentJobs returns the most recent jobs, newest first.
func (s *ScanService) RecentJobs(ctx context.Context) ([]domain.Job, error) {
	return repository.NewJobRepository(s.pool).List(ctx, recentJobLimit)
}

// ReapOrphanedJobs fails jobs left running by a previous process.
//
// Called once at startup. Jobs run in-process, so anything still marked
// running has no goroutine behind it; leaving the row would both misreport a
// dead scan as live and — through the partial unique index — permanently block
// new scans of that library.
func (s *ScanService) ReapOrphanedJobs(ctx context.Context) error {
	n, err := repository.NewJobRepository(s.pool).FailOrphaned(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		s.log.Warn("reaped jobs left running by a previous process",
			slog.String(logging.KeyOperation, "reap_jobs"),
			slog.Int64("jobs", n))
	}
	return nil
}

// run executes a scan to completion, recording progress as it goes.
func (s *ScanService) run(ctx context.Context, jobID, libraryID uuid.UUID) {
	log := s.log.With(slog.String(logging.KeyJobID, jobID.String()))
	jobs := repository.NewJobRepository(s.pool)

	if err := jobs.MarkRunning(ctx, jobID); err != nil {
		log.Error("could not start job",
			slog.String(logging.KeyOperation, "scan"),
			slog.String(logging.KeyError, err.Error()))
		return
	}

	started := time.Now()
	summary, err := s.scan(ctx, log, jobs, jobID, libraryID)

	if err != nil {
		log.Error("scan failed",
			slog.String(logging.KeyOperation, "scan"),
			slog.String(logging.KeyError, err.Error()))

		if ferr := jobs.Finish(ctx, jobID, domain.JobStateFailed, err.Error()); ferr != nil {
			log.Error("could not record job failure",
				slog.String(logging.KeyOperation, "scan"),
				slog.String(logging.KeyError, ferr.Error()))
		}
		return
	}

	log.Info("scan completed",
		slog.String(logging.KeyOperation, "scan"),
		slog.Int("discovered", summary.discovered),
		slog.Int("probed", summary.probed),
		slog.Int("skipped", summary.skipped),
		slog.Int("failed", summary.failed),
		slog.Duration("took", time.Since(started).Truncate(time.Millisecond)),
		// The split, so the shape of a scan is readable from the completion
		// line alone without collecting the per-file debug lines.
		slog.Int64("walk_ms", summary.walk.Milliseconds()),
		slog.Int64("probe_wall_ms", summary.probeWall.Milliseconds()),
		slog.Int64("probe_cpu_ms", summary.probeCPU.Milliseconds()),
		slog.Int64("db_ms", summary.db.Milliseconds()))

	if err := jobs.Finish(ctx, jobID, domain.JobStateCompleted, ""); err != nil {
		log.Error("could not record job completion",
			slog.String(logging.KeyOperation, "scan"),
			slog.String(logging.KeyError, err.Error()))
	}
}

// scanSummary counts what one scan did, and what each part of it cost.
//
// THE TIMINGS ARE SPLIT THREE WAYS ON PURPOSE, because "the scan took an hour"
// does not say what to change. Walk, probe and database are separately
// addressable — a walk-dominated scan wants fewer stat calls, a probe-dominated
// one wants the wall-versus-CPU question in media.ProbeTiming answered before
// anybody reaches for concurrency, and a database-dominated one wants the
// per-file transaction batched.
type scanSummary struct {
	discovered int
	probed     int
	skipped    int
	failed     int

	// walk is the directory traversal alone, before any probe runs. On a
	// spinning array with thousands of release folders this can be the
	// expensive half, and it is invisible in a single total.
	walk time.Duration
	// probeWall and probeCPU aggregate media.ProbeTiming across every file
	// ffprobe actually ran against, failures included. Their RATIO is the
	// answer to whether the scan is I/O-bound or process-bound.
	probeWall time.Duration
	probeCPU  time.Duration
	// db is time inside the per-file transaction.
	db time.Duration
}

// fileTiming is what one file cost.
type fileTiming struct {
	// probed reports whether ffprobe ran; an unchanged file is persisted
	// without one.
	probed bool
	probe  media.ProbeTiming
	db     time.Duration
}

// scan walks the library, probes what needs probing, and persists everything.
//
// Only a walk-level failure returns an error. A file that will not probe is
// counted and left with a null probed_at, so the next scan retries it: one
// corrupt file must not cost an operator the other nine hundred.
func (s *ScanService) scan(
	ctx context.Context,
	log *slog.Logger,
	jobs *repository.JobRepository,
	jobID, libraryID uuid.UUID,
) (scanSummary, error) {
	var summary scanSummary

	paths, err := repository.NewLibraryRepository(s.pool).ListPaths(ctx, libraryID)
	if err != nil {
		return summary, err
	}
	if len(paths) == 0 {
		return summary, errors.New("library has no paths to scan")
	}

	roots := make([]string, len(paths))
	for i, p := range paths {
		roots[i] = p.Path
	}

	walkStarted := time.Now()
	found, err := media.Scan(ctx, roots)
	if err != nil {
		return summary, fmt.Errorf("walking library: %w", err)
	}
	summary.walk = time.Since(walkStarted)
	summary.discovered = len(found)

	log.Info("walk complete",
		slog.String(logging.KeyOperation, "scan"),
		slog.Int("files", len(found)),
		slog.Duration("took", summary.walk.Truncate(time.Millisecond)))

	if err := jobs.UpdateProgress(ctx, jobID, 0, len(found), ""); err != nil {
		return summary, err
	}

	repo := repository.NewMediaRepository(s.pool)
	lastProgress := time.Now()

	for i, f := range found {
		if err := ctx.Err(); err != nil {
			return summary, err
		}

		// Progress is written on a timer rather than per file: one UPDATE per
		// probe would be pure overhead on a large library.
		if time.Since(lastProgress) >= progressInterval {
			if err := jobs.UpdateProgress(ctx, jobID, i, len(found), f.Path); err != nil {
				return summary, err
			}
			lastProgress = time.Now()
		}

		timing, err := s.persistFile(ctx, repo, libraryID, f)

		summary.db += timing.db
		if timing.probed {
			summary.probeWall += timing.probe.Wall
			summary.probeCPU += timing.probe.CPU()

			// One line per probed file, at debug so an ordinary scan does not
			// write a line per film. Everything needed to attribute the cost
			// is here: size, because a seek-bound scan does not care about it
			// and a throughput-bound one does; and wall against CPU, which is
			// the discriminator itself. See media.ProbeTiming.
			log.Debug("file probed",
				slog.String(logging.KeyOperation, "scan"),
				slog.String("path", f.Path),
				slog.Int64("size_bytes", f.SizeBytes),
				slog.Int64("probe_wall_ms", timing.probe.Wall.Milliseconds()),
				slog.Int64("probe_cpu_ms", timing.probe.CPU().Milliseconds()),
				slog.Int64("db_ms", timing.db.Milliseconds()))
		}

		switch {
		case err != nil:
			summary.failed++
			log.Warn("could not index file",
				slog.String(logging.KeyOperation, "scan"),
				slog.String("path", f.Path),
				slog.String(logging.KeyError, err.Error()))
		case timing.probed:
			summary.probed++
		default:
			summary.skipped++
		}
	}

	if err := jobs.UpdateProgress(ctx, jobID, len(found), len(found), ""); err != nil {
		return summary, err
	}
	return summary, nil
}

// persistFile records one discovered file, probing it when needed.
//
// Returns what the file cost. An already-probed, unchanged file is persisted
// but not re-probed, and reports probed=false. A probe that FAILED still
// reports its timing — a file that took the full two-minute timeout to give up
// is one of the more interesting numbers a scan produces.
func (s *ScanService) persistFile(
	ctx context.Context,
	repo *repository.MediaRepository,
	libraryID uuid.UUID,
	f media.DiscoveredFile,
) (fileTiming, error) {
	var timing fileTiming

	existing, err := repo.GetFileByPath(ctx, f.Path)
	switch {
	case err == nil:
	case errors.Is(err, repository.ErrNotFound):
		existing = domain.MediaFile{}
	default:
		return timing, err
	}

	// Skip the probe when this file has already been probed and has not
	// changed size.
	//
	// Size is a weaker change signal than modification time; media_files has no
	// mtime column, so a file edited in place without changing length would be
	// missed. Deleting the row, or clearing probed_at, forces a re-probe.
	needsProbe := existing.ProbedAt == nil || existing.SizeBytes != f.SizeBytes

	var probe media.ProbeResult
	if needsProbe {
		probe, err = s.prober.Probe(ctx, f.Path)
		// Recorded before the error check: a failed probe still consumed the
		// time, and a scan whose cost is dominated by files that fail is
		// exactly the diagnosis that would be lost by only timing successes.
		timing.probed = true
		timing.probe = probe.Timing
		if err != nil {
			return timing, err
		}
	}

	item := domain.MediaItem{
		LibraryID:  libraryID,
		Kind:       domain.MediaItemKindMovie,
		Title:      f.Name.Title,
		Year:       f.Name.Year,
		SourcePath: f.SourcePath,
	}

	file := domain.MediaFile{
		ID:        existing.ID,
		Path:      f.Path,
		Filename:  f.Filename,
		SizeBytes: f.SizeBytes,
	}

	if needsProbe {
		now := time.Now().UTC()
		file.Container = nonEmpty(probe.Container)
		file.DurationSeconds = probe.Duration
		file.ProbedAt = &now
	} else {
		file.Container = existing.Container
		file.DurationSeconds = existing.DurationSeconds
		file.ProbedAt = existing.ProbedAt
	}

	// One transaction per file: the item, the file, and its streams appear
	// together or not at all. A file recorded without its streams would look
	// probed to the next scan while carrying no playable track information.
	dbStarted := time.Now()
	err = db.InTx(ctx, s.pool, func(q db.Querier) error {
		tx := repository.NewMediaRepository(q)

		if err := tx.UpsertItem(ctx, &item); err != nil {
			return err
		}
		file.MediaItemID = item.ID

		// Give the item somewhere for identification to land. Creating the
		// row here rather than in the identify pass means a pass never has to
		// discover items on its own, and ON CONFLICT DO NOTHING means a
		// re-scan cannot drag an already-decided item back to pending — a film
		// somebody corrected by hand must survive its file being seen again.
		//
		// This is a local write. It contacts no provider, which is what keeps
		// the scan an offline operation.
		if err := repository.NewIdentityRepository(q).Ensure(ctx, item.ID); err != nil {
			return err
		}

		if err := tx.UpsertFile(ctx, &file); err != nil {
			return err
		}

		if !needsProbe {
			return nil
		}

		streams := make([]domain.MediaStream, 0, len(probe.Streams))
		for _, ps := range probe.Streams {
			streams = append(streams, domain.MediaStream{
				StreamIndex: ps.Index,
				Kind:        domain.StreamKind(ps.Kind),
				Codec:       nonEmpty(ps.Codec),
				Width:       ps.Width,
				Height:      ps.Height,
				Channels:    ps.Channels,
				BitRate:     ps.BitRate,

				Language:    ps.Language,
				Title:       ps.Title,
				Profile:     ps.Profile,
				Level:       ps.Level,
				PixelFormat: ps.PixelFormat,

				AverageFrameRate: ps.AverageFrameRate,
				RealFrameRate:    ps.RealFrameRate,

				IsDefault:         ps.IsDefault,
				IsForced:          ps.IsForced,
				IsHearingImpaired: ps.IsHearingImpaired,

				ChannelLayout: ps.ChannelLayout,
				SampleRate:    ps.SampleRate,
			})
		}
		return tx.ReplaceStreams(ctx, file.ID, streams)
	})
	timing.db = time.Since(dbStarted)
	if err != nil {
		return timing, err
	}

	return timing, nil
}

// nonEmpty maps "" to nil, so an absent value is null rather than an empty
// string that reads as present.
func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
