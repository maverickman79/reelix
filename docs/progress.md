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

## 2026-08-27 — 0.0.2: case-insensitive routes, and a bug that was not what it looked like

**Completed:**
- `internal/compat/jellyfin/routefold.go`: a trie built from the registered
  route patterns. Literal segments fold to their canonical spelling;
  parameter segments pass through byte for byte.
- `normalizeStreamSpelling` compares `/Videos/` and `stream` case-insensitively
  and still runs before the fold.
- `GET /Users/{userId}`, delegating to `handleUsersMe` behind `requireUserPath`.

**Verified:**
- `gofmt`, `go vet ./...` clean. Suite green with a database and without one.
- **Fault-injected both halves independently, each caught**, plus a third
  injection that caught a bad test — see below.
- **Live, the whole VidHub flow lowercased**: prefixed lowercase login issues a
  token; `/emby/videos/{id}/stream.mkv` answers 206, as do the all-uppercase
  and unprefixed spellings; `/emby/users/{id}`, `/emby/userviews`,
  `/emby/items/{id}` and `POST /emby/items/{id}/playbackinfo` all 200.
  `/emby/nonsense`, `/jellyfin/items` and `/emby/emby/items` still 404.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Next step:**
- VidHub on hardware: play and seek, which is this change's completion
  criterion and the only part a test cannot reach.
- Then the deferred Shield checks: The Singers reading
  `English DD+ 5.1 (Default)`, and Fight Club's track picker.

**Decisions made:**

- **The bug was pipeline order, not case sensitivity, and only reproducing it
  first showed that.** `normalizeStreamSpelling` guarded on
  `strings.HasPrefix(path, "/Videos/")` — case-sensitive — so a lowercase
  `videos` never had its extension stripped; and the fold could not rescue it
  afterwards either, because `stream.mkv` is not a literal in any registered
  pattern and the trie has nothing to match it against. **Adding case folding
  alone would have left VidHub broken while looking like a fix**, and the
  symptom (`404` on a lowercase path) points squarely at folding. The
  giveaway was in the reproduction: `/Videos/{id}/stream.MKV` already
  answered 206, so the *extension* case was never the problem — the
  *directory* case was. Reproducing before diagnosing is what surfaced that,
  and both halves are now fault-injected separately to keep it visible.

- **Literals fold, parameters do not.** Lowercasing a whole path would corrupt
  every value it carries — hex ids a client may echo back in its own casing,
  container extensions. Knowing which segments are which means matching
  against route shapes, hence a trie rather than a string operation. A flat
  map of lowercased to canonical path cannot work: parameters make the path
  set infinite.

- **The trie is built from what is registered**, via a `routeTable` that
  records patterns as they are declared. A hand-maintained second list would
  drift the first time somebody added a route and forgot.

- **Literals beat parameters, with backtracking.** This reproduces net/http's
  own precedence, so `/Users/Me` keeps winning over `/Users/{userId}` rather
  than parsing "Me" as a user id. Backtracking matters where a path's leading
  segments match a literal route but its tail exists only under the parameter
  — `/Items/Latest` is a route, `/Items/Latest/Intros` is not, but
  `/Items/{id}/Intros` is.

- **An unmatched path is returned unchanged.** The fold must never invent a
  match; an unknown path keeps reaching the mux and getting an honest 404.

- **A casing conflict panics at startup.** Two patterns whose literals differ
  only by case have no correct fold — one spelling would silently win and
  route the other somewhere unintended. `Routes()` runs at boot, so this fails
  loudly there rather than quietly in production.

- **`/Users/{userId}` is opportunistic, not blocking, and this is INFERENCE
  FROM OBSERVED BEHAVIOUR rather than a source read.** VidHub is closed
  source, unlike Wholphin and Findroid where the composition code was read
  directly. The inference: its flow reached `PlaybackInfo` and the stream
  request, so it was not blocked; and its `AuthenticateByName` response
  already carried the full `User` object including `Policy`, so the request is
  a refresh rather than an acquisition. Marked this way deliberately, per the
  allowance-audit rule — a claim about someone else's software that was not
  verified against their source is exactly the kind that reads as established
  fact later.

- **A fault injection caught a broken test, not broken code.** Reverting the
  ordering fix left `TestVidHubStreamRequest` green. The test was building its
  URL by splitting the canonical stream URL without separating the query
  string, so the container extension landed in the QUERY and every case
  silently requested the bare `/stream` route. It asserted 206 on a request
  that exercised nothing. **A passing test proves nothing until something has
  made it fail** — and the same lesson as the `displayChannelLayout` gap last
  session: verifying the piece I wrote rather than the behaviour under test.

