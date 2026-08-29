package tmdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/maverickman79/reelix/internal/domain"
)

// lang builds the pointer TMDB's iso_639_1 is decoded into. Nil is
// language-neutral, which is a different answer from any code.
func lang(code string) *string { return &code }

// gangland is the real listing that found the selection bug, recorded from
// TMDB for movie 1147610 rather than invented.
//
// The first entry is the Locarno festival poster titled "Keep Quiet". It is
// language-neutral and carries the highest vote_average in the set — from ONE
// vote — which is exactly how it beat both English posters and ended up on the
// detail screen of a correctly identified film.
var gangland = []imageCandidate{
	{FilePath: "/keepquiet.jpg", Iso639: nil, VoteAverage: 3.334, VoteCount: 1, Width: 1834, Height: 2751},
	{FilePath: "/english-a.jpg", Iso639: lang("en"), VoteAverage: 2.278, VoteCount: 4, Width: 1787, Height: 2679},
	{FilePath: "/english-b.jpg", Iso639: lang("en"), VoteAverage: 1.222, VoteCount: 3, Width: 2000, Height: 3000},
	{FilePath: "/neutral-b.jpg", Iso639: nil, VoteAverage: 0.000, VoteCount: 0, Width: 1372, Height: 2058},
}

// TestGanglandPrefersAnEnglishPoster is the regression fixture for the bug.
//
// The identity was never wrong — tmdb 1147610 is the right film, matching the
// reference — so nothing about matching is under test here. Only the choice of
// artwork is.
//
// IT DOES NOT TELL YOU WHICH RULE SAVED IT, and that is worth knowing before
// trusting it as anything but a regression fixture. Both the language tier and
// the vote gate independently reject the festival poster on this input, so
// removing either one alone leaves this test green. That is fine for pinning a
// real-world outcome and useless for locating a fault — the tests that
// discriminate are TestALowVoteScoreIsNotConsulted for the gate and
// TestBackdropsPreferNeutralArtwork for the tier, each of which holds every
// other rule constant.
func TestGanglandPrefersAnEnglishPoster(t *testing.T) {
	best, ok := bestImage(gangland, imageLanguagePreference[domain.ImagePrimary])
	if !ok {
		t.Fatal("no poster chosen")
	}
	if best.FilePath == "/keepquiet.jpg" {
		t.Fatal("chose the language-neutral festival poster over an English one")
	}
	if best.Iso639 == nil || *best.Iso639 != "en" {
		t.Fatalf("chose a %v poster, want en", best.Iso639)
	}

	// Within the English tier both candidates fall short of minImageVotes on
	// average but /english-a has 4 votes and /english-b has 3, so both are
	// credible and the higher average wins.
	if best.FilePath != "/english-a.jpg" {
		t.Errorf("chose %s, want /english-a.jpg — the better-supported English poster",
			best.FilePath)
	}
}

// TestALowVoteScoreIsNotConsulted is the assertion that proves the gate exists
// rather than merely being written down.
//
// EVERY CANDIDATE IS ENGLISH, deliberately. A first draft of this used the
// Gangland listing, where the loud poster is language-neutral — and fault
// injection showed that version cannot see the gate at all: the language tier
// rejects that poster anyway, so removing the gate left the test green. Two
// rules reaching one outcome, and neither testable through it. Holding the
// language constant is what leaves the gate as the only thing that can decide.
func TestALowVoteScoreIsNotConsulted(t *testing.T) {
	candidates := []imageCandidate{
		// A perfect score from a single voter. Without the gate this wins on
		// its average, which is how the festival poster won in the first place.
		{FilePath: "/one-vote.jpg", Iso639: lang("en"), VoteAverage: 10, VoteCount: 1, Width: 3000},
		{FilePath: "/well-supported.jpg", Iso639: lang("en"), VoteAverage: 2.278, VoteCount: 4, Width: 1787},
	}

	best, _ := bestImage(candidates, imageLanguagePreference[domain.ImagePrimary])
	if best.FilePath != "/well-supported.jpg" {
		t.Errorf("a 10/10 from one vote moved the choice to %s; the vote gate is not working",
			best.FilePath)
	}
}

// TestACredibleScoreStillWins checks the gate did not throw the signal out with
// the noise. Votes are gated, not discarded: Fight Club's poster carries 52 of
// them and that is real evidence.
func TestACredibleScoreStillWins(t *testing.T) {
	candidates := []imageCandidate{
		{FilePath: "/big-but-mediocre.jpg", Iso639: lang("en"), VoteAverage: 4.0, VoteCount: 20, Width: 4000},
		{FilePath: "/smaller-but-loved.jpg", Iso639: lang("en"), VoteAverage: 7.1, VoteCount: 52, Width: 2000},
	}

	best, _ := bestImage(candidates, imageLanguagePreference[domain.ImagePrimary])
	if best.FilePath != "/smaller-but-loved.jpg" {
		t.Errorf("chose %s; a well-supported score must outrank a larger file", best.FilePath)
	}
}

