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
