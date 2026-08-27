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

## 2026-08-27 — 0.0.2: playback state

**Completed:**
- `0005_playback_state.sql`: one row per user per item — the resume position,
  the raw reported position, played, play count, last played.
- `internal/repository/playback.go`: the report upsert. Play count applied as
  a delta, played sticky in SQL, and a report that changes nothing writes
  nothing.
- `internal/repository/media.go`: `ListItems` joins one user's state, and
  gains in-progress / played / unplayed filters and a last-played ordering.
  `ItemRuntime` is the single-row lookup every progress report makes.
- `internal/service/playback.go`: `Evaluate` (the thresholds) and `Record`.
- `MediaService.Browse` and `.Item` take the requesting user.
- Compatibility: real `UserData` everywhere, `/UserItems/Resume`,
  `/Items/Latest` excluding played items, and the three `/Sessions/Playing*`
  reports writing through.

**Verified:**
- `gofmt`, `go vet ./...` clean. Full suite green with a database and clean
  without one.
- **Fault-injected three times, each caught:** dropping the lower threshold
  failed both capture-derived cases; removing the no-op suppression failed the
  paused-client test; making the start report store failed the test that a
  resume position survives pressing play.
- **Live against the real library**, whole flow: press play (nothing
  recorded), two minutes in (nothing — 2.5%), thirty minutes in (appears in
  Continue Watching at 0:30:00), press play again (position survives), stop at
  forty minutes (0:40:00), watch to 98% (played, count 1, gone from the list,
  gone from the latest row). Five identical paused reports left `updated_at`
  untouched. A failed stop changed nothing.
- Migration 5 applied to the real database on restart; six items and 136
  streams intact.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Next step:**
- Hardware: play Idiocracy on the SK1, stop partway, resume from Continue
  Watching, finish it, and watch it leave the row. Then 0.0.2's second item,
  stream metadata — language, title, profile, frame rate — which needs a wider
  probe, a migration and a re-scan, and retires most of the allowance list in
  `fixture_test.go`.

**Decisions made:**
- **The thresholds are 5% / 90% / five minutes, and they are Reelix's.** The
  capture bounds the lower one without pinning it: the reference server was
  watched to 2.5% of Idiocracy and 0.24% of Congo and reported a resume
  position of zero and an empty list both times. Jellyfin exposes the same
  three as dashboard settings with these defaults — published configuration,
  not source. Both recorded stops are now regression tests.
- **The runtime floor gates resuming only.** A four-minute item is never
  resumable, but a four-minute item watched to the end is still played.
- **Both positions are stored.** The judged one keeps every read trivial; the
  raw one means changing the thresholds later reinterprets history instead of
  having discarded it.
- **Every progress report is a write, and the paused ones are not.** One
  report per five seconds per stream is a single-row upsert on a primary key.
  Coalescing in memory would put this state in the one place a crash destroys
  it, and would be wrong the moment there is a second process. The upsert
  suppresses no-op writes, so a paused client costs nothing.
- **The start report is logged, never stored.** It carries no position, so
  storing it would clear the resume point the client is about to seek to.
  Pinned by a test that fails if anyone makes the three reports uniform.
- **A viewing is counted when a completed playback ends**, not when one
  begins. The reference server counted a fifteen-second sample as a play;
  "watched 3 times" should mean watched. Rewatching and finishing again counts
  a second.
- **Played is sticky**, and can coexist with a resume position when someone
  starts a rewatch.
- `/Items/Latest` excludes played items, because Reelix has advertised
  `HidePlayedInLatest: true` since Step 5 and that was not true until now.
- `sortBy=DatePlayed` and `isPlayed` are answerable for the first time and are
  now wired rather than falling back.
- A report about an item no longer in the library answers 204 and records
  nothing, logging `unknown_item`. The client cannot correct that.

## 2026-08-27 — v0.0.1 RETROSPECTIVE — the first vertical slice is complete

Wholphin on the Ugoos SK1 discovers Reelix, authenticates, browses a movie
library, opens a film, and direct-plays it with working seeking. All eleven
success criteria in `docs/mvp-0.0.1.md` are met, verified against a real
client on real hardware with a real library. Tagged `v0.0.1`.

### What the eleven criteria actually proved

Each was verified end to end, not in isolation:

1. **Clean start.** `docker compose up -d` on a fresh database brings up the
   app and Postgres, migrations apply automatically, health responds.
2. **Administrator creation.** First-run setup creates the account once and
   refuses a second attempt, including concurrently, under an advisory lock.
3. **Library configuration.** One movie library with one filesystem path,
   created through the native API.
4. **Scan.** The walk found all six files, skipping sample directories and
   non-video entries; a re-scan updates rather than duplicates.
5. **Probe and persistence.** ffprobe filled containers, durations and 136
   stream rows, Fight Club's 63 streams among them, into PostgreSQL.