## 2026-08-27 — 0.0.2: the fields clients actually read, and an allowance audit

**Completed:**
- `channel_layout` and `sample_rate` probed, stored, and emitted; migration
  `0007` with the same `probed_at` re-scan trigger migration 6 used.
- `ChannelLayout` normalised at the compat boundary: stored `5.1(side)`
  reaches the wire as `5.1`.
- The five `Localized*` fields emitted as the constants the reference server
  sends.
- **Every allowance in `fixture_test.go` audited** and marked with the
  evidence behind it.
- `docs/compat-capture.md` gains "What the harness cannot prove".

**Verified:**
- `gofmt`, `go vet ./...` clean. Suite green with a database and without one.
- **Fault-injected three times, each caught** (one only after closing a gap
  the injection exposed — see below).
- **Live**: migration 7 applied on restart, one scan re-probed all six files,
  136 stream rows intact, 11 with a layout. The real library contains
  `5.1(side)` — the exact value that would have mislabelled tracks.
- **The Singers now returns** `ChannelLayout: "5.1"`, `SampleRate: 48000`,
  `LocalizedDefault: "Default"`. Wholphin's own composition of those fields is
  `English DD+ 5.1 (Default)`, against `English DD+ null (null)` before.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Next step:**
- Hardware: confirm on the Shield that The Singers reads
  `English DD+ 5.1 (Default)`, and check Fight Club's picker while there.
- Then case-insensitive route matching, still outstanding.

**Decisions made:**

- **A rule for future sessions, and the most reusable thing here.** Auditing
  all 38 allowances found the pattern exactly:

  > **A reason that states a fact about Reelix held every time. A reason that
  > states a fact about someone else's software is where every error was.**

  Claims about our own schema, our own code, our own constitution are
  checkable in seconds and were all correct. Claims about how a client
  behaves were written from how a client *ought* to behave, and three were
  wrong. **When a justification depends on another program's internals, read
  that program's source or mark the claim unverified.** Reading third-party
  client source is explicitly permitted. Every entry now carries `[us]`,
  `[capture]`, `[client]` with the file named, or `[unread]`; an unmarked
  reason means unaudited.

