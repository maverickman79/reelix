-- Two more per-track fields ffprobe already reports.
--
-- channel_layout is the one clients actually read. Reelix composes a complete
-- DisplayTitle, but Wholphin ignores it and builds its own label from the
-- individual fields; a null layout renders there as the literal string "null".
-- Findroid does use DisplayTitle, and separately matches channel_layout
-- against "2.0"/"2.1"/"5.1"/"7.1" to classify a track — where a null falls
-- through to the stereo arm and labels a 5.1 track as 2.0. A visible "null"
-- gets reported; a 5.1 track quietly described as stereo does not.
--
-- sample_rate is not implicated in either fault. It is here because the cost
-- of a migration is the re-scan, not the column, and that cost is identical
-- for one field or two. A real library is terabytes; re-probing it later to
-- collect a value ffprobe already returned, for free, in a pass that has to
-- happen anyway would be the expensive choice. Batching is prudent sequencing
-- here precisely because the field needs no new probe pass and no schema
-- decision — a field that needed either would not qualify.
--
-- Stored raw, qualifier included: ffprobe says "5.1(side)" for many files and
-- that is a fact about the container. What a client should see is a fact about
-- Jellyfin clients, and is decided at the compatibility boundary.

ALTER TABLE media_streams
    ADD COLUMN channel_layout text,
    ADD COLUMN sample_rate    integer;


-- Force a re-probe, exactly as migration 6 did and for the same reason: the
-- new columns are empty for every existing row, nothing on disk changes to
-- announce it, and probed_at is the flag the scanner already reads. Each file
-- is probed in its own transaction, so an interrupted scan resumes where it
-- stopped, and every file keeps its container, duration and existing streams
-- until the replacements are written.
UPDATE media_files SET probed_at = NULL;
