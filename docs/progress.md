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

## 2026-08-27 — Step 6, first half: the socket and the polled routes

**Completed:**
- `socket.go`: `GET /socket` accepts the upgrade and holds the connection.
  Wholphin retries it forever with backoff until it gets a 101, so this is a
  requirement rather than polish. The handler goroutine *is* the read loop;
  a ping goroutine is cancelled and waited for before teardown.
- `polled.go`: the home-screen routes Wholphin polls —
  `/DisplayPreferences/default`, `/UserImage`, `/UserItems/Resume`,
  `/Items/Latest`, `/Shows/NextUp`, `/LiveTv/Recordings/Folders`. All
  authenticated, all answering in the recorded shape, none returning a 404
  except `/UserImage` (see below).
- `dto.go`: `queryResult` (the `Items`/`TotalRecordCount`/`StartIndex`
  envelope), `displayPreferences`, `problemDetails`.
- **Dependency added: `github.com/coder/websocket` v1.8.15.** Zero transitive
  dependencies, verified from its `go.mod` before adding.

**Verified:**
- `gofmt`, `go vet ./...` clean. 359 tests pass with a database; without
  `REELIX_TEST_DB_DSN` 87 skip cleanly and nothing fails.
- Every recorded fixture for all six routes passes the superset comparison,
  replayed with the query string the client actually sent — including the
  `/UserItems/Resume` call that carries a `parentId` and no `userId` at all.
- **The comparison was fault-injected again.** Dropping a `CustomPrefs` key
  and renaming a JSON tag produced exactly
  `$.CustomPrefs.skipForwardLength: missing (recorded string)` and
  `$.TotalRecordCount: missing (recorded number)`, then was reverted.
- **The handshake is validated against the capture itself.** `GET_socket/00`
  recorded Wholphin's `Sec-WebSocket-Key` and the exact `Sec-WebSocket-Accept`
  a real Jellyfin server computed from it; replaying that key must reproduce
  that value. Asserted in the test suite by hand-written handshake, and again
  live against a running server.
- **Live run against the real middleware stack** (scratch database, since the
  compat test harness does not run `requestLogger`): 101 upgrade with the
  recorded accept value, `permessage-deflate` declined, held open and silent
  after a client message, ping answered with a pong, close handshake echoed.
  All six polled routes answered correctly, and `traceId` carried the real
  request id. Neither the token nor the authorization header reached the logs.
- Goroutine leak test: 20 connections opened and closed, count returns to
  baseline. Wholphin reconnects often enough that a leak would compound.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Next step:**
- Step 6, second half: `/UserViews`, `/Items`, `/Items/{id}`. The home screen
  stays empty until those exist.

**Decisions made:**
- **`github.com/coder/websocket` rather than hand-rolling RFC 6455.** A
  hand-rolled parser was scoped at ~200 lines and its risk was bounded by
  discarding every payload — but that protects 0.0.1, not 0.0.2 when real
  messages get pushed over this socket and someone has to revisit a frame
  parser written for a different purpose. A protocol parser reading
  attacker-controlled length fields is the "mature dependency for
  security-critical functionality" case the constitution names.
- **`/UserImage` returns 404. This is the one deliberate exception to the
  never-404 rule, and it must not be "fixed".** The reference server 404s for
  a user with no avatar, which every Reelix user is — the project stores no
  user images. Wholphin re-requested it ten times across the recorded session
  (call orders 10, 31, 45 … 196), spread out as the user moved between
  screens: a per-screen re-request, not the backoff storm a 404 provokes on a
  route the client actually needs. A 200 with an empty body would instead hand
  the client a zero-byte image to fail on. The reason is on the handler and
  pinned by a test.
- **`/Items/Latest` returns `[]`, and must be revisited in the second half.**
  The reference server returned six movies. An empty Latest row on a populated
  server is wrong; it is just not wrong in a way that stops the client, and
  the item DTO belongs with `/Items`. `[]` passes the fixture comparison
  legitimately — a recorded array only constrains the type.
- **Reelix sends nothing over the socket.** The capture recorded only the 101;
  the proxy never saw a frame, so any server-to-client message shape would be
  a guess, and the SDK's strictness makes a wrong guess a hard client-side
  exception. Silence cannot be misparsed. Every inbound message is logged by
  `MessageType` at debug — never its payload, which a client is free to put a
  token in — so the next hardware run collects the evidence this decision
  currently lacks.
- `permessage-deflate` is offered by Wholphin and declined. Not echoing the
  extension is a legal answer meaning no compression, and it keeps the inflate
  path out of a connection that carries no data.
- A protocol-level ping every 60s, with a 10s pong deadline, closes the
  connection when the peer has vanished without a FIN. There is no read
  deadline: a quiet but healthy client must never be dropped, which would put
  it straight back into its reconnect loop.
