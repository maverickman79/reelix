package jellyfin

import (
	"fmt"
	"strings"

	"github.com/maverickman79/reelix/internal/domain"
)

// This file composes the human-readable label a client shows when someone
// picks an audio or subtitle track, and it lives at the compatibility
// boundary on purpose.
//
// The string is not a fact about the media. It is a fact about what Jellyfin
// clients expect to be handed, assembled from columns that are facts. A
// native Reelix interface reading the same rows would compose a different
// label and must not have to take this one apart to do it. Clients depending
// on the exact shape is an argument for reproducing the shape faithfully, not
// for moving the composition somewhere every consumer inherits it.
//
// CLEAN-ROOM PROVENANCE — read this before changing the rules below.
//
// The composition is reproduced from recorded JSON responses in testdata, not
// ported from Jellyfin's implementation, which nobody working on Reelix has
// read. Eight captured responses pin these eight strings:
//
//	1080p H264 SDR
//	English - Dolby Digital+ - 5.1 - Default
//	Surround AC3 5.1 - English - Dolby Digital - Default
//	Commentary by author/screenwriter … - English - Dolby Digital - Stereo
//	English - SUBRIP
//	SDH - English - Hearing Impaired - SUBRIP
//	Latin American - Spanish - SUBRIP
//	English SDH - SUBRIP
//
// From those, the rule is: join the applicable parts with " - ", and drop any
// part the track's own title already contains. THE CONTAINMENT RULE IS
// INFERRED FROM FOUR DATA POINTS — "Surround AC3 5.1" suppressing the layout
// "5.1", "English SDH" suppressing the language "English", and the two
// captures where a title shares nothing with its other parts and everything
// survives. It is the simplest rule that reproduces every recorded string; it
// is not knowledge of what the reference server actually does. A ninth capture
// could contradict it. If one does, change the rule to fit the evidence rather
// than reasoning about what the implementation "must" be doing.
//
// Every recorded string above is pinned as a test case in tracklabel_test.go.

