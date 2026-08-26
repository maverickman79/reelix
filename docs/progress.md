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

## 2026-08-26 — Step 3: native API, users and libraries

**Completed:**
- `internal/auth`: argon2id password hashing in PHC string format
  (`$argon2id$v=19$m=65536,t=3,p=4$…`), constant-time verification, and
  native API tokens — 32 bytes of CSPRNG, `rlx_` prefix, SHA-256 at rest.
- `0002_api_tokens.sql`: `api_tokens` (id, user_id, token_hash, created_at,
  expires_at).
- `internal/service`: `AuthService` (first-run setup, login, token
  resolution) and `LibraryService` (create with paths, list). Services own
  their transaction boundaries.
- `internal/api/v1`: handlers, DTOs, bearer middleware, error mapping.
  Mounted at `/api/v1` by `internal/server`.
- `internal/logging` gained `WithLogger`/`FromContext`, so the API package
  reads the same request-scoped logger the HTTP middleware installs. The
  storage moved out of `internal/server`, which previously owned the key.
- Endpoints: `POST /setup`, `POST /auth/login`, `GET /me`,
  `POST /libraries`, `GET /libraries`.

**Verified:**
- `gofmt`, `go vet ./...` clean. 80 tests pass with a database; all
  integration tests skip cleanly without `REELIX_TEST_DB_DSN`.
- **Completion criteria, by curl against the compose stack:** created the
  first admin (201), refused a second (409), authenticated, rejected a wrong
  password (401), rejected an unauthenticated request (401), created a movie
  library with one path (201), listed it back (200).
- **Expired token with its row still present is rejected** — the row is left
  in place and only `expires_at` moved into the past; the request returns
  401. A companion test moves expiry one second into the future and asserts
  200, so the comparison cannot be inverted without a failure.
- Eight concurrent `POST /setup` requests produce exactly one administrator.
- App logs across the whole flow contain zero occurrences of the token, the
  password, or the password hash. No response body mentions a password field.
- The real deployment applied migration 2 on restart and stayed healthy.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Next step:**
- Step 4: scanner and probe. Walk the library path, identify video files, run
  `ffprobe`, persist items, files, and streams as a background job.

**Decisions made:**
- Dependency added: `golang.org/x/crypto` v0.55.0 for argon2id, pulling
  `golang.org/x/sys` as indirect. Nine modules total. PBKDF2 is in the stdlib
  now (`crypto/pbkdf2`) and would have been zero dependencies, but it is not
  memory-hard, which is the property that makes GPU and ASIC cracking
  expensive — the whole threat model for a stored password hash.
- **Argon2id at m=64MiB means each concurrent login holds 64MiB for the
  duration of the hash. Login concurrency is therefore bounded by the
  container's memory limit, not by CPU.** The unknown-user path costs the
  same, because it runs a dummy verification to equalise timing — so a
  credential-stuffing attempt against nonexistent usernames is exactly the
  shape of traffic that hits this hardest. Not a problem at 0.0.1's scale and
  not being solved now; recorded so it is not a surprise later. If it needs
  bounding, a semaphore around hashing is the answer, not weaker parameters.
- Tokens are hashed with SHA-256, not argon2. A 256-bit random token has no
  guessable structure, so a slow hash protects nothing, while paying argon2
  on every authenticated request would be a self-inflicted denial of service.
- **Token expiry is filtered inside the SQL query** (`expires_at > now()`),
  not compared after loading the row. Nothing deletes expired tokens — there
  is no sweeper and `DeleteExpired` is not scheduled — so their rows persist
  indefinitely and a lookup matching on `token_hash` alone would authenticate
  them. Filtering in SQL also means no call site can forget the check.
  PostgreSQL's `now()` is the transaction timestamp, so expiry is judged by
  the database's clock, not the application's.
