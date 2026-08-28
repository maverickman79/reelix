// Package metadata identifies media items against external providers.
//
// Identity only. This package answers "which real film is this file?" and
// deliberately answers nothing else — no overview, no rating, no genre, no
// artwork. Those hang off an identity once one exists, and mixing them in here
// would mean the thing everything else depends on arrives entangled with the
// things that depend on it.
//
// A provider search returns a candidate's title and year. They are carried for
// SCORING and are not stored: writing the provider's title over the parsed one
// is field fetching, which is the next slice, not this one.
package metadata

import "context"

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
}
