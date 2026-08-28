package service

import (
	"context"
	"errors"
	"log/slog"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/metadata"
	"github.com/maverickman79/reelix/internal/repository"
)

// metadataBatch bounds how many items one refresh claims at a time.
const metadataBatch = 200

// MetadataService fetches and edits the managed metadata fields.
type MetadataService struct {
	pool     *pgxpool.Pool
	provider metadata.Provider
	log      *slog.Logger
}

// NewMetadataService returns a service backed by pool and provider.
func NewMetadataService(pool *pgxpool.Pool, provider metadata.Provider, log *slog.Logger) *MetadataService {
	return &MetadataService{
		pool:     pool,
		provider: provider,
		log:      logging.Component(log, "metadata"),
	}
}

// Get returns one item's metadata and provenance.
func (s *MetadataService) Get(ctx context.Context, itemID uuid.UUID) (domain.ItemMetadata, error) {
	if _, err := repository.NewMediaRepository(s.pool).GetItem(ctx, itemID); err != nil {
		return domain.ItemMetadata{}, err
	}
	return repository.NewMetadataRepository(s.pool).Get(ctx, itemID)
}

// Edit is one field a person is changing by hand.
type Edit struct {
	Field string
	// Value is the new value, already typed. Nil clears the field.
	Value any
}

// Set applies hand edits and LOCKS the fields it touched.
//
// The lock is a default for this operation, not a merging of two concepts. The
// constitution models Source and Locked as independent, and they remain
// independent: a value can be sourced from a provider and locked, or manual
// and unlocked after an explicit Unlock. What is decided here is only what an
// EDIT should imply when the caller says nothing.
//
// It implies a lock because the alternative is a correction that silently
// reverts on the next refresh, and a correction that does not survive is one
// nobody makes twice. The same reasoning made a manual identity outrank every
// identify pass. Anyone who wants the other behaviour asks for it, by
// unlocking; nobody has to know about locking to have their edit respected.
func (s *MetadataService) Set(ctx context.Context, itemID uuid.UUID, edits []Edit) error {
	if len(edits) == 0 {
		return InvalidArgumentf("no fields to set")
	}
	if _, err := repository.NewMediaRepository(s.pool).GetItem(ctx, itemID); err != nil {
		return err
	}

	repo := repository.NewMetadataRepository(s.pool)
	for _, e := range edits {
		// Unlock first: a person editing a field they had already locked must
		// not be refused by their own lock.
		if err := repo.SetLocked(ctx, itemID, e.Field, false); err != nil {
			return err
		}

		var err error
		if e.Field == domain.FieldGenres {
			genres, ok := e.Value.([]string)
			if !ok {
				return InvalidArgumentf("genres must be a list of strings")
			}
			_, err = repo.WriteGenres(ctx, itemID, domain.MetadataSourceManual, genres)
		} else {
			_, err = repo.WriteField(ctx, itemID, e.Field, domain.MetadataSourceManual, e.Value)
		}
		if err != nil {
			return err
		}

		if err := repo.SetLocked(ctx, itemID, e.Field, true); err != nil {
			return err
		}
	}
	return nil
}

// SetLocked pins or unpins one field without changing its value.
func (s *MetadataService) SetLocked(ctx context.Context, itemID uuid.UUID, field string, locked bool) error {
	if !domain.IsManagedField(field) {
		return InvalidArgumentf("unknown metadata field %q", field)
	}
	if _, err := repository.NewMediaRepository(s.pool).GetItem(ctx, itemID); err != nil {
		return err
	}
	return repository.NewMetadataRepository(s.pool).SetLocked(ctx, itemID, field, locked)
}

// StartRefresh enqueues a metadata refresh over a library.
//
// all=false, the default, considers only items that have never been fetched.
// all=true re-fetches everything identified, which is one provider request per
// film in the library — a real cost on a large one, and therefore something a
// caller asks for explicitly rather than discovers.
func (s *MetadataService) StartRefresh(ctx context.Context, libraryID uuid.UUID, all bool) (domain.Job, error) {
	if _, err := repository.NewLibraryRepository(s.pool).GetByID(ctx, libraryID); err != nil {
		return domain.Job{}, err
	}

	job := domain.Job{Kind: domain.JobKindLibraryMetadata, LibraryID: &libraryID}
	if err := repository.NewJobRepository(s.pool).Create(ctx, &job); err != nil {
		return domain.Job{}, err
	}

	go s.run(context.WithoutCancel(ctx), job.ID, libraryID, all)
	return job, nil
}

