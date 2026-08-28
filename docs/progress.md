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

## 2026-08-28 — metadata fields, and two guards that made each other untestable

**Completed:**
- 0.0.2 item 3, fields half: overview, community rating, official rating,
  release date and genres, each with per-field provenance and a lock.
- Four allowances retired in `fixture_test.go`.

**Live against real TMDB, all six films:**

| Film | Rating | Cert | Premiere | Genres |
|---|---|---|---|---|
| Congo | 5.62 | PG-13 | 1995-06-09 | Action, Adventure, Science Fiction |
| Fight Club | 8.44 | R | 1999-10-15 | Drama, Thriller |
| Gangland | 7.05 | **(empty)** | 2025-08-14 | Action, Crime, Drama |
| Idiocracy | 6.38 | R | 2006-09-01 | Comedy, Science Fiction, Adventure, Thriller |
| The Legend of Aang | 9.19 | PG | 2026-07-24 | Animation, Action, Adventure, Fantasy |
| The Singers | 6.80 | NR | 2026-02-17 | Music, Drama, Comedy |

**Gangland's empty certification is the region rule working**, not a gap: TMDB
has no US certification for it, and the field is left empty rather than filled
from another region. An operator who configured GB and was shown `R` could not
tell it was a US rating — it renders exactly like a real answer — so the wrong
value would be indistinguishable from the right one. An empty field is visibly
missing, which is the failure that gets noticed.

**The lock, proved end to end.** A hand-corrected overview on Fight Club
survived a full `?all=true` re-fetch — reported as `fields_skipped_locked=1` —
while its four unlocked neighbours updated from the provider.

### Two guards that made each other untestable

The most useful thing in this session, and it is a **design** finding rather
than a test one.

`WriteField` checked the lock twice: once in Go before writing, and again in
the UPDATE's `WHERE` clause. That reads as defence in depth. It is not.

**Fault injection removed each guard in turn and the suite stayed green both
times.** Two guards on one outcome mean removing either alone changes nothing
observable — the other still produces the same result — so neither can be
tested, and the pair can only be verified by deleting both. The redundancy did
not make the code safer. It made the safety unverifiable.

Both were replaced by **one** guard: `claimField`, an atomic conditional write
on the provenance row that succeeds only when the field is unlocked. Every
write goes through it, so there is one path to test and one line to remove to
make the tests fail — which it now does, failing both lock tests including the
genre list.

It is also strictly more correct than what it replaced. A lock read in Go and
acted on afterwards loses the race against somebody locking in between, and
"silently overwriting a locked field" is exactly what the constitution forbids.
Observing and claiming are now the same statement.

**This is the same family as the four instances catalogued in the entry below,
in a new dress.** Those were tests asserting an outcome reachable by more than
one path. This is *code* offering more than one path to the same outcome, which
produces the identical symptom. The generalisation:

> **Redundant enforcement is untestable enforcement.** If two mechanisms
> guarantee one outcome, no test can tell you whether either works. Prefer one
> guard you can delete and watch fail over two you cannot distinguish.

The counter-measure is the same: inject, and inject each part separately. Both
guards were written in good faith and each looked correct in isolation.

**Verified:**
- `gofmt`, `go vet ./...` clean. Full suite green with a database and without.
- **Fault-injected five ways.** Three caught immediately (an edit that does not
  lock, a default refresh that re-fetches everything, an absent provider field
  clearing a stored one). Two were NOT — one per redundant guard — which is
  what produced the finding above. After collapsing to one guard, removing it
  fails both lock tests.
- Live: migration 11 applied on restart; six films fetched; the lock survived a
  full re-fetch.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Decisions made:**
- **Runtime is deliberately not collected**, and the reason sits in
  `FetchMetadata` where someone will notice the omission, because TMDB returns
  a runtime field beside the ones that are collected. `RunTimeTicks` drives the
  seek bar and must describe the file, which ffprobe measured; the provider's
  runtime describes the work. They agree on an ordinary release and diverge on
  an extended cut or a PAL transfer — the files most likely to be misidentified
  in the first place. Its one genuine future use is as a **match-verification**
  signal: a large gap between the file's duration and the work's runtime is
  evidence the identity is wrong.
- **An edit locks the field it touches.** A default for one operation, not a
  merging of Source and Locked, which remain independent as the constitution
  models them. It implies a lock because a correction that silently reverts is
  one nobody makes twice — the reasoning that made a manual identity outrank
  every pass.
- **The default refresh considers only items never fetched**; `?all=true`
  forces a full re-fetch. Making the expensive form explicit means nobody
  discovers a per-film request across 13TB by running the obvious command.
- **A field the provider does not know is skipped, not written null.**
  Overwriting a good value with "the provider has nothing" loses it to a bad
  fetch.
- **`CriticRating` keeps its allowance**, with a corrected reason: TMDB
  publishes no critic score, so it has no source rather than an unfetched one.
  It also replaced Overview as the example in `TestAbsenceNeedsAnAllowance`,
  which had to change because Overview stopped being an allowed field — the
  allowance list doing its job.
- **`ProductionYear` prefers the provider's release year**, falling back to the
  parsed one. A boundary decision: `media_items.year` stays as parsed because
  it is the matcher's input.

