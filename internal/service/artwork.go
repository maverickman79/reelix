package service

import (
	"context"
	"errors"
	"log/slog"

	"uuid"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/metadata"
	"github.com/maverickman79/reelix/internal/repository"
)

// reconcile deletes image rows whose file is no longer on disk.
//
// THIS IS WHAT MAKES PUTTING THE BYTES IN /cache HONEST. The constitution's
// persistent state is /config, /cache and the Postgres volume, and artwork
// splits across two of them: the decision is durable in Postgres, the bytes are
// a re-downloadable derivative in /cache. That split is only defensible if a
// wiped cache directory recovers by itself, and this is where it does — the
// missing file becomes an absent row, and an absent row is already the retry
// queue, so the very next ordinary refresh re-downloads it.
//
// It runs before selection rather than during it, because the "needs work"
// predicate is SQL and SQL cannot stat a file. Deleting first turns a state the
// query cannot see into the ordinary "no row" case that it can.
//
// Deleting also stops the item advertising an image tag that would answer 404.
// A client that is told an image exists and then cannot fetch it is worse off
// than a client told there is none: the first is a broken image, the second is
// a placeholder.
func (s *MetadataService) reconcile(
	ctx context.Context, libraryID uuid.UUID, log *slog.Logger,
) (int, error) {
	repo := repository.NewMetadataRepository(s.pool)

	stored, err := repo.StoredImages(ctx, libraryID)
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, img := range stored {
		if s.images.Exists(img.StoragePath) {
			continue
		}
		if err := repo.DeleteImage(ctx, img.MediaItemID, img.ImageType); err != nil {
			return removed, err
		}
		removed++
		log.Warn("image file is missing; the row was cleared and will be re-fetched",
			slog.String("item_id", img.MediaItemID.String()),
			slog.String("image_type", img.ImageType),
			slog.String("path", img.StoragePath))
	}
	return removed, nil
}

// storeImages downloads and records this item's artwork.
//
// Each type is attempted and recorded INDEPENDENTLY. A film whose logo cannot
// be fetched still gets its poster, and a film that fails entirely does not
// fail the pass — the same shape as one unprobeable file during a scan.
//
// The three outcomes are deliberately different states, because the pass has to
// tell them apart on its next run:
//
//	downloaded          a row with a path        -> skip next time
//	provider has none   a row with a NULL path   -> skip next time
//	download failed     NO ROW AT ALL            -> retry next time
//
// Writing nothing on failure is what stops a timeout or a 404 marking a film
// permanently imageless. There is no attempt counter and no backoff column,
// because absence already is the retry queue.
func (s *MetadataService) storeImages(
	ctx context.Context, repo *repository.MetadataRepository,
	itemID uuid.UUID, md metadata.MovieMetadata, all bool, log *slog.Logger,
) (downloaded, skippedLocked int, err error) {
	existing, err := repo.ImagesFor(ctx, []uuid.UUID{itemID})
	if err != nil {
		return 0, 0, err
	}
	have := existing[itemID]
	source := s.provider.Name()

	for _, imageType := range domain.ImageTypes {
		// A row of either kind is an answer already recorded. Only an explicit
		// full refresh asks the provider again.
		if _, recorded := have[imageType]; recorded && !all {
			continue
		}

		candidate, offered := md.Images[imageType]
		if !offered {
			// A recorded negative, and most films need one: TMDB has no logo
			// for the majority of releases. Without it every pass would
			// re-ask about every one of them forever, which is exactly what
			// "re-running the pass downloads nothing" forbids.
			written, err := repo.WriteImage(ctx, itemID, imageType, source, domain.ItemImage{})
			if err != nil {
				return downloaded, skippedLocked, err
			}
			if !written {
				skippedLocked++
			}
			continue
		}

		img, err := s.download(ctx, itemID, imageType, candidate)
		switch {
		case errors.Is(err, metadata.ErrRateLimited):
			// Abandon the pass rather than keep asking, the same as the field
			// fetch. Nothing was written for this type, so it retries.
			return downloaded, skippedLocked, err
		case err != nil:
			// No row is written, deliberately. The absence IS the retry.
			log.Warn("could not fetch artwork",
				slog.String("image_type", imageType),
				slog.Any(logging.KeyError, err))
			continue
		}

		written, err := repo.WriteImage(ctx, itemID, imageType, source, img)
		if err != nil {
			return downloaded, skippedLocked, err
		}
		if !written {
			skippedLocked++
			// The one case worth naming precisely. A locked field with no row
			// means the bytes were swept and the lock is now refusing to
			// restore them, so the item stays imageless until somebody
			// unlocks it. Warned rather than worked around: a repair path
			// that bypassed the lock, or a lock check that knew about
			// repairs, would both be a second write path to one outcome.
			if _, recorded := have[imageType]; !recorded {
				log.Warn("artwork is locked but not stored; unlock and refresh to restore it",
					slog.String("item_id", itemID.String()),
					slog.String("image_type", imageType))
			}
			continue
		}
		downloaded++
	}

	return downloaded, skippedLocked, nil
}

// download fetches one image and puts it on disk.
//
// The order — bytes durable first, database row second — is the whole design,
// and it is the caller that completes it: this function returns only once the
// file is atomically in place, and storeImages writes the row only once this
// returns. A crash in between leaves an orphan file that nothing advertises and
// that the next pass overwrites. The reverse order would advertise a tag for a
// file that is not there.
func (s *MetadataService) download(
	ctx context.Context, itemID uuid.UUID, imageType string, candidate metadata.ImageCandidate,
) (domain.ItemImage, error) {
	body, contentType, err := s.provider.FetchImage(ctx, candidate.URL)
	if err != nil {
		return domain.ItemImage{}, err
	}
	defer body.Close()

	saved, err := s.images.Save(itemID, imageType, contentType, body)
	if err != nil {
		return domain.ItemImage{}, err
	}

	return domain.ItemImage{
		MediaItemID: itemID,
		ImageType:   imageType,
		StoragePath: saved.Path,
		Tag:         saved.Tag,
		ContentType: saved.ContentType,
		// From the provider's own listing, so PrimaryImageAspectRatio is
		// answerable without decoding a pixel.
		Width:     candidate.Width,
		Height:    candidate.Height,
		SourceURL: candidate.URL,
	}, nil
}