func (s *MetadataService) run(ctx context.Context, jobID, libraryID uuid.UUID, all bool) {
	log := s.log.With(slog.String(logging.KeyJobID, jobID.String()))
	jobs := repository.NewJobRepository(s.pool)

	if err := jobs.MarkRunning(ctx, jobID); err != nil {
		log.Error("could not mark the metadata job running", slog.Any(logging.KeyError, err))
		return
	}

	fetched, skipped, err := s.refresh(ctx, jobID, libraryID, all, log)
	if err != nil {
		log.Error("metadata refresh failed",
			slog.String(logging.KeyOperation, "refresh_metadata"),
			slog.Any(logging.KeyError, err))
		if ferr := jobs.Finish(ctx, jobID, domain.JobStateFailed, err.Error()); ferr != nil {
			log.Error("could not record the failure", slog.Any(logging.KeyError, ferr))
		}
		return
	}

	log.Info("metadata refresh complete",
		slog.String(logging.KeyOperation, "refresh_metadata"),
		slog.Int("fetched", fetched),
		slog.Int("fields_skipped_locked", skipped))

	if err := jobs.Finish(ctx, jobID, domain.JobStateCompleted, ""); err != nil {
		log.Error("could not record completion", slog.Any(logging.KeyError, err))
	}
}

// refresh walks the library's identified items and stores what the provider
// knows about each.
func (s *MetadataService) refresh(
	ctx context.Context, jobID, libraryID uuid.UUID, all bool, log *slog.Logger,
) (fetched, skippedLocked int, err error) {
	repo := repository.NewMetadataRepository(s.pool)
	identities := repository.NewIdentityRepository(s.pool)
	jobs := repository.NewJobRepository(s.pool)

	items, err := repo.ItemsNeedingMetadata(ctx, libraryID, !all, metadataBatch)
	if err != nil {
		return 0, 0, err
	}

	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return fetched, skippedLocked, err
		}
		if i > 0 {
			time.Sleep(providerPause)
		}
		if err := jobs.UpdateProgress(ctx, jobID, i+1, len(items), item.Title); err != nil {
			log.Warn("could not record progress", slog.Any(logging.KeyError, err))
		}

		identity, err := identities.Get(ctx, item.ID)
		if err != nil {
			return fetched, skippedLocked, err
		}
		providerID := identity.ExternalIDs[s.provider.Name()]
		if providerID == "" {
			// Identified against a provider this server is not configured for.
			// Not an error and not something a retry fixes.
			log.Warn("no provider id for item", slog.String("item", item.Title))
			continue
		}

		md, err := s.provider.FetchMetadata(ctx, providerID)
		switch {
		case errors.Is(err, metadata.ErrRateLimited):
			return fetched, skippedLocked, err
		case err != nil:
			// One film we could not fetch is not a reason to abandon the pass,
			// the same as one unprobeable file during a scan.
			log.Warn("could not fetch metadata",
				slog.String("item", item.Title),
				slog.Any(logging.KeyError, err))
			continue
		}

		skipped, err := s.store(ctx, repo, item.ID, md)
		if err != nil {
			return fetched, skippedLocked, err
		}
		fetched++
		skippedLocked += skipped

		log.Info("metadata fetched",
			slog.String("item", item.Title),
			slog.Int("fields_skipped_locked", skipped))
	}

	return fetched, skippedLocked, nil
}

// store writes each field the provider supplied, counting those the lock
// refused.
//
// A field the provider does not know is skipped rather than written as null:
// overwriting a value with "the provider has nothing" would lose a good value
// to a bad fetch, and an absent field and an empty one are different claims.
func (s *MetadataService) store(
	ctx context.Context, repo *repository.MetadataRepository,
	itemID uuid.UUID, md metadata.MovieMetadata,
) (skippedLocked int, err error) {
	source := s.provider.Name()

	write := func(field string, value any) error {
		written, err := repo.WriteField(ctx, itemID, field, source, value)
		if err != nil {
			return err
		}
		if !written {
			skippedLocked++
		}
		return nil
	}

	if md.Overview != "" {
		if err := write(domain.FieldOverview, md.Overview); err != nil {
			return skippedLocked, err
		}
	}
	if md.CommunityRating != nil {
		if err := write(domain.FieldCommunityRating, *md.CommunityRating); err != nil {
			return skippedLocked, err
		}
	}
	if md.OfficialRating != "" {
		if err := write(domain.FieldOfficialRating, md.OfficialRating); err != nil {
			return skippedLocked, err
		}
	}
	if !md.ReleaseDate.IsZero() {
		if err := write(domain.FieldPremiereDate, md.ReleaseDate); err != nil {
			return skippedLocked, err
		}
	}
	if len(md.Genres) > 0 {
		written, err := repo.WriteGenres(ctx, itemID, source, md.Genres)
		if err != nil {
			return skippedLocked, err
		}
		if !written {
			skippedLocked++
		}
	}
	return skippedLocked, nil
}
