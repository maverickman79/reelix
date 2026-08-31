package service

import (
	"context"
	"errors"
	"log/slog"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/media"
	"github.com/maverickman79/reelix/internal/metadata"
	"github.com/maverickman79/reelix/internal/repository"
)

// identifyBatch bounds how many items one pass claims from the database at a
// time. The pass is network-bound, so this is about bounding memory and the
// blast radius of a failure, not throughput.
const identifyBatch = 200

// providerPause is the gap between provider requests.
//
// Deliberately conservative. TMDB no longer publishes a hard rate limit, but a
// library-wide pass is precisely the traffic shape that gets an API key
// throttled, and identification is not something anybody is waiting on: it
// runs in the background and its result is visible when it lands. Being a
// polite client costs a few minutes on a first pass and nothing afterwards,
// because an identified item is never asked about again.
const providerPause = 250 * time.Millisecond

// altTitleLookupLimit caps the alternative-title fan-out for one item.
//
// It only bites when the filename carries no year, because a year gives
// metadata.AlternativeTitleCandidates a window that usually narrows the list
// to one or two. Without a cap, an unidentified film with no year would cost
// one request per search result, which across a library is the difference
// between a pass being viable and not.
const altTitleLookupLimit = 10

// IdentityService identifies media items against an external provider.
type IdentityService struct {
	pool     *pgxpool.Pool
	provider metadata.Provider
	log      *slog.Logger
}

// NewIdentityService returns a service backed by pool and provider.
func NewIdentityService(pool *pgxpool.Pool, provider metadata.Provider, log *slog.Logger) *IdentityService {
	return &IdentityService{
		pool:     pool,
		provider: provider,
		log:      logging.Component(log, "identity"),
	}
}

// Start enqueues an identify pass for a library and begins running it.
//
// Returns as soon as the job row exists, like a scan: the constitution forbids
// blocking a request on a library-wide operation. ErrConflict comes back when
// an identify pass for this library is already in flight, enforced by the
// partial unique index rather than by a check here.
func (s *IdentityService) Start(ctx context.Context, libraryID uuid.UUID) (domain.Job, error) {
	if _, err := repository.NewLibraryRepository(s.pool).GetByID(ctx, libraryID); err != nil {
		return domain.Job{}, err
	}

	job := domain.Job{Kind: domain.JobKindLibraryIdentify, LibraryID: &libraryID}
	if err := repository.NewJobRepository(s.pool).Create(ctx, &job); err != nil {
		return domain.Job{}, err
	}

	// Not the request's context: the pass must outlive the HTTP request that
	// asked for it, exactly as a scan does.
	go s.run(context.WithoutCancel(ctx), job.ID, libraryID)

	return job, nil
}

// Get returns one item's identity.
func (s *IdentityService) Get(ctx context.Context, itemID uuid.UUID) (domain.Identity, error) {
	return repository.NewIdentityRepository(s.pool).Get(ctx, itemID)
}

// SetManual records a human's decision about what an item is.
//
// Manual is the highest-authority state: no pass overwrites it. That is what
// makes declining to guess a workable policy rather than an inconvenience —
// an operator who corrects one film must not find the correction gone after
// the next run.
func (s *IdentityService) SetManual(ctx context.Context, itemID uuid.UUID, ids map[string]string) error {
	if len(ids) == 0 {
		return InvalidArgumentf("at least one external id is required")
	}
	for provider, value := range ids {
		if provider == "" || value == "" {
			return InvalidArgumentf("provider and id must both be non-empty")
		}
	}

	repo := repository.NewIdentityRepository(s.pool)
	if _, err := repo.Get(ctx, itemID); err != nil {
		return err
	}
	return repo.SetManual(ctx, itemID, ids)
}

// Reset returns an item to pending so a later pass reconsiders it.
func (s *IdentityService) Reset(ctx context.Context, itemID uuid.UUID) error {
	repo := repository.NewIdentityRepository(s.pool)
	if _, err := repo.Get(ctx, itemID); err != nil {
		return err
	}
	return repo.Reset(ctx, itemID)
}

// run executes an identify pass to completion.
func (s *IdentityService) run(ctx context.Context, jobID, libraryID uuid.UUID) {
	log := s.log.With(slog.String(logging.KeyJobID, jobID.String()))
	jobs := repository.NewJobRepository(s.pool)

	if err := jobs.MarkRunning(ctx, jobID); err != nil {
		log.Error("could not mark the identify job running", slog.Any(logging.KeyError, err))
		return
	}

	matched, unmatched, err := s.identify(ctx, jobID, libraryID, log)
	if err != nil {
		log.Error("identify pass failed",
			slog.String(logging.KeyOperation, "identify"),
			slog.Any(logging.KeyError, err))
		if ferr := jobs.Finish(ctx, jobID, domain.JobStateFailed, err.Error()); ferr != nil {
			log.Error("could not record the failure", slog.Any(logging.KeyError, ferr))
		}
		return
	}

	log.Info("identify pass complete",
		slog.String(logging.KeyOperation, "identify"),
		slog.Int("matched", matched),
		slog.Int("unmatched", unmatched))

	if err := jobs.Finish(ctx, jobID, domain.JobStateCompleted, ""); err != nil {
		log.Error("could not record completion", slog.Any(logging.KeyError, err))
	}
}