- **What the audit found:** one false reason (`Localized*` — "the client has
  its own strings"; Wholphin prints whatever arrives), one stale reason
  (`LastPlayedDate` still cited "Step 7" as future, months after it shipped),
  and four inferential ones (`Level` ×2 and subtitle geometry ×2 explained a
  recorded `0` as "a .NET default" — a guess about the reference server,
  unverifiable without reading source this project does not read, and the
  kind of guess that reads as established fact three sessions later). Only
  the observation is stated now: the recording holds 0.

- **Thirteen reasons were sound but unverified**, and are now checked: every
  one is read by Wholphin through a null-safe `?.let`, so a null omits a row.
  Six fields no client reads at all. That distinction — free allowance versus
  one that puts "null" in front of a user — is what the audit was for.

- **`sample_rate` is in scope though nothing implicated it.** The cost of a
  migration is the re-scan, not the column, and that cost is identical for one
  field or two. A real library is terabytes; re-probing it later to collect a
  value ffprobe already returns, in a pass that has to happen anyway, is the
  expensive choice. **This is prudent sequencing rather than scope creep
  precisely because the field needs no new probe pass and no schema
  decision** — a field needing either would not qualify, and this reasoning
  should not be stretched to cover one that does.

- **The layout is stored raw and normalised at the boundary**, the same
  division as `DisplayTitle`: `5.1(side)` is a fact about the container, `5.1`
  is a fact about Jellyfin clients. Findroid matches the string against
  exactly `2.0`/`2.1`/`5.1`/`7.1` and sends anything else to its stereo arm,
  so an unstripped qualifier labels a 5.1 track as 2.0 — **a confident wrong
  answer nobody reports, which is worse than the visible `null` that started
  this.**

- **`DisplayTitle` still derives its layout from the channel count**, and this
  is deliberate. The capture shows the reference server sending
  `ChannelLayout: "stereo"` and `DisplayTitle: "Stereo"` for the same stream.
  The two disagree on purpose and the count-derived form already reproduces
  the capture. Pinned by a test so the apparent inconsistency is not tidied
  away.

- **The `Localized*` strings are a field, not a translation layer.** The
  reference server sends English regardless of client. Returning those words
  answers a question; it makes no promise about localisation. If Reelix ever
  localises, this is one of the places that changes — deliberately, rather
  than a null quietly becoming a word.

- **Structural conformance is not behavioural conformance**, now written into
  `docs/compat-capture.md` because it is a property of the harness rather than
  of this change. Setting `IsDefault` correctly — an unambiguous improvement —
  made the visible output strictly worse, turning `English DD+ null` into
  `English DD+ null (null)`, because it pushed Wholphin into a branch reading a
  field Reelix answered null for. **The fixture suite could not catch either
  state: `false` and `true` are the same JSON type, so the comparison passed
  identically before and after.** A field whose *value* drives client
  branching needs a real client or a source read.

- **A fault injection exposed a gap in its own test.** Dropping the
  `ChannelLayout` normaliser from the DTO left `TestDisplayChannelLayout`
  green, because that test exercises the function directly and nothing
  asserted the DTO calls it. An assertion on the response body was added and
  the injection then failed correctly. Testing a helper is not testing that
  anything uses it.

## 2026-08-27 — 0.0.2: route aliases, and a correction to compat-capture.md

**Completed:**
- `internal/compat/jellyfin/routes.go`: `withLegacyPaths` normalises the
  request path ahead of the mux. Strips one leading `/emby` or
  `/mediabrowser` segment case-insensitively, strips a trailing slash, and
  rewrites `/Videos/{id}/stream.{ext}` onto `/Videos/{id}/stream`.
- Seven user-scoped aliases: `/Users/{userId}/Items`, `/Items/{id}`,
  `/Items/Resume`, `/Items/Latest`, `/Views`, `/Items/{id}/Intros`,
  `/Items/{id}/SpecialFeatures`. Each delegates to the handler the modern
  spelling uses.
- `requireUserPath`: the path `userId` must be the authenticated user, or
  403.
- **`docs/compat-capture.md` corrected**, which is the most important part of
  this change. See below.

**Verified:**
- `gofmt`, `go vet ./...` clean. Full suite green with a database and clean
  without one.
- **Fault-injected three times, each caught:** trusting the path `userId`
  failed the 403 test; adding a `ThemeSongs` twin failed the absence test;
  stripping any first segment rather than the two known aliases failed the
  unknown-prefix test.
- **Live against the running server**, every case: prefixed login (the VidHub
  blocker) returns a token; `/emby`, `/mediabrowser` and `/Emby` all 200;
  `/jellyfin`, `/api` and `/emby/emby` all still 404; all four user-scoped
  list routes 200, including under a prefix; another user's items 403;
  `ThemeSongs` 200 bare and 404 user-scoped; `stream`, `stream.mkv` and
  `stream.mp4` each answer 206 to a range request at offset 5255045235.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Next step:**
- Case-insensitive route matching, as its own change. A real server folds the
  whole surface; Reelix does not. It needs a trie over the route table that
  folds literal segments and leaves path parameters untouched — lowercasing
  the whole path would corrupt item ids and container extensions. No observed
  client needs it yet, which is why it was deliberately not bundled here.
- Still outstanding from the previous entry: open Fight Club on the SK1 and
  confirm the track picker.

**Decisions made:**
- **The OpenAPI spec is not authoritative for route existence, and
  `docs/compat-capture.md` said it was.** Its opening line read "Jellyfin's
  OpenAPI spec tells you what routes exist." That claim has been the
  foundation of every compatibility decision since Step 0 and it is false.
  Working from it, the conclusion would have been that
  `/Users/{userId}/Items` had been removed and should not be implemented.
- **What the spec omits**, measured against a real 10.11.8: both prefix
  aliases, the entire user-scoped family, and the `stream.{container}`
  spellings. Diffing the 10.10.7 and 10.11.0 specs shows exactly one path
  changed between them (`/System/WakeOnLanInfo`), so these were dropped from
  the documentation long ago and are still served.
- **Route existence is a question for the reference server.** The method is
  now documented: bring up `jellyfin-ref` alone, probe unauthenticated, and
  distinguish a routing 404 (`Content-Length: 0`, empty) from a handler 404
  (`text/plain`, `Error processing request.`). Any other status means the
  route exists. Always probe a control.
- **The prefix list is fixed, not a rule.** Exactly `emby` and
  `mediabrowser`, matched without regard to case, stripped once. Probing
  confirmed `/jellyfin` and `/api` are not aliases and `/emby/emby` is not a
  route. "Strip the first segment" would turn every typo into a 200 for the
  wrong route.
- **A middleware, not duplicate registrations.** The alias then applies to
  routes added later, rather than leaving the next person wondering why
  theirs is the only one VidHub cannot reach.
- **User-scoped routes are written out, not generated.** The family is not
  uniform: a real server has `/Items/{id}/ThemeSongs` and no user-scoped
  twin. `TestThemeSongsHasNoUserScopedTwin` asserts that absence, so nobody
  later completes the set mechanically. Matching the reference server means
  matching what it declines to serve.
- **Match-or-403 on the path `userId`, no administrator override.** Trusting
  the path would let any authenticated client read any user's playback state
  by editing a URL. An override would be a permissions system built ahead of
  the permissions system; it goes in when groups and roles do.
- **`stream.{container}` discards the extension.** Reelix serves the original
  file whatever container is asked for, which is correct while direct play is
  the only thing it does — there are no other bytes to send, and refusing a
  request it can satisfy would be worse than ignoring a hint. **This becomes
  a real decision the moment transcoding lands**: at that point the extension
  is a client asking for a specific container and answering with a different
  one is wrong rather than lenient. Noted in the code at
  `normalizeStreamSpelling`, not only here.
- **Deliberately excluded**, each a capability rather than an alias:
  `/Audio/*` (no music library), the HLS routes (transcoding), the positional
  image forms (no artwork), and `PlayedItems` / `FavoriteItems` / `Rating`
  (features that exist in no spelling).

## 2026-08-27 — 0.0.2: stream metadata

**Completed:**
- `internal/media/probe.go`: ffprobe already returned all of this under
  `-show_streams`; the output struct never declared the fields. Language,
  title, profile, level, pixel format, both frame rates, and the default /
  forced / hearing-impaired dispositions. The invocation is unchanged.
- `0006_stream_metadata.sql`: ten columns. Seven nullable — absence is an
  answer — and three dispositions `NOT NULL DEFAULT false`, because ffprobe
  always reports them so "not flagged" is a fact.
- The migration ends with `UPDATE media_files SET probed_at = NULL`. That is
  the whole re-scan trigger: nothing on disk changes, so no incremental
  signal could find the staleness, and `probed_at` is the flag the scanner
  already reads. One `POST /libraries/{id}/scan` re-probes everything through
  the ordinary per-file path, resuming where it stopped if interrupted.
- `internal/compat/jellyfin/tracklabel.go`: DisplayTitle composition and a
  hand-written ISO 639 table, at the compat boundary.
- Six allowances retired in `fixture_test.go`; `Level` narrowed to audio and
  subtitle; `ReferenceFrameRate` kept with a rewritten reason.

**Verified:**
- `gofmt`, `go vet ./...` clean. Full suite green with a database and clean
  without one.
- **Fault-injected twice, both caught:** removing the migration's `UPDATE`
  failed `TestStreamMetadataMigrationClearsProbedAt`; nulling Language, Title
  and Profile in the DTO failed `TestItemDetailMatchesFixture` on every
  stream carrying one — which is what shows the retired allowances are
  load-bearing rather than removed.
- **Live against the real library.** Migration 6 applied on restart; all six
  files had `probed_at` cleared, one scan re-probed them, 136 stream rows
  intact. 133 carry a language, 76 a title, 13 are default tracks, 9 are SDH.
- **Fight Club renders.** 63 streams, 118,732 bytes, no client-side
  exception. Video `2160p HEVC`, profile Main 10, level 153, yuv420p10le,
  23.976 fps. Audio leads with `DTS-HD MA 5.1 - English - Default`, and the
  four commentary tracks are named by who is speaking. All 57 subtitle tracks
  are individually identifiable, including four SDH commentary tracks.
  `DefaultAudioStreamIndex` is 1 and the two default streams are flagged.
- Direct play unaffected: a range request at offset 5255045235 still answers
  206.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Field report — remote playback hit a real bandwidth ceiling:**
- The Shield at a second house connects **directly** over Tailscale, not
  through a relay, and the path still measured about **43 Mbit/s**.
- Files up to roughly 5 GB direct-play fine over it. The 76 GB Fight Club
  remux needs 73 Mbit/s sustained and stalls.
- **Reelix behaved correctly.** It decided direct play from the device
  profile, which is a true statement about the device: the Shield can decode
  that file. Reelix has no knowledge of network conditions and made no claim
  about them. Nothing here is a bug.
- **This is the first concrete evidence for `NetworkConditions` as a playback
  decision input**, which `docs/constitution.md` lists alongside
  `DeviceCapabilities` and the rest. Until now that entry was a design
  intention with nothing behind it. A decision engine reading capability
  alone is right about the device and wrong about the outcome, and the
  0.0.1 retrospective's argument for a binary hand-over-or-not decision
  holds only while the link can carry whatever the device can decode.
- **It is also the motivating case for transcoding.** The remaining answer
  for a 73 Mbit/s file over a 43 Mbit/s link is to send fewer bits. Both
  belong to a later milestone; recorded here so the requirement arrives with
  a measurement attached rather than as an assumption.

**Next step:**
- Hardware: open Fight Club on the SK1 and confirm the track picker shows the
  names above, with the default audio track pre-selected. This is the one
  completion criterion no test can reach — `IsDefault` was hardcoded `false`
  until now and the fixture comparison could not see it, because `false` is
  the same JSON type as `true`.
- Then decide whether `hdmv_pgs_subtitle` gets a friendly name (see below).

**Decisions made:**
- **DisplayTitle composition lives at the compat boundary**, in
  `tracklabel.go`. The string is a fact about what Jellyfin clients expect,
  not about the media; a native interface reading the same columns would
  compose a different label and should not have to take this one apart.
  Clients depending on the exact shape argues for reproducing it faithfully,
  not for relocating it.
- **The containment rule is inferred from four data points**, and the file
  says so where someone would change it. Parts join with " - " and a part the
  track title already contains is dropped. It is the simplest rule that
  reproduces all eight recorded strings; it is not knowledge of what the
  reference server does. A ninth capture could contradict it — change the
  rule to fit the evidence, do not reason about the implementation.
  Reproduced from recorded JSON, never from Jellyfin source.
- **The rule holds on data it never saw.** Fight Club's `DTS-HD MA 5.1` title
  suppressed both the codec and the layout, exactly as the captured
  `Surround AC3 5.1` did, and `English (SDH)` suppressed the language exactly
  as `English SDH` did.
- **`hearing_impaired` was carried**, beyond the original field list. It is
  the only thing separating an SDH track from an ordinary one where both read
  "English", and without it one recorded DisplayTitle was unreachable. Nine
  tracks in the real library are flagged.
- **Hand-written ISO 639 table, no new dependency.** `x/text/display` would
  pull a CLDR table to answer what a few KB of Go answers, and promoting an
  indirect dependency to direct needs more than convenience. Unlisted codes
  fall back to the raw code; `und` renders nothing.
- **Both frame rates are stored.** ffprobe reports two different things; they
  agree on CFR content and diverge on VFR, so deriving one from the other
  would be a guess made at write time.
- **`ReferenceFrameRate` is not emitted.** The captures show all three equal,
  but that is CFR content agreeing with itself, and what the reference server
  means by "reference" is not something the traffic reveals.
- **`Level` is emitted for video only.** The recorded 0 on audio and subtitle
  streams is a non-nullable C# int with nothing in it, not a measurement.
  Same reasoning corrected the subtitle geometry allowance, whose stated
  reason claimed the recording held the video's dimensions; it holds 0.
- **Video DisplayTitle omits the ` SDR` suffix** the reference server
  appended. That needs colour metadata this change does not store, and
  asserting SDR for a Dolby Vision remux would be worse than saying nothing.
- **An allowance retires only when Reelix emits the field.** The compat tests
  seed their own streams, so retiring one also means seeding a value — and
  that seed is not the justification. The justification is the field
  travelling ffprobe to schema to DTO, proved in four other packages, and the
  comment above `absentInReelix` now says so. The seed edit and the proofs
  are separate commits.
- **`IsDefault` and `IsForced` retired no allowance.** They were already
  emitted as hardcoded `false`, and the superset test passed on a wrong
  answer because `false` is the right JSON type. Only the real client can
  confirm this one.
- **Knowingly unpolished: PGS subtitles read `HDMV_PGS_SUBTITLE`**, the
  uppercased ffprobe codec, on 47 of Fight Club's tracks. Left as-is by
  decision. The capture only ever contained SUBRIP, so no observed string
  pins a friendlier name, and inventing one would break the rule that has
  kept this layer honest — the same rule that left DTS-HD MA as `DTS`.
  Revisit if a future capture pins the real string; until then ugly and
  correct beats tidy and invented.

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
- ~~**Route matching is case-sensitive, and ASP.NET's is not.**~~ RESOLVED.
  VidHub was the client that lowercases its paths. Closed by the fold trie in
  `routefold.go`.
- **`/Items/{id}/Images/{type}` declares the image type as a route PARAMETER,
  so case folding does not touch it.** `[us]` the pattern is
  `/Items/{id}/Images/{type}`, and the fold rewrites literal segments only, by
  design, so a request for `.../Images/primary` reaches the handler as
  `primary`. `[capture]` the reference server matches the type
  case-insensitively like everything else. Harmless while the route 404s
  everything, because there is no artwork. **Whoever implements artwork must
  compare the type case-insensitively, or split the pattern into literal
  alternatives** — otherwise a client that lowercases its paths gets a 404 for
  an image that exists. Noted in `api.go` at the registration and pinned by a
  test asserting the current behaviour.

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