- First-run setup takes `pg_advisory_xact_lock` and performs its count and
  insert in one transaction. The endpoint is necessarily unauthenticated, so
  "succeeds exactly once" is what stops an attacker racing the operator on a
  freshly started server.
- Unknown username and wrong password return an identical code and message,
  and the unknown-user path performs a dummy hash so the two take similar
  time. Neither the body nor the latency should identify which accounts exist.
- Password minimum is 12 runes — runes, not bytes, so a short multi-byte
  password cannot slip through. No composition rules: they push people toward
  predictable substitutions without adding entropy.
- Native API JSON is camelCase with RFC3339 timestamps and **dashed** UUIDs.
  The 32-character dashless form is a Jellyfin convention belonging solely to
  the compatibility layer.
- Request bodies are capped at 64KiB and reject unknown fields, so a
  misspelled key is an error rather than a silently ignored one.
- Collections are wrapped in an object (`{"libraries":[…]}`) rather than
  returned as a bare array, leaving room for pagination later. Empty
  collections marshal as `[]`, never `null`.
- `server.New` takes the native API as a parameter and accepts nil, so
  `/health` remains testable with no database wired.

## 2026-08-26 — Startup resilience: database connect retry

**Completed:**
- `db.Open` now retries the initial connectivity check with exponential
  backoff — 250ms doubling to a 5s cap, 60s total budget — and aborts
  immediately if the context is cancelled.
- Authentication and configuration failures are *not* retried. SQLSTATE
  28P01, 28000, and 3D000 fail fast; everything else (refused connections,
  DNS failures, a server in recovery) is treated as transient.
- Startup failures after the logger exists now log structured at error with
  `component`/`operation`/`error` instead of an unstructured stderr line.
  Configuration failures still go to stderr, because the logger cannot be
  built until the config describing it parses.

**Why:**
- `docker compose restart` restarts services in parallel and does not honour
  `depends_on: service_healthy` — that ordering applies only to `up`. The app
  reached `db.Open` before Postgres had bound its socket, exited, and was
  respawned by `restart: unless-stopped`. It self-healed, but every restart
  cost a crash cycle, and the failure was invisible to log tooling.
- The same window opens on a Postgres upgrade or a host reboot, neither of
  which `depends_on` covers either.

**Verified:**
- `gofmt`, `go vet ./...` clean. 33 tests pass with a database; integration
  tests still skip cleanly without `REELIX_TEST_DB_DSN`.
- Retry, give-up, and context-cancellation paths tested against a closed port
  with an injected fast policy, so the suite does not wait a minute.
- Fail-fast confirmed against real Postgres: a rejected password errors in
  0.01s rather than retrying for the budget.
- Both error paths asserted not to contain the database password.
- **Regression check:** rebuilt and ran the same `docker compose restart` that
  crashed. Log shows `database not ready, retrying` on attempt 1 and
  `database became reachable` after 272ms. Zero crashes.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Next step:**
- Step 3: first-run administrator creation, password hashing, native auth,
  create/list library over `/api/v1/*`.

**Decisions made:**
- Retry budget is 60s, chosen against PostgreSQL's own startup: an unclean
  shutdown means WAL recovery before it accepts connections, which on a large
  database takes tens of seconds.
- The budget is a constant, not configuration. No deployment has needed to
  tune it; it becomes an env var when one does.
- The retry policy is injected into an unexported `open` so tests exercise
  the give-up path in milliseconds rather than mutating package globals.

## 2026-08-26 — Step 2: persistence

**Completed:**
- Migration runner in `internal/db`: SQL embedded via `embed.FS`, a
  `schema_migrations` ledger, one transaction per migration wrapping both the
  DDL and its ledger insert, and a session advisory lock so concurrent
  starts serialise. Forward-only; there are no down migrations.
- `0001_init.sql` — seven tables: `schema_migrations`, `users`, `libraries`,
  `library_paths`, `media_items`, `media_files`, `media_streams`.