6. **Discovery.** Wholphin added the server by address; `LocalAddress` came
   back as the address the client had dialled.
7. **Authentication.** The 42-field `Policy` and the authorization header
   parser both held against the real client's strict deserialization.
8. **Browse.** The Movies library and its six films, with real durations and
   normalised containers.
9. **Detail.** 55 fields, media sources and streams, rendering at 48,996
   bytes for the 63-stream remux without a client-side exception.
10. **Direct play and seeking.** The original file, no transcode; nine 206
    responses across one session at varying offsets and sizes.
11. **Session logging.** One playback legible start to finish in the log,
    correlated by play session id.

### The whole library plays, and that vindicates the minimal profile match

Every one of the six files direct-played on the SK1 — including the 76 GB
Fight Club remux: Dolby Vision, HDR10+, HEVC, DTS-HD MA, 63 streams, streamed
over Tailscale from the VPS to the living room at roughly 73 Mbit/s sustained,
no transcode and no stutter. (Reelix's own derived bitrate for that file is
72.9 Mbit/s, which is the same number arriving from the other direction.)

**This is the evidence that the container-and-codec-membership decision was
the right scope.** Every file the device could actually decode was served
without transcoding. A stricter engine — checking levels, reference frames or
bitrate ceilings — would have had more opportunities to refuse a file the
hardware plays perfectly well, and every one of those refusals would have been
invisible in testing until someone pressed play on the wrong film. The
decision Reelix can act on is binary: hand over the file, or do not. A "no"
finer-grained than that changes no outcome and adds ways to be wrong.

### Known limitations carried into 0.0.2

None of these is a bug. Each is a subsystem 0.0.1 deliberately excluded or a
decision taken with its consequence understood, and each has a visible effect
worth knowing before someone reports it as a fault.

**What a user or a client can see:**

- **No playback state.** `/UserItems/Resume` stays empty and
  `UserData.PlaybackPositionTicks` stays 0 however much of a film is watched.
  Needs a table, a migration, and a decision about what "watched" means.
- **No stream language or title metadata.** The scanner records index, kind,
  codec, dimensions, channels and bitrate only, so Fight Club's 57 subtitle
  tracks render unlabelled. Needs a wider probe, a migration and a re-scan.
- **The `stream.mkv` URL spelling is not routed.** Only `/Videos/{id}/stream`
  exists. Some clients append the container extension; that is a deliberate
  gap rather than an oversight, and the first client that needs it will 404.
- **`/Items/{id}/Images/Primary` answered 401 rather than 404 once during
  playback.** At least one client path requests artwork without a credential —
  the same shape as the bare stream request. Harmless today, because there is
  no artwork and the client draws a placeholder either way. **When scraping
  lands this needs resolving**, because a 401 on an image a client expects to
  exist is a retry, where a 404 is final.

**Compatibility surface nothing has ever exercised.** When a client other than
Wholphin misbehaves, look here first — each of these was written from the
specification and has never met a real request:

- **`X-Emby-Authorization` and `X-MediaBrowser-Token`.** Written from published
  documentation, unit-tested, and never sent by anything. The
  `Authorization: MediaBrowser` path is hardware-proven; these two are not.
- **`/System/Info`.** No fixture exists — Wholphin never called it — so its
  shape comes from the published OpenAPI specification alone and has never
  been compared against a real response.
- **Route matching is case-sensitive, and ASP.NET's is not.** Wholphin sends
  the exact casing in the capture, so this is correct for the 0.0.1 gate. A
  client that lowercases its paths would 404 on everything. Deliberately not
  papered over with a normalising layer nothing has needed yet.

**Operational and data lifecycle:**

- **A re-probe is triggered by a size change, not mtime.** `media_files` has
  no mtime column, so a file edited in place without changing length is never
  re-probed and its stream data silently goes stale. Clearing `probed_at`
  forces one.
- **Files that vanish from disk are never removed from the library.** This is
  deliberate — a transient mount failure would otherwise wipe a library — but
  it means a genuinely deleted film stays in the browse list and 404s on play
  until something removes it by hand.
- **There is no update-library endpoint.** A library's path can only be
  changed with hand-written SQL, which is how 0.0.1's own library was
  repointed.
- **Nothing sweeps expired tokens.** `DeleteExpired` exists but is not
  scheduled, so rows accumulate indefinitely. Safety rests entirely on expiry
  being filtered inside the SQL query rather than after loading the row — any
  future lookup that matches on `token_hash` alone would authenticate an
  expired token.
- **Each concurrent login holds 64 MiB for the duration of the argon2id
  hash**, so login concurrency is bounded by the container's memory limit
  rather than by CPU. The unknown-user path costs the same, because it runs a
  dummy verification to equalise timing — credential stuffing against
  nonexistent usernames is exactly the traffic shape that hits this hardest.
  If it needs bounding, a semaphore around hashing is the answer, not weaker
  parameters.

