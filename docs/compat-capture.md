# Compatibility Capture Harness

Reelix derives Jellyfin compatibility from **observed wire traffic**, never from
Jellyfin's source code. This document is how that traffic gets captured and
turned into test fixtures.

Do this before writing any compatibility-layer code.

---

## Why

**The OpenAPI spec does not tell you what routes exist.** It is a partial
description of the surface, and treating it as the authority will send you the
wrong way. It also does not tell you:

- which routes a given client actually calls, in what order, during first run
- which query parameters the client sends
- which response fields the client will crash on if omitted
- what a client does when a route it expects returns `404` vs `500` vs `{}`

That first line used to read "Jellyfin's OpenAPI spec tells you what routes
exist." It was wrong, and it had been the foundation of every compatibility
decision since Step 0. See "The spec is incomplete" below for what it missed
and how the gap was found.

Wholphin is built on the Jellyfin Kotlin SDK, which is generated from that spec.
That means its calls are disciplined and predictable — but also that its
deserialization is **strict**. A missing non-nullable field is a hard exception,
not a graceful degradation. The capture tells you exactly which fields are
non-negotiable.

---

## The spec is incomplete

Measured against a real Jellyfin **10.11.8**, the published 10.11.0
specification declares 315 paths and **omits entire families of routes the
server answers**:

- **Prefix aliases.** Every route is also served under `/emby/...` and
  `/mediabrowser/...`, inherited from Emby. The spec contains neither string.
  Multi-backend clients use the prefixed form; VidHub sends `/emby` on every
  request and cannot log in without it.
- **User-scoped routes.** `/Users/{userId}/Items`,
  `/Users/{userId}/Items/{id}`, `/Users/{userId}/Items/Resume`,
  `/Users/{userId}/Items/Latest`, `/Users/{userId}/Views` and others are all
  live. None appears in the 10.11.0 spec, and none appears in 10.10.7 either —
  diffing those two specs shows exactly one path changed between them
  (`/System/WakeOnLanInfo`), so these were dropped from the *documentation*
  long ago and are still *served*. Infuse and VidHub both use them.
- **Stream spellings.** `/Videos/{id}/stream.mkv`, `.mp4` and `.ts` are all
  answered; the spec documents the bare form.

The lesson is not that the spec is useless — it is the only place to learn
request and response *shapes*. It is that **route existence is a question for
the reference server, not for the spec.**

---

## Probing the reference server for routes

The capture harness answers "what does this client send". This answers "what
would a real server accept", which is a different question and the one you
need when a client reports a 404 for a route no capture contains.

Bring up `jellyfin-ref` alone — mitmproxy is not needed — and probe. **No
credentials are required**, because the useful signal is whether the route
exists at all, and that survives an unauthenticated request:

```bash
cd hack/capture && docker compose up -d jellyfin-ref
curl -s -o /dev/null -w '%{http_code}' http://<host>:8096/emby/Items   # 401
```

### Telling a routing 404 from a handler 404

This is the whole technique, and without it the probe is useless: a request for
a route that does not exist and a request for an item that does not exist both
answer `404`. They are distinguishable by **response shape**:

| Meaning | Status | Body |
|---|---|---|
| Route does not exist | 404 | `Content-Length: 0`, empty |
| Route exists, item does not | 404 | `Content-Type: text/plain`, `Error processing request.` |

So `/Videos/{fake-id}/stream.mkv` answering `404` with a body means the
**route is real** and only the item was missing. The same request answering
`404` with an empty body would mean the spelling is not served.

A status other than 404 — `401`, `400`, `415` — always means the route exists;
the request got far enough to be rejected on its merits.

Always probe a control alongside the real question. `/notaprefix/System/Info/Public`
and `/emby/emby/System/Info/Public` both answer an empty 404, which is what
proves the prefix is a fixed list stripped once rather than a rule that drops
any first segment.

### What this method has established

Confirmed present: both prefixes, case-insensitively (`/Emby`, `/EMBY`,
`/MediaBrowser`); the user-scoped family; `stream.{container}` for any
extension; trailing slashes; the long positional image forms; and
case-insensitive matching across the whole surface.

Confirmed **absent**, which matters just as much: `/jellyfin` and `/api` are
not prefixes; `/emby/emby/...` is not a route; and
`/Users/{userId}/Items/{id}/ThemeSongs` **does not exist** even though the
bare `/Items/{id}/ThemeSongs` does. The user-scoped family looks mechanical
and is not. Reelix has a test asserting that absence, so nobody later
"completes" the set by generating twins — matching the reference server means
matching what it declines to serve.

