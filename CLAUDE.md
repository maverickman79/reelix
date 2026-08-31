# Reelix

Reelix is a modern, Docker-first media server written in Go. It serves existing
Jellyfin clients over a Jellyfin-compatible API while running an entirely native
internal architecture.

**Reelix is not a fork of Jellyfin.** No Jellyfin source code, in any language,
is ever copied into this repository. See "Clean-room rule" below — this is a
hard constraint, not a preference.

---

## Read this first

This file is the operating contract for every session. It is deliberately short.
The full architecture lives in `docs/constitution.md` — read it before any
non-trivial change.

| Document | When to read it |
|---|---|
| `CLAUDE.md` (this file) | Every session, first |
| `docs/progress.md` | Every session, second — this is where we left off |
| `docs/constitution.md` | Before any architectural or cross-cutting change |
| `docs/mvp-0.0.2.md` | Before implementing anything in the current milestone |
| `docs/compat-capture.md` | Before touching the Jellyfin compatibility layer |
| `docs/unraid.md` | Before deploying anywhere that is not the development VPS |
| `docs/measurement-0.0.2.md` | Before changing anything for performance, and before trusting a number about one |

---

## Current milestone

**Version 0.0.2.** Five items, in this order:

> 1. Playback state — **done**
> 2. Stream metadata — **done**
> 3. Metadata scraping — **done**
> 4. Emby/Jellyfin watch-history importer
> 5. Admin GUI

Multi-client validation was pulled forward out of order and is partly done:
Wholphin, VidHub and jellyfin-web all play.

Full scope, exclusions, and completion criteria are in `docs/mvp-0.0.2.md`.
The exclusion list there is binding. Do not implement excluded features.
`docs/mvp-0.0.1.md` is closed and kept for reference only.

---

## Stack

- Go (backend)
- PostgreSQL (persistence — no SQLite, no ORM-driven schema)
- FFmpeg / ffprobe via `jellyfin-ffmpeg7` binaries (shelled out, never linked)
- Docker Compose (canonical deployment)
- REST, plus WebSocket where the compatibility layer requires it

Not yet chosen, do not assume: admin frontend framework. Propose and get
approval before writing any frontend code.

Not present, do not add: Redis, message brokers, gRPC, Kubernetes manifests.

---

## Clean-room rule

Reelix is licensed AGPL-3.0 and is legally independent of Jellyfin. This is only
true if we keep it true.

**Permitted sources of compatibility knowledge:**
- Observed HTTP traffic between a real client and a real Jellyfin server
  (see `docs/compat-capture.md`)
- Jellyfin's published OpenAPI specification
- Jellyfin's public API documentation
- Third-party client source (e.g. Wholphin, Jellyfin Kotlin SDK) read to
  understand *what a client sends and expects*

**Never permitted:**
- Copying, pasting, or transliterating Jellyfin server source code
- Porting Jellyfin C# implementations into Go
- Vendoring Jellyfin code in any form

If a task seems to require reading Jellyfin server source to proceed, stop and
say so instead of proceeding.

---

## Compatibility target

- Jellyfin API version: **10.11.x** (pinned; do not target 10.10 or `master`)
- Primary client (defines success): **Wholphin** — Android TV, open source,
  built on the Jellyfin Kotlin SDK
- Secondary client (should also work, does not gate the milestone): **VidHub**

Because Wholphin uses an SDK generated from Jellyfin's OpenAPI spec, its
deserialization is strict. Omitting a non-nullable field will produce a hard
client-side exception, not a graceful degradation. Assume strictness.

---

## Operating rules

Before changing code:

1. Read this file and `docs/progress.md`.
2. Inspect the relevant existing code.
3. Identify which packages are affected.
4. State the intended implementation and wait for confirmation on anything
   architectural.
5. Make the smallest coherent change.
6. Run applicable tests.
7. Run `gofmt` and `go vet ./...`. There is no other linter configured;
   do not go looking for one.
8. Report exactly what changed.

Do not implement features that were not requested.
Do not perform speculative refactors.
Do not replace an existing subsystem because another design seems cleaner.
Do not silently change architectural conventions.
If a request conflicts with `docs/constitution.md`, explain the conflict before
implementing.

## Task shape

Every task should be expressible as:

```
Goal
Scope
Non-goals
Implementation
Tests
Completion criteria
```

Prefer a working narrow vertical slice over a broad, partially implemented
system. Never respond to a task by attempting to build the entire future
product.

## Stop-and-confirm

Stop and ask before:

- any destructive or system-level operation (`useradd`, writes under `/etc`,
  systemd unit changes, `systemctl enable/start`)
- `docker compose down -v` or anything that drops the Postgres volume
- destructive database migrations
- adding a dependency (see the dependency rules in `docs/constitution.md`)
- rewriting more than one package in a single change

## Git discipline

Small commits representing understandable units of work. Never combine
refactors, features, dependency upgrades, and formatting rewrites into one
change. Do not rewrite large working areas of the project unless specifically
necessary.

## End of session

Before finishing, append a dated entry to `docs/progress.md`: what was
completed, what is in flight, what is blocked, and the exact next step.
This file is how the next session avoids re-deriving state.
