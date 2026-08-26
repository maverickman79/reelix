-- Background jobs, and a stable identity for media items.
--
-- Both changes arrive together because the library scan needs each: one to be
-- observable, one to be repeatable.


-- Long-running work with observable state.
--
-- The constitution requires background operations to be visible rather than
-- opaque: state, progress, the item currently being worked on, timings, and
-- the error if it failed. 0.0.1 has exactly one kind of job; the discriminator
-- exists because adding one to a populated table later is worse.
CREATE TABLE jobs (
    id               uuid        PRIMARY KEY,
    kind             text        NOT NULL CHECK (kind IN ('library_scan')),
    state            text        NOT NULL CHECK (state IN ('queued', 'running', 'completed', 'failed', 'cancelled')),
    -- Nullable because not every future job kind belongs to a library.
    library_id       uuid        REFERENCES libraries (id) ON DELETE CASCADE,
    -- Progress is files probed out of files discovered. total is 0 until the
    -- walk finishes, since the count is not known before then.
    progress_current integer     NOT NULL DEFAULT 0,
    progress_total   integer     NOT NULL DEFAULT 0,
    -- The file currently being probed, for an operator watching a long scan.
    current_item     text,
    -- Set only when state = 'failed'. Safe to show to an administrator; it
    -- never carries credentials or database internals.
    error            text,
    created_at       timestamptz NOT NULL,
    started_at       timestamptz,
    finished_at      timestamptz
);

CREATE INDEX jobs_library_id_idx ON jobs (library_id);
-- Listing recent jobs newest-first. id is UUIDv7, so it is already in
-- creation order and the index serves the sort directly.
CREATE INDEX jobs_recent_idx ON jobs (id DESC);

-- At most one scan in flight per library. Two concurrent scans of the same
-- path would race on the same media_files rows to no purpose, and the
-- constraint says so in the one place that cannot be bypassed.
CREATE UNIQUE INDEX jobs_one_active_per_library
    ON jobs (library_id)
    WHERE state IN ('queued', 'running');


-- Where a media item came from on disk.
--
-- media_items had no natural key. media_files.path de-duplicates files
-- correctly, but nothing identified "the item created for this movie last
-- time", so a second scan produced duplicate items pointing at the same files.
--
-- source_path is the movie's directory, or the file's own path when the file
-- sits directly in a library root. It is what makes a re-scan update rather
-- than duplicate.
ALTER TABLE media_items ADD COLUMN source_path text;

-- Existing rows predate the scanner and have no source path to backfill from;
-- 0.0.1 has never run a scan, so in practice there are none. Filling with the
-- id keeps the column NOT NULL and unique without inventing a plausible-
-- looking path that no scanner would ever match.
UPDATE media_items SET source_path = id::text WHERE source_path IS NULL;

ALTER TABLE media_items ALTER COLUMN source_path SET NOT NULL;

ALTER TABLE media_items ADD CONSTRAINT media_items_library_source_path_key
    UNIQUE (library_id, source_path);
