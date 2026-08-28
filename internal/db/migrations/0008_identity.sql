-- Identity: which real film a media item is, and nothing about that film.
--
-- Two tables rather than columns on media_items, and rather than one table.
--
-- media_item_identity records the OUTCOME of identification: whether it has
-- been attempted, what decided it, and how confident that decision was. It is
-- one row per item, so it is a natural place for provenance.
--
-- media_item_external_ids records the IDS themselves, one row per provider,
-- because an item has a TMDB id and an IMDb id and will later have more. A
-- column per provider would need a migration for each; a JSONB blob would put
-- relational data somewhere it cannot be joined or constrained.
--
-- Nothing here stores a title, an overview, a rating, or an artwork path. This
-- migration is identity only: the thing artwork and the watch-history importer
-- both sit on. Fields hang off it later.


-- status is a THREE-state model, and that is the load-bearing decision.
--
-- "no tmdb id" would otherwise mean two different things at once — never
-- attempted, and attempted and found nothing — and the importer cannot behave
-- sensibly without telling them apart. It has to retry the first and leave the
-- second alone, and a null cannot say which is which.
--
--   pending   -- never attempted, or queued for another attempt
--   matched   -- a provider decided, with the confidence recorded
--   unmatched -- attempted, and deliberately declined to guess
--   manual    -- a human said so; no pass may overwrite this
--
-- unmatched is a success, not a failure. Reelix declines to guess when
-- candidates are ambiguous, because a wrong identity does not merely show the
-- wrong poster: it silently attaches someone's imported viewing history to the
-- wrong film, which is an error nobody reports because it looks like a mistake
-- they made themselves. Unmatched is visible and fixable; a bad match is
-- neither.
CREATE TABLE media_item_identity (
    media_item_id uuid        PRIMARY KEY REFERENCES media_items (id) ON DELETE CASCADE,
    status        text        NOT NULL CHECK (status IN ('pending', 'matched', 'unmatched', 'manual')),

    -- The provider that decided, e.g. 'tmdb'. Null while pending, and null for
    -- an unmatched item, because no provider claimed it.
    provider      text,

    -- How the decision was reached. Null unless status is matched.
    --   exact      -- title and year both agreed
    --   year_near  -- title agreed, year within one
    --   title_only -- title agreed, no year to check against
    confidence    text        CHECK (confidence IN ('exact', 'year_near', 'title_only')),

    -- Why an attempt produced nothing. Null unless status is unmatched.
    -- Free text for the operator, not a code anything branches on.
    reason        text,

    -- When identification last ran. Null while pending. This is what makes a
    -- re-run cheap: an item with a recent attempt need not be asked again.
    attempted_at  timestamptz,

    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL,

    -- A matched item must name what matched it. Without this a row could claim
    -- a match with no provider and no confidence, and the importer would trust
    -- it.
    CONSTRAINT identity_matched_is_attributed CHECK (
        status <> 'matched' OR (provider IS NOT NULL AND confidence IS NOT NULL)
    )
);

-- The identify pass reads "everything not yet decided", so that is the index.
CREATE INDEX media_item_identity_status_idx ON media_item_identity (status);


-- provider is the lowercase internal name -- 'tmdb', 'imdb'. The capitalised
-- spellings a Jellyfin client expects ("Tmdb", "Imdb" as ProviderIds keys;
-- "TMDB", "IMDb" as ExternalUrls display names -- three different spellings of
-- two providers in one response) are a fact about Jellyfin clients and belong
-- at the compatibility boundary, exactly like DisplayTitle and ChannelLayout.
--
-- external_id is text, not an integer, and that is not laziness: a TMDB id is
-- numeric but an IMDb id is "tt0137523", and the reference server sends both
-- as JSON strings. Storing the numeric one as an integer would mean converting
-- it back at every boundary to produce the string a client expects.
CREATE TABLE media_item_external_ids (
    media_item_id uuid        NOT NULL REFERENCES media_items (id) ON DELETE CASCADE,
    provider      text        NOT NULL,
    external_id   text        NOT NULL CHECK (external_id <> ''),
    created_at    timestamptz NOT NULL,

    PRIMARY KEY (media_item_id, provider)
);

-- The importer's query is the reverse of the browse query: given a provider
-- and an id from an export, find the local item. Without this index that is a
-- sequential scan per imported row.
CREATE INDEX media_item_external_ids_lookup_idx ON media_item_external_ids (provider, external_id);


-- Every existing item starts pending, so the first identify pass has something
-- to find. New items get their row when the scanner creates them.
--
-- Note what this migration does NOT do: it does not clear probed_at. Migrations
-- 6 and 7 did, because they added columns ffprobe fills and the only way to
-- fill them was to probe every file again. Identity needs no probe and no file
-- access at all -- it is a network operation over data already in the database,
-- and it runs as its own pass. Re-scanning here would cost a terabyte of reads
-- to collect nothing.
INSERT INTO media_item_identity (media_item_id, status, created_at, updated_at)
SELECT id, 'pending', now(), now() FROM media_items;
