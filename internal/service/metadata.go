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
	"github.com/maverickman79/reelix/internal/media/artwork"
	"github.com/maverickman79/reelix/internal/metadata"
	"github.com/maverickman79/reelix/internal/repository"
)

// metadataBatch bounds how many items one refresh claims at a time.
const metadataBatch = 200

// MetadataService fetches and edits the managed metadata fields.
type MetadataService struct {
	pool     *pgxpool.Pool
	provider metadata.Provider
	images   *artwork.Store
	log      *slog.Logger
}

// NewMetadataService returns a service backed by pool, provider and an artwork
// store rooted at cacheDir.
func NewMetadataService(
	pool *pgxpool.Pool, provider metadata.Provider, cacheDir string, log *slog.Logger,
) *MetadataService {
	return &MetadataService{
		pool:     pool,
		provider: provider,
		images:   artwork.NewStore(cacheDir),
		log:      logging.Component(log, "metadata"),
	}
}

// Images returns the artwork store, so the compatibility layer can serve the
// bytes a row points at.
func (s *MetadataService) Images() *artwork.Store { return s.images }

// GetImage returns one item's stored image, for the serving path.
func (s *MetadataService) GetImage(
	ctx context.Context, itemID uuid.UUID, imageType string,
) (domain.ItemImage, error) {
	return repository.NewMetadataRepository(s.pool).GetImage(ctx, itemID, imageType)
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

	// An image is lockable but not hand-settable in 0.0.2: choosing one means
	// supplying a URL or bytes, which is the image selection UI this milestone
	// excludes. Refused here by name rather than left to fail on a missing
	// column, because an error by coincidence reads like a bug to whoever
	// meets it — and it would stop being an error the day somebody added the
	// column.
	for _, e := range edits {
		if domain.IsImageField(e.Field) {
			return InvalidArgumentf(
				"%s cannot be set by hand; it can only be locked or unlocked", e.Field)
		}
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

	result, err := s.refresh(ctx, jobID, libraryID, all, log)
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
		slog.Int("fetched", result.fetched),
		slog.Int("images_downloaded", result.imagesDownloaded),
		slog.Int("images_cleared", result.imagesCleared),
		slog.Int("fields_skipped_locked", result.skippedLocked))

	if err := jobs.Finish(ctx, jobID, domain.JobStateCompleted, ""); err != nil {
		log.Error("could not record completion", slog.Any(logging.KeyError, err))
	}
}

// refreshResult counts what one pass did.
type refreshResult struct {
	fetched          int
	skippedLocked    int
	imagesDownloaded int
	imagesCleared    int
}

// refresh walks the library's identified items and stores what the provider
// knows about each.
func (s *MetadataService) refresh(
	ctx context.Context, jobID, libraryID uuid.UUID, all bool, log *slog.Logger,
) (refreshResult, error) {
	var result refreshResult

	repo := repository.NewMetadataRepository(s.pool)
	identities := repository.NewIdentityRepository(s.pool)
	jobs := repository.NewJobRepository(s.pool)

	// Partial downloads first. Save removes its own temporary file on every
	// failure path, so this only ever catches the one case that cannot — the
	// process dying mid-download — which would otherwise leave a file per
	// interrupted pass forever, each with a random suffix and so never
	// overwritten.
	if swept, err := s.images.Sweep(); err != nil {
		log.Warn("could not sweep partial image downloads", slog.Any(logging.KeyError, err))
	} else if swept > 0 {
		log.Info("swept partial image downloads", slog.Int("removed", swept))
	}

	// Then rows whose file has gone, so the selection below sees them as items
	// with no image rather than as items already done. See reconcile.
	cleared, err := s.reconcile(ctx, libraryID, log)
	if err != nil {
		return result, err
	}
	result.imagesCleared = cleared

	items, err := repo.ItemsNeedingMetadata(ctx, libraryID, !all, metadataBatch)
	if err != nil {
		return result, err
	}

	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if i > 0 {
			time.Sleep(providerPause)
		}
		if err := jobs.UpdateProgress(ctx, jobID, i+1, len(items), item.Title); err != nil {
			log.Warn("could not record progress", slog.Any(logging.KeyError, err))
		}

		identity, err := identities.Get(ctx, item.ID)
		if err != nil {
			return result, err
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
			return result, err
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
			return result, err
		}
		result.fetched++
		result.skippedLocked += skipped

		// Artwork rides the SAME provider response as the fields, so a
		// library-wide refresh sends TMDB no more requests than it did before
		// artwork existed. Only the image bytes are extra, and they come from
		// a CDN rather than the API.
		downloaded, imagesSkipped, err := s.storeImages(
			ctx, repo, item.ID, md, all, log.With(slog.String("item", item.Title)))
		if err != nil {
			return result, err
		}
		result.imagesDownloaded += downloaded
		result.skippedLocked += imagesSkipped

		log.Info("metadata fetched",
			slog.String("item", item.Title),
			slog.Int("images_downloaded", downloaded),
			slog.Int("fields_skipped_locked", skipped+imagesSkipped))
	}

	return result, nil
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
