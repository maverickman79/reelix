-- How a match was reached: the film's primary title, or an alternative one.
--
-- This column exists to keep an argument honest rather than to drive any
-- behaviour, and that is worth stating because nothing branches on it.
--
-- Until now the evidence that the matcher's threshold was set correctly was
-- the HAND-RESOLVED LIST: the films a pass declined and a person had to
-- identify manually. One film in six on the first real library, and it was a
-- renamed release. A short list means the threshold is right; a long one would
-- mean it is set wrong.
--
-- Alternative-title matching empties that list. The one film on it now matches
-- automatically, which is the point of the change and also the problem: the
-- measurement that justified the threshold disappears along with the failure
-- it was measuring. Without a replacement, "we decline rather than guess"
-- becomes an assertion nobody can check, and the first person looking at a
-- hundred unmatched films in a larger library has no way to tell a working
-- threshold from a broken one.
--
-- matched_via is the replacement signal. A film matched via an alternative
-- title is one the old matcher would have declined, so counting them answers
-- the same question the hand-resolved list used to answer, without anybody
-- having to do the work by hand to generate the evidence.
--
--   SELECT matched_via, count(*) FROM media_item_identity
--    WHERE status = 'matched' GROUP BY matched_via;
--
-- NULL for rows written before this migration, and for anything not matched.
-- Backfilling would mean re-running every identification to learn something
-- about films already correctly identified, which is not worth a library-wide
-- pass; the counts are read forward from here.

ALTER TABLE media_item_identity
    ADD COLUMN matched_via text
        CHECK (matched_via IS NULL OR matched_via IN ('primary', 'alternative'));
