# Compatibility Capture Harness

Reelix derives Jellyfin compatibility from **observed wire traffic**, never from
Jellyfin's source code. This document is how that traffic gets captured and
turned into test fixtures.

Do this before writing any compatibility-layer code.

---

## Why

Jellyfin's OpenAPI spec tells you what routes exist. It does not tell you:

- which routes a given client actually calls, in what order, during first run
- which query parameters the client sends
- which response fields the client will crash on if omitted
- what a client does when a route it expects returns `404` vs `500` vs `{}`

Wholphin is built on the Jellyfin Kotlin SDK, which is generated from that spec.
That means its calls are disciplined and predictable — but also that its
deserialization is **strict**. A missing non-nullable field is a hard exception,
not a graceful degradation. The capture tells you exactly which fields are
non-negotiable.

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
