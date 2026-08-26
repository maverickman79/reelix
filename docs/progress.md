# Progress Log

Append-only. Newest entry at the top. Every Claude Code session adds an entry
before finishing.

Entry format:

```markdown
## YYYY-MM-DD — short title

**Completed:**
**In flight:**
**Blocked:**
**Next step:**
**Decisions made:**
```

Keep entries short. This file is read at the start of every session, so it must
stay scannable. Prune entries older than the current minor version into
`docs/archive/`.

---

## 2026-08-26 — Step 0 complete: Wholphin capture

**Completed:**
- Reference stack captured: Wholphin 1.0.7 on Android TV (Ugoos SK1) against
  `jellyfin/jellyfin:10.11.8`, proxied through mitmproxy. Full flow —
  discovery, login, library browse, movie select, direct play, seek, stop.
- 31 routes, 202 redacted fixtures landed in
  `internal/compat/jellyfin/testdata`, one directory per route.
- Test library is 6 files, chosen to exercise the paths that break: H.264 MP4,
  H.264 MKV, x264 BluRay, a filename containing spaces and brackets, HEVC
  2160p, and a 70GB remux.
- *Idiocracy* direct-played on the SK1. That is the reference decision Step 7
  must reproduce.

**Findings that constrain implementation:**
- Every range request is open-ended (`bytes=N-`). No multi-range, no suffix
  ranges. Max observed offset 5255045235, which exceeds int32 — the stream
  path must be 64-bit end to end, including any intermediate arithmetic.
- Wholphin opens a new TCP connection per request rather than relying on
  keep-alive. Do not assume connection reuse when reasoning about session or
  per-connection state.
- `/socket` is opened once and held for the session, not polled.
- QuickConnect is probed during login (`GET /QuickConnect/Enabled` and
  `POST /QuickConnect/Initiate`). Both must be answered, even if only to
  report the feature disabled.
- Client is jellyfin-sdk-kotlin over OkHttp: deserialization is strict, so a
  missing non-nullable field is a hard client-side exception, not degradation.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Next step:**
- Step 2: migration tooling, initial schema, repository layer.
- Supersedes the "Do not begin Step 1 until the capture exists" line in the
  2026-08-24 entry. Steps 0 and 1 are both complete; that gate is historical.

**Decisions made:**
- All published container ports are now bound to the Tailscale IP rather than
  `0.0.0.0`. An earlier capture run through a reverse proxy exposed 8096 and
  8097 publicly and drew vulnerability scanners within the hour. That capture
  was discarded and retaken direct.
- Raw captures under `hack/capture/captures/` are gitignored: they contain live
  tokens and the reference admin password. Only redacted fixtures are
  committed.

## 2026-08-25 — Step 1: repo skeleton

**Completed:**
- Go layout: `cmd/reelixd`, `internal/config`, `internal/logging`,
  `internal/server`. No third-party dependencies.
- Config from environment (`REELIX_` prefix), validated at startup; all
  problems reported together rather than one per restart.
- `log/slog` structured logging, json/text, constitution field vocabulary.
  Request middleware assigns a UUIDv7 `request_id` and echoes it as
  `X-Request-ID`.
- `GET /health` → `200 {"status":"ok","version":"0.0.1"}`. No dependency
  checks; a stalled database must not make the container look dead.
- Multi-stage Dockerfile: `golang:1.27-bookworm` build, `debian:bookworm-slim`
  runtime, non-root uid/gid 1000, 145MB.
- Root `docker-compose.yml` (project name `reelix`) with app + PostgreSQL 17,
  named volumes, healthchecks, `.env.example`.

**Verified:**
- `gofmt`, `go vet ./...`, `go test ./...` clean. 12 tests across config and
  server.
- `docker compose up -d --build` from clean (`down -v` first): both containers
  healthy, `/health` returns 200, both logs free of errors and warnings,
  graceful shutdown on SIGTERM.
- Database is `en_US.utf8` under the libc provider on Debian 17.11. Accent sort
  smoke test gives glibc dictionary order
  (`Ámbar | Amelie | Amélie | Ångström | École | Zulu`), not musl byte order.
- `pg_hba.conf` contains no `trust` line; an unauthenticated local socket
  connection is rejected.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Next step:**
