package metadata

import "testing"

// cand is a candidate with only the fields the matcher reads.
func cand(title string, year int, tmdb string) Candidate {
	return Candidate{Title: title, Year: year, IDs: map[string]string{"tmdb": tmdb}}
}

func TestMatchAcceptsOnlyAnUnambiguousTitle(t *testing.T) {
	for _, tc := range []struct {
		name       string
		query      MovieQuery
		candidates []Candidate
		wantID     string
		wantConf   Confidence
	}{
		{
			name:       "title and year both agree",
			query:      MovieQuery{Title: "Fight Club", Year: 1999},
			candidates: []Candidate{cand("Fight Club", 1999, "550")},
			wantID:     "550", wantConf: ConfidenceExact,
		},
		{
			name:  "the exact year wins over a near one",
			query: MovieQuery{Title: "Congo", Year: 1995},
			candidates: []Candidate{
				cand("Congo", 1994, "wrong"),
				cand("Congo", 1995, "right"),
			},
			wantID: "right", wantConf: ConfidenceExact,
		},
		{
			// A festival showing and a general release legitimately differ by
			// a year, as do regional releases.
			name:       "a year out by one is still a match",
			query:      MovieQuery{Title: "Idiocracy", Year: 2005},
			candidates: []Candidate{cand("Idiocracy", 2006, "9367")},
			wantID:     "9367", wantConf: ConfidenceYearNear,
		},
		{
			name:       "no year in the filename, one candidate carries the title",
			query:      MovieQuery{Title: "Gangland"},
			candidates: []Candidate{cand("Gangland", 2025, "1147610"), cand("Gangs", 2019, "other")},
			wantID:     "1147610", wantConf: ConfidenceTitleOnly,
		},
		{
			// Punctuation, case and accents are spelling, not identity.
			name:       "normalisation ignores punctuation, case and accents",
			query:      MovieQuery{Title: "amelie: the story", Year: 2001},
			candidates: []Candidate{cand("Amélie — The Story!", 2001, "194")},
			wantID:     "194", wantConf: ConfidenceExact,
		},
		{
			// The correct answer is not the popular one, and the provider
			// returned the popular one first.
			name:  "provider ranking does not decide",
			query: MovieQuery{Title: "The Singers", Year: 2026},
			candidates: []Candidate{
				cand("The Singer", 2002, "famous"),
				cand("The Singers", 2026, "correct"),
			},
			wantID: "correct", wantConf: ConfidenceExact,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Match(tc.query, tc.candidates)
			if !got.Matched {
				t.Fatalf("declined, want a match: %s", got.Reason)
			}
			if got.Candidate.IDs["tmdb"] != tc.wantID {
				t.Errorf("matched tmdb %q, want %q", got.Candidate.IDs["tmdb"], tc.wantID)
			}
			if got.Confidence != tc.wantConf {
				t.Errorf("confidence %q, want %q", got.Confidence, tc.wantConf)
			}
		})
	}
}

// TestMatchDeclinesRatherThanGuessing is the test that carries the design.
//
// Every case here has a plausible answer available, and taking it would look
// like an improvement. Each one is refused, because a wrong identity does not
// show a wrong poster and stop there — it attaches imported viewing history to
// the wrong film, and nobody reports that because it looks like their own
// mistake.
func TestMatchDeclinesRatherThanGuessing(t *testing.T) {
	for _, tc := range []struct {
		name       string
		query      MovieQuery
		candidates []Candidate
	}{
		{
			name:       "nothing came back",
			query:      MovieQuery{Title: "Fight Club", Year: 1999},
			candidates: nil,
		},
		{
			// The single obvious answer, refused. The title does not agree,
			// and "one result" is not evidence that it is the right one.
			name:       "one candidate whose title does not agree",
			query:      MovieQuery{Title: "The Legend of Aang - The Last Airbender", Year: 2026},
			candidates: []Candidate{cand("Avatar: The Last Airbender", 2010, "10214")},
		},
		{
			name:  "two candidates share the title and the year",
			query: MovieQuery{Title: "Gangland", Year: 2025},
			candidates: []Candidate{
				cand("Gangland", 2025, "a"),
				cand("Gangland", 2025, "b"),
			},
		},
		{
			name:  "two candidates are each within a year",
			query: MovieQuery{Title: "Congo", Year: 1995},
			candidates: []Candidate{
				cand("Congo", 1994, "a"),
				cand("Congo", 1996, "b"),
			},
		},
		{
			name:  "no year to separate two films of the same name",
			query: MovieQuery{Title: "Congo"},
			candidates: []Candidate{
				cand("Congo", 1995, "a"),
				cand("Congo", 2001, "b"),
			},
		},
		{
			name:       "the title agrees but no year is close",
			query:      MovieQuery{Title: "Congo", Year: 1995},
			candidates: []Candidate{cand("Congo", 2001, "a")},
		},
		{
			name:       "an empty title normalises to nothing",
			query:      MovieQuery{Title: "!!! ---", Year: 1999},
			candidates: []Candidate{cand("Fight Club", 1999, "550")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Match(tc.query, tc.candidates)
			if got.Matched {
				t.Fatalf("matched %q (%d), want a decline",
					got.Candidate.Title, got.Candidate.Year)
			}
			if got.Reason == "" {
				t.Error("declined without a reason; the reason is shown to an operator")
			}
		})
	}
}

// TestAmbiguityDoesNotFallThroughToAWeakerTier pins the one ordering rule that
// is easy to get wrong.
//
// Two candidates agree exactly, and one other is within a year. Falling
// through would "resolve" the ambiguity by picking the near one — arriving at
// a single answer that the stronger evidence says is wrong.
func TestAmbiguityDoesNotFallThroughToAWeakerTier(t *testing.T) {
	got := Match(
		MovieQuery{Title: "Congo", Year: 1995},
		[]Candidate{
			cand("Congo", 1995, "a"),
			cand("Congo", 1995, "b"),
			cand("Congo", 1996, "near"),
		},
	)
	if got.Matched {
		t.Fatalf("matched %q, want a decline", got.Candidate.IDs["tmdb"])
	}
}

func TestNormaliseTitle(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Fight Club", "fight club"},
		{"  Spider-Man:  Homecoming ", "spider man homecoming"},
		{"Amélie", "amelie"},
		{"WALL·E", "wall e"},
		{"9", "9"},
		{"!!!", ""},
		{"", ""},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := normaliseTitle(tc.in); got != tc.want {
				t.Errorf("normaliseTitle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Articles are deliberately kept. Stripping them would match more films,
	// and matching more films is only an improvement when the extra matches
	// are correct.
	t.Run("articles are not stripped", func(t *testing.T) {
		if normaliseTitle("The Thing") == normaliseTitle("Thing") {
			t.Error("The Thing and Thing normalise alike; they are different films")
		}
	})
}
