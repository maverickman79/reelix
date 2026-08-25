# Reelix 0.0.1 — First Vertical Slice

## The one goal

> **Wholphin on Android TV can discover Reelix, authenticate, browse one local
> movie library, select a movie, and direct-play it — including seeking.**

Nothing beyond this is necessary to declare the first vertical slice
successful.

---

## Success criteria

0.0.1 succeeds when all of the following are true, in order:

1. A clean machine can start Reelix with `docker compose up -d`.
2. An administrator account can be created on first run.
3. An administrator can configure one movie library with one filesystem path.
4. The server scans the mounted directory and discovers video files.
5. Each file is probed with `ffprobe` and stored in PostgreSQL.
6. Wholphin can add the server by address and completes discovery.
7. Wholphin authenticates with the administrator credentials.
8. Wholphin displays the movie library and its items.
9. The user selects a movie and the detail view renders without error.
10. Playback begins, the original file is streamed without transcoding, and
    **seeking forward and backward works**.
11. A playback session is recorded and visible in the server logs.

Criterion 10 includes seeking deliberately. A direct-play endpoint that streams
from byte zero but mishandles range requests will pass a naive smoke test and
fail in real use.

---

## In scope

- Docker Compose deployment (app + Postgres)
- Go API service
- PostgreSQL schema with versioned migrations
- Initial administrator creation
- Minimal native admin API (`/api/v1/*`) sufficient to create a library and
  trigger a scan
- One movie library, one filesystem path
- Filesystem scan of supported video container extensions
- `ffprobe` media probing (container, duration, video/audio stream basics)
- Persistence of discovered media
- Minimal movie listing
- Jellyfin-compatible server discovery
- Jellyfin-compatible authentication
- Jellyfin-compatible item/library responses sufficient for Wholphin
- Direct-play video endpoint with correct HTTP range support
- Basic playback-session logging

---

## Explicitly excluded

Do not implement these during 0.0.1 unless explicitly instructed:

transcoding · TV-series libraries · metadata scraping · Redis · plugins ·
recommendations · custom home rows · advanced user permissions · groups ·
hardware acceleration · distributed workers · intro detection · trickplay ·
chapters · subtitle downloads · subtitle burn-in · Plex compatibility ·
admin web frontend · mobile applications · TV applications · SyncPlay ·
Live TV / DVR · music · photos · collections · people/cast · artwork
downloading · multi-path libraries · library monitoring / filesystem watching

These exclusions are intentional. The admin frontend in particular is excluded:
0.0.1 is driven by the native API via `curl` or an HTTP client.

---

## Filename parsing scope

Minimal. Movie title and year from the filename or its parent directory, in the
common form `Title (Year)`. Store the raw filename alongside the parsed result.

Do not build a general-purpose release-name parser. Do not attempt edition
detection, resolution parsing, or scene-name handling. Titles that parse badly
are acceptable in 0.0.1 — the media just has to appear and play.

---

## Suggested build order

Each step ends in something demonstrably working. Do not start a step before
the previous one is verified.

**Step 0 — Capture before code.**
Stand up real Jellyfin 10.11.x, point Wholphin at it, run the entire flow above,
capture every request and response. See `docs/compat-capture.md`. This produces
the fixture set everything else is validated against. Nothing in the
compatibility layer should be written before this exists.

**Step 1 — Skeleton.**
Repo, Go module, Dockerfile, compose file with Postgres, health endpoint,
structured logging, config loading, `.env.example`. Verify: `docker compose up`
gives a responding health endpoint.

**Step 2 — Persistence.**
Migration tooling, initial schema, repository layer. Verify: migrations apply
cleanly on an empty database and are idempotent.

**Step 3 — Native API: users and libraries.**
First-run administrator creation, password hashing, native auth, create library,
list libraries. Verify: `curl` can create an admin and a library.

**Step 4 — Scanner and probe.**
Walk the library path, identify video files, run `ffprobe`, persist media items
and files and streams. Runs as a background job with observable state. Verify:
scanning a test directory populates Postgres.

**Step 5 — Compatibility: discovery and auth.**
`/System/Info`, `/System/Info/Public`, authorization header parsing,
authenticate-by-name, token issuance, `/Users/Me`. Verify against Step 0
fixtures, then verify Wholphin can add the server and log in.

**Step 6 — Compatibility: browse.**
User views, library items, item detail. Dashless hex IDs. Well-formed empty
responses for artwork and anything unimplemented. Accept the `/socket`
WebSocket and keep it open. Verify: Wholphin shows the library and opens a
movie.

**Step 7 — Direct play.**
`PlaybackInfo` returning a direct-play decision, the static stream endpoint via
`http.ServeContent`, session logging. Verify: playback starts and seeking works
on real hardware.

---

## Test devices

Wholphin runs on the Shield Pro and the Ugoos SK1, both reachable over ADB.
`adb logcat` is the primary diagnostic channel — because Wholphin is built on
the Jellyfin Kotlin SDK, malformed or incomplete responses tend to surface as
Kotlin deserialization exceptions with usable stack traces rather than silent
failures.

When a failure is ambiguous, run the same request against the reference Jellyfin
instance from Step 0 and diff the responses. That is faster than reasoning about
it.

---

## Definition of done

All eleven success criteria pass on a clean `docker compose up -d` against a
freshly created database, with Wholphin on physical hardware.

Tag `v0.0.1`. Write the retrospective into `docs/progress.md` before starting
0.0.2.
