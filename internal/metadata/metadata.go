// Package metadata resolves media items against external providers.
//
// IDENTITY FIRST, and the ordering is structural rather than historical.
// Matching answers "which real film is this file?" using nothing but the file:
// a provider search returns a candidate's title and year, and those are carried
// for SCORING and never stored, because writing a provider's title over the
// parsed one would make re-identification change its own input on every run.
//
// Everything else — overview, rating, genres, artwork — hangs off an identity
// once one exists, and is fetched by the field and image methods below. Keeping
// the match independent of them is what stops the thing everything depends on
// arriving entangled with the things that depend on it.
package metadata

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrRateLimited means a provider asked the caller to slow down.
//
// It lives on the interface rather than in one provider's package because the
// identify pass has to act on it — stopping rather than continuing — and a
// service that had to import a concrete provider to recognise its errors would
// be coupled to the implementation it was written to be independent of.
//
// It is the one provider error where retrying later is the correct response
// rather than a wasted request.
var ErrRateLimited = errors.New("provider rate limited")

// Provider searches an external catalogue for films.
//
// One method, because identity needs one question answered. A provider that
// later also fetches fields gains a second method rather than complicating
// this one, and the matcher never needs to know about it.
type Provider interface {
	// Name is the lowercase internal provider name stored in the database,
	// e.g. "tmdb". The capitalised spellings Jellyfin clients expect are a
	// fact about those clients and live at the compatibility boundary.
	Name() string

	// SearchMovie returns candidates ordered however the provider ranks them.
	//
	// That order is deliberately NOT used to break ties. A provider's
	// popularity ranking is a good answer to "what did the user probably mean
	// in a search box" and a bad answer to "which film is this file", because
	// it is confident about the wrong thing: it ranks the famous film above
	// the obscure one that happens to be the correct match.
	SearchMovie(ctx context.Context, q MovieQuery) ([]Candidate, error)

	// ExternalIDs resolves the ids OTHER providers use for one of this
	// provider's items, keyed by lowercase provider name.
	//
	// It belongs on this interface rather than in a later slice because a
	// cross-provider id is identity, not a field: the watch-history importer
	// matches an Emby or Jellyfin export against whatever ids that export
	// happens to carry, and an export keyed on IMDb is useless against a
	// library that only knows TMDB.
	//
	// Called once for the item that matched, never for the candidates that
	// did not, so the cost is one request per identified film rather than one
	// per result.
	ExternalIDs(ctx context.Context, providerID string) (map[string]string, error)

	// AlternativeTitles returns the other titles this provider publishes for
	// one of its items — regional releases, working titles, the name a
	// distributor used.
	//
	// A search response carries the primary title only, so a file named after
	// a release title the provider files as an ALTERNATIVE cannot match on the
	// search result alone. That is the whole reason this exists; see
	// AlternativeTitleCandidates for when it is worth asking.
	AlternativeTitles(ctx context.Context, providerID string) ([]string, error)

	// FetchMetadata returns the managed fields for one of this provider's
	// items. Called once per identified film by the refresh pass.
	FetchMetadata(ctx context.Context, providerID string) (MovieMetadata, error)

	// FetchImage downloads the bytes of one image, returning the body and its
	// content type. The caller closes the body.
	//
	// It takes a URL rather than an id because FetchMetadata already resolved
	// which image to fetch and where it lives; this is the transfer, not the
	// choice. It is on the interface rather than being a bare HTTP GET in the
	// service so that a provider owns its own CDN, timeouts and rate-limit
	// signalling — and so a test can supply bytes without standing up a server.
	//
	// The body is returned UNREAD so the caller can stream it to disk while
	// hashing, rather than holding a backdrop in memory.
	FetchImage(ctx context.Context, imageURL string) (io.ReadCloser, string, error)
}

// MovieMetadata is what a provider knows about a film, beyond its identity.
//
// Every field is optional. A provider that does not know a value returns the
// zero value, and the refresh pass stores nothing for it rather than storing a
// blank — an absent overview and an empty one are different claims.
//
// NO RUNTIME FIELD, DELIBERATELY. See the note in the TMDB provider: the field
// exists upstream and is not collected, which looks like an oversight unless
// the reason is written down.
type MovieMetadata struct {
	// Overview is the plot summary.
	Overview string

	// CommunityRating is the provider's audience score on Jellyfin's 0-10
	// scale. Nil when the provider has no score, which is different from a
	// score of zero — a film nobody has rated is not a film everybody hated.
	CommunityRating *float64

	// OfficialRating is the certification for the configured region, e.g.
	// "R", "15", "FSK 16". Empty when that region has no certification for
	// this film; it is never filled from another region.
	OfficialRating string

	// ReleaseDate is the provider's release date. Zero when unknown.
	ReleaseDate time.Time

	// Genres in the provider's own order, which is meaningful: providers list
	// the primary genre first and clients show the first few.
	Genres []string

	// Images is the best candidate per image type, keyed by Reelix's lowercase
	// type name. A type the provider has no image for is ABSENT from the map,
	// which is what lets the refresh pass record a negative rather than
	// re-asking about it on every run.
	//
	// One candidate per type, not a list: choosing between candidates is the
	// image selection UI that 0.0.2 excludes, so the provider picks and says
	// which it picked.
	Images map[string]ImageCandidate
}