**Development workflow, and it will bite:**

- **Bring the stack up with both compose files:**
  `docker compose -f docker-compose.yml -f docker-compose.test.yml up -d`.
  Plain `docker compose up -d` recreates Postgres with no published host port,
  which silently breaks every integration test — the canonical stack
  deliberately publishes none, and the override binds `127.0.0.1:5432` for
  host-side tests only. This has cost time in more than one session.

### Two security decisions that must NOT be "tidied" for consistency

Both look inconsistent with the rest of the surface. Both are deliberate, and
reverting either one breaks playback on the only client that defines success:

- **The stream endpoint accepts a request carrying no session token.** All
  nine recorded `/Videos/{id}/stream` requests come from ExoPlayer's own HTTP
  stack (`Dalvik/2.1.0`, not the SDK's OkHttp) with no `Authorization` header
  and no `api_key`. It is protected instead by the media source ETag as a
  capability: a client can only have learned that value from an authenticated
  PlaybackInfo call, which makes the URL unguessable rather than open.
- **`api_key` is accepted on that one route.** Query-string credentials are
  refused everywhere else on the compatibility surface, because a credential
  in a URL is far easier to leak than one in a header. That reasoning does not
  apply here and only here, because the request logger deliberately omits
  query strings from the access log — which means the exception is safe
  exactly as long as that stays true. Anyone adding query strings to the
  access log must revisit this route.

### 0.0.2 ordering, and why it is this order

1. **Playback state.** Everything else queues behind it, and it is the most
   visible gap: a media server that forgets where you were is not one people
   keep using.
2. **Stream metadata** — language, title, profile, frame rate. A wider probe,
   a migration and a re-scan. It also retires most of the allowance list in
   `fixture_test.go`: the fields Reelix answers with null because nothing
   probes them, each of which has to state its reason to be listed there.
3. **Metadata scraping.** Titles, overviews, ratings, artwork — and the
   external IDs the importer depends on.
4. **Emby/Jellyfin watch-history importer.** It must come after scraping,
   because matching an existing library to Reelix's items needs external IDs,
   and after playback state, because there has to be somewhere to import
   into. **This is the feature that decides whether anyone switches:** friends
   and family will not move servers if it means losing their watched status.
5. **Admin GUI.** Last on purpose. The native API has driven everything so far
   and the framework choice still needs proposing and approving separately.

### Multi-client validation, after 0.0.2

Wholphin defines success for 0.0.1, and one client's behaviour is not a
specification. The method is the one the fixtures were built with: point the
client at the reference Jellyfin through a recording proxy, run the whole
flow, redact the capture, then diff Reelix's answers against those recordings
route by route. The reference stack in `hack/capture/` is torn down but intact
— its config is in bind mounts, so `docker compose -f
hack/capture/docker-compose.yml up -d` brings back the same server, the same
admin account and the same library, which is what makes a new capture
comparable to the existing fixtures.

**One prerequisite before the next capture:** `redact.py` replaces the whole
value of every `Authorization` header with "REDACTED", which destroys the
header's structure along with the token. Fix it to redact only the token value
first — otherwise the capture cannot show what a new client actually sends,
which is the one thing it is being run to find out.

The clients, in the order they are worth doing:

- **VidHub** — the secondary target already named in the compatibility
  contract, and the most likely to exercise the two unproven authorization
  header paths.
- **jellyfin-web** — the reference implementation's own client, and the
  strictest reading of the API.
- **Findroid** — another Android client on the same Kotlin SDK, which should
  agree with Wholphin and will say so loudly if Reelix has quietly depended on
  something Wholphin-specific.
- **UHF on tvOS** — the most interesting and the hardest. AVPlayer's range
  behaviour differs materially from ExoPlayer's: it issues parallel and
  probing reads rather than the simple open-ended `bytes=N-` this milestone
  was built and tested against. There is also no `adb logcat` equivalent, so
  a failure surfaces as a spinner with nothing behind it — capture-and-diff
  is not a convenience there, it is the only diagnostic channel.

### Next step

Begin 0.0.2 with playback state: a table, a migration, and a decision about
what "watched" means, so that `/UserItems/Resume` and
`UserData.PlaybackPositionTicks` can stop being zero.

## The 0.0.1 archive

The twelve session entries behind this retrospective — Step 7 back through the
day the project was bootstrapped — are in `docs/archive/0.0.1.md`, verbatim.

They hold the reasoning this summary compresses: what the capture showed and
what was inferred from it, which dependencies were taken and which were
refused, why the schema is shaped as it is, and the wrong turns. Read them
when you need to know *why* something is the way it is rather than *what* it
is. Anything in them still true of the current state is carried above.
