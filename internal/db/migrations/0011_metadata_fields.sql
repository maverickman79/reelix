-- Managed metadata fields, with per-field provenance.
--
-- The constitution's model is Value / Source / Locked for every managed field.
-- That is three tables here rather than one, and the split is deliberate.


-- The values, in TYPED columns.
--
-- Not an entity-attribute-value table with a `value text`, even though EAV
-- would express "any field, any source" in one shape. A rating that cannot be
-- constrained to a number is a rating that eventually holds '8.4/10', and a
-- release date that is text is a date nothing can sort. The same reasoning
-- that kept external ids out of a JSONB blob applies to their values.
--
-- SEPARATE FROM media_items, which is the important boundary. media_items
-- holds what the SCANNER derived: the title and year parsed from the filename.
-- Those are the matcher's input, so a provider value overwriting them in place
-- would mean re-identification silently changing its own input on every run.
-- Provider values live beside scanner values and the compatibility layer picks
-- between them, the same division as DisplayTitle and ChannelLayout.
--
-- No runtime column. Jellyfin's RunTimeTicks drives the seek bar and must
-- describe the file being played, which ffprobe already measured; the
-- provider's runtime describes the work. See FetchMetadata in the TMDB
-- provider for why the two must not be conflated.
CREATE TABLE media_item_metadata (
    media_item_id    uuid        PRIMARY KEY REFERENCES media_items (id) ON DELETE CASCADE,

    overview         text,

    -- Jellyfin's 0-10 scale. Nullable because a film nobody has rated is not
    -- a film everybody hated, and a client renders 0 as zero stars.
    community_rating numeric(4, 2) CHECK (community_rating IS NULL
                                          OR (community_rating >= 0 AND community_rating <= 10)),

    -- The certification for the configured region, e.g. 'R', '15', 'FSK 16'.
    -- Empty region coverage yields NULL and is never filled from another
    -- region; see officialRating in the TMDB provider.
    official_rating  text,

    premiere_date    date,

    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL
);


-- Where each field came from, and whether a person has pinned it.
--
-- One row per managed field per item, rather than a source/locked column pair
-- beside every value column. A pair per value would mean three columns per
-- field and a migration every time a field is added; this shape adds a row.
--
-- source is the provider name ('tmdb') or 'manual'. There is one provider
-- today, so nothing arbitrates between sources yet — the column exists because
-- the question "where did this value come from" has to be answerable before a
-- second provider makes it interesting, not after.
CREATE TABLE media_item_field_provenance (
    media_item_id uuid        NOT NULL REFERENCES media_items (id) ON DELETE CASCADE,

    -- The field name as the API and the repository know it: 'overview',
    -- 'community_rating', 'official_rating', 'premiere_date', 'genres'.
    -- Deliberately not a foreign key to a field catalogue: the set is small,
    -- known at compile time, and validated in Go where the field names are
    -- already spelled out.
    field         text        NOT NULL CHECK (field <> ''),

    source        text        NOT NULL CHECK (source <> ''),

    -- A refresh must not silently overwrite a locked field. Enforced in the
    -- WHERE clause of the write rather than by a caller reading first, so a
    -- refresh running concurrently with someone editing cannot lose the edit.
    locked        boolean     NOT NULL DEFAULT false,

    updated_at    timestamptz NOT NULL,

    PRIMARY KEY (media_item_id, field)
);

-- The refresh pass asks "which of this item's fields may I write", so the
-- primary key already serves it. This index serves the opposite question, the
-- one an operator asks: what has been pinned by hand across the library.
CREATE INDEX media_item_field_provenance_locked_idx
    ON media_item_field_provenance (media_item_id)
    WHERE locked;


-- Genres, ordered.
--
-- A table rather than a text[] column, because the order is meaningful —
-- providers list the primary genre first and clients show the first few — and
-- because a genre is a thing a library will eventually be browsed by. An array
-- cannot be joined against.
--
-- The list locks as a whole, under field = 'genres' in the provenance table.
-- Per-genre locking would mean deciding what a refresh does when it returns a
-- list overlapping a locked subset, which is a question nobody has asked.
CREATE TABLE media_item_genres (
    media_item_id uuid        NOT NULL REFERENCES media_items (id) ON DELETE CASCADE,
    genre         text        NOT NULL CHECK (genre <> ''),

    -- Provider order, zero-based.
    ordinal       integer     NOT NULL CHECK (ordinal >= 0),

    created_at    timestamptz NOT NULL,

    PRIMARY KEY (media_item_id, genre)
);

CREATE INDEX media_item_genres_genre_idx ON media_item_genres (genre);


-- Metadata refresh is a third kind of background job.
--
-- Its own kind rather than a flag on the identify job, for the reason identify
-- is not part of the scan: they fail independently and an operator needs to
-- run one without the other. A film can be identified but have stale fields,
-- and refetching fields for a library that is already identified must not
-- re-run identification.
ALTER TABLE jobs
    DROP CONSTRAINT jobs_kind_check;

ALTER TABLE jobs
    ADD CONSTRAINT jobs_kind_check
        CHECK (kind IN ('library_scan', 'library_identify', 'library_metadata'));