**Next step:**
- **Artwork**, the remaining half of item 3, and a different kind of problem:
  storage and serving rather than fetching. The two landmines cleared earlier
  were clearing the way for exactly this, and both now need using rather than
  merely being fixed — the image routes are unauthenticated and the type is
  canonicalised, so the handler has somewhere to look an image up.
- Confirm on the SK1 that the fields render. Unlike the `ProviderIds` change
  this one **should** be visible: overviews, ratings and genres on the detail
  screen.

---

## 2026-08-28 — alternative titles, and the evidence base that replaces the hand-resolved list

**Completed:**
- Alternative-title matching, gated and year-windowed.
- `matched_via` on `media_item_identity`, which is the point of the entry below.

**The whole library, reset and re-identified live against real TMDB:**

| Film | Status | Confidence | Via | TMDB |
|---|---|---|---|---|
| Congo (1995) | matched | exact | primary | 10329 |
| Fight Club (1999) | matched | exact | primary | 550 |
| Gangland (2025) | matched | exact | primary | 1147610 |
| Idiocracy (2006) | matched | exact | primary | 7512 |
| **The Legend of Aang (2026)** | **matched** | **exact** | **alternative** | **980431** |
| The Singers (2026) | matched | exact | primary | 1442908 |

**6/6, and every id identical to the run before.** The film that needed hand
resolution now identifies on its own, to the same id a person chose, and no
existing match moved.

### Why this is not "widening the matcher"

The comparison is unchanged. An alternative title is compared with the same
exact equality the primary title gets — **more places to look, not a looser
look.** A looser comparison invents matches; more exact comparisons can only
find matches that were already there.

Measured before building, across all six films: five outcomes identical, one
`none` → `exact`. Nothing became ambiguous, nothing moved.

### The Gangland finding, which is now in the code

Searching "Gangland" returns our 2025 film on its primary title **and**
tmdb 870843 — a different 2018 film whose US alternative title is also
"Gangland". The year gap keeps it out of every tier, so today nothing changes.
**Had our release been a 2018 one, this pass would have found two candidates
called Gangland at the same tier and refused to choose — turning a match into
a decline.**

That is the real cost of the change: **alternative titles enlarge the candidate
pool, and a larger pool can manufacture ambiguity.** It is acceptable only
because the matcher declines rather than guesses, so the worst case is a
*visible* unmatched item and never a wrong match.

**The reasoning lives at the call site in `withAlternativeTitles`, not only
here**, because the extra request is where somebody will ask why it is gated on
a year window. It also says what breaks the argument: *if the matcher is ever
changed to break ties, this call becomes a way to attach a watch history to the
wrong film.*

### The evidence base, and the thing most likely to erode

The justification for declining rather than guessing has rested on the
**hand-resolved list** — the films a pass declined that a person had to
identify manually. One in six on the first real library, and it was a renamed
release. A short list means the threshold is right; a long one would mean it is
set wrong.

**This change empties that list.** That is the point of it, and also the
problem: the measurement that justified the threshold disappears along with the
failure it was measuring. Without a replacement, "we decline rather than guess"
becomes an assertion nobody can check — and the person most likely to need to
check it is the one staring at a hundred unmatched films in a much larger
library, deciding whether to loosen something.

`matched_via` is the replacement, and it is the reason a column was added for a
value nothing branches on. **A film matched via an alternative title is one the
old matcher would have declined**, so counting them answers the same question
the hand-resolved list answered, without anyone doing the work by hand to
generate the evidence:

```sql
SELECT matched_via, count(*) FROM media_item_identity
 WHERE status = 'matched' GROUP BY matched_via;
```

Today: `primary 5`, `alternative 1`.

**How to read it later, stated now while nothing is at stake:**

- **A small `alternative` count and a short unmatched list** means the threshold
  is right. Nothing to do.
- **A large `alternative` count** means renamed releases are common in this
  library. That is the change working, not a problem.
- **A long unmatched list** is the case that matters, and the instinct will be
  to loosen the comparison. **Read the reasons first.** They are stored per row
  precisely so the failures can be counted by shape. If they are ambiguity
  declines, loosening makes it *worse* — more candidates, more ties, more
  refusals. If they are `no_title_match` on names no provider publishes in any
  form, the answer is better input (the filename parser, or another provider),
  not a fuzzier comparison.
- **The failure shape is the next piece of evidence, not a reason to widen.**
  Every safe change so far has been of the form "compare exactly against more
  things"; every dangerous one is of the form "compare less exactly". The
  hand-resolved list is gone, but that distinction is the thing it was
  protecting, and it survives it.

**Verified:**
- `gofmt`, `go vet ./...` clean. Full suite green with a database and without.
- **Fault-injected four ways, each caught**: removing the decline gate (an
  ambiguous decline then cost two needless lookups), removing the year window
  (caught at both the helper and the service level), breaking a tie instead of
  declining, and never recording `alternative` provenance.
- **One injection broke the build, which is not a result.** Redone so it
  compiled before being believed — the rider written into the previous entry,
  applied the first time it came up.
- Live: all six reset and re-identified; migration 10 applied on restart.
- Call counts are asserted by unit test rather than counted over the wire: zero
  alternative-title lookups for a film matching on its primary, exactly one for
  the rescue, none for an ambiguous decline.