- Display preferences are not persisted. The recorded fields are all emitted
  because the SDK's generated type declares them non-nullable, with the
  reference server's default values; `Id` is the user's own id, stable across
  calls without storing anything.
- Inbound messages are capped at 32 KiB; the library closes with 1009 beyond
  that. Jellyfin socket messages are small JSON objects.

**Known limitations — the unobserved second client:**
Two risks now sit together, both "nothing has ever sent this at us":
- `/socket` authenticates from the `Authorization` header only. Wholphin sends
  it and no query string, so this is safe for the milestone gate, but a client
  passing its token only as `api_key` would 401 and reconnect forever — the
  exact failure this step exists to prevent. The Step 5 decision not to accept
  a query-string credential stands; this is the cost of it.
- `X-Emby-Authorization` and `X-MediaBrowser-Token` remain unit-tested and
  unobserved (Step 5).
Both would surface first on VidHub, which does not gate 0.0.1.

**Trap hit this session, for the next one:**
`docker compose up -d` without `-f docker-compose.test.yml` recreates Postgres
with no published host port and silently breaks every integration test.
`docker-compose.test.yml` documents this; it is still easy to walk into.
Always bring the stack up with both files during development.

## 2026-08-26 — Step 5: compatibility discovery and auth

**Completed:**
- `internal/compat/jellyfin`: the first compatibility code. Handlers for
  `/System/Info/Public`, `/System/Info`, `/Users/Public`,
  `/QuickConnect/Enabled`, `/QuickConnect/Initiate`,
  `/Users/AuthenticateByName`, `/Users/Me`, `/Sessions/Capabilities`.
- **Fixture-comparison helper** (`fixture_test.go`), which Steps 6 and 7
  depend on: `assertSuperset` walks a recorded response and the Reelix
  response together, reporting every divergence with a `$.User.Policy.Field`
  path. Reelix may add fields; it may never omit one.
- `authheader.go`: parses `Authorization: MediaBrowser`,
  `X-Emby-Authorization`, and `X-MediaBrowser-Token`.
- `ids.go`: 32-char dashless lowercase hex at this boundary only.
- `0004_sessions.sql`: `server_settings` (single row, seeded server id) and
  `sessions` (native session/device record).
- `internal/service/session.go`: `SessionService` — authenticate, resolve,
  set capabilities.

**Verified:**
- `gofmt`, `go vet ./...` clean. 125 tests pass with a database; all
  integration tests skip cleanly without `REELIX_TEST_DB_DSN`.
- Every fixture for these routes passes the superset comparison: four
  `/System/Info/Public` recordings, three `/Users/Me`, and the
  `/Users/AuthenticateByName` response with its 42-field `Policy` and
  15-field `Configuration`.
- **The comparison was fault-injected to prove it works.** Renaming two JSON
  tags produced exactly the expected failures —
  `$.SessionInfo.LastPlaybackCheckIn: missing (recorded string)` and
  `$.User.Policy.SyncPlayAccess: missing (recorded string)` — and the helper
  has 20 of its own test cases covering the object, array, scalar, and null
  rules.
- `TestCapturedFlowInOrder` replays the recorded login sequence in call order.
- The full flow reproduced by `curl` against the compose stack: discovery →
  QuickConnect declined → authenticate → `/Users/Me` → `/System/Info` →
  `/Sessions/Capabilities` 204, with a bogus token correctly rejected 401.
  `LocalAddress` came back as `http://100.95.0.122:8080`, derived from the
  Host header the client dialled.
- A native `/api/v1` bearer token is rejected by the compatibility surface,
  asserted by test — the two schemes are genuinely independent.
- No credential reaches the logs: neither the access token, the raw
  authorization header, the password, nor either stored hash, checked in both
  the test harness and the live container logs.
- **COMPLETION CRITERION MET — verified on hardware.** Wholphin on the SK1
  added the server, authenticated, and displays the `steven` user and the
  Reelix server name. No serialization exception: the 42-field `Policy` and
  the authorization header parser both held against the real client. Every
  Step 5 route answered correctly, and every 404 in the server log is a
  Step 6 route.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Next step:**
- Step 6: user views, library items, item detail, dashless ids, well-formed
  empty responses, and accepting the `/socket` WebSocket.

**Observed from the real client — these shape Step 6:**
- **Wholphin retries `/socket` indefinitely with backoff** until it gets a
  WebSocket upgrade. Accepting and holding the connection is not optional
  polish; refusing it leaves the client reconnecting forever.
- **Wholphin retries 404s rather than treating them as final.** `/UserViews`
  was requested three times in two seconds. So for any route Step 6 does not
  fully implement, a well-formed empty response is materially better than a
  404 — a 404 produces a retry storm, an empty response settles. This
  sharpens the constitution's existing rule about opportunistically polled
  endpoints: it is not only that a 500 shows an empty screen, it is that a
  404 does not stop the client asking.

