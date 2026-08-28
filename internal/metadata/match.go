package metadata

import (
	"fmt"
	"strings"
	"unicode"
)

// Match decides which candidate, if any, a query identifies.
//
// The rule in one sentence: a candidate is accepted only when its normalised
// title equals the query's and it is the ONLY candidate at the best available
// tier. Everything else declines.
//
// Three tiers, tried in order, each requiring exact normalised title equality:
//
//	exact       title agrees and the year agrees
//	year_near   title agrees and the year is within one
//	title_only  title agrees and the query carried no year
//
// A tier with two or more survivors is ambiguous and ends the decision — it
// does NOT fall through to a weaker tier, because a weaker tier cannot resolve
// an ambiguity a stronger one could not.
//
// Nothing here consults the provider's ranking. Accepting the top result when
// the title does not agree is exactly the behaviour that produces a confident
// wrong answer, and a wrong identity is not a cosmetic fault: it silently
// attaches imported viewing history to the wrong film, which nobody reports
// because it looks like their own mistake. Unmatched is visible and fixable.
func Match(q MovieQuery, candidates []Candidate) Decision {
	if len(candidates) == 0 {
		return Decision{
			Decline: DeclineNoCandidates,
			Reason:  "the provider returned no candidates",
		}
	}

	want := normaliseTitle(q.Title)
	if want == "" {
		return Decision{
			Decline: DeclineUnusableTitle,
			Reason:  "the parsed title is empty after normalisation",
		}
	}

	// A candidate is titled if its PRIMARY title matches, or any alternative
	// title the caller has attached does. Both comparisons are the same exact
	// equality; an alternative title is more places to look, not a looser look.
	var titled []Candidate
	var viaAlt bool
	for _, c := range candidates {
		switch {
		case normaliseTitle(c.Title) == want:
			titled = append(titled, c)
		case matchesAnAlternative(c, want):
			titled = append(titled, c)
			viaAlt = true
		}
	}
	if len(titled) == 0 {
		return Decision{
			Decline: DeclineNoTitleMatch,
			Reason: fmt.Sprintf(
				"no candidate title matches %q (%d returned, best was %q)",
				q.Title, len(candidates), candidates[0].Title),
		}
	}

	// No year in the filename: the title is all there is, so it has to be
	// unique on its own.
	if q.Year == 0 {
		if len(titled) > 1 {
			return Decision{
				Decline: DeclineAmbiguous,
				Reason: fmt.Sprintf(
					"%d candidates share the title %q and the filename carries no year",
					len(titled), q.Title),
			}
		}
		return Decision{
			Matched: true, Candidate: titled[0], Confidence: ConfidenceTitleOnly,
			ViaAlternativeTitle: viaAlt,
		}
	}

	var exact, near []Candidate
	for _, c := range titled {
		switch diff := c.Year - q.Year; {
		case diff == 0:
			exact = append(exact, c)
		case diff == 1 || diff == -1:
			near = append(near, c)
		}
	}

	if len(exact) == 1 {
		return Decision{
			Matched: true, Candidate: exact[0], Confidence: ConfidenceExact,
			ViaAlternativeTitle: matchedViaAlternative(exact[0], want),
		}
	}
	if len(exact) > 1 {
		return Decision{
			Decline: DeclineAmbiguous,
			Reason: fmt.Sprintf("%d candidates match %q (%d) exactly",
				len(exact), q.Title, q.Year),
		}
	}

	if len(near) == 1 {
		return Decision{
			Matched: true, Candidate: near[0], Confidence: ConfidenceYearNear,
			ViaAlternativeTitle: matchedViaAlternative(near[0], want),
		}
	}
	if len(near) > 1 {
		return Decision{
			Decline: DeclineAmbiguous,
			Reason: fmt.Sprintf("%d candidates match %q within a year of %d",
				len(near), q.Title, q.Year),
		}
	}

	return Decision{
		Decline: DeclineYearMismatch,
		Reason: fmt.Sprintf("the title %q matches but no candidate is within a year of %d",
			q.Title, q.Year),
	}
}

// matchesAnAlternative reports whether any alternative title equals want.
func matchesAnAlternative(c Candidate, want string) bool {
	for _, alt := range c.AltTitles {
		if normaliseTitle(alt) == want {
			return true
		}
	}
	return false
}

// matchedViaAlternative reports whether a chosen candidate was reachable only
// through an alternative title. Provenance for the row, so that "how many
// films needed alternative titles" stays an answerable question rather than a
// thing somebody remembers.
func matchedViaAlternative(c Candidate, want string) bool {
	return normaliseTitle(c.Title) != want && matchesAnAlternative(c, want)
}

// AlternativeTitleCandidates returns the indices of candidates worth fetching
// alternative titles for, given how the matcher will use them.
//
// THIS IS A COST CONTROL WITH A CORRECTNESS ARGUMENT, not a heuristic. A
// candidate outside the year window can never reach the exact or year_near
// tier no matter what it is called, so asking about it buys an API call and
// nothing else. The filter therefore discards only candidates the matcher had
// already ruled out.
//
// When the filename carries no year there is no window, and title_only demands
// a unique title anyway. The list is capped there rather than fetched in full:
// an unbounded fan-out per unidentified film is the difference between a
// library-wide pass being viable and not.
func AlternativeTitleCandidates(q MovieQuery, candidates []Candidate, limit int) []int {
	var out []int
	for i, c := range candidates {
		if q.Year != 0 {
			if diff := c.Year - q.Year; diff < -1 || diff > 1 {
				continue
			}
		}
		out = append(out, i)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// normaliseTitle reduces a title to the form two spellings of the same film
// have in common: lowercase, unaccented, punctuation removed, spaces
// collapsed.
//
// It is deliberately small. Articles are NOT stripped — "The Thing" and
// "Thing" are different films, and an over-eager normaliser produces exactly
// the confident wrong answer this package exists to avoid. Roman numerals,
// subtitles after a colon, and alternate release titles are all left alone for
// the same reason: each would match more films, and matching more films is
// only an improvement when the extra matches are correct.
//
// The cost of being conservative is an item left unmatched for a human to
// resolve. That is the cheap failure.
func normaliseTitle(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	prevSpace := true // leading spaces are skipped
	for _, r := range strings.ToLower(s) {
		if folded, ok := latinFolds[r]; ok {
			r = folded
		}
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevSpace = false
		case !prevSpace:
			// Any punctuation or space becomes a single separator, so
			// "Spider-Man" and "Spider Man" agree.
			b.WriteRune(' ')
			prevSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// latinFolds maps the accented Latin letters that appear in film titles onto
// their unaccented forms.
//
// Hand-written, for the same reason tracklabel.go's ISO 639 table is:
// golang.org/x/text/unicode/norm would decompose these correctly and would
// also promote an indirect dependency to a direct one to answer what a few
// lines of Go answers. Unlisted runes pass through unchanged, which leaves a
// title unmatched rather than mismatched.
var latinFolds = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ø': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u',
	'ý': 'y', 'ÿ': 'y',
	'ñ': 'n', 'ç': 'c',
}
