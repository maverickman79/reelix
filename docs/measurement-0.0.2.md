# Measuring Reelix against a real library

Everything in 0.0.2 was verified against six files on SSD. This document is the
method for finding out what changes when it meets a few hundred real films on a
spinning, parity-protected array — and, more importantly, for making the answer
*measured* rather than assumed.

Deployment is `docs/unraid.md`. Read its storage-placement section first: get
that wrong and it dominates every number here.

**Status: method written, instrumentation landed, numbers not yet collected.**

---

## The question the numbers have to answer

Not "how long does a scan take". That number is not actionable on its own.

> **Where does scan time actually go, and therefore would concurrency help?**

The two candidate answers point in opposite directions:

- **Per-file process overhead** — `exec`, dynamic linking, demuxing headers.
  Concurrency helps roughly linearly up to the core count.
- **Disk seek** — the array head moving between thousands of files.
  Concurrent probes then *compete for one head*, and the scan gets **slower**.

Guessing wrong is not neutral. It is a change that makes things worse, on
hardware where it is slow and annoying to find out.

---

## How to tell them apart

### The primary discriminator: wall time against CPU time

`media.ProbeTiming` records both per ffprobe invocation. `User` and `Sys` come
from the kernel's own rusage via `wait4`, which Go exposes on `ProcessState`
after `Wait` — no profiler, no sampling, no overhead to subtract back out.

| Reading | Meaning | Implication |
|---|---|---|
| wall ≈ cpu | ffprobe is *working* | Process-bound. Concurrency helps ~linearly to core count. |
| wall ≫ cpu | ffprobe is *waiting* | I/O-bound. Concurrency on one spinning array makes it worse. |

**Baseline, measured on the VPS against the real six-film library, warm cache:**

| Size | wall | cpu | wall/cpu |
|---|---|---|---|
| 4.9 GB | 0.05s | 0.04s | 1.25 |
| 70.8 GB | 0.06s | 0.05s | 1.20 |
| 4.8 GB | 0.05s | 0.05s | 1.00 |
| 1.9 GB | 0.11s | 0.11s | 1.00 |
| 1.6 GB | 0.10s | 0.09s | 1.11 |
| 3.2 GB | 0.04s | 0.04s | 1.00 |

Two things to carry forward. **The ratio is ~1.0–1.25 on fast storage** — that
is what "process-bound" looks like, and it is the number Unraid gets compared
against. And **file size does not matter**: 70.8 GB probes as fast as 1.6 GB,
because ffprobe reads headers, not content. A scan whose cost tracks file size
is measuring something other than ffprobe.

Same code and same binary on Unraid, so any divergence there is the storage.

### Corroboration, none of which needs code

- **Warm-cache re-probe.** Clear `probed_at` on a fixed ~25-file sample and
  re-scan. Cold-versus-warm delta *is* the disk contribution. An order of
  magnitude means seek.
- **`/proc/pressure/io`** sampled during the scan. PSI `some avg10` is the
  cleanest available signal that the scan is stalled on I/O. Unraid's kernel
  has it.
- **`/proc/diskstats`** deltas per array device — or `iostat -x 5` if Nerd Tools
  is installed.

### Confounds to control

- **Spin the array up first.** Otherwise ~10s per disk of spin-up latency lands
  in the first numbers and looks like a slow probe.
- **`/mnt/user` is FUSE.** Its per-syscall cost looks exactly like per-file
  process overhead. Probe the sample through `/mnt/diskN` too. See
  `docs/unraid.md`.

---

## What the instrumentation emits

Per-file and per-item lines are `debug`; aggregates are `info`. An ordinary
scan is unchanged — set `REELIX_LOG_LEVEL=debug` for a measurement run.

| Line | Level | Carries |
|---|---|---|
| `walk complete` | info | `files`, `took` |
| `file probed` | debug | `path`, `size_bytes`, `probe_wall_ms`, `probe_cpu_ms`, `db_ms` |
| `scan completed` | info | counts, `took`, `walk_ms`, `probe_wall_ms`, `probe_cpu_ms`, `db_ms` |
| `item identified` / `item left unmatched` | info | `took_ms`, `provider_requests` |
| `identify pass cost` | info | `considered`, `provider_requests`, `took_ms` |
| `metadata fetched` | info | `took_ms`, `images_downloaded` |
| `metadata refresh complete` | info | counts, `took_ms` |

The three-way scan split — walk, probe, database — exists because "the scan took
an hour" does not say what to change. Each part is separately addressable, and
the walk in particular was previously logged with a file count and no duration
at all, despite plausibly being the expensive half on thousands of release
folders.

Identify counts **provider requests, not items**, because the alternative-title
fan-out makes one item cost anywhere between 1 and 12 requests.

---

## Run sequence

| # | Run | Purpose |
|---|---|---|
| 0 | Scan, discarded | Array spin-up, page cache, first-run noise |
| 1 | **Cold scan** | The real number |
| 2 | Immediate re-scan | Should probe nothing. Proves the skip logic and isolates walk-only cost |
| 3 | Warm re-probe of a 25-file sample | Cold-vs-warm delta = the disk contribution |
| 4 | Identify ×N until pending = 0 | `identifyBatch` is 200 per pass and a pass does not loop |
| 5 | Metadata refresh | Now walks every batch in one pass |

Run 4 needs repeating: the identify pass claims at most 200 items and reports
`completed` regardless. It makes progress each run because identified items
leave the pending set. Run 5 does not need repeating any more — see the fix
below.

---

## Queries for the outcome distribution