- Step 0 has still not been run — `hack/capture/` has no `captures/`,
  `ref-config/`, or `test-media/`. Step 1 contained no compatibility code so
  this did not block it, but Step 5 onward is gated on those fixtures.
- Otherwise Step 2: migration tooling, initial schema, repository layer.

**Decisions made:**
- Binary and package: `cmd/reelixd`, image `reelix/server`.
- App listens on 8080 — matches the swap-test target already written into
  `hack/capture/docker-compose.yml`.
- Runtime base is `debian:bookworm-slim`, not scratch/distroless: Step 4 shells
  out to `jellyfin-ffmpeg7`, which needs apt, shared libs, and eventually
  driver packages. Choosing minimal now would mean rebuilding the Dockerfile.
- Postgres is `postgres:17` (Debian), not `17-alpine`. musl collation differs
  from glibc for text sorting and the library is full of accented and non-Latin
  titles. This is not reversible after data exists without a reindex.
- `POSTGRES_INITDB_ARGS=--auth-local=scram-sha-256 --auth-host=scram-sha-256`
  replaces the image's local-socket `trust` default.
- `/health` does not touch Postgres in Step 1. A liveness check that fails on a
  slow database causes restart loops; a `checks` object is added in Step 2 when
  there is a driver and something real to check.
- No Postgres driver yet. `REELIX_DB_*` is loaded and validated but unused; the
  driver is a dependency decision belonging with the repository layer.
- `/health` access logs are emitted at debug. The compose healthcheck polls
  every 10s and at info that traffic buries everything operationally useful.
- `X-Forwarded-For` deliberately not honoured — no trusted-proxy config exists,
  and an unvalidated forwarded header is spoofable.
- Access logs record the path but never the query string: Jellyfin clients pass
  tokens as query parameters.
- Postgres publishes no host port; only the app container reaches it.
- This file is now ordered newest-first, matching its own header. Earlier
  entries had been appended chronologically.

## 2026-08-25 — Toolchain confirmed

**Decisions made:**
- Go 1.27.0 installed. Verified via `go doc` on the box: stdlib now provides a
  top-level `uuid` package (RFC 9562) and `encoding/json/v2`. Reelix takes
  neither `github.com/google/uuid` nor any third-party JSON library.
- Entity IDs use `uuid.NewV7()`, not `NewV4()` — time-ordered UUIDs sort by
  creation time, giving sequential B-tree locality on insert-heavy library
  scans. The compat layer still serializes these as 32-char dashless hex.
- Note: stdlib `uuid` has no v1/v3/v5/v6/v8 constructors and no version/time
  introspection. Reelix does not need them.
- Claude Code moved from npm-global to native install (~/.local/bin), 2.1.245.

**Next step:**
- Step 1: repo skeleton.

## 2026-08-24 — Project bootstrapped

**Completed:**
- Project constitution written (`docs/constitution.md`)
- 0.0.1 scope and success criteria defined (`docs/mvp-0.0.1.md`)
- Compatibility capture harness designed (`docs/compat-capture.md`)
- Operating rules established (`CLAUDE.md`)

**In flight:**
- Nothing yet. No code written.

**Blocked:**
- Nothing.

**Next step:**
- Step 0 of the build order: stand up reference Jellyfin 10.11.8 + mitmproxy,
  run the Wholphin capture flow, produce redacted fixtures.
- Do not begin Step 1 until the capture exists.

**Decisions made:**
- Name: Reelix. Module `github.com/maverickman79/reelix`. License AGPL-3.0.
- Not a fork. Clean-room rule: compatibility from the wire only, never from
  Jellyfin server source.
- Backend Go + PostgreSQL. Frontend framework deliberately undecided.
- FFmpeg: use `jellyfin-ffmpeg7` binaries, shelled out, never linked.
- Jellyfin API target pinned at 10.11.x.
- Primary client / success gate: Wholphin (Android TV, open source, Jellyfin
  Kotlin SDK). Secondary: VidHub. Wholphin chosen over VidHub as primary
  because its source is readable and its SDK-generated calls are predictable.
- 0.0.1 explicitly excludes the admin web frontend — driven by `curl` only.