// ImageCandidate is one image a provider offers, before it is downloaded.
//
// It carries dimensions because the provider already published them, which is
// what lets PrimaryImageAspectRatio be answered without decoding a single
// pixel.
type ImageCandidate struct {
	// URL is absolute and ready to fetch.
	URL string

	Width  int
	Height int
}

// MovieQuery is what the filename parser produced.
type MovieQuery struct {
	Title string
	// Year is 0 when the filename carried none. That is common enough to be a
	// normal case rather than an error, and it weakens matching rather than
	// preventing it.
	Year int
}

// Candidate is one possible identity.
type Candidate struct {
	// IDs are keyed by lowercase provider name and always include the
	// searching provider's own id. A provider that also knows an item's IMDb
	// id returns both, which is how an item ends up with two external ids from
	// one search.
	IDs map[string]string

	// Title and Year are for scoring only and are never persisted.
	Title string
	Year  int

	// AltTitles are other titles the provider publishes for this same item.
	// Empty until a caller has asked for them, which it does only when the
	// primary titles produced no match at all.
	//
	// They are compared with exactly the same equality the primary title gets.
	// This is a LARGER SET OF EXACT COMPARISONS, not a fuzzier one, and the
	// distinction is load bearing: a looser comparison invents matches, while
	// more exact comparisons can only find ones that were already there.
	AltTitles []string
}

// Confidence records how a match was reached, and is stored alongside it so a
// later pass can revisit the weak ones without re-deciding the strong ones.
type Confidence string

const (
	// ConfidenceExact means title and year both agreed.
	ConfidenceExact Confidence = "exact"
	// ConfidenceYearNear means the title agreed and the year was within one.
	// Release years legitimately disagree by one between a festival showing
	// and a general release, and between regions.
	ConfidenceYearNear Confidence = "year_near"
	// ConfidenceTitleOnly means the title agreed and there was no year to
	// check it against. The weakest match Reelix will accept, and only when
	// exactly one candidate carries the title.
	ConfidenceTitleOnly Confidence = "title_only"
)

// Decision is the matcher's verdict.
//
// Not-matched is an ordinary outcome carrying a reason, not an error. An error
// means the provider could not be asked; a Decision with Matched false means
// it was asked and the answer was not good enough to act on.
type Decision struct {
	Matched    bool
	Candidate  Candidate
	Confidence Confidence
	// Reason is operator-facing text explaining a decline. It is stored and
	// shown; nothing branches on its contents.
	Reason string
	// Decline classifies a refusal for the one caller that has to act on the
	// kind rather than the words. Zero when Matched.
	Decline DeclineKind
	// ViaAlternativeTitle records that the match was made against a title the
	// provider files as an alternative rather than its primary one. Provenance,
	// not confidence: the comparison was equally exact either way.
	ViaAlternativeTitle bool
}

// DeclineKind classifies why a match was refused.
//
// It exists so the identify pass can tell "nothing was called that" from "too
// many things were called that". Only the first is worth a second look with
// alternative titles: more titles cannot resolve an ambiguity, they can only
// deepen it.
type DeclineKind string

const (
	// DeclineNoCandidates means the provider returned nothing at all.
	DeclineNoCandidates DeclineKind = "no_candidates"
	// DeclineNoTitleMatch means candidates came back and none carried the
	// title. This is the only kind alternative titles can rescue.
	DeclineNoTitleMatch DeclineKind = "no_title_match"
	// DeclineAmbiguous means more than one candidate matched at the best
	// available tier.
	DeclineAmbiguous DeclineKind = "ambiguous"
	// DeclineYearMismatch means the title matched but no candidate was within
	// a year. The film family was found, so the failure is not a naming one.
	DeclineYearMismatch DeclineKind = "year_mismatch"
	// DeclineUnusableTitle means the parsed title normalised to nothing.
	DeclineUnusableTitle DeclineKind = "unusable_title"
)
