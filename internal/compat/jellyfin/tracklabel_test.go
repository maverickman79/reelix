package jellyfin

import (
	"testing"

	"github.com/maverickman79/reelix/internal/domain"
)

func str(s string) *string { return &s }
func num(n int) *int       { return &n }

// TestDisplayTitleMatchesTheCaptures pins every DisplayTitle the recorded
// Jellyfin traffic contains.
//
// These are the evidence the composition rules were derived from, so they are
// the thing that must not drift. The containment rule in particular is
// inferred rather than known — see the provenance note in tracklabel.go — and
// these cases are what would catch a later change that reasons about the rule
// instead of checking it.
func TestDisplayTitleMatchesTheCaptures(t *testing.T) {
	tests := []struct {
		name   string
		stream domain.MediaStream
		want   string
	}{
		{
			// GET_Items_{id}/00.json stream 1.
			name: "audio with no title",
			stream: domain.MediaStream{
				Kind: domain.StreamKindAudio, Codec: str("eac3"),
				Channels: num(6), Language: str("eng"), IsDefault: true,
			},
			want: "English - Dolby Digital+ - 5.1 - Default",
		},
		{
			// GET_Items_{id}/04.json stream 1. The title carries "5.1", so
			// the channel layout is not repeated after it.
			name: "audio whose title already names the layout",
			stream: domain.MediaStream{
				Kind: domain.StreamKindAudio, Codec: str("ac3"),
				Channels: num(6), Language: str("eng"),
				Title: str("Surround AC3 5.1"), IsDefault: true,
			},
			want: "Surround AC3 5.1 - English - Dolby Digital - Default",
		},
		{
			// GET_Items_{id}/04.json stream 2: a long title, stereo, and not
			// the default track, so no Default marker.
			name: "commentary track",
			stream: domain.MediaStream{
				Kind: domain.StreamKindAudio, Codec: str("ac3"),
				Channels: num(2), Language: str("eng"),
				Title: str("Commentary by author/screenwriter Kelly Goodner and film historian Jim Hemphill"),
			},
			want: "Commentary by author/screenwriter Kelly Goodner and film historian Jim Hemphill - English - Dolby Digital - Stereo",
		},
		{
			// GET_Items_{id}/00.json stream 2.
			name: "plain subtitle",
			stream: domain.MediaStream{
				Kind: domain.StreamKindSubtitle, Codec: str("subrip"),
				Language: str("eng"),
			},
			want: "English - SUBRIP",
		},
		{
			// GET_Items_{id}/00.json stream 3: title "SDH" and the hearing
			// impaired disposition set. This is the string that would be
			// unreachable without the hearing_impaired column.
			name: "SDH subtitle with the disposition set",
			stream: domain.MediaStream{
				Kind: domain.StreamKindSubtitle, Codec: str("subrip"),
				Language: str("eng"), Title: str("SDH"), IsHearingImpaired: true,
			},
			want: "SDH - English - Hearing Impaired - SUBRIP",
		},
		{
			// GET_Items_{id}/00.json stream 7.
			name: "subtitle with a regional title",
			stream: domain.MediaStream{
				Kind: domain.StreamKindSubtitle, Codec: str("subrip"),
				Language: str("spa"), Title: str("Latin American"),
			},
			want: "Latin American - Spanish - SUBRIP",
		},
		{
			// GET_Items_{id}/04.json stream 3: the title contains "English",
			// so the language is not repeated after it.
			name: "subtitle whose title already names the language",
			stream: domain.MediaStream{
				Kind: domain.StreamKindSubtitle, Codec: str("subrip"),
				Language: str("eng"), Title: str("English SDH"),
			},
			want: "English SDH - SUBRIP",
		},
		{
			// GET_Items_{id}/00.json stream 0, minus the " SDR" the recorded
			// server derived from colour metadata Reelix does not store.
			// Deliberate divergence, not a regression.
			name: "video",
			stream: domain.MediaStream{
				Kind: domain.StreamKindVideo, Codec: str("h264"),
				Width: num(1920), Height: num(1080), IsDefault: true,
			},
			want: "1080p H264",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayTitle(tt.stream); got != tt.want {
				t.Errorf("displayTitle()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// TestDisplayTitleDegradesHonestly covers the tracks the capture does not,
// where the rule is to say less rather than to invent.
func TestDisplayTitleDegradesHonestly(t *testing.T) {
	tests := []struct {
		name   string
		stream domain.MediaStream
		want   string
	}{
		{
			// Fight Club's audio. There is no captured name for DTS-HD MA,
			// so the codec is uppercased rather than given a marketing name
			// nothing has verified.
			name: "an audio codec the capture never named",
			stream: domain.MediaStream{
				Kind: domain.StreamKindAudio, Codec: str("dts"),
				Channels: num(6), Language: str("eng"), IsDefault: true,
			},
			want: "English - DTS - 5.1 - Default",
		},
		{
			// "und" is the container saying it does not know. Rendering
			// "Undefined" would dress that up as information.
			name: "an undefined language says nothing about language",
			stream: domain.MediaStream{
				Kind: domain.StreamKindSubtitle, Codec: str("subrip"),
				Language: str("und"),
			},
			want: "SUBRIP",
		},
		{
			name: "no language tag at all",
			stream: domain.MediaStream{
				Kind: domain.StreamKindSubtitle, Codec: str("subrip"),
			},
			want: "SUBRIP",
		},
		{
			// An unlisted code is shown raw: worse than a name, far better
			// than dropping the only thing distinguishing two tracks.
			name: "an unlisted language code falls back to the code",
			stream: domain.MediaStream{
				Kind: domain.StreamKindSubtitle, Codec: str("subrip"),
				Language: str("mri"),
			},
			want: "mri - SUBRIP",
		},
		{
			name: "a forced subtitle is marked forced",
			stream: domain.MediaStream{
				Kind: domain.StreamKindSubtitle, Codec: str("subrip"),
				Language: str("eng"), IsForced: true,
			},
			want: "English - Forced - SUBRIP",
		},
		{
			name: "forced and hearing impaired together",
			stream: domain.MediaStream{
				Kind: domain.StreamKindSubtitle, Codec: str("subrip"),
				Language: str("eng"), IsForced: true, IsHearingImpaired: true,
			},
			want: "English - Forced - Hearing Impaired - SUBRIP",
		},
		{
			name: "an unusual channel count is stated rather than named",
			stream: domain.MediaStream{
				Kind: domain.StreamKindAudio, Codec: str("flac"),
				Channels: num(4), Language: str("jpn"),
			},
			want: "Japanese - FLAC - 4 ch",
		},
		{
			name: "a stream with nothing but a codec",
			stream: domain.MediaStream{
				Kind: domain.StreamKindAudio, Codec: str("mp3"),
			},
			want: "MP3",
		},
		{
			name: "a video stream with no height",
			stream: domain.MediaStream{
				Kind: domain.StreamKindVideo, Codec: str("hevc"),
			},
			want: "HEVC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayTitle(tt.stream); got != tt.want {
				t.Errorf("displayTitle()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// TestLanguageNameIsCaseAndWhitespaceTolerant covers what containers actually
// write, which is not always a tidy lowercase code.
func TestLanguageNameIsCaseAndWhitespaceTolerant(t *testing.T) {
	for _, code := range []string{"eng", "ENG", "Eng", " eng ", "en", "EN"} {
		if got := languageName(&code); got != "English" {
			t.Errorf("languageName(%q) = %q, want English", code, got)
		}
	}

	if got := languageName(nil); got != "" {
		t.Errorf("languageName(nil) = %q, want empty", got)
	}
	empty := ""
	if got := languageName(&empty); got != "" {
		t.Errorf("languageName(\"\") = %q, want empty", got)
	}
}