// identify walks the library's pending items and decides each one.
//
// A provider failure on one item is recorded against that item and the pass
// continues, the same way one unprobeable file must not cost an operator the
// other nine hundred. Only rate limiting stops the pass: it is the one error
// where continuing makes things worse rather than merely wasting a request,
// and where the right response is to stop and try again later.
func (s *IdentityService) identify(ctx context.Context, jobID, libraryID uuid.UUID, log *slog.Logger) (matched, unmatched int, err error) {
	items, err := repository.NewIdentityRepository(s.pool).Pending(ctx, libraryID, identifyBatch)
	if err != nil {
		return 0, 0, err
	}

	// Counted rather than derived, because the alternative-title fan-out makes
	// an item cost anywhere between one request and twelve. A pass measured on
	// item count alone would say nothing about what it asked TMDB for.
	started := time.Now()
	requestTotal := 0
	defer func() {
		log.Info("identify pass cost",
			slog.String(logging.KeyOperation, "identify"),
			slog.Int("considered", matched+unmatched),
			slog.Int("provider_requests", requestTotal),
			slog.Int64("took_ms", time.Since(started).Milliseconds()))
	}()

	jobs := repository.NewJobRepository(s.pool)
	repo := repository.NewIdentityRepository(s.pool)

	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return matched, unmatched, err
		}
		if i > 0 {
			time.Sleep(providerPause)
		}

		if err := jobs.UpdateProgress(ctx, jobID, i+1, len(items), item.Title); err != nil {
			log.Warn("could not record progress", slog.Any(logging.KeyError, err))
		}

		itemStarted := time.Now()
		decision, lookups, err := s.decide(ctx, item)

		// One search, plus whatever the alternative-title fan-out cost. The
		// cross-provider id lookup on a match is added below, so the number
		// logged is what this item actually sent.
		requests := 1 + lookups
		switch {
		case errors.Is(err, metadata.ErrRateLimited):
			// Stop the pass rather than hammering a provider that has just
			// asked us not to. Everything already decided is kept; the rest
			// stays pending and the next run resumes from there.
			return matched, unmatched, err
		case err != nil:
			// The provider could not be asked about THIS item. That is not a
			// decision, so the item stays pending rather than being recorded
			// as unmatched — an unmatched row would claim a judgement nobody
			// made, and would stop the next pass from retrying it.
			log.Warn("could not identify item",
				slog.String("item", item.Title),
				slog.Any(logging.KeyError, err))
			continue
		}

		if decision.Matched {
			ids := s.resolveIDs(ctx, decision.Candidate, log)
			requests++
			via := "primary"
			if decision.ViaAlternativeTitle {
				via = "alternative"
			}
			if err := repo.RecordMatch(ctx, item.ID, s.provider.Name(),
				string(decision.Confidence), via, ids); err != nil {
				return matched, unmatched, err
			}
			matched++
			requestTotal += requests
			log.Info("item identified",
				slog.String("item", item.Title),
				slog.String("confidence", string(decision.Confidence)),
				slog.String("matched_via", via),
				slog.Any("ids", ids),
				slog.Int64("took_ms", time.Since(itemStarted).Milliseconds()),
				slog.Int("provider_requests", requests))
			continue
		}

		if err := repo.RecordUnmatched(ctx, item.ID, decision.Reason); err != nil {
			return matched, unmatched, err
		}
		unmatched++
		requestTotal += requests
		log.Info("item left unmatched",
			slog.String("item", item.Title),
			slog.String("reason", decision.Reason),
			slog.Int64("took_ms", time.Since(itemStarted).Milliseconds()),
			slog.Int("provider_requests", requests))
	}

	return matched, unmatched, nil
}

