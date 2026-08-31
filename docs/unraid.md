# Running Reelix on Unraid

Docker Compose, not an Unraid template. A template is a later job and it needs
a stable image first.

This document is the deployment half of the "does Reelix survive a real
library" work. The measurement half is `docs/measurement-0.0.2.md`, and it
depends on this being set up exactly as described — particularly the storage
placement, which will otherwise dominate every number you collect.

---

## Prerequisite

Unraid does not ship `docker compose`. Install **Docker Compose Manager** from
Community Applications. Everything below is ordinary compose after that.

---

## Why a published image rather than a build on the box

Both work. The registry is recommended, and the reason is specific to what this
deployment is for.

A host that builds its own image resolves its own Go patch release and its own
`jellyfin-ffmpeg7` package on whatever day it happens to build. That is fine on
the development VPS, where a rebuild is the point. It is not fine here, because
this box exists to produce trustworthy measurements: a rebuild puts a variable
between the artifact that was measured and the artifact that is running, at the
exact moment you are trying to remove variables.

The registry also makes the Unraid box a pure runtime, which is the Docker-first
premise in the constitution, and it is the path to a Community Apps template.

Build and publish from the VPS:

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml build app
docker push ghcr.io/maverickman79/reelix/server:0.0.1
```

Then on Unraid, `docker compose pull && docker compose up -d` runs those exact
bytes.

---

## Storage placement

**This is the decision that matters most, and it is easy to get wrong in a way
that looks like Reelix being slow.**

| What | Where | Why |
|---|---|---|
| `postgres` data | **Cache pool** | Every write to a parity-protected array costs a read-modify-write across the data disk and both parity disks. PostgreSQL writes constantly. |
| `/cache` (artwork) | **Cache pool** | Small — a few hundred KB per film, so a few GB across a large library. |
| `/config` | **Cache pool** | Tiny, but it belongs with the rest of the state. |
| `/media` | **Array, read-only** | The constitution requires `:ro` by default. Write access is requested only when a feature needs it, and none does. |

Set the `appdata` share to primary storage = **cache** with the mover
**disabled**. That is Unraid's own default for appdata; the point is to confirm
it rather than assume it.

### Bind mounts, not named volumes

`docker-compose.unraid.yml` swaps the named volumes for bind mounts under
`/mnt/user/appdata/reelix`. Unraid operators are routinely advised to delete and
recreate Docker's storage as a first troubleshooting step — standard forum
advice, not an exotic act — and a named volume goes with it, taking the database
and the whole identified library along. A bind mount under appdata survives, and
it is where an Unraid operator expects to find an application's state.

---

## Setup

```bash
mkdir -p /mnt/user/appdata/reelix/{postgres,config,cache}
chown -R 99:100 /mnt/user/appdata/reelix
```

`.env`, starting from `.env.example`:

```ini
REELIX_DB_PASSWORD=<generate one>
REELIX_TMDB_API_KEY=<your key>

REELIX_IMAGE=ghcr.io/maverickman79/reelix/server
REELIX_VERSION=0.0.1

# 0.0.0.0 is correct here: the LAN is the boundary. There is no default, so
# that a forgotten value fails at "compose up" rather than publishing a
# listener somewhere nobody intended.
REELIX_HTTP_BIND=0.0.0.0
REELIX_HTTP_PORT=8080

# nobody:users. The image's own default is 1000, which suits a desktop-Linux
# host and owns nothing on Unraid.
REELIX_UID=99
REELIX_GID=100

REELIX_APPDATA=/mnt/user/appdata/reelix
REELIX_MEDIA_DIR=/mnt/user/movies
```

Bring it up:

```bash
docker compose -f docker-compose.yml -f docker-compose.unraid.yml up -d
```

Port 8080 is normally free — Unraid's own web UI is on 80 and 443.

---

## Pointing at a subset

Mount the **whole** media share read-only and set the library path to a
subdirectory inside it. Widening the scope later is then a library-path change
rather than a remount, and the container never has to be recreated to measure
more films.

---

## `/mnt/user` versus `/mnt/diskN`, and why it matters for measurement

`/mnt/user` is a FUSE layer (shfs) that presents the array's disks as one share.
It adds per-syscall overhead that **looks exactly like per-file process
overhead** in a scan — which is one of the two things the measurement is trying
to tell apart.

Mount `/mnt/user` for normal running. When measuring, probe the same sample
through both `/mnt/user/...` and `/mnt/diskN/...` and compare. If they differ
materially, shfs is in your numbers and the wall-versus-CPU reading needs
adjusting for it before anybody concludes anything about concurrency.

---

## Known operational sharp edges

Carried from `docs/progress.md` because they land differently on a real library
than on six files.

- **A file that vanishes from disk is never removed from the library.** This is
  deliberate — a transient mount failure would otherwise wipe a library — but a
  genuinely deleted film stays in the browse list and 404s on play.
- **A re-probe is triggered by a size change, not mtime.** A file edited in
  place at the same length is never re-probed. Clearing `probed_at` forces one.
- **There is no update-library endpoint.** A library's path can only be changed
  with hand-written SQL.
- **A scan cannot fail.** Every file failing to probe still finishes as a
  `completed` job. See the measurement document; this is the expected first
  change after the numbers are in.