- **`POST /libraries/{id}/identify` driven live by a real caller** (run
  separately, alongside this work), which closes the last unverified path in
  the identity slice. `DELETE /items/{id}/identity` returned Congo to pending;
  the POST answered **202** with a job id; the job completed at progress
  **1/1**, which is the part worth recording — it confirms the pass scopes
  itself to pending items rather than re-walking the library; and the GET
  returned `matched` / `exact` / tmdb 10329 / imdb tt0112715. Every layer of
  the identity slice has now been exercised by the path a real caller uses.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Decisions made:**
- **A column was added for a value nothing reads**, which normally would not
  earn a migration. It earns one here because it is the successor to the
  evidence base this change destroys, and an argument that cannot be checked is
  one that gets overridden by whoever is most frustrated.
- **Not backfilled.** Rows written before migration 10 carry NULL. Backfilling
  would mean re-identifying films that are already correct in order to learn how
  they were found; the counts are read forward.
- **Only `no_title_match` triggers the second pass.** An ambiguous decline is
  not retried, because more titles can only deepen an ambiguity. A
  `year_mismatch` is not retried either: the title matched, so the film family
  was found and the failure is not a naming one. Both are narrower than they
  could be, deliberately.
- **All regions' titles are used, not a locale subset.** Reelix does not know
  which region a file came from, and guessing would drop the correct title for a
  release the operator actually has.

**Next step:**
- Confirm on the SK1 that nothing regressed now items carry `ProviderIds`.
  Wholphin reads it in one place, for external links it then omits, so the
  expected result is **no visible change** — worth confirming precisely because
  it should be invisible.
- Then the fields that hang off an identity, starting with artwork.

---

## 2026-08-28 — the identify pass runs, and the dominant test failure mode named

**Completed:**
- TMDB key in `.env` (gitignored, never committed), placeholder in
  `.env.example`, and passed through `docker-compose.yml` with the `:?` form so
  a missing key fails at `compose up` rather than as a container that restarts
  forever.
- **The identify pass ran against real TMDB over the six-film library.**
- The one decline resolved by hand.
- **The recurring test failure mode written up as a pattern**, below.

### The pass: five of six matched, every one `exact`

| Film | Status | Confidence | TMDB | IMDb |
|---|---|---|---|---|
| Congo (1995) | matched | exact | 10329 | tt0112715 |
| Fight Club (1999) | matched | exact | 550 | tt0137523 |
| **Gangland (2025)** | **matched** | **exact** | **1147610** | **tt28263483** |
| Idiocracy (2006) | matched | exact | 7512 | tt0387808 |
| The Singers (2026) | matched | exact | 1442908 | tt33508491 |
| The Legend of Aang - The Last Airbender (2026) | **unmatched → manual** | — | 980431 | tt18259538 |

**Gangland is the answer to the question that was asked of it.** The reference
identified it as tmdb 1147610 / imdb tt28263483; Reelix produced the same two
ids at `exact` confidence. **We are not stricter than the reference here** — the
matcher is doing the ordinary thing on ordinary input, and the conservative
policy costs nothing on five of six films.

A second pass reported `matched=0 unmatched=0`: nothing already decided is
re-asked, and the manual row survived it.

### The one decline, and why it is evidence the threshold is RIGHT

`The Legend of Aang - The Last Airbender` was declined with:

> no candidate title matches "The Legend of Aang - The Last Airbender"
> (1 returned, best was "Avatar Aang: The Last Airbender")

Resolved by hand to tmdb 980431 / imdb tt18259538. **The decline was correct on
the evidence the matcher had.** TMDB's primary title for 980431 is
"Avatar Aang: The Last Airbender"; its US *alternative* title is
"The Legend of Aang: The Last Airbender" — our filename exactly, differing only
by `:` against ` - `, which the normaliser already folds. The matcher never saw
it, because a search response carries the primary title only.

So the gap is **missing input, not an over-tight threshold**, and the fix is
not to loosen matching. If this recurs, the evidence-backed change is to ask
the provider for alternative titles and match against those too — a strictly
larger set of *exact* comparisons, which is a different thing from a fuzzier
comparison.

**What loosening would have cost, concretely.** A search for "Aang" returns
`The Last Airbender (2010)` — the live-action film — in the same result set. A
substring or article-stripping matcher has a real path to that answer, and
would have attached this file's imported watch history to a different film with
no visible symptom.

**Hand-resolved list, which is the evidence base going forward: one film, and
it was a renamed release.** That is the number to watch. One in six on a
deliberately awkward library is a threshold doing its job; a majority needing
hand resolution would be a threshold set wrong.

### The pattern: a test that never reaches the line it guards

**Four instances now, and it is the dominant failure mode in this project's
tests.** Stated here as a pattern rather than a fourth incident.

**The shape.** A test asserts an outcome that is reachable by more than one
path. The path it was written to guard is one of them. Some other path — a
default, an earlier filter, an empty fixture, a coincidence of types — reaches
the same outcome without touching the guarded code. The assertion is true
either way, so the test passes whether or not the thing it was written for
works at all.

**The four:**

| Instance | Asserted | Reached by |
|---|---|---|
| `displayChannelLayout` | the helper normalises `5.1(side)` | the helper directly; nothing checked the DTO called it |
| `TestVidHubStreamRequest` | a 206 on a lowercase stream URL | the bare `/stream` route, the extension having landed in the query string |
| `TestManualIdentitySurvivesAPass` | a manual identity survives a pass | the `Pending` query never returning it; the guarded write was never called |
| `TestItemDetailEmitsIdentity` (this session, caught pre-merge) | `ProviderIds` is correct | every seeded item having no identity, so `{}` was right either way |