// TestBackdropsPreferNeutralArtwork pins the inversion.
//
// A language-tagged backdrop usually has the title burned into it, which is
// wrong behind a detail screen where the client draws its own title. Measured
// across six films, backdrops run 154 neutral to 28 English — the opposite
// balance to posters.
func TestBackdropsPreferNeutralArtwork(t *testing.T) {
	candidates := []imageCandidate{
		{FilePath: "/with-title.jpg", Iso639: lang("en"), VoteAverage: 8.0, VoteCount: 40, Width: 1920},
		{FilePath: "/clean.jpg", Iso639: nil, VoteAverage: 5.0, VoteCount: 10, Width: 1920},
	}

	best, _ := bestImage(candidates, imageLanguagePreference[domain.ImageBackdrop])
	if best.FilePath != "/clean.jpg" {
		t.Errorf("chose %s; a backdrop must prefer neutral artwork even against a better-scored English one",
			best.FilePath)
	}

	// And the same candidates as POSTERS must choose the other way, or the two
	// orderings are not actually distinct.
	best, _ = bestImage(candidates, imageLanguagePreference[domain.ImagePrimary])
	if best.FilePath != "/with-title.jpg" {
		t.Errorf("chose %s as a poster; posters must prefer English", best.FilePath)
	}
}

// TestLogosPreferEnglish checks logos take the poster order. A logo is the
// title rendered as artwork, so it is language-specific by nature — neutral
// logos appeared once in six films.
func TestLogosPreferEnglish(t *testing.T) {
	candidates := []imageCandidate{
		{FilePath: "/neutral.png", Iso639: nil, VoteAverage: 9.0, VoteCount: 30, Width: 800},
		{FilePath: "/english.png", Iso639: lang("en"), VoteAverage: 2.0, VoteCount: 5, Width: 800},
	}

	best, _ := bestImage(candidates, imageLanguagePreference[domain.ImageLogo])
	if best.FilePath != "/english.png" {
		t.Errorf("chose %s; a logo must prefer English", best.FilePath)
	}
}

// TestAForeignOnlyFilmStillGetsArtwork is the third tier, and it is the reason
// the language filter was dropped from the request.
//
// With include_image_language=en,null this film returned NO posters, the pass
// recorded a negative, and it showed no poster forever with nothing to say why.
// That is a live gap for any non-English library.
func TestAForeignOnlyFilmStillGetsArtwork(t *testing.T) {
	candidates := []imageCandidate{
		{FilePath: "/japanese-small.jpg", Iso639: lang("ja"), VoteAverage: 5, VoteCount: 4, Width: 1000},
		{FilePath: "/japanese-large.jpg", Iso639: lang("ja"), VoteAverage: 5, VoteCount: 4, Width: 2000},
	}

	best, ok := bestImage(candidates, imageLanguagePreference[domain.ImagePrimary])
	if !ok {
		t.Fatal("a film with only Japanese posters got none")
	}
	if best.FilePath != "/japanese-large.jpg" {
		t.Errorf("chose %s, want the larger source", best.FilePath)
	}
}

// TestSelectionIsDeterministic is what "re-running the pass downloads nothing"
// rests on: identical input must always choose identically, including when
// every comparable field ties.
func TestSelectionIsDeterministic(t *testing.T) {
	tied := []imageCandidate{
		{FilePath: "/b.jpg", Iso639: lang("en"), VoteAverage: 5, VoteCount: 10, Width: 2000},
		{FilePath: "/a.jpg", Iso639: lang("en"), VoteAverage: 5, VoteCount: 10, Width: 2000},
		{FilePath: "/c.jpg", Iso639: lang("en"), VoteAverage: 5, VoteCount: 10, Width: 2000},
	}

	first, _ := bestImage(tied, imageLanguagePreference[domain.ImagePrimary])
	if first.FilePath != "/a.jpg" {
		t.Errorf("chose %s, want /a.jpg — the ordering must be total", first.FilePath)
	}

	reversed := []imageCandidate{tied[2], tied[0], tied[1]}
	again, _ := bestImage(reversed, imageLanguagePreference[domain.ImagePrimary])
	if again.FilePath != first.FilePath {
		t.Errorf("input order changed the choice: %s then %s", first.FilePath, again.FilePath)
	}
}

// TestFetchMetadataRequestsEveryLanguage pins the request shape, since the
// selection above is only correct if it is given every candidate to choose
// from.
func TestFetchMetadataRequestsEveryLanguage(t *testing.T) {
	var got url.Values

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		json.NewEncoder(w).Encode(map[string]any{
			"overview": "x",
			"images": map[string]any{
				"posters": []map[string]any{
					{"file_path": "/p.jpg", "iso_639_1": "ja", "width": 1000, "height": 1500,
						"vote_average": 5.0, "vote_count": 4},
				},
			},
		})
	})

	md, err := c.FetchMetadata(context.Background(), "1147610")
	if err != nil {
		t.Fatalf("fetching: %v", err)
	}

	if _, filtered := got["include_image_language"]; filtered {
		t.Error("the request still filters by image language; a foreign-only film would get nothing")
	}
	if appended := got.Get("append_to_response"); !strings.Contains(appended, "images") {
		t.Errorf("append_to_response = %q, want it to include images", appended)
	}

	// And the Japanese poster survives all the way out, which is the behaviour
	// the dropped filter exists to produce.
	if _, ok := md.Images[domain.ImagePrimary]; !ok {
		t.Error("a Japanese-only poster did not reach the caller")
	}
}