// languageNames maps ISO 639 codes onto the English names the recorded server
// rendered. Both the three-letter 639-2/B codes ffprobe usually reports and
// the two-letter 639-1 codes some containers carry are accepted.
//
// Hand-written rather than reached for through golang.org/x/text/display: that
// package pulls a CLDR table to answer a question a few kilobytes of Go
// answers, and promoting an indirect dependency to a direct one needs a better
// reason than convenience. Unlisted codes fall back to the raw code, which is
// worse than a name and much better than nothing.
var languageNames = map[string]string{
	"afr": "Afrikaans", "af": "Afrikaans",
	"ara": "Arabic", "ar": "Arabic",
	"aze": "Azerbaijani", "az": "Azerbaijani",
	"bel": "Belarusian", "be": "Belarusian",
	"ben": "Bengali", "bn": "Bengali",
	"bos": "Bosnian", "bs": "Bosnian",
	"bul": "Bulgarian", "bg": "Bulgarian",
	"cat": "Catalan", "ca": "Catalan",
	"ces": "Czech", "cze": "Czech", "cs": "Czech",
	"cym": "Welsh", "wel": "Welsh", "cy": "Welsh",
	"dan": "Danish", "da": "Danish",
	"deu": "German", "ger": "German", "de": "German",
	"ell": "Greek", "gre": "Greek", "el": "Greek",
	"eng": "English", "en": "English",
	"est": "Estonian", "et": "Estonian",
	"eus": "Basque", "baq": "Basque", "eu": "Basque",
	"fas": "Persian", "per": "Persian", "fa": "Persian",
	"fil": "Filipino",
	"fin": "Finnish", "fi": "Finnish",
	"fra": "French", "fre": "French", "fr": "French",
	"gle": "Irish", "ga": "Irish",
	"glg": "Galician", "gl": "Galician",
	"heb": "Hebrew", "he": "Hebrew",
	"hin": "Hindi", "hi": "Hindi",
	"hrv": "Croatian", "hr": "Croatian",
	"hun": "Hungarian", "hu": "Hungarian",
	"hye": "Armenian", "arm": "Armenian", "hy": "Armenian",
	"ind": "Indonesian", "id": "Indonesian",
	"isl": "Icelandic", "ice": "Icelandic", "is": "Icelandic",
	"ita": "Italian", "it": "Italian",
	"jpn": "Japanese", "ja": "Japanese",
	"kat": "Georgian", "geo": "Georgian", "ka": "Georgian",
	"kaz": "Kazakh", "kk": "Kazakh",
	"khm": "Khmer", "km": "Khmer",
	"kor": "Korean", "ko": "Korean",
	"lav": "Latvian", "lv": "Latvian",
	"lit": "Lithuanian", "lt": "Lithuanian",
	"mal": "Malayalam", "ml": "Malayalam",
	"mkd": "Macedonian", "mac": "Macedonian", "mk": "Macedonian",
	"mon": "Mongolian", "mn": "Mongolian",
	"msa": "Malay", "may": "Malay", "ms": "Malay",
	"mya": "Burmese", "bur": "Burmese", "my": "Burmese",
	"nep": "Nepali", "ne": "Nepali",
	"nld": "Dutch", "dut": "Dutch", "nl": "Dutch",
	"nob": "Norwegian Bokmål", "nb": "Norwegian Bokmål",
	"nor": "Norwegian", "no": "Norwegian",
	"pol": "Polish", "pl": "Polish",
	"por": "Portuguese", "pt": "Portuguese",
	"ron": "Romanian", "rum": "Romanian", "ro": "Romanian",
	"rus": "Russian", "ru": "Russian",
	"sin": "Sinhala", "si": "Sinhala",
	"slk": "Slovak", "slo": "Slovak", "sk": "Slovak",
	"slv": "Slovenian", "sl": "Slovenian",
	"spa": "Spanish", "es": "Spanish",
	"sqi": "Albanian", "alb": "Albanian", "sq": "Albanian",
	"srp": "Serbian", "sr": "Serbian",
	"swa": "Swahili", "sw": "Swahili",
	"swe": "Swedish", "sv": "Swedish",
	"tam": "Tamil", "ta": "Tamil",
	"tel": "Telugu", "te": "Telugu",
	"tha": "Thai", "th": "Thai",
	"tur": "Turkish", "tr": "Turkish",
	"ukr": "Ukrainian", "uk": "Ukrainian",
	"urd": "Urdu", "ur": "Urdu",
	"vie": "Vietnamese", "vi": "Vietnamese",
	"zho": "Chinese", "chi": "Chinese", "zh": "Chinese",
}

// languageName renders an ISO 639 code the way a viewer reads it.
//
// The empty string means "say nothing about the language". That covers both a
// track with no language tag and one tagged "und": a picker entry reading
// "Undefined - SUBRIP" is noise where the codec alone at least distinguishes
// the track from its neighbours.
func languageName(code *string) string {
	if code == nil {
		return ""
	}

	normalized := strings.ToLower(strings.TrimSpace(*code))
	if normalized == "" || normalized == "und" {
		return ""
	}
	if name, ok := languageNames[normalized]; ok {
		return name
	}
	// An unlisted code is shown as the container wrote it. "mri - SUBRIP"
	// is poor, and it still tells someone which of two tracks to pick.
	return normalized
}

// audioCodecNames are the marketing names the recorded server used in place of
// the codec ffprobe reports.
//
// Only ac3 and eac3 appear in the capture; both are here because both were
// observed. Everything else falls back to the uppercased codec rather than to
// a name guessed from what the codec is "probably" called, so Fight Club's
// DTS-HD MA track reads "DTS" instead of a string nothing has ever verified.
var audioCodecNames = map[string]string{
	"ac3":  "Dolby Digital",
	"eac3": "Dolby Digital+",
}

// audioCodecName renders an audio codec for display.
func audioCodecName(codec *string) string {
	if codec == nil {
		return ""
	}
	if name, ok := audioCodecNames[strings.ToLower(*codec)]; ok {
		return name
	}
	return strings.ToUpper(*codec)
}