**How to spot it in advance.** Ask of every assertion: *what else could make
this pass?* Three questions catch all four:

1. **Is the expected value also the zero value, the default, or the empty
   case?** `{}`, `false`, `0`, `nil`, an empty list. If the fixture never
   supplies a non-default value, the assertion cannot distinguish a working
   implementation from an absent one. `IsDefault` was unreachable this way for
   an entire milestone, because `false` and `true` are the same JSON type.
2. **Does the test set up the state that forces execution through the guarded
   line?** A guard on a manual row needs a manual row *and* a call that hits
   the guard. Skipping the item and being refused by the guard produce
   identical observable outcomes.
3. **Am I testing a helper, or testing that something calls it?** These are
   different tests and the second is the one that regresses.

**The counter-measure is unchanged and is the only reliable one: inject the
fault and watch the test fail.** All four were found that way and none by
reading. Two riders learned this session:

- **Inject each half separately.** Removing the manual guard failed nothing;
  removing only the `RowsAffected` check that followed it failed exactly one
  assertion. Injecting both at once would have hidden which half was load
  bearing.
- **Confirm the injection actually landed.** One injection here reported a pass
  because the `sed` had not matched anything. *A fault injection that fails to
  apply is indistinguishable from a test that catches nothing.* Print the
  patched line, or assert the build broke, before believing a green result.

**Verified:**
- `gofmt`, `go vet ./...` clean. Full suite green with a database and without.
- Live pass against real TMDB, results in the table above; second pass a no-op.
- Two new compat tests, both halves of the DTO wiring injected separately and
  each caught. The control test — an unidentified item emitting `{}` and `[]` —
  is kept deliberately and is documented as unable to discriminate on its own.

**In flight:**
- Nothing.

**Blocked:**
- Nothing.

**Decisions made:**
- **The matcher was NOT widened**, per the standing instruction, and the
  investigation supports the instruction rather than merely obeying it: the
  decline was right on the input available.
- **`docker-compose.yml` uses `${REELIX_TMDB_API_KEY:?...}`**, matching
  `REELIX_DB_PASSWORD`. Without it the container starts, fails config
  validation, and restarts forever — the loud failure is only loud if it
  happens where somebody is looking.
- **The identify pass was driven through the service layer, not the HTTP
  route.** `POST /libraries/{id}/identify` needs the administrator password,
  which that session did not have. **CLOSED the same day** — the route was
  driven live by a real caller and answered 202 with a job that completed at
  progress 1/1; see the entry above.

**Next step:**
- Drive `POST /api/v1/libraries/{id}/identify` live with the admin password, to
  close the one unverified path:
  `curl -X POST -H "Authorization: Bearer $TOKEN" http://100.95.0.122:8080/api/v1/libraries/01a03fa4-09ab-76e1-a134-5068f65d4074/identify`
- Confirm on the SK1 that nothing regressed now that items carry `ProviderIds`.
  Wholphin reads it in one place, for external links it then omits, so the
  expected result is no visible change — which is worth confirming precisely
  because it should be invisible.
- Then 0.0.2 item 3 proper: the fields that hang off an identity — titles,
  overviews, ratings, artwork — starting with the artwork decision the two
  fixed landmines were clearing the way for.

---

## 2026-08-28 — 0.0.2 item 3 begins: identity, and a bug the injection found

**Completed:**
- The two artwork landmines, resolved ahead of scraping. Live-verified.
- **Metadata scraping, identity slice.** Provider interface, matcher, TMDB
  client, schema, the identify pass, native API, and `ProviderIds` on the
  compatibility surface. Fields hang off this later; none are fetched yet.

**The two landmines, both grounded in probes rather than inference:**

| Request, no credential | Reference | Reelix before | Reelix now |
|---|---|---|---|
| `/Items/{id}/Images/{type}` | 400 / 404, never 401 | **401** | 404 |
| `.../Images/primary` vs `/Primary` | one type | two | one |
| `/Items/{id}` (control) | 401 | 401 | 401 |

The 401 was reproduced in the SK1 session log: a browse grid answered 404 six
times *with* a token, and five seconds later the playback screen answered 401
*without* one. Artwork is public on a real Jellyfin, so both fixes **match**
the reference rather than relaxing something it enforces. The image routes are
now the second deliberately unauthenticated exception after the stream
endpoint, and they are **not** its capability model — there is nothing to
protect yet, and whoever adds artwork decides then.

The thirteen valid image types were enumerated by probing: an unknown type
answers 400 with a validation body naming it, a known one falls through to the
id and answers 404. `Poster`, `Cover` and `Fanart` are the controls, all three
rejected.

### The identity model, and why it is shaped this way

**Three states, not a nullable id.** `pending` / `matched` / `unmatched` /
`manual`. "No TMDB id" would otherwise mean both "never attempted" and
"attempted and found nothing", and the importer has to retry the first and
leave the second alone.

**The matcher declines rather than guesses.** Exact normalised title equality
plus a unique candidate at the best tier (exact year, within one, or title
alone). An ambiguous tier ends the decision instead of falling through to a
weaker one, because a weaker tier cannot resolve an ambiguity a stronger one
could not — it can only produce a single answer the stronger evidence says is
wrong. Provider ranking never breaks a tie.

