# Reelix 0.0.2 — The Switch-Over Milestone

## The goal

0.0.1 proved one client could play one file. 0.0.2 is about whether anyone
would actually move to Reelix and stay:

> **A media server that remembers where you were, labels what it is playing,
> knows what a film is, can take over an existing watch history, and can be
> administered without `curl`.**

Unlike 0.0.1 this is not a single vertical slice. It is five items with a
deliberate order, below.

---

## The five items, in order

The order is load-bearing. Do not start an item before the one above it is
verified, except where a dependency is explicitly stated as absent.

### 1. Playback state — **DONE**

Resume positions, played/unplayed, play counts, Continue Watching.

Everything else queues behind it, and it was the most visible gap: a media
server that forgets where you were is not one people keep using.

Delivered: `0005_playback_state.sql`, one row per user per item;
`internal/repository/playback.go`; `internal/service/playback.go` with the
5% / 90% / five-minute thresholds; real `UserData` across the compatibility
surface, `/UserItems/Resume`, `/Items/Latest` excluding played items, and the
three `/Sessions/Playing*` reports writing through.

**Hardware CONFIRMED 2026-08-28**, SK1: Idiocracy plays, stops, appears in
Continue Watching at the right position, resumes from there, and passing the
end removes it from that row. Server-side state agrees — `played=t`,
`play_count=1`, `position_seconds=0`.

### 2. Stream metadata — **DONE**

Language, title, profile, level, pixel format, frame rates, dispositions,
channel layout, sample rate. A wider probe, a migration, and a re-scan.

It also retires most of the allowance list in `fixture_test.go` — the fields
Reelix answered with null because nothing probed them.

Delivered: `internal/media/probe.go` widened; migrations `0006` and `0007`,
each clearing `probed_at` as its re-scan trigger;
`internal/compat/jellyfin/tracklabel.go` for `DisplayTitle` composition and
the ISO 639 table; `ChannelLayout`, `SampleRate` and the five `Localized*`
fields; every remaining allowance audited and marked with its evidence.

**Hardware CONFIRMED 2026-08-28**, SK1. The Singers reads
`English DD+ 5.1 (Default)` — the nulls are gone, and it is the file's only
audio track, correct for a single-stream WEB-DL. Fight Club's picker shows
`DTS-HD MA 5.1` and the commentary tracks, each named by who is speaking.

**`IsDefault` is confirmed by the only means available.** The default track is
pre-selected on the device. `IsDefault` and `IsForced` are **structurally
unconfirmable by the test suite** — `false` and `true` are the same JSON type,
so the fixture comparison passes identically on a wrong answer. A real client
was always going to be the only instrument that could read this, and it has
now read it.

### 3. Metadata scraping

Titles, overviews, ratings, artwork — **and the external IDs item 4 depends
on**. That dependency is the reason this comes before the importer rather
than after it.

DONE. TMDB is the provider, chosen and approved. Identity, fields and artwork
all landed; see the progress log entries of 2026-08-28 and 2026-08-29.

**The two known limitations that became live with artwork are both RESOLVED**,
and each is now pinned by a test that fails if it regresses:

- ~~`/Items/{id}/Images/Primary` answers **401 rather than 404**.~~ RESOLVED.
  The image routes are unauthenticated, matching the reference, which was
  probed rather than assumed. `TestItemImageIsPublic` pins it. What that
  concedes is recorded at the route registration in `api.go`: anyone holding an
  item UUID can fetch its poster.
- ~~`/Items/{id}/Images/{type}` declares the image type as a route
  **parameter**~~ RESOLVED. The type is canonicalised in the handler through
  `canonicalImageType`, and storage is keyed on its output.
  `TestItemImageTypeFoldsCase` now requires every spelling to SERVE the image
  rather than merely to 404 with the same name.

**One question is deliberately left open for a real client:** the long
positional image forms exist on the reference but appear in no captured
request, so they are still not routed. Watch the access log while a client
renders a grid and add them only if one appears.

### 4. Emby/Jellyfin watch-history importer

**This is the feature that decides whether anyone switches.** Friends and
family will not move servers if it means losing their watched status.

It must come after item 3, because matching an existing library to Reelix's
items needs external IDs, and after item 1, because there has to be somewhere
to import into.

### 5. Admin GUI

Last on purpose. The native API has driven everything so far.

**The framework is not chosen. Propose it and get approval before writing any
frontend code** — this is a standing rule in `CLAUDE.md`, repeated here
because this is the item where someone will be tempted to skip it.

---

## Change to the plan: multi-client validation was pulled forward

The v0.0.1 retrospective scheduled multi-client validation for **after**
0.0.2. It did not wait. Roughly half the commits since `v0.0.1` are that work,
carried out between items 2 and 3.

**This was not scope creep and should not be recorded as a mistake.** Each
piece was driven by a real client failing against the running server, not by
anticipation. It is written down here so a future session does not have to
reconstruct it from the git log.

What was delivered out of order:

- **Route aliases** — `/emby` and `/mediabrowser` stripped case-insensitively,
  trailing slash, `stream.{container}`, seven user-scoped aliases, and
  `requireUserPath` (match-or-403 on the path `userId`).
- **Case-insensitive route matching** — the fold trie in `routefold.go`.
  Literal segments fold, path parameters pass through byte for byte. Plus
  `/Users/{userId}`.