```sql
-- Where identification landed.
SELECT status, count(*) FROM media_item_identity GROUP BY status;

-- Why the declines declined. The distribution is the actionable part:
-- "no candidates" is a naming problem, "N candidates match exactly" is an
-- ambiguity the matcher correctly refused.
SELECT reason, count(*) FROM media_item_identity
 WHERE status = 'unmatched' GROUP BY reason ORDER BY count(*) DESC;

-- Artwork coverage, negatives included: a NULL storage_path is a recorded
-- "the provider has none", not a failure.
SELECT image_type,
       count(*) FILTER (WHERE storage_path IS NOT NULL) AS stored,
       count(*) FILTER (WHERE storage_path IS NULL)     AS none_available
  FROM media_item_images GROUP BY image_type;

-- Files that failed to probe. probed_at IS NULL is the retry queue.
SELECT count(*) FROM media_files WHERE probed_at IS NULL;
```

---

## The failure surface, as it stands today

Audited by reading the code, not by assumption. This is what a pass *actually*
does — the reason the failure audit came before any throughput work.

### TMDB, identify pass

| Failure | Today |
|---|---|
| Timeout (15s), 500, truncated or invalid JSON | Warned; item stays **pending**; retried next pass. Correct. |
| **200 with an empty `results` array** | Recorded **`unmatched` permanently**. `Pending` selects only `pending`, so it is never retried — it needs a manual `Reset`. |
| **401, wrong or expired key** | Warned per item; job **completes** with `matched=0, unmatched=0`. |
| 429 | `ErrRateLimited` stops the pass; job **failed**. Work so far is kept and the next pass resumes. |

The two bolded rows are the same shape of fault: **a failure that presents as
"nothing needed doing" rather than as an error.** A provider hiccup that returns
an empty result set is indistinguishable, at the database, from a film TMDB
genuinely does not have. A dead API key produces a pass that looks like it ran
and found nothing to do.

### Metadata refresh

Same shape. A failed fetch leaves no row, and the absent row *is* the retry
queue — deliberate, and documented at `storeImages`. A rate limit fails the job.
Database write errors fail the job, which is right.

### Scan

| Failure | Today |
|---|---|
| ffprobe hits the 2-minute timeout | Counted `failed`, `probed_at` stays null, retried next scan. Scan continues. Costs 2 minutes serially. |
| ffprobe blocked in uninterruptible I/O | `CommandContext` sends SIGKILL at 2 minutes, but `Run()` blocks in `Wait` until the kernel actually reaps it. On a hung shfs mount that can exceed the timeout arbitrarily. |
| Mount gone **before** the scan | `os.Stat(root)` fails, `Scan` errors, job **failed**. Correct. |
| Mount gone **mid-walk** | `WalkDir`'s error callback returns `SkipDir`/`nil`, so the walk returns **no error**. Truncated results, job **completes**, `discovered` silently wrong. |
| Mount present but empty | `discovered=0`, job completes. Nothing is deleted, so no damage. |
| Mount gone **after** the walk | Every probe fails; job still **completes**. |

---

## Expected first change after measurement: a scan that can fail

**There is no failure ratio at which a scan reports failure.** 900 files
discovered and 900 failing to probe finishes as a `completed` job with
`failed=900` in a log line nobody is reading.

This is deliberate at the level of one file — "one corrupt file must not cost an
operator the other nine hundred" is the right rule and should stay. What is
missing is the other end: no threshold above which the pass concludes that the
problem is not the files.

It matters more on real hardware than anything about throughput, because it is
the difference between a mount that dropped mid-scan announcing itself and one
that quietly indexes a fraction of a library and calls it done. On six local
files it could never happen; on a spinning array with a share that can go away,
it is a Tuesday.

It is deliberately **not** being fixed in the same work as the measurement, so
that it does not compete for attention with the numbers. It is the first thing
to pick up once they are in.

---

## TMDB rate limits

Checked against the published documentation rather than assumed.

The legacy 40-requests-per-10-seconds limit was **disabled in December 2019**.
TMDB's current published position is an upper limit "somewhere in the 40
requests per second range", explicitly subject to change at any time, with an
instruction to respect `429`. There is no documented `Retry-After` contract.
Community and CDN reports put the practical ceiling near 50 req/s with 20
connections per IP.

**Do the passes respect it?** Incidentally, yes — but not by design:

- `providerPause` is 250 ms **between items**: 4 items/s, far under the ceiling.
- The pause is between items, **not between requests**. One item can burst
  1 search + up to 10 alternative-title lookups + 1 external-IDs call — **12
  requests back-to-back, unpaced**. Still under the ceiling in practice, but the
  constant does not enforce what its name suggests.
- `429` is honoured only by failing the job. No backoff, no `Retry-After`, no
  resume.
- Image downloads go to a different host and are not the API limit.

At a few hundred films this needs no change. At thousands the pacing is still
not the problem — 4 items/s is ~42 minutes per 10,000 films. The batch cap and
the failure handling are.

---

## Fixed before measuring: the full refresh could not see past its first batch

`ItemsNeedingMetadata` was a `LIMIT` over a fixed `ORDER BY` with no offset and
no cursor, and `refresh()` called it once. With `?all=true` there is no "needs
work" predicate, so fetching an item does not remove it from the selection set:
every run re-selected the same first 200 films, and the 201st was unreachable by
any number of runs.

The default pass concealed this rather than escaping it — fetching an item does
remove it from the "never fetched" set, so a second run moved on by itself. That
is why a six-film library never showed the fault.

It was fixed first because it is a **precondition of the measurement**, not
scope creep: `?all=true` is the instrument for "one metadata refresh" over a
library larger than a batch, and it did not work. Keyset pagination on
`(created_at, id)`; regression tests shrink the batch rather than growing the
library, because at the real size of 200 the obvious test needs 200 films to say
anything at all.
