-- Reelix initial schema.
--
-- Scope is the 0.0.1 vertical slice: users and libraries for the native admin
-- API, and the media tables the scanner populates. Auth tokens, devices,
-- playback sessions, jobs, external IDs, artwork, people, and collections are
-- deliberately absent — each arrives in the migration belonging to the step
-- that first reads it.
--
-- Applied migrations are immutable. The runner refuses to start if this file's
-- checksum stops matching what the database recorded. Change the schema by
-- adding 0002_*.sql, never by editing this file.
--
-- All id columns are uuid generated in Go with uuid.NewV7(). There is no
-- column default: PostgreSQL 17 has no built-in v7 generator, and generating
-- application-side means the id is known before the INSERT. v7 is
-- time-ordered, which gives inserts B-tree locality during a library scan.


-- Accounts. 0.0.1 needs exactly one, the administrator created on first run.
CREATE TABLE users (
    id            uuid        PRIMARY KEY,
    username      text        NOT NULL,
    password_hash text        NOT NULL,
    is_admin      boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL
);

-- Usernames are compared case-insensitively. A functional unique index avoids
-- requiring the citext extension, which would have to be installed in every
-- deployment's database before the first migration could run.
CREATE UNIQUE INDEX users_username_lower_key ON users (lower(username));


-- A library is a logical collection, not a directory. See library_paths.
CREATE TABLE libraries (
    id         uuid        PRIMARY KEY,
    name       text        NOT NULL,
    -- One legal value in 0.0.1. The discriminator exists now because adding
    -- one to a populated table later is more disruptive than carrying a
    -- single-value constraint.
    kind       text        NOT NULL CHECK (kind IN ('movie')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);


-- Filesystem locations belonging to a library.
--
-- Separate table by constitutional requirement: a library may eventually span
-- several paths, and "do not hard-code assumptions that one library equals one
-- filesystem folder" is a direct instruction. 0.0.1 will only ever write one
-- row per library.
CREATE TABLE library_paths (
    id         uuid        PRIMARY KEY,
    library_id uuid        NOT NULL REFERENCES libraries (id) ON DELETE CASCADE,
    path       text        NOT NULL,
    created_at timestamptz NOT NULL,
    -- Also serves lookups by library_id, being the leading column.
    UNIQUE (library_id, path)
);


-- A logical piece of media: one movie, regardless of how many files back it.
CREATE TABLE media_items (
    id         uuid        PRIMARY KEY,
    library_id uuid        NOT NULL REFERENCES libraries (id) ON DELETE CASCADE,
    kind       text        NOT NULL CHECK (kind IN ('movie')),
    -- Parsed from the filename. 0.0.1's parser is deliberately minimal and a
    -- bad parse is acceptable, so this is whatever the parser produced.
    title      text        NOT NULL,
    -- Nullable: the year is frequently absent from a filename.
    year       integer,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE INDEX media_items_library_id_idx ON media_items (library_id);


-- A physical file on disk.
--
-- Separate from media_items by constitutional requirement: alternate editions,
-- multiple resolutions, and multi-part media all mean Movie != File.
CREATE TABLE media_files (
    id               uuid        PRIMARY KEY,
    media_item_id    uuid        NOT NULL REFERENCES media_items (id) ON DELETE CASCADE,
    -- Absolute path. Unique so a re-scan updates the existing row rather than
    -- inserting a duplicate.
    path             text        NOT NULL UNIQUE,
    -- The raw filename, kept alongside the parsed title as 0.0.1 requires.
    filename         text        NOT NULL,
    -- bigint, not integer. The capture recorded a range request at offset
    -- 5255045235 against a 70GB remux; the largest test file exceeds int32 on
    -- its own. Every byte quantity in this schema is 64-bit.
    size_bytes       bigint      NOT NULL,
    -- The remaining columns are ffprobe output, so they are null until the
    -- file has been probed.
    container        text,
    duration_seconds double precision,
    probed_at        timestamptz,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL
);

CREATE INDEX media_files_media_item_id_idx ON media_files (media_item_id);


-- One track within a file, as reported by ffprobe.
--
-- 'subtitle' is permitted because ffprobe reports embedded subtitle tracks and
-- the schema records what is on disk. 0.0.1 excludes subtitle downloading and
-- burn-in; it does not exclude knowing a track exists, and a CHECK that
-- rejected them would force the scanner to error or discard probe output.
CREATE TABLE media_streams (
    id            uuid    PRIMARY KEY,
    media_file_id uuid    NOT NULL REFERENCES media_files (id) ON DELETE CASCADE,
    -- ffprobe's own stream index within the container.
    stream_index  integer NOT NULL,
    kind          text    NOT NULL CHECK (kind IN ('video', 'audio', 'subtitle')),
    codec         text,
    -- Video only.
    width         integer,
    height        integer,
    -- Audio only.
    channels      integer,
    bit_rate      bigint,
    -- Also serves lookups by media_file_id, being the leading column.
    UNIQUE (media_file_id, stream_index)
);