// displayTitle is the label a client shows when a user picks a track.
//
// See the provenance note at the top of this file before changing the shape.
func displayTitle(s domain.MediaStream) string {
	switch s.Kind {
	case domain.StreamKindVideo:
		return videoDisplayTitle(s)
	case domain.StreamKindAudio:
		return joinParts(s.Title,
			languageName(s.Language),
			audioCodecName(s.Codec),
			audioChannelLayout(s.Channels),
			defaultMarker(s.IsDefault),
		)
	case domain.StreamKindSubtitle:
		return joinParts(s.Title,
			languageName(s.Language),
			forcedMarker(s.IsForced),
			hearingImpairedMarker(s.IsHearingImpaired),
			upperCodec(s.Codec),
		)
	}
	return upperCodec(s.Codec)
}

// videoDisplayTitle renders "1080p H264".
//
// The recorded server appended the video range — "1080p H264 SDR" — which it
// derived from colour metadata. Reelix does not record colour metadata, so
// that part is omitted rather than asserted: claiming SDR for an HDR remux
// would be worse than saying nothing, and Fight Club is Dolby Vision.
func videoDisplayTitle(s domain.MediaStream) string {
	codec := upperCodec(s.Codec)

	if s.Height != nil && *s.Height > 0 {
		if codec == "" {
			return fmt.Sprintf("%dp", *s.Height)
		}
		return fmt.Sprintf("%dp %s", *s.Height, codec)
	}
	return codec
}

// joinParts assembles a label from a track's title and its other parts.
//
// A part the title already contains is dropped, so "English SDH" does not
// become "English SDH - English". See the provenance note: this rule is
// inferred from the captures, not known.
func joinParts(title *string, parts ...string) string {
	var kept []string

	name := ""
	if title != nil {
		name = strings.TrimSpace(*title)
	}
	if name != "" {
		kept = append(kept, name)
	}

	lowered := strings.ToLower(name)
	for _, p := range parts {
		if p == "" {
			continue
		}
		if lowered != "" && strings.Contains(lowered, strings.ToLower(p)) {
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, " - ")
}

// displayChannelLayout renders a stored channel layout for the ChannelLayout
// field a client reads directly.
//
// ffprobe reports a positional qualifier for many files — "5.1(side)",
// "7.1(wide)" — and the recorded server sent bare "5.1" and "stereo". The
// qualifier is stripped here rather than at probe time, so the database keeps
// what the container said.
//
// This is not cosmetic. Findroid matches this string against exactly "2.0",
// "2.1", "5.1" and "7.1" to classify a track; "5.1(side)" falls through to the
// stereo arm and labels a 5.1 track as 2.0. A wrong label that looks right is
// worse than a missing one, which is the whole reason this field stopped being
// an allowance.
func displayChannelLayout(layout *string) *string {
	if layout == nil {
		return nil
	}

	trimmed := strings.TrimSpace(*layout)
	if i := strings.IndexByte(trimmed, '('); i > 0 {
		trimmed = strings.TrimSpace(trimmed[:i])
	}
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// audioChannelLayout names a channel count the way a listener would.
//
// DELIBERATELY STILL DERIVED FROM THE COUNT, even though a stored layout now
// exists. The capture shows the reference server sending ChannelLayout
// "stereo" while the same stream's DisplayTitle says "Stereo" — the two fields
// disagree on purpose, and the count-derived form already reproduces the
// DisplayTitle side exactly. Feeding the stored layout in here would change a
// string that currently matches the capture. The column feeds the
// ChannelLayout field; this feeds the label.
func audioChannelLayout(channels *int) string {
	if channels == nil || *channels <= 0 {
		return ""
	}

	switch *channels {
	case 1:
		return "Mono"
	case 2:
		return "Stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		return fmt.Sprintf("%d ch", *channels)
	}
}

func defaultMarker(isDefault bool) string {
	if isDefault {
		return "Default"
	}
	return ""
}

func forcedMarker(isForced bool) string {
	if isForced {
		return "Forced"
	}
	return ""
}

func hearingImpairedMarker(isHearingImpaired bool) string {
	if isHearingImpaired {
		return "Hearing Impaired"
	}
	return ""
}

func upperCodec(codec *string) string {
	if codec == nil {
		return ""
	}
	return strings.ToUpper(*codec)
}