- `internal/domain`: plain models, no struct tags in either direction.
- `internal/repository`: `UserRepository`, `LibraryRepository`,
  `MediaRepository`, all taking a `db.Querier` satisfied by both the pool and
  a transaction. `db.InTx` composes them atomically.
- Migrations run at startup before the listener binds.
- `docker-compose.test.yml`: dev-only override publishing Postgres on
  `127.0.0.1` so host-side integration tests can reach it.

**Verified:**
- `gofmt`, `go vet ./...` clean. 28 tests pass with a database; all 20
  integration tests skip cleanly without `REELIX_TEST_DB_DSN`, so
  `go test ./...` is green on a machine with no Postgres.
- **Completion criterion, clean apply:** migrations applied to a fresh
  database; all seven tables present.
- **Completion criterion, idempotent:** four consecutive runs; ledger row
  count and every `applied_at` unchanged. Confirmed again on the real
  deployment — app restarted at 18:35:19 logged `schema up to date` and left
  migration 1's `applied_at` at 18:30:47.
- Checksum drift and unknown-applied-version both rejected at startup with
  explanatory errors. Four concurrent runners produce exactly one apply.
- `docker compose up -d --build`: both containers healthy, migration applied,
  `/health` 200, no errors in either log.
- Testing used scratch databases created and dropped inside the running
  instance. The existing `pgdata` volume was never dropped.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Next step:**
- Step 3: first-run administrator creation, password hashing, native auth,
  create/list library over `/api/v1/*`.

**Decisions made:**
- Dependency added: `github.com/jackc/pgx/v5` v5.10.0, using the native
  `pgxpool` interface rather than `database/sql` — typed uuid/jsonb handling
  and a built-in pool, so one dependency instead of two. Pure Go, so
  `CGO_ENABLED=0` and the static binary are unaffected. Brings the module
  count from zero to seven: `pgpassfile`, `pgservicefile`, `puddle/v2`,
  `golang.org/x/crypto`, `golang.org/x/sync`, `golang.org/x/text`.
- Migration tooling is hand-rolled, no dependency. `golang-migrate` and
  `goose` carry driver matrices and CLI surface for a project with one
  database.
- **pgx handles stdlib `uuid.UUID` natively — no custom codec is needed.**
  The plan assumed one would be; `[16]byte` resolves through pgx's
  underlying-type wrapping. A round-trip test pins the behaviour, since it is
  something Reelix depends on but does not control across pgx upgrades.
- IDs are generated in Go, never by a column default: Postgres 17 has no v7
  generator (that is 18), and generating application-side means the id is
  known before the INSERT.
- Applied migrations are immutable. An edited file whose checksum no longer
  matches the ledger is a startup error, not a silent skip — otherwise new
  and existing deployments diverge invisibly.
- A database carrying a migration version this binary does not have is also a
  startup error: an older binary must not serve a newer schema.
- Repositories are concrete types, not interfaces. Their consumers do not
  exist yet, and in Go the interface belongs to the consumer; Step 3 declares
  the method set it needs.
- `media_files.path` is globally unique so a re-scan updates in place. Upsert
  preserves `id` and `created_at`, so anything already referencing a file
  keeps pointing at it.
- `library_paths` is a separate table on constitutional instruction even
  though 0.0.1 writes exactly one row per library.
- Usernames use a unique index on `lower(username)` rather than `citext`,
  which would need the extension installed before the first migration runs.
- Test databases are per-test scratch databases, created and dropped by the
  test. No testcontainers: a heavyweight test dependency right after taking
  the first runtime one.
- Postgres publishes no host port in the canonical stack. The loopback
  binding lives in `docker-compose.test.yml` and is not merged into
  `docker-compose.yml`.
- `/health` still performs no I/O. The Step 1 entry anticipated adding a
  `checks` object here; Step 2 was scoped with no API surface, so that moves
  to whichever step first needs it.

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