---

## Topology

Everything stays on plain HTTP on the LAN. Do not attempt to MITM TLS on Android
TV — certificate installation there is painful and entirely unnecessary here.

```text
Wholphin  →  mitmproxy (reverse mode, :8097)  →  Jellyfin (:8096)
```

The client is pointed at the proxy, not at Jellyfin. No proxy configuration on
the device, no certificates, full request and response capture.

Later, the same proxy points at Reelix instead, and the captures are diffed.

---

## Reference stack

`hack/capture/docker-compose.yml`:

```yaml
services:
  jellyfin-ref:
    image: jellyfin/jellyfin:10.11.8
    volumes:
      - ./ref-config:/config
      - ./ref-cache:/cache
      - /path/to/test-media:/media:ro
    ports:
      - "8096:8096"

  mitmproxy:
    image: mitmproxy/mitmproxy:latest
    command: >
      mitmdump
      --mode reverse:http://jellyfin-ref:8096
      --listen-port 8097
      --set stream_large_bodies=1m
      -w /captures/session.flow
    volumes:
      - ./captures:/captures
    ports:
      - "8097:8097"
    depends_on:
      - jellyfin-ref
```

`stream_large_bodies` matters: without it, mitmproxy buffers the entire video
stream into the capture file.

Point the reference Jellyfin at a **small** test library — five or six movies,
mixed containers and codecs, at least one file large enough to make seeking
meaningful.

---

## The capture run

Point Wholphin at `http://<host>:8097` and execute the full 0.0.1 flow in one
uninterrupted session:

1. Add server by address
2. Complete server discovery
3. Log in as the administrator
4. Land on the home screen
5. Open the movie library
6. Scroll the library until images load
7. Open a movie detail page
8. Start playback
9. Seek forward
10. Seek backward
11. Stop playback and exit

Order matters. First-run behavior differs from steady-state behavior, and 0.0.1
has to survive first run.

Do a second pass after killing and reopening the app, to capture the
already-authenticated path.

---

## Turning captures into fixtures

Export the flow to something reviewable and commit it:

```bash
mitmdump -nr captures/session.flow \
  --set hardump=captures/session.har
```

Then extract, per unique route:

- method and path
- query parameters actually sent
- request headers, **with tokens and authorization headers redacted**
- response status
- response body (pretty-printed JSON)
- the order in which the route was first called

Fixtures live in `internal/compat/jellyfin/testdata/`, one directory per route.

**Redaction is mandatory before anything is committed.** The captures contain
live access tokens, device IDs, and the administrator password on the
authenticate call. Write the redaction step as a script, not as a manual pass —
`hack/capture/redact.py` — and run it before `git add`.

Add `hack/capture/captures/` to `.gitignore`. Only redacted fixtures are
committed.

---

## Using fixtures in tests

Compatibility translation tests assert that Reelix's response for a given route
is **structurally compatible** with the recorded Jellyfin response:

- every field present in the Jellyfin response is present in the Reelix response
- types match
- ID-shaped fields are 32-character dashless lowercase hex
- Reelix may add fields; it may not omit them

Byte-for-byte equality is not the goal and is not achievable. Structural
superset compatibility is.

---

## The swap test

Once the compatibility layer is written, change one line:

```yaml
--mode reverse:http://reelix:8080
```

Run the identical flow with Wholphin. Capture. Diff against the reference
capture, route by route.

Any route where Reelix returns a different status, omits a field, or is called
in a different order is a bug. This diff is the acceptance gate for the
compatibility layer.

---

## When something fails

In order, fastest first:

1. `adb logcat | grep -i wholphin` — SDK deserialization exceptions name the
   offending field directly.
2. Diff the failing route against the reference capture.
3. Read Wholphin's source (`github.com/damontecres/Wholphin`) or the Jellyfin
   Kotlin SDK to see what the client expects. Reading client code is permitted;
   reading Jellyfin *server* code is not.
4. Point Wholphin back at the reference Jellyfin to confirm the client itself
   is not at fault.

If a failure cannot be resolved without reading Jellyfin server source, stop and
raise it rather than proceeding.

---

## Secondary client

VidHub is a secondary target. It is multi-backend, so it exercises a different
and generally more conservative slice of the API, which surfaces different
assumptions. Capture it the same way once Wholphin passes.

VidHub does not gate 0.0.1.