**The year is not sent to TMDB as a filter**, though it would return a shorter
list. Filtering server-side hides both a legitimate off-by-one release year
and a second candidate sharing the title — and hiding the evidence of
ambiguity manufactures a confident answer out of a situation that did not
deserve one.

**`ProviderIds` spellings were probed, and nothing we hold pinned them.** Every
fixture carries `{}` because the captured library had no metadata. A film
identified on a live 10.11.8 sends **three different spellings of two
providers in one response**: the key is `Tmdb`/`Imdb`, the `ExternalUrls`
display name is `TMDB`/`IMDb`, and Reelix stores `tmdb`/`imdb`. Values are
JSON strings even when numeric, which is why `external_id` is a text column.

### The bug, and the gap that hid it

`RecordMatch` guards its UPDATE with `status <> 'manual'` but wrote the
external ids **unconditionally**. A pass racing a human correction therefore
left an item marked `manual` while carrying the pass's TMDB id — the status
saying a person decided and the ids saying otherwise. `FindByExternalID`
believes the ids, so an imported watch history would resolve onto the wrong
film: **the exact failure the manual state exists to prevent.**

**The test that was supposed to cover this could not reach it.**
`TestManualIdentitySurvivesAPass` proves the pass *skips* manual items, which
it does — via the `Pending` query, which never returns them. So the write
itself was never exercised, and **removing the guard entirely left the whole
suite green.** Only the fault injection revealed that, and the fix needed a
new test reproducing the race by calling the repository in the order the race
produces.

This is the third time the same shape has appeared, and it is worth naming:
`displayChannelLayout` tested the helper and not its caller, `TestVidHubStream`
asserted 206 on a request exercising nothing, and this asserted the path that
avoids the code rather than the code. **A test that passes because it never
reaches the guarded line is indistinguishable from one that passes because the
guard works.** Only injection separates them.

**Verified:**
- `gofmt`, `go vet ./...` clean. Full suite green with a database and without.
- **Fault-injected twelve times across five packages**, each caught by its own
  test — except the manual-overwrite injection, which was caught by nothing
  and is what found the bug above.
- Discriminating injections, which are the ones that show a test measures the
  behaviour rather than merely failing: dropping the credential redaction fails
  only the leak test; dropping the canonicalisation fails `primary`/`PRIMARY`
  while `Primary` stays green; removing the `RowsAffected` check fails only the
  ids assertion while the status assertion still passes.
- Artwork fixes live through the rebuilt container: all spellings fold to
  `Primary`, unauthenticated is 404, an unknown type echoes as sent, and
  `/Items/{id}` still 401s.
- Startup without `REELIX_TMDB_API_KEY` exits 1 naming the variable, before
  touching the database.

**In flight:**
- Nothing.

**Blocked — and this one needs an action before the next restart:**
- **`REELIX_TMDB_API_KEY` is required and `.env` has it empty.** The running
  container is still on the previous binary, so it is unaffected, but **the
  next rebuild will refuse to start until a key is filled in.** That is the
  requested behaviour, not a fault. Nothing has run against the real TMDB yet:
  no live pass, and no confirmation that the six-film library identifies.

**Decisions made:**
- **The key is required to boot, and the consequence is deliberate.** An
  instance that only browses and plays local files now needs a TMDB key to
  start. That is the cost of finding out at startup rather than on the first
  item of a pass, which is what the ffprobe precedent asks for.
- **Reachability is NOT a startup condition**, and the asymmetry with ffprobe
  is the point. ffprobe is a local binary, so its absence is permanent and
  knowable. Refusing to start a media server because a metadata API is
  unreachable would turn someone else's outage into ours.
- **Identification is its own pass.** Migration 9 widens the job kind and
  changes the active-job index to one job *per kind* per library. Forbidding
  two concurrent scans stays right; also forbidding an identify pass during a
  scan was an accident of the old index.
- **Nothing from the provider is stored except ids.** The search response
  carries a title and a year and they are used for scoring only. Writing the
  provider's title over the parsed one is field fetching, which is the next
  slice.
- **`ExternalIDs` is on `ItemDetail` and deliberately not on `Browse`.** The
  list DTO has no `ProviderIds` field, so loading them per listing would have
  run a query per page to render nothing. Built, then cut.
- **Hand-written accent table, no `x/text`**, the same call as `tracklabel.go`'s
  ISO 639 table. Articles are deliberately NOT stripped: "The Thing" and
  "Thing" are different films.

**Next step:**
- Put a TMDB key in `.env`, rebuild, and run one live pass over the six-film
  library. The expected result is the interesting part: `Congo`, `Fight Club`
  and `Idiocracy` should match exactly, and `The Legend of Aang - The Last
  Airbender` is expected to be left **unmatched**, because the file is a
  renamed release and the matcher will not guess. The reference instance
  identified `Gangland` as tmdb 1147610 / imdb tt28263483, so that one is a
  usable cross-check: if Reelix declines it, the matcher is stricter than the
  reference rather than wrong.
- Then decide whether the unmatched ones are resolved by hand or whether the
  parser needs to feed the matcher a better title. **Resist widening the
  matcher to fix them** — that is the change that turns visible gaps into
  silent wrong answers.
