-- Per-track metadata the 0.0.1 schema dropped.
--
-- ffprobe reported every one of these from the beginning; media_streams had
-- nowhere to put them, so Fight Club's 57 subtitle tracks arrived at a client
-- as 57 unlabelled entries. These columns are what makes a track selectable
-- by name.
--
-- The first seven are nullable because absence is a real answer: a track with
-- no language tag has no language, and a default would erase the difference
-- between "unknown" and "measured". The three dispositions are the opposite —
-- ffprobe always reports the disposition object, so "not flagged" is a fact
-- and NOT NULL DEFAULT false records it as one.

ALTER TABLE media_streams
    -- The container's tag verbatim, including the literal 'und'. Normalising
    -- that to null would discard the difference between a track tagged
    -- undefined and a track with no tag at all.
    ADD COLUMN language     text,
    -- The track's own name: 'SDH', 'Commentary', 'Latin American'. This is
    -- the field that separates two English subtitle tracks from each other.
    ADD COLUMN title        text,
    ADD COLUMN profile      text,
    -- Video only in practice. ffprobe's -99 and 0 sentinels are mapped to
    -- null before they reach this column.
    ADD COLUMN level        integer,
    ADD COLUMN pixel_format text,

    -- Two rates, because ffprobe reports two different things: the
    -- container's base rate and the measured average. They agree on
    -- constant-frame-rate content and diverge on variable, so storing one
    -- and deriving the other would discard a measurement that cannot be
    -- recovered without re-probing.
    ADD COLUMN avg_frame_rate  double precision,
    ADD COLUMN real_frame_rate double precision,

    ADD COLUMN is_default          boolean NOT NULL DEFAULT false,
    ADD COLUMN is_forced           boolean NOT NULL DEFAULT false,
    -- Carried alongside default and forced because it is the only thing that
    -- distinguishes an SDH track from an ordinary one in a picker where both
    -- read 'English'.
    ADD COLUMN is_hearing_impaired boolean NOT NULL DEFAULT false;


-- Force a re-probe of every existing file.
--
-- The new columns are empty for every row already in the table, and no
-- incremental signal can discover that: the files on disk have not changed,
-- so neither size nor mtime would trigger anything. probed_at is the existing
-- "needs probing" flag, and clearing it makes the next scan re-probe the
-- library through the ordinary path rather than through a one-off mechanism.
--
-- Deliberately not a full reset. Each file is probed in its own transaction,
-- so an interrupted scan resumes at the file it stopped on. Until the scan
-- runs, every file keeps its container, duration and existing stream rows:
-- browsing and playback are unaffected, and nothing is deleted until the
-- replacement streams are ready to be written in the same transaction.
UPDATE media_files SET probed_at = NULL;
