package repository

import (
	"context"

	"uuid"

	"github.com/maverickman79/reelix/internal/domain"
)

// WriteImage records which image an item has, unless the field is locked.
//
// GUARDED BY THE SAME claimField AS EVERY OTHER FIELD, and that is the whole
// reason artwork provenance lives in media_item_field_provenance rather than in
// columns on media_item_images. There is one lock guard in the system and one
// line to delete to make its tests fail. A lock check written specially for
// images would be a second mechanism guaranteeing one outcome, which is the
// redundant-enforcement pattern the fields slice found and collapsed: remove
// either alone and nothing observable changes, so neither can be tested.
//
// An img with no StoragePath records a NEGATIVE — the provider has no image of
// this type — which is a fact worth storing. See migration 0012: it is what
// stops the pass re-asking about every film that has no logo, while a failed
// download still writes nothing at all and so retries on the next pass.
//
// Returns whether the write happened. A refused write is not an error; it is
// the guard working, and the caller counts it.
func (r *MetadataRepository) WriteImage(
	ctx context.Context, itemID uuid.UUID, imageType, source string, img domain.ItemImage,
) (bool, error) {
	field, ok := domain.ImageField(imageType)
	if !ok {
		return false, mapError("writing image", &unknownImageTypeError{imageType: imageType})
	}

	ts := now()

	claimed, err := r.claimField(ctx, itemID, field, source, ts)
	if err != nil || !claimed {
		return false, err
	}

	// Nullable together, so that a negative result cannot be written as half a
	// stored image. The database enforces the same thing in a CHECK; this is
	// the shape that satisfies it rather than a second guard on the same
	// outcome.
	var path, tag, contentType, sourceURL *string
	var width, height *int
	if img.Stored() {
		path, tag, contentType = &img.StoragePath, &img.Tag, &img.ContentType
		if img.SourceURL != "" {
			sourceURL = &img.SourceURL
		}
		if img.Width > 0 {
			width = &img.Width
		}
		if img.Height > 0 {
			height = &img.Height
		}
	}

	if _, err := r.q.Exec(ctx, `
		INSERT INTO media_item_images (
			media_item_id, image_type, storage_path, image_tag, content_type,
			width, height, source_url, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
		ON CONFLICT (media_item_id, image_type) DO UPDATE SET
			storage_path = EXCLUDED.storage_path,
			image_tag    = EXCLUDED.image_tag,
			content_type = EXCLUDED.content_type,
			width        = EXCLUDED.width,
			height       = EXCLUDED.height,
			source_url   = EXCLUDED.source_url,
			updated_at   = EXCLUDED.updated_at`,
		itemID, imageType, path, tag, contentType, width, height, sourceURL, ts); err != nil {
		return false, mapError("writing image", err)
	}
	return true, nil
}

// GetImage returns one item's image of one type, for the serving path.
//
// A recorded negative answers ErrNotFound like an absent row: neither has bytes
// to serve, and the handler's answer is the same 404 either way.
func (r *MetadataRepository) GetImage(
	ctx context.Context, itemID uuid.UUID, imageType string,
) (domain.ItemImage, error) {
	var img domain.ItemImage
	img.MediaItemID, img.ImageType = itemID, imageType

	var path, tag, contentType, sourceURL *string
	var width, height *int

	err := r.q.QueryRow(ctx, `
		SELECT storage_path, image_tag, content_type, width, height, source_url,
		       created_at, updated_at
		  FROM media_item_images
		 WHERE media_item_id = $1 AND image_type = $2`, itemID, imageType).
		Scan(&path, &tag, &contentType, &width, &height, &sourceURL,
			&img.CreatedAt, &img.UpdatedAt)
	if err != nil {
		if isNoRows(err) {
			return domain.ItemImage{}, ErrNotFound
		}
		return domain.ItemImage{}, mapError("loading image", err)
	}

	assignImage(&img, path, tag, contentType, sourceURL, width, height)
	if !img.Stored() {
		return domain.ItemImage{}, ErrNotFound
	}
	return img, nil
}