- Also still open, unchanged: the `/socket` decision, and the "Recently Added"
  observation from the previous entry.

---

## 2026-08-28 — the deferred hardware batch closes, and 0.0.2 gets a scope doc

**Completed:**
- **All three deferred hardware checks pass on the SK1.** This closes a batch
  carried across three sessions.
- `docs/mvp-0.0.2.md`, new. 0.0.2's definition of done existed only as a list
  buried in the v0.0.1 retrospective, which is not where a future session
  looks.
- `CLAUDE.md`: current milestone is 0.0.2, items 1 and 2 marked done, document
  table repointed.
- `CLAUDE.md` step 7 no longer says to run a linter that does not exist.

**The three confirmations, verbatim from the device:**

1. **Idiocracy resume, end to end.** Plays, stops, appears in Continue Watching
   at the right position, resumes from there, and passing the end removes it
   from that row. The database agrees: `played=t`, `play_count=1`,
   `position_seconds=0`.
2. **The Singers reads `English DD+ 5.1 (Default)`.** The nulls are gone. It is
   the file's only audio track, which is correct for a single-stream WEB-DL.
3. **Fight Club's picker shows `DTS-HD MA 5.1` and the commentary tracks**,
   each named by who is speaking, **with the default track pre-selected.**

**`IsDefault` is now verified by the only means available**, and this is the
part worth carrying. `IsDefault` and `IsForced` are **structurally
unconfirmable by the fixture suite**: `false` and `true` are the same JSON
type, so the comparison passes identically on a wrong answer. No amount of
test-writing could have closed this one. A real client on real hardware was
always the only instrument that could read it, and it has now read it. When a
field's *value* drives client branching rather than its type, plan for hardware
from the start rather than discovering the gap afterwards.

**The 43 Mbit/s ceiling is a property of one link, not of the profile
decision.** Fight Club direct-plays on the SK1. The stall recorded last session
was the Shield's Tailscale path at the second house. `docs/mvp-0.0.2.md` says
so where the measurement is quoted, because "the 76 GB remux stalls" reads as a
statement about the file if the link is not named alongside it.

**One observation not yet explained, and it is a client question rather than a
server one.** The report is that a finished Idiocracy "leaves it in Recently
Added". Server-side the exclusion demonstrably works: across the completion at
16:17:29, `/Items/Latest` went from 4795 to 4009 bytes and stayed there two
minutes later, `/UserItems/Resume` returned an empty result, and
`handleLatestItems` sets `ExcludePlayed` unconditionally. No other browse
endpoint was called in that window. So whichever row still shows the film is
either fed by something other than `/Items/Latest` or is a stale client-side
render. **Not recorded as a bug in either direction** — one glance at the SK1
next time it is out settles it.

**Verified:**
- Documentation only in this change; no code touched, so no test run applies.
- The playback-state claim was checked against the database rather than taken
  from the screen, and the `/Items/Latest` byte drop was read from the app log.

**In flight:**
- The two artwork landmines, fixed but not committed at the time of writing —
  see the next entry.

**Blocked:**
- Nothing.

**Decisions made:**
- **The wording was fixed rather than a linter added.** `CLAUDE.md` step 7 had
  demanded "the project linter" since the beginning and there has never been
  one: no `.golangci*`, no `Makefile`, no CI directory, nothing installed.
  Adding `golangci-lint` is a real toolchain decision under the dependency
  rules in `docs/constitution.md` — a version pin, a config, a first-pass
  triage of an existing codebase, and somewhere to run it — and it does not
  belong inside a documentation fix. It is also **not what has been catching
  the bugs here**: the last four were found by fault injection, by probing the
  reference, and by reading a client bundle, and no linter would have found any
  of them. Propose it on its own merits when a JS toolchain arrives with the
  admin GUI. A rule nobody can follow is worse than no rule.
- **Multi-client validation being pulled forward is recorded as a plan change,
  not an accident.** It was scheduled after 0.0.2 and happened between items 2
  and 3, driven by real clients failing against the running server rather than
  by anticipation. `docs/mvp-0.0.2.md` has a section for it so nobody
  reconstructs it from the git log.

**Next step:**
- The two artwork landmines, resolved ahead of scraping rather than during it.
- Then metadata scraping, **identity first**: the provider interface and
  external IDs, before any field fetching. Artwork and the watch-history
  importer both sit on that model.

---

## 2026-08-27 — Gangland: the container asymmetry was half right

**Completed:**
- `mediaSourceContainer`, used by the media source DTO only. `containerName`
  and the item-level DTO are unchanged.
- `queryValue`: query parameter lookup ignoring case and underscores, used for
  the stream credentials and for BitrateTest's `Size`.
- **Gangland plays in jellyfin-web. Three clients now play: Wholphin, VidHub,
  and the reference client.** Pushed to origin/main.

**The bug, which was two bugs.** jellyfin-web direct-played an MP4 and
requested `/Videos/{id}/stream.mov,mp4,m4a,3gp,3g2,mj2` → 401. Both faults had
to be fixed for playback; each was fault-injected separately and caught by its
own test.

**1. The media source reported ffprobe's raw list.** Probing a real 10.11.8
with one file per extension — all three of the mp4 family probing as the same
ffprobe string — shows the reference splits the two levels:

| extension | `Item.Container` | `MediaSource.Container` |
|---|---|---|
| `.mp4` | `mov,mp4,m4a,3gp,3g2,mj2` | `mp4` |
| `.m4v` | `mov,mp4,m4a,3gp,3g2,mj2` | **`mov`** |
| `.mov` | `mov,mp4,m4a,3gp,3g2,mj2` | `mov` |
| `.mkv` | `mkv` | `mkv` |

**The Step 6 asymmetry was half right and should not be recorded as a
mistake.** `containerName` matches the reference exactly at the item level,
including leaving the mp4 list alone. What was wrong is that one function was
applied at *both* levels. Item-level behaviour is unchanged and now has a test,
because "tidying" it to `mp4` would diverge from a field the fixtures record.

The rule fitting all four observations: **the extension when it appears in the
list, otherwise the first token.** `.m4v` → `mov` is the case that rules out
the obvious "just use the extension" shortcut.

**2. The 401 rather than a 404, which is the more general finding.** The
extension normalisation was never at fault: `normalizeStreamSpelling` rejects
only a *dotted* multi-part extension, so the comma list came off cleanly and
the route matched. `authorizeStream` then could not see the credential —
**jellyfin-web sends `Tag` and `ApiKey`, Wholphin sends `tag`, the convention
is `api_key`, and Go's query lookup is an exact map hit.** The request carried
a valid capability and was answered 401.

A real server accepts `api_key`, `API_KEY`, `ApiKey`, `apikey`, `APIKEY` and
`Api_Key` alike — all probed. `queryValue` now matches that, and replaces the
two-spelling hack on `Size` from the previous session, which was the same bug
found by luck rather than by looking.

### The query-parameter case bug is a CLASS, and it was hit twice first

This is the point most worth carrying forward, because it will recur with the
next client.

The fragile exact-name lookup entered in **Step 7** (`b2a7fee`, direct play),
narrowing a Step 5 *decision* about query-string credentials — the decision is
Step 5's, the code carrying the bug is Step 7's. It then went undiagnosed
through **two separate encounters, because both times it presented as
something else:**

1. **Previous session, as `/Playback/BitrateTest`.** Presented as a missing
   route and a retry storm. The capitalised `Size` was noticed only
   incidentally while reading the client bundle for a different reason, and was
   patched *locally* by reading both spellings. Treated as a quirk of one
   parameter. **It was the same bug, and the local patch is what hid it.**
2. **This session, as Gangland.** Presented first as a container bug, then as
   an authentication failure — a 401 on a request carrying a perfectly valid
   capability. Only reading the bundle's URL construction showed `Tag` and
   `ApiKey` and connected it to the `Size` patch.

Both encounters were one defect: **Go's query lookup is an exact map hit, and
clients do not agree on the spelling of parameter names.** A real server
accepts `api_key`, `API_KEY`, `ApiKey`, `apikey`, `APIKEY` and `Api_Key`
alike — all probed. `queryValue` now matches that and absorbs the `Size` patch.

**The general lesson, which is the reason this is written down:** a parameter
read by exact name does not fail loudly. It fails as *absence* — the server
behaves as though the client never sent it — so it surfaces as a missing
feature, a wrong default, or a credential rejection, and never as "bad
parameter name". Any of those symptoms on a new client should make this the
first hypothesis, not the last.

**What was deliberately left alone:** `browseQuery` and the `client`/`userId`
reads still match exact names, because every observed client agrees on those
spellings. That is a judgement, not an oversight — converting the whole surface
blind would be a speculative refactor. Convert a read when a client is observed
disagreeing with it.

**Decisions made:**
- **The capability check was NOT relaxed.** The reference server was probed
  serving these bytes to a request carrying no credential at all, and even one
  carrying a *wrong* tag — `/Videos/{id}/stream` is effectively unauthenticated
  on a real Jellyfin. Reelix is deliberately stricter, and
  `TestStreamStillRefusesABadCredential` pins that this fix changed the
  spelling of the lookup and not the model. Do not "align with the reference"
  here without deciding to give up the capability model.

**Verified:** `gofmt`, `go vet ./...` clean; full suite green. Both fixes
fault-injected independently and each caught; under the credential injection
the Wholphin spelling stayed green, which is what shows the test discriminates
rather than just failing.

**Next step:** the `/socket` decision, which the entry below left the evidence
for and neither session has taken. Nothing is blocked on it — the socket does
not gate rendering or playback on any of the three clients.

---

## 2026-08-27 — jellyfin-web: the spinner, and five 404s triaged

**Completed:**
- `GET /DisplayPreferences/{prefsId}`, replacing the `default` literal. **This
  was the spinner.** jellyfin-web's `setUserInfo` awaits
  `/DisplayPreferences/usersettings` with no rejection handler, and
  `localusersignedin` fires only on fulfilment — so the 404 stopped the
  post-login chain before it ever reached `/UserViews`.
- `POST /Sessions/Capabilities/Full`. **Included for cohesion, NOT because it
  blocks anything** — jellyfin-web neither awaits this call nor handles its
  rejection, so its 404 cost an unhandled promise rejection and nothing else.
  It is the same login exchange and lands on an existing `SetCapabilities`.
  Do not read its presence in the same commit as evidence it was blocking.