// decide asks the provider about one item and applies the matcher.
//
// The second return is how many alternative-title lookups it cost. It is the
// only variable part of an item's provider cost — a well-named film costs one
// search and nothing else — so it is what a measurement of pass throughput has
// to be able to see.
func (s *IdentityService) decide(ctx context.Context, item domain.MediaItem) (metadata.Decision, int, error) {
	query := metadata.MovieQuery{Title: item.Title}
	if item.Year != nil {
		query.Year = *item.Year
	}

	// The stored title came from the filename parser and may be a bad parse;
	// re-parsing the source path costs nothing and recovers the cases where
	// the item was created before the parser understood a form. It is the same
	// function, so it cannot disagree with what a fresh scan would produce.
	if parsed := media.ParseName(item.SourcePath); parsed.Title != "" && query.Title == "" {
		query.Title = parsed.Title
		if parsed.Year != nil {
			query.Year = *parsed.Year
		}
	}

	candidates, err := s.provider.SearchMovie(ctx, query)
	if err != nil {
		return metadata.Decision{}, 0, err
	}

	decision := metadata.Match(query, candidates)

	// Only one kind of decline is worth a second look. "Nothing was called
	// that" can be a renamed release; "too many things were called that"
	// cannot be fixed by finding more things called that.
	if decision.Matched || decision.Decline != metadata.DeclineNoTitleMatch {
		return decision, 0, nil
	}

	enriched, lookups, err := s.withAlternativeTitles(ctx, query, candidates)
	if err != nil {
		return metadata.Decision{}, lookups, err
	}
	return metadata.Match(query, enriched), lookups, nil
}

// withAlternativeTitles asks the provider for the other titles it publishes
// for the candidates that could still match, and returns them enriched.
//
// WHY THIS IS GATED ON THE YEAR WINDOW, which is the question somebody looking
// at this extra request will have:
//
// Alternative titles ENLARGE THE CANDIDATE POOL. That is the point — it is how
// a renamed release is found — but it is also the cost, because a larger pool
// can contain a second candidate carrying the same title, and the matcher
// treats two candidates at the same tier as ambiguous and declines. So adding
// alternative titles can turn a clean match into a decline.
//
// THE RECORDED CASE IS GANGLAND. Searching "Gangland" returns our film,
// tmdb 1147610 (2025), matching on its primary title. It ALSO returns
// tmdb 870843, a different film from 2018 whose US alternative title is
// likewise "Gangland". Today the year gap keeps that second film out of both
// the exact and the near tier, so the match is unaffected. Had our release
// been a 2018 or 2019 one, this pass would have found two candidates called
// Gangland at the same tier and refused to choose — turning a match into a
// decline.
//
// THAT IS ACCEPTABLE ONLY BECAUSE THE MATCHER DECLINES RATHER THAN GUESSES.
// The worst this change can do is produce more unmatched items, which are
// visible and fixable. It cannot produce a wrong match, because every added
// title is compared with the same exact equality the primary title gets, and
// two equally exact matches are refused rather than ranked. If the matcher is
// ever changed to break ties, this reasoning collapses and this call becomes a
// way to attach a watch history to the wrong film.
//
// The year window is what keeps that cost small: a candidate outside it can
// never reach the exact or near tier whatever it is called, so filtering it out
// discards nothing the matcher could have used and saves the request.
// The int return is how many provider requests it actually made.
func (s *IdentityService) withAlternativeTitles(
	ctx context.Context, q metadata.MovieQuery, candidates []metadata.Candidate,
) ([]metadata.Candidate, int, error) {
	enriched := make([]metadata.Candidate, len(candidates))
	copy(enriched, candidates)

	lookups := 0
	for _, i := range metadata.AlternativeTitleCandidates(q, candidates, altTitleLookupLimit) {
		id := enriched[i].IDs[s.provider.Name()]
		if id == "" {
			continue
		}

		lookups++
		titles, err := s.provider.AlternativeTitles(ctx, id)
		if errors.Is(err, metadata.ErrRateLimited) {
			// Propagated so the pass stops, exactly as a rate-limited search
			// does. Continuing would keep asking a provider that has just said
			// no.
			return nil, lookups, err
		}
		if err != nil {
			// One candidate we could not ask about is not a reason to abandon
			// the item: the others may still carry the title, and the worst
			// outcome is the decline we already had.
			s.log.Warn("could not fetch alternative titles",
				slog.String("provider_id", id),
				slog.Any(logging.KeyError, err))
			continue
		}
		enriched[i].AltTitles = titles
	}
	return enriched, lookups, nil
}

// resolveIDs adds the other providers' ids for a matched candidate.
//
// A failure here is deliberately not fatal to the match. The TMDB id is the
// identity; an IMDb id is a convenience for the importer, and losing the whole
// identification because a second request failed would be trading the thing
// that matters for the thing that helps. The item is matched with what is
// known, and a later Reset can try again.
func (s *IdentityService) resolveIDs(ctx context.Context, c metadata.Candidate, log *slog.Logger) map[string]string {
	ids := map[string]string{}
	for provider, value := range c.IDs {
		ids[provider] = value
	}

	own := ids[s.provider.Name()]
	if own == "" {
		return ids
	}

	extra, err := s.provider.ExternalIDs(ctx, own)
	if err != nil {
		log.Warn("could not resolve cross-provider ids",
			slog.String("provider_id", own),
			slog.Any(logging.KeyError, err))
		return ids
	}
	for provider, value := range extra {
		ids[provider] = value
	}
	return ids
}