// ImagesFor loads every item's images at once, for a listing.
//
// Negatives are included, because a caller distinguishing "no image" from "not
// fetched yet" needs them; domain.ItemMetadata.Image is what collapses the two
// for callers that do not.
func (r *MetadataRepository) ImagesFor(
	ctx context.Context, itemIDs []uuid.UUID,
) (map[uuid.UUID]map[string]domain.ItemImage, error) {
	out := map[uuid.UUID]map[string]domain.ItemImage{}
	if len(itemIDs) == 0 {
		return out, nil
	}

	rows, err := r.q.Query(ctx, `
		SELECT media_item_id, image_type, storage_path, image_tag, content_type,
		       width, height, source_url, created_at, updated_at
		  FROM media_item_images
		 WHERE media_item_id = ANY($1)`, itemIDs)
	if err != nil {
		return nil, mapError("loading images", err)
	}
	defer rows.Close()

	for rows.Next() {
		var img domain.ItemImage
		var path, tag, contentType, sourceURL *string
		var width, height *int

		if err := rows.Scan(&img.MediaItemID, &img.ImageType, &path, &tag, &contentType,
			&width, &height, &sourceURL, &img.CreatedAt, &img.UpdatedAt); err != nil {
			return nil, mapError("scanning image", err)
		}
		assignImage(&img, path, tag, contentType, sourceURL, width, height)

		if out[img.MediaItemID] == nil {
			out[img.MediaItemID] = map[string]domain.ItemImage{}
		}
		out[img.MediaItemID][img.ImageType] = img
	}
	return out, mapError("loading images", rows.Err())
}

// StoredImages lists every image row in a library that claims bytes on disk.
//
// The input to the reconcile sweep, which is what makes storing the bytes under
// /cache honest: the pass stats each of these and deletes the rows whose file
// has gone, so a wiped cache directory is repaired by an ordinary refresh.
// Negatives are excluded because there is nothing to stat.
func (r *MetadataRepository) StoredImages(
	ctx context.Context, libraryID uuid.UUID,
) ([]domain.ItemImage, error) {
	var libraryFilter *uuid.UUID
	if libraryID != (uuid.UUID{}) {
		libraryFilter = &libraryID
	}

	rows, err := r.q.Query(ctx, `
		SELECT m.media_item_id, m.image_type, m.storage_path
		  FROM media_item_images m
		  JOIN media_items i ON i.id = m.media_item_id
		 WHERE m.storage_path IS NOT NULL
		   AND ($1::uuid IS NULL OR i.library_id = $1)`, libraryFilter)
	if err != nil {
		return nil, mapError("listing stored images", err)
	}
	defer rows.Close()

	var out []domain.ItemImage
	for rows.Next() {
		var img domain.ItemImage
		if err := rows.Scan(&img.MediaItemID, &img.ImageType, &img.StoragePath); err != nil {
			return nil, mapError("scanning stored image", err)
		}
		out = append(out, img)
	}
	return out, mapError("listing stored images", rows.Err())
}

// DeleteImage removes an image row, returning the item to "never attempted".
//
// Used by the reconcile sweep for a row whose file has gone. It deletes rather
// than rewriting the row as a negative, because "the provider has no logo" and
// "the bytes we had are missing" are different claims and only the first is
// true.
//
// IT DELETES A LOCKED ROW TOO, and that is deliberate. A row claiming bytes
// that are not there is false whether or not somebody pinned it, and leaving it
// makes the item advertise an image tag that answers 404 — worse for a client
// than advertising nothing. The lock protects a CHOICE from being changed; a
// vanished file is not a choice to protect.
//
// The cost is stated rather than designed around: a LOCKED image whose file is
// swept will not be re-fetched, because claimField correctly refuses the
// rewrite, so the item stays imageless until somebody unlocks it. Both
// alternatives — a repair path that bypasses the lock, or a second lock check
// that knows about repairs — mean two write paths to one outcome, which is the
// pattern this codebase just spent a session removing. A narrow, logged,
// recoverable limitation is the better trade; see the Warn in the sweep.
func (r *MetadataRepository) DeleteImage(
	ctx context.Context, itemID uuid.UUID, imageType string,
) error {
	_, err := r.q.Exec(ctx,
		`DELETE FROM media_item_images WHERE media_item_id = $1 AND image_type = $2`,
		itemID, imageType)
	return mapError("deleting image", err)
}

// assignImage copies the nullable columns onto an image. Absent columns leave
// the zero value, which reads correctly as a negative.
func assignImage(
	img *domain.ItemImage,
	path, tag, contentType, sourceURL *string,
	width, height *int,
) {
	if path != nil {
		img.StoragePath = *path
	}
	if tag != nil {
		img.Tag = *tag
	}
	if contentType != nil {
		img.ContentType = *contentType
	}
	if sourceURL != nil {
		img.SourceURL = *sourceURL
	}
	if width != nil {
		img.Width = *width
	}
	if height != nil {
		img.Height = *height
	}
}

type unknownImageTypeError struct{ imageType string }

func (e *unknownImageTypeError) Error() string { return "unknown image type: " + e.imageType }