- `GET /System/Endpoint` and `GET /Playback/BitrateTest` (second commit).
- `GET /Branding/Configuration`, unauthenticated, byte-identical to the
  reference.

**Verified:**
- `gofmt`, `go vet ./...` clean; full suite green with a database and without.
  Note: **CLAUDE.md refers to "the project linter" and there is none** — no
  config in the repo, nothing installed. `gofmt` and `go vet` are the whole
  toolchain. Either add one deliberately or fix the wording.
- Live through the nginx proxy: all five routes answer; branding matches the
  reference byte for byte.
- **The browser itself has not been re-driven.** The chain that was blocking
  is exercised end to end by tests against the real server and database, but
  whether the library renders needs a reload by someone with a browser on it.

**How the shapes were established — and a correction to a stated premise:**

The reference server was **not** running at the start of the session, contrary
to what the task assumed. It was brought up. The Step 0 wizard is complete, so
every `/Startup/*` route answers 401 and a throwaway account could not be made
there without the admin password. A **pristine 10.11.8 instance** was therefore
run alongside it on `:8098` with its own empty config — user `probe`, password
`probe-probe-2026`, plus a `probe2` used to test user-scoping. It is a
throwaway: `docker rm -f reelix-jellyfin-probe` and the scratchpad config are
the whole of it. **The Step 0 instance was never modified.**

None of the five routes appears in the Step 0 capture, because Wholphin calls
none of them. Every shape here came from probing.

**Two things probing caught that inference would have got wrong:**

1. **`/Branding/Configuration` omits null strings rather than sending them.**
   Setting branding on a reference instance and reading it back showed a null
   dropped field by field while an empty string is emitted. Reelix has no
   branding, so the entire correct response is `{"SplashscreenEnabled":false}`.
   Writing all three fields — the obvious guess — sends a shape no real server
   sends.
2. **The DisplayPreferences key is CASE-SENSITIVE.** `default`, `DEFAULT` and
   `Default` are three separate records with three different ids on a real
   server. That is why the key is a path parameter and not a literal: a literal
   would fold `/displaypreferences/DEFAULT` onto `default` and merge records
   the reference keeps apart. **`routefold_test.go`'s expectation was wrong on
   this and was corrected**, with the reason recorded in the test.

**Decisions made:**
- **The DisplayPreferences `Id` is derived, not reproduced.** The reference
  computes it from the key by a hash that is *not* a plain MD5 (tested and
  ruled out). Working the algorithm out would mean reconstructing a
  server-side implementation detail, which is the wrong side of the clean-room
  rule. Reelix reproduces the observable properties instead — stable per key,
  distinct between keys, identical across users, UUID-shaped — and the client
  only ever echoes the value back.
- **Capabilities decoding is more lenient than the reference.** The reference
  validates `SupportedCommands` against its enum and answers 400; Reelix stores
  the strings. It acts on none of those commands in 0.0.1, so rejecting a
  client for advertising a newer capability than our copy of the list would
  break it over a value nothing reads.
- **`/System/Endpoint` ignores `X-Forwarded-For`.** Behind a proxy it therefore
  reports the proxy's network, not the caller's — the safe direction for a
  header nothing authenticates, and wrong in exactly one way worth knowing.

**A bug the new tests caught:** an absent JSON array in the capabilities body
decodes to a nil slice, which binds as SQL NULL against two `NOT NULL` columns
— a 500 for a body the reference answers 204 to. The query-parameter route
could never hit it, because `trimmed()` never returns nil.

**Still open — the `/socket` 401, deliberately not decided here.**

Reelix answers 401; the reference answers **403** unauthenticated. Matching the
status would **not** make the socket connect, because Reelix rejects for a
different reason: the credential arrives somewhere `requireAuth` does not read.
The socket is also **not** what blocks the render — `ensureWebSocket` is
fire-and-forget inside a try/catch — so the ~45s cycle in the log is
jellyfin-web's own reconnect timer, not a symptom.

The evidence base for whenever that decision is taken, so it need not be
re-derived:

| Request to the reference `/socket` | Result |
|---|---|
| no credentials | 403 |
| `Authorization` header, valid token | 101 |
| **`?api_key=` valid token** | **101** |
| `?ApiKey=` valid token | 101 (case-insensitive) |
| `?api_key=` invalid token | 403 |
| `?api_key=` valid + foreign `Origin` | **101** |
| header auth + foreign `Origin` | **101** |

Three conclusions follow. **`api_key` is a server-wide credential channel on
the reference, not a socket-specific exception** — `/System/Endpoint?api_key=`
answers 200 too. **The reference performs no Origin check on the socket at
all**, so Reelix is already strictly stricter there, and would remain so.
**The convention assumed from Wholphin does not hold for browsers**: Wholphin
sent a header because OkHttp can, while jellyfin-web builds
`?api_key=<token>&deviceId=<id>` literally, because a browser cannot set
headers on a WebSocket handshake.

So allowing `api_key` on `/socket` would be **matching** the reference rather
than inventing something — but it still means relaxing a deliberate decision
documented on `requireAuth`, and that stays a separate task with its own
reasoning. `requireAuth` was **not** touched by this work.

**Next step:** reload jellyfin-web and confirm the library renders. If it does,
the socket becomes its own task on the evidence above. If it does not, the next
thing to read is the browser console — the remaining candidates are all
client-side, and the server-side 404s from this session are gone.

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
