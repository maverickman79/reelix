-- Per-user, per-item playback position and played status.
--
-- This is Reelix's own model, not a Jellyfin one: the compatibility layer
-- translates it into UserData at the boundary and never persists that shape.
-- Positions are seconds, matching media_files.duration_seconds; the .NET tick
-- is a unit of the compatibility surface and stops there.

CREATE TABLE playback_state (
    user_id       uuid NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
    media_item_id uuid NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,

    -- The resume position: where playback should start, or 0 for an item
    -- that is not in progress. Written already judged against the resume
    -- thresholds, so every read path is a plain comparison and the resume
    -- list is an index scan rather than a calculation.
    position_seconds double precision NOT NULL DEFAULT 0
        CHECK (position_seconds >= 0),

    -- What the client actually reported, unjudged.
    --
    -- The thresholds above it are a policy that will change once real people
    -- use this. Keeping the raw number means lowering the lower bound later
    -- brings back everyone who stopped just under it, rather than having
    -- discarded them at write time. One column now; unrecoverable later.
    raw_position_seconds double precision NOT NULL DEFAULT 0
        CHECK (raw_position_seconds >= 0),

    -- Sticky: nothing un-marks a watched item. A rewatch may carry a resume
    -- position alongside it, which is what a client expects to see.
    played boolean NOT NULL DEFAULT false,

    -- Incremented when a completed playback ends, not when one begins: a
    -- fifteen-second sample is not a viewing, and "watched 3 times" should
    -- mean watched.
    play_count integer NOT NULL DEFAULT 0 CHECK (play_count >= 0),

    last_played_at timestamptz,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,

    -- One row per user per item. The pair is the identity, so a report is an
    -- upsert on the primary key rather than a lookup and a branch.
    PRIMARY KEY (user_id, media_item_id)
);

-- The resume list: one user's in-progress items, most recent first.
--
-- Partial, because rows with no resume position are the overwhelming
-- majority once a library has been watched through, and none of them can
-- ever appear in this answer.
CREATE INDEX playback_state_resume_idx
    ON playback_state (user_id, last_played_at DESC)
    WHERE position_seconds > 0;
