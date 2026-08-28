-- Identification runs as a background job, like a scan.
--
-- A separate pass rather than a step inside the scan, and that is the decision
-- worth recording. A scan is filesystem plus ffprobe: it is a local operation
-- that works with the network unplugged. Identification is a remote call to
-- somebody else's API. Folding one into the other would mean a library scan
-- fails when TMDB is down or rate-limits us — turning a local operation into a
-- remote one, and making an outage nobody here controls into a reason the
-- library cannot be re-scanned.
--
-- The same reasoning keeps TMDB reachability out of the startup checks.

ALTER TABLE jobs
    DROP CONSTRAINT jobs_kind_check;

ALTER TABLE jobs
    ADD CONSTRAINT jobs_kind_check CHECK (kind IN ('library_scan', 'library_identify'));


-- The active-job constraint now admits one job PER KIND per library, rather
-- than one job per library.
--
-- As written in migration 3 it forbade two concurrent scans of one library,
-- which is right and stays right: they would race on the same media_files rows
-- to no purpose. But it would also have forbidden identifying a library while
-- it was being scanned, which is a different pair of operations touching a
-- different pair of tables — the scan writes media_items and media_streams,
-- identification writes media_item_identity — and there is no reason to
-- serialise them. An item the scan adds mid-pass is simply picked up by the
-- next identify run, because it is created pending.
DROP INDEX jobs_one_active_per_library;

CREATE UNIQUE INDEX jobs_one_active_per_library_and_kind
    ON jobs (library_id, kind)
    WHERE state IN ('queued', 'running');