- **The jellyfin-web login chain** — `/DisplayPreferences/{prefsId}` (which
  was the spinner), `/Sessions/Capabilities/Full`, `/System/Endpoint`,
  `/Playback/BitrateTest`, `/Branding/Configuration`.
- **The Gangland fixes** — `mediaSourceContainer` (the extension when it
  appears in ffprobe's list, otherwise the first token), and `queryValue`, a
  query-parameter lookup ignoring case and underscores.

**Result: three clients play — Wholphin, VidHub, and jellyfin-web.**

Two consequences worth carrying:

- The retrospective's prerequisite for the next capture is **done**:
  `redact.py` no longer destroys the `Authorization` header's structure.
- The retrospective's "route matching is case-sensitive" limitation is
  **closed**.

Still un-run from the original multi-client list, and still scheduled after
0.0.2: **Findroid** (same Kotlin SDK as Wholphin; will say so loudly if
Reelix has quietly depended on something Wholphin-specific) and **UHF on
tvOS** (AVPlayer issues parallel and probing reads rather than the simple
open-ended `bytes=N-` this project was built against, and there is no
`adb logcat` equivalent — capture-and-diff is the only diagnostic channel
there).

---

## Explicitly excluded

Do not implement these during 0.0.2 unless explicitly instructed. This is
0.0.1's list with the three items 0.0.2 claims — metadata scraping, artwork
downloading, admin web frontend — removed:

transcoding · TV-series libraries · Redis · plugins · recommendations ·
custom home rows · advanced user permissions · groups · hardware acceleration ·
distributed workers · intro detection · trickplay · chapters ·
subtitle downloads · subtitle burn-in · Plex compatibility ·
mobile applications · TV applications · SyncPlay · Live TV / DVR · music ·
photos · collections · people/cast · multi-path libraries ·
library monitoring / filesystem watching

**Transcoding stays excluded, but now has a measurement behind it.** A Shield
at a second house connects directly over Tailscale at about **43 Mbit/s**; the
76 GB Fight Club remux needs **73 Mbit/s** sustained and stalls there. **That
ceiling is a property of that one link, not of the profile decision** — the
same remux direct-plays on the SK1 (confirmed 2026-08-28). Reelix behaved
correctly — it decided direct play from the device profile, which is a true
statement about the device, and it has no knowledge of network conditions.
This is the first concrete evidence for `NetworkConditions` as a playback
decision input alongside `DeviceCapabilities`, and the motivating case for
transcoding. Both belong to a later milestone. Recorded so the requirement
arrives with a measurement attached rather than as an assumption.

---

## Open decisions carried into 0.0.2

Neither blocks anything. Both have their evidence already gathered; do not
re-derive it.

- **The `/socket` 401.** Reelix answers 401 where the reference answers 403,
  and matching the status would not make the socket connect — the credential
  arrives somewhere `requireAuth` does not read. The full probe table is in
  the 2026-08-27 jellyfin-web entry in `docs/progress.md`. Allowing `api_key`
  there would be *matching* the reference, not inventing something, but it
  means relaxing a deliberate decision documented on `requireAuth`, so it
  stays a separate task with its own reasoning. The socket gates neither
  rendering nor playback on any of the three clients.
- **PGS subtitles read `HDMV_PGS_SUBTITLE`** — the uppercased ffprobe codec,
  on 47 of Fight Club's tracks. Left ugly and correct by decision: no observed
  capture pins a friendlier string, and inventing one would break the rule
  that has kept this layer honest. Revisit when a capture pins the real name.

---

## Verification method

Unchanged from 0.0.1, and it is what has been catching the real bugs:

- `gofmt` and `go vet ./...` clean; the suite green **with a database and
  without one**.
- **Fault-inject every fix and confirm the test catches it.** A passing test
  proves nothing until something has made it fail. Where a change has two
  halves, inject them separately — that is what showed the VidHub 404 was
  pipeline order rather than case sensitivity.
- **Live against the running server**, not only against tests.
- **Hardware for anything a test cannot reach.** `IsDefault` is the standing
  example.

Two rules earned in 0.0.2 that apply to everything after it:

> **A reason that states a fact about Reelix held every time. A reason that
> states a fact about someone else's software is where every error was.**
> When a justification depends on another program's internals, read that
> program's source or mark the claim unverified.

> **A parameter read by exact name does not fail loudly.** It fails as
> *absence* — the server behaves as though the client never sent it — so it
> surfaces as a missing feature, a wrong default, or a credential rejection,
> never as "bad parameter name". On a new client, make this the first
> hypothesis, not the last.

**Bring the stack up with both compose files:**
`docker compose -f docker-compose.yml -f docker-compose.test.yml up -d`.
Plain `docker compose up -d` recreates Postgres with no published host port
and silently breaks every integration test.

---

## Test devices

Wholphin on the Shield Pro and the Ugoos SK1, both over ADB; `adb logcat` is
the primary diagnostic channel. VidHub and jellyfin-web are now also live
targets — jellyfin-web runs against Reelix behind one origin via
`hack/jellyfin-web/`.

A pristine 10.11.8 reference instance for probing route shapes is a throwaway:
see the 2026-08-27 jellyfin-web entry in `docs/progress.md` for how it was
stood up and what must not be modified.

---

## Definition of done

All five items complete. The three deferred hardware confirmations are
**closed** (2026-08-28, SK1). Findroid and UHF remain scheduled after this milestone and
do not gate it.

Tag `v0.0.2`. Write the retrospective into `docs/progress.md` before starting
0.1.0.
