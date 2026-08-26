-- Native API authentication tokens.
--
-- Deferred from 0001 on purpose: tables arrive with the step that first reads
-- them. This is that step.
--
-- These are Reelix's own tokens for /api/v1. The Jellyfin compatibility layer
-- authenticates differently and will not reuse this table — the constitution
-- requires the two schemes to stay independent.
--
-- token_hash holds SHA-256, not argon2. The token is 32 bytes from a CSPRNG,
-- so it has no guessable structure and a deliberately slow hash buys nothing;
-- running argon2 on every authenticated request would cost 64MiB and tens of
-- milliseconds per call. Passwords are the opposite case and are hashed with
-- argon2id.
CREATE TABLE api_tokens (
    id         uuid        PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Hex-encoded SHA-256 of the presented token. The token itself is never
    -- stored: a database disclosure must not hand over live credentials.
    token_hash text        NOT NULL UNIQUE,
    created_at timestamptz NOT NULL,
    -- Nothing deletes expired rows. Every lookup filters on this column, so an
    -- expired token is rejected while its row is still present.
    expires_at timestamptz NOT NULL
);

CREATE INDEX api_tokens_user_id_idx ON api_tokens (user_id);
