-- Artwork: the record of which image an item has, and where its bytes are.
--
-- THE BYTES ARE NOT HERE, DELIBERATELY. They live on the filesystem under the
-- cache directory; see internal/media/artwork. The constitution puts persistent
-- state in /config, /cache and the Postgres volume, and artwork divides cleanly
-- across two of them: the DECISION — which image, its digest, its dimensions,
-- who chose it — is durable state and lives in this table, while the bytes are
-- a re-downloadable derivative of an external provider and live in /cache.
--
-- That split is only honest if a wiped /cache actually recovers, so the refresh
-- pass stats the file and treats a row whose file is missing as work to do. See
-- ItemsNeedingImages. Recovery is an ordinary refresh, not an operator
-- procedure.
CREATE TABLE media_item_images (
    media_item_id uuid        NOT NULL REFERENCES media_items (id) ON DELETE CASCADE,

    -- The canonical Jellyfin image type, lowercased: 'primary', 'backdrop',
    -- 'logo'. Lowercase because this is Reelix's own spelling; the capitalised
    -- form clients expect is decided at the compatibility boundary, the same
    -- division as DisplayTitle and ChannelLayout.
    image_type    text        NOT NULL CHECK (image_type <> ''),

    -- NULL PATH MEANS THE PROVIDER HAS NO SUCH IMAGE, and that is the whole
    -- reason this column is nullable.
    --
    -- Three states have to be distinguishable, or the pass either re-requests
    -- forever or gives up permanently:
    --
    --   no row      never attempted, or the attempt failed  -> retry
    --   path set    we have it                              -> skip
    --   path NULL   the provider says there is none         -> skip
    --
    -- A failed download writes NOTHING, so absence is the retry queue and no
    -- attempt counter or backoff column is needed. A negative result is a fact
    -- worth recording, because most films have no logo and re-asking about
    -- every one of them on every pass is the cost this row avoids. It is the
    -- same distinction the fields slice draws between an absent value and an
    -- empty one: only the first is honest about not knowing.
    --
    -- The path is RELATIVE to the cache directory, so moving or remounting the
    -- cache does not invalidate every row.
    storage_path  text        CHECK (storage_path IS NULL OR storage_path <> ''),

    -- The 32-lowercase-hex tag a client uses to build an image URL and to
    -- cache-bust. NULL exactly when storage_path is; see the CHECK below.
    --
    -- Its VALUE is ours to choose: the recorded reference tags are opaque
    -- 32-hex digests and nothing about their derivation is observable, so
    -- Reelix uses the first 32 hex of the SHA-256 of the file content. That
    -- satisfies the observed contract — stable while the image is unchanged,
    -- different when it changes, so cache-busting is correct by construction —
    -- and it is computed from the download stream at no extra cost.
    image_tag     text        CHECK (image_tag IS NULL OR image_tag ~ '^[0-9a-f]{32}$'),

    content_type  text        CHECK (content_type IS NULL OR content_type <> ''),

    -- Pixel dimensions, for PrimaryImageAspectRatio. From the provider's own
    -- image listing, so no decoding is needed to answer it.
    width         integer     CHECK (width IS NULL OR width > 0),
    height        integer     CHECK (height IS NULL OR height > 0),

    -- Where the bytes came from. Kept so a re-download needs no second provider
    -- request, and so an operator can see what was fetched.
    source_url    text,

    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL,

    PRIMARY KEY (media_item_id, image_type),

    -- A stored image has all four, a negative result has none. Half a row would
    -- mean a tag advertised for bytes nobody can serve, which is precisely the
    -- failure this slice exists to avoid.
    CONSTRAINT media_item_images_complete_or_absent CHECK (
        (storage_path IS NULL AND image_tag IS NULL AND content_type IS NULL)
        OR (storage_path IS NOT NULL AND image_tag IS NOT NULL AND content_type IS NOT NULL)
    )
);


-- NO PROVENANCE COLUMNS HERE, DELIBERATELY.
--
-- Source and Locked for an image live in media_item_field_provenance under
-- 'image_primary', 'image_backdrop' and 'image_logo', beside the scalar fields.
-- That table's `field` is free text with a CHECK, so three new names need no
-- migration.
--
-- The point is not to save columns. It is that every image write goes through
-- the SAME claimField as every field write, so there is one lock guard in the
-- system and one line to delete to make its tests fail. A second lock check
-- built for images would be two mechanisms guaranteeing one outcome, which is
-- the redundant-enforcement pattern the fields slice found and collapsed:
-- removing either alone changes nothing observable, so neither can be tested.