**Decisions made:**
- **The authorization header parser had no recorded reference — now
  hardware-verified for Wholphin.** `redact.py` replaced every
  `Authorization` value in the capture with "REDACTED", and
  `X-Emby-Authorization` and `X-MediaBrowser-Token` appear nowhere in it at
  all, so the parser was written from Jellyfin's published API documentation
  and the format the Kotlin SDK is known to emit rather than from observed
  traffic. It was the piece of this step most likely to be subtly wrong.
  The SK1 run retires that risk **for Wholphin's exact header format only**:
  the real client authenticated, so the `Authorization: MediaBrowser` path is
  now proven end to end. The `X-Emby-Authorization` and `X-MediaBrowser-Token`
  paths remain unit-tested and unobserved — nothing has ever sent them — so
  VidHub or another client could still expose a fault there.
  `redact.py` should be fixed before the next capture to preserve the header
  structure while redacting only the token value.
- **`/System/Info` is unvalidated and unexercised.** No fixture exists —
  Wholphin never called it — so its shape comes from the published OpenAPI
  specification alone and has never been compared against a real response.
  Noted in the code as well as here. If a later client breaks somewhere
  unexpected, this is the first place to look.
- QuickConnect reports `false` and `/QuickConnect/Initiate` answers 401.
  Reelix does not implement it, and advertising a flow that then fails in the
  user's hands is worse than declining cleanly. The reference server had the
  feature enabled, so the capture covered only the enabled path and Wholphin's
  reaction to `false` was unverified — **the SK1 run confirms it: the client
  falls through to the credentials form and logs in normally.** Declining a
  feature cleanly is a path the SDK handles.
- `/Users/Public` returns `[]`, matching the reference. It sends the client
  to a credentials form, which is the only login Reelix supports, and avoids
  disclosing account names to anyone who can reach the port.
- `ProductName` stays "Jellyfin Server" because clients branch on it.
  `ServerName`, which is what a user sees, is "Reelix".
- `LocalAddress` is derived from the request's Host header. The listener
  binds a port, not a hostname, and clients arrive by LAN address, tailnet
  address, or hostname; what the client dialled is the only correct answer.
- Sessions are keyed unique on (user_id, device_id), so re-authenticating
  from one device replaces its session rather than adding a row per app
  launch. The superseded token stops working, which is what "log in again"
  should mean.
- Sessions do not expire. Jellyfin's tokens do not either, and a television
  client forced to re-authenticate unprompted reads as a broken server.
- `User.Policy` is Reelix's single `is_admin` flag translated into the
  closest Jellyfin representation, not a permission model Reelix implements.
  Capabilities a client might use to hide UI are granted, because a client
  that believes it may not play shows a broken library rather than an error.
- Timestamps are formatted with .NET's seven fractional digits to match the
  recorded responses exactly. No failure was observed from Go's default, but
  matching removes a variable.
- Filesystem paths in `/System/Info` are returned empty rather than real: the
  constitution forbids leaking filesystem detail, and no client needs them.
- The `api_key` query parameter is deliberately not accepted as a credential.
  The access log records paths, and a query-string credential is far easier
  to leak than a header.
- Route matching is case-sensitive; ASP.NET's is not. Wholphin sends the
  exact casing recorded in the capture, so this is correct for the milestone
  gate. A client that lowercases its paths would 404. Known gap, not papered
  over with a normalising layer nothing has needed yet.

## 2026-08-26 — Step 4: scanner and probe

**Completed:**
- `internal/media`: filename parsing, filesystem walk, and an `ffprobe`
  wrapper. No database or HTTP dependencies.
- `0003_jobs.sql`: `jobs` table with state, progress, current item, timings,
  and error; a partial unique index allowing one active scan per library.
  `media_items` gains `source_path` with `UNIQUE (library_id, source_path)`.
- `internal/service/scan.go`: `ScanService` — walk, probe, persist, one
  transaction per file, progress written on a timer.
- `internal/repository/job.go`: `JobRepository`, including `FailOrphaned`.
- API: `POST /libraries/{id}/scan` (admin, 202), `GET /jobs/{id}`,
  `GET /jobs`.
- Dockerfile installs `jellyfin-ffmpeg7` (7.1.4-3-bookworm) from
  repo.jellyfin.org, pinned to the 7.x series. `REELIX_FFPROBE_PATH`,
  `REELIX_FFMPEG_PATH`, and `REELIX_PROBE_TIMEOUT` are configurable; ffprobe
  is version-checked at startup so a missing binary fails immediately.
