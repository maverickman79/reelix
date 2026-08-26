-- Server identity and native session/device records.
--
-- Both exist because the Jellyfin compatibility layer needs them, but neither
-- is Jellyfin-shaped: the constitution requires compatibility structures to be
-- translated at the boundary, never persisted.


-- The server's own identity.
--
-- Every Jellyfin client caches the server id and treats a change as a
-- different server, so this must survive restarts and redeploys. Jellyfin
-- persists the same value for the same reason.
--
-- Single row, enforced by the primary key check rather than by convention.
CREATE TABLE server_settings (
    id          smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    -- 32 lowercase hex characters, the form clients expect. Stored in that
    -- form because it is an opaque identity, not an entity id: nothing joins
    -- on it, and converting at every read would be pure ceremony.
    server_id   text        NOT NULL,
    server_name text        NOT NULL,
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);

-- Seeded here rather than at startup so the identity exists from the moment
-- the schema does, and cannot be raced by two starting containers.
-- gen_random_uuid() is built in from PostgreSQL 13; no pgcrypto needed.
INSERT INTO server_settings (id, server_id, server_name, created_at, updated_at)
VALUES (1, replace(gen_random_uuid()::text, '-', ''), 'Reelix', now(), now());


-- A client session bound to a device.
--
-- Deliberately NOT api_tokens. The constitution requires the native API's
-- authentication and the Jellyfin compatibility layer's to stay independent;
-- sharing a table would couple two schemes that must be free to diverge, and
-- would be a security boundary defined by a column value rather than by
-- structure.
--
-- These are the native facts about a session. The Jellyfin SessionInfo DTO is
-- assembled from them at the boundary and never stored.
CREATE TABLE sessions (
    id             uuid        PRIMARY KEY,
    user_id        uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- SHA-256 of the issued token. As with api_tokens, the token itself is
    -- never stored: a database disclosure must not hand over live credentials.
    token_hash     text        NOT NULL UNIQUE,

    -- Device identity, as reported by the client's authorization header.
    device_id      text        NOT NULL,
    device_name    text        NOT NULL,
    client         text        NOT NULL,
    client_version text        NOT NULL,

    -- Capabilities, reported separately by POST /Sessions/Capabilities after
    -- authentication. Defaulted so a session is valid before that call lands.
    playable_media_types           text[]  NOT NULL DEFAULT '{}',
    supported_commands             text[]  NOT NULL DEFAULT '{}',
    supports_media_control         boolean NOT NULL DEFAULT false,
    supports_persistent_identifier boolean NOT NULL DEFAULT false,

    created_at       timestamptz NOT NULL,
    last_activity_at timestamptz NOT NULL
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- One session per user per device. Re-authenticating from the same device
-- replaces its session rather than accumulating a new row on every app start,
-- which is how a long-lived TV client would otherwise grow the table without
-- bound.
CREATE UNIQUE INDEX sessions_user_device_key ON sessions (user_id, device_id);