- `.env` sets `REELIX_MEDIA_DIR=./hack/capture/test-media`. The mount already
  existed in `docker-compose.yml` as `${REELIX_MEDIA_DIR:-./media}:/media:ro`
  and was pointing at a directory that did not exist — an unset variable, not
  a missing bind. The canonical compose file is unchanged.
- Library path updated `/media/movies` → `/media` with a one-off
  `UPDATE library_paths`. There is no update-library endpoint and adding one
  was out of scope.

**Verified:**
- `gofmt`, `go vet ./...` clean. 108 tests pass with a database; all
  integration tests skip cleanly without `REELIX_TEST_DB_DSN`.
- **Completion criterion, populates Postgres:** scanning `/media` indexed all
  six files — Congo 1995, Fight Club 1999, Gangland 2025, Idiocracy 2006,
  The Legend of Aang 2026, The Singers 2026 — with real durations
  (18–139 min), containers, and 136 stream rows. Fight Club's 71GB remux
  alone contributed 63 streams.
- **Completion criterion, re-scan updates rather than duplicates:** second
  scan left items/files/streams at 6/6/136 and the md5 of every item id and
  source path byte-identical. Log summary: first scan `probed=6 skipped=0`,
  second `probed=0 skipped=6`, 749ms then 18ms.
- Sample-directory skip, grouping, extension filter, hidden entries, awkward
  filenames, determinism, and cancellation covered in `internal/media`;
  sample-skip covered again end-to-end through persistence.
- Probe failure on one file leaves the other files indexed and the job
  completed; the bad file is left unrecorded so the next scan retries it.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Next step:**
- Step 5: compatibility discovery and auth — `/System/Info`,
  `/System/Info/Public`, authorization header parsing, authenticate-by-name,
  token issuance, `/Users/Me`, validated against the Step 0 fixtures.

**Decisions made:**
- **Image size: 151MB → 558MB.** jellyfin-ffmpeg7 is 400MB of that, and it is
  unavoidable — those builds carry the QSV, NVENC, and VA-API plumbing the
  constitution chose them for. Worth knowing before deploy: image pulls are
  now a ~3.7x heavier operation. Trimming would mean building a custom FFmpeg,
  which the constitution explicitly rules out.
- `media_items.source_path` added. `media_items` had no natural key, so a
  re-scan could not identify the item it created last time and produced
  duplicates even though `media_files.path` de-duplicated files correctly.
  It is the movie's directory, or the file's own path for a file sitting in a
  library root.
- Name parsing extends slightly past the MVP's literal `Title (Year)`: dots
  and underscores normalise to spaces and a bare year token counts. Five of
  the six test files are scene-named, so the strict reading produced usable
  titles for exactly one of them. Still not a release parser — no edition,
  resolution, codec, or group handling.
- A delimited year outranks a bare one. Found while writing the tests:
  "Blade Runner 2049 (2017)" parsed as year 2049 under a single pattern.
- **Known limitation: re-probe is triggered by a size change, not mtime.**
  `media_files` has no mtime column — cut in Step 2 and deliberately not added
  now — so a file edited in place without changing length will not be
  re-probed. Clearing `probed_at` forces one. Revisit if it bites.
- One `media_item` per directory, so a release folder with several files is
  one movie with several `media_files` — the constitution's `Movie != File`.
- Sample directories are skipped by name only. A minimum-size heuristic would
  also exclude legitimate short films, and directory naming is the actual
  convention. The current test library has no such directories; the rsync that
  populated it used an explicit file list. Covered synthetically instead.
- Probing is sequential. Progress accounting stays simple and these are large
  I/O-bound reads.
- One bad file does not fail a scan: it is counted, logged, and left
  unrecorded so the next scan retries it. Only a walk-level failure — such as
  a library root that has vanished — fails the job.
- Jobs still marked running at startup are failed. They run in-process, so
  their goroutine died with the previous process; leaving them would also
  block the library permanently through the partial unique index.
- The scan runs on `context.WithoutCancel` of the request context: it must
  outlive the HTTP request that started it.
- `ScanService` takes a `Prober` interface declared in the service package, so
  persistence and idempotency tests run without ffprobe installed on the host.
- Deleting files that vanished from disk is deliberately not implemented. A
  transient mount failure would otherwise wipe a library.
- **Development now brings the stack up with the test override by default:**

  ```
  docker compose -f docker-compose.yml -f docker-compose.test.yml up -d
  ```

  Plain `docker compose up -d` silently reverts Postgres to publishing no
  host port, which broke three test runs in one session before this was
  settled. The friction costs more than the exposure.

  **The canonical stack is unchanged and still publishes no host port for
  Postgres.** `docker-compose.test.yml` is a dev convenience only: it binds
  `127.0.0.1:5432`, so the database is reachable from this machine alone —
  not from the tailnet, not from the internet. It must not be merged into
  `docker-compose.yml`, and a deployment should never use it.

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
