package jellyfin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fixtureDir holds the recorded Jellyfin traffic from Step 0.
const fixtureDir = "testdata"

// fixture is one recorded request/response pair.
type fixture struct {
	CallOrder int `json:"call_order"`
	Request   struct {
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Query   map[string]string `json:"query"`
		Headers map[string]string `json:"headers"`
		Body    struct {
			MimeType string          `json:"mimeType"`
			Text     string          `json:"text"`
			JSON     json.RawMessage `json:"json"`
		} `json:"body"`
	} `json:"request"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    struct {
			MimeType string          `json:"mimeType"`
			Text     string          `json:"text"`
			JSON     json.RawMessage `json:"json"`
		} `json:"body"`
	} `json:"response"`
}

// loadFixture reads one recorded exchange, e.g. ("GET_Users_Me", "00").
func loadFixture(t *testing.T, route, name string) fixture {
	t.Helper()

	path := filepath.Join(fixtureDir, route, name+".json")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", path, err)
	}

	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parsing fixture %s: %v", path, err)
	}
	return f
}

// fixtureNames lists the recorded exchanges for one route, in order.
func fixtureNames(t *testing.T, route string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(fixtureDir, route))
	if err != nil {
		t.Fatalf("listing fixtures for %s: %v", route, err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(names)
	return names
}

// recordedJSON decodes a fixture's recorded response body.
func (f fixture) recordedJSON(t *testing.T) any {
	t.Helper()

	if len(f.Response.Body.JSON) == 0 {
		return nil
	}

	var v any
	if err := json.Unmarshal(f.Response.Body.JSON, &v); err != nil {
		t.Fatalf("decoding recorded response: %v", err)
	}
	return v
}

// assertSuperset checks that got contains everything want does.
//
// This is the contract the whole compatibility layer is validated against, and
// Steps 6 and 7 depend on it. Reelix may add fields a real Jellyfin server
// does not send; it may never omit one, because the client SDK is generated
// from Jellyfin's OpenAPI specification and a missing non-nullable field is a
// hard deserialization exception rather than a degraded screen.
//
// The rules:
//
//   - object: every key in want must exist in got, and recurse into it. Extra
//     keys in got are fine.
//   - array: got must be an array. Every element of got is checked against
//     want[0], so a list whose third entry is malformed fails even though its
//     first is correct. Lengths need not match — content legitimately differs
//     between two servers, structure does not.
//   - string, bool, number: got must be the same JSON type. All numbers are
//     one type; int versus float is not distinguished, because JSON has no
//     such distinction and the recorded values would make it arbitrary.
//   - null: the key must be present, with no constraint on its type. A
//     recorded null says the field exists and nothing about what it holds.
//
// Three refinements exist for data the recorded server had and Reelix does
// not. Each is narrow and each is named:
//
//   - dataObjects: objects whose KEYS are data rather than schema —
//     ImageTags is keyed by image id, ImageBlurHashes by hash. Requiring
//     those keys would require Reelix to invent an image id it would then
//     have to 404 on. The value must still be an object.
//
//   - arrays of typed objects are matched BY TYPE. MediaStreams holds video,
//     audio and subtitle streams with different key sets, and checking an
//     audio stream against the recorded video stream at index 0 fails it for
//     missing Width and Profile. Comparing like with like is stricter than
//     the general rule, not looser.
//
//   - absentInReelix: leaf fields Reelix returns as null because 0.0.1
//     excludes the subsystem that would fill them. Every entry states why,
//     enforced at init: an allowance without a reason panics the test binary
//     rather than quietly widening the contract.
func assertSuperset(t *testing.T, want, got any) {
	t.Helper()

	var problems []string
	compare(&problems, "$", want, got)

	if len(problems) > 0 {
		t.Errorf("response is not a structural superset of the recorded Jellyfin response:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
}

// allowance records why a field the reference server filled is null here.
type allowance struct{ reason string }

// because builds an allowance. It panics on an empty reason, which is what
// makes the reason mandatory rather than customary: this list is the piece
// most likely to erode into "the test was failing, so I added it".
func because(reason string) allowance {
	if strings.TrimSpace(reason) == "" {
		panic("an allowance must state why the field is absent")
	}
	return allowance{reason: reason}
}

// absentInReelix are the fields Reelix answers with null where the reference
// server had a value.
//
// A "$..Field" key matches that field wherever it appears. Nothing is listed
// here because a test was inconvenient; every entry is a subsystem Reelix
// does not have yet, and each one disappears when that subsystem arrives.
//
// An entry is retired ONLY when Reelix emits the field. The compat tests seed
// their own streams, so removing an entry also means seeding a value for it —
// and that seed is not the justification. For the stream metadata the
// justification is that the field travels ffprobe -> schema -> DTO, proved
// away from this package by TestParseProbeOutput (internal/media),
// TestStreamMetadataRoundTrip (internal/repository),
// TestScanPersistsStreamMetadata (internal/service) and
// TestStreamMetadataMigrationClearsProbedAt (internal/db). A future retirement
// with no such proof behind it is this list eroding.
//
// # Every reason is marked with the evidence behind it
//
// A full audit of this list found that two reasons were written from
// assumption and one was flatly false about a client's internals. The pattern
// was exact, and it is the rule to carry forward:
//
//	A reason that states a fact about REELIX held every time.
//	A reason that states a fact about SOMEONE ELSE'S SOFTWARE was
//	where every error was.
//
// So each entry below carries a marker:
//
//	[us]       a checkable statement about Reelix's own code or schema.
//	[capture]  read off a recorded response in testdata.
//	[client]   read in a client's source; the file is named.
//	[unread]   no known client reads the field, so the allowance costs
//	           nothing until one does.
//
// A reason with no marker has not been audited. Do not add one.
//
// Where a client is named, "null-safe" means the read goes through a null
// check and a null omits a row rather than rendering. That distinction is the
// difference between an allowance that is free and one that puts the string
// "null" in front of a user — see TestNoStreamFieldSerialisesAsTheStringNull.
var absentInReelix = map[string]allowance{
	// Metadata scraping is not implemented, so nothing describes a movie
	// beyond what its filename and container say.
	"$..Overview":       because("[us] no metadata provider; the overview is genuinely unknown"),
	"$..CriticRating":   because("[us] no metadata provider"),
	"$..OfficialRating": because("[us] no metadata provider"),
	"$..OriginalTitle":  because("[us] no metadata provider; only the filename-derived title is known"),
	"$..ProductionYear": because("[us] parsed from the filename, so null when the filename carries no year"),

	// These two are null rather than zero deliberately. Both clients read
	// them through a null check, so null omits the element — and a fabricated
	// value would NOT be omitted, it would render as a real rating or date.
	"$..CommunityRating": because("[us] no metadata provider [client] Wholphin Formatting.kt renders any non-null value, so a fabricated 0 would show as zero stars"),
	"$..PremiereDate":    because("[us] no metadata provider [client] Wholphin ScreensaverService.kt renders any non-null value, so a fabricated date would show as a real one"),

	// Artwork downloading is not implemented, so there is no image to measure.
	"$..PrimaryImageAspectRatio": because("[us] no artwork, so no primary image to have a ratio [client] Wholphin BaseItem.kt guards with takeIf { it > 0 }"),

	// Stream metadata the scanner still does not probe.
	//
	// Language, Title, Profile, PixelFormat, the two measured frame rates,
	// ChannelLayout, SampleRate and the five Localized strings were all
	// listed here once and are gone because Reelix emits them, not because
	// this list was relaxed.
	//
	// Every entry below is read by Wholphin in ItemDetailsDialogInfo.kt
	// through `stream.field?.let { ... }`, or by nothing at all. A null omits
	// a row from a details dialog; none of them is concatenated into a label.
	// That was ASSUMED until the audit and is now checked.
	"$..BitDepth":       because("[us] the scanner does not record bit depth [client] Wholphin ItemDetailsDialogInfo.kt:382, null-safe"),
	"$..RefFrames":      because("[us] the scanner does not record reference frames [client] Wholphin ItemDetailsDialogInfo.kt:410, null-safe"),
	"$..NalLengthSize":  because("[us] the scanner does not record NAL length size [client] Wholphin ItemDetailsDialogInfo.kt:411, null-safe"),
	"$..ColorSpace":     because("[us] the scanner does not record colour metadata [client] Wholphin ItemDetailsDialogInfo.kt:406, null-safe"),
	"$..ColorTransfer":  because("[us] the scanner does not record colour metadata [client] Wholphin ItemDetailsDialogInfo.kt:407, null-safe"),
	"$..ColorPrimaries": because("[us] the scanner does not record colour metadata [client] Wholphin ItemDetailsDialogInfo.kt:408, null-safe"),
	"$..IsAnamorphic":   because("[us] the scanner does not record anamorphic flags [client] Wholphin ItemDetailsDialogInfo.kt:381, null-safe"),
	"$..BitRate":        because("[us] ffprobe reports no bitrate for some streams; null rather than a guess [client] Wholphin ItemDetailsDialogInfo.kt:442, null-safe"),

	"$..TimeBase": because("[us] the scanner does not record the stream time base [unread] neither Wholphin nor Findroid reads it"),
	"$..IsAVC":    because("[us] the scanner does not record whether a stream is AVC [unread] neither Wholphin nor Findroid reads it"),

	// Reelix records avg_frame_rate and r_frame_rate, both measured. It does
	// not emit a reference rate: the captures show all three equal, but that
	// is constant-frame-rate content agreeing with itself, and what the
	// reference server means by "reference" is not something the recorded
	// traffic reveals. A value that happens to be right for CFR is a guess.
	"$..ReferenceFrameRate": because("[us] Reelix stores two measured frame rates and will not guess a third [unread] neither Wholphin nor Findroid reads it"),

	// Level is emitted for video, where ffprobe reports one. The recording
	// holds 0 on audio and subtitle streams. Reelix stores no level for
	// those rather than writing a zero to match.
	//
	// An earlier version of these reasons explained the recorded 0 as "a
	// .NET default". That was a guess about the reference server's
	// implementation, unverifiable without reading source this project does
	// not read, and the kind of guess that reads as established fact a few
	// sessions later. Only the observation is stated now.
	"$..MediaStreams[Audio].Level":    because("[us] ffprobe reports no codec level for an audio stream [capture] the recording holds 0 there [unread] no client reads a stream's level"),
	"$..MediaStreams[Subtitle].Level": because("[us] ffprobe reports no codec level for a subtitle stream [capture] the recording holds 0 there [unread] no client reads a stream's level"),

	// The recording holds 0 for both on subtitle tracks. Reelix does not
	// probe subtitle geometry and will not write zeroes to match. A video
	// stream's dimensions are still required; this covers subtitles only.
	// The ".NET default" explanation that used to sit here was a guess about
	// the reference server, and is gone for the same reason as above.
	"$..MediaStreams[Subtitle].Width":  because("[us] the scanner does not probe subtitle geometry [capture] the recording holds 0 there"),
	"$..MediaStreams[Subtitle].Height": because("[us] the scanner does not probe subtitle geometry [capture] the recording holds 0 there"),

	// Playback state exists now, and this field is emitted whenever there is
	// one. It is null only for an item nobody has played, which is what the
	// compat tests seed.
	//
	// The reason here used to say "no playback state until Step 7". Step 7
	// shipped; the entry survived and its reason described a world that had
	// stopped existing. A stale reason is as misleading as a wrong one.
	"$..LastPlayedDate": because("[us] emitted from playback state when present, null for an item never played [client] Wholphin LatestNextUpService.kt null-checks it"),

	// A library is the top of the tree in Reelix.
	"$..ParentId": because("[us] no folder above a library, and an invented root id would resolve to nothing [client] neither Wholphin nor Findroid reads an item's parentId; every hit is a request parameter or the client's own config"),

	// The constitution forbids returning filesystem layout.
	"$..Path": because("[us] the constitution forbids leaking filesystem paths through an API [client] Wholphin SubtitleSearchUtils.kt and ItemDetailsDialogInfo.kt both read it null-safely"),
}

// dataObjects are recorded objects whose keys are data rather than schema.
var dataObjects = map[string]allowance{
	"$..ImageTags":           because("[us] keyed by image id; requiring one would mean inventing an image Reelix cannot serve [client] observed on hardware in 0.0.1, both clients draw a placeholder for an empty map"),
	"$..ProviderIds":         because("[us] keyed by metadata provider, and Reelix has none [client] Wholphin reads it in one place, for external links it then omits"),
	"$..Trickplay":           because("[us] keyed by resolution; trickplay is not implemented [client] Findroid fetches trickplay separately and treats an empty map as none"),
	"$..ImageBlurHashes":     because("[us] keyed by image hash, for images Reelix does not have [unread] neither Wholphin nor Findroid reads it"),
	"$..RequiredHttpHeaders": because("[us] a header map, empty for a file Reelix serves directly [unread] neither Wholphin nor Findroid reads it"),
}

// allowed reports whether path is covered by one of the named lists.
//
// A key is either an exact normalised path, or a "$.." prefix matching any
// path that ends in the rest of it: "$..Overview" covers the field wherever
// it appears, "$..MediaStreams[Subtitle].Width" only on subtitle streams.
func allowed(list map[string]allowance, path string) bool {
	normalized := normalizePath(path)
	if _, ok := list[normalized]; ok {
		return true
	}

	for key := range list {
		rest, found := strings.CutPrefix(key, "$..")
		if found && strings.HasSuffix(normalized, "."+rest) {
			return true
		}
	}
	return false
}

// normalizePath makes a walked path comparable to an allowance key.
//
// A numeric index becomes "[]", so one entry covers every element of a list.
// An index the by-type rule labelled — "[2:Subtitle]" — keeps the label and
// loses the number, so an allowance can name a kind of element without
// naming its position.
func normalizePath(path string) string {
	var b strings.Builder

	for i := 0; i < len(path); i++ {
		if path[i] != '[' {
			b.WriteByte(path[i])
			continue
		}

		end := strings.IndexByte(path[i:], ']')
		if end < 0 {
			b.WriteByte(path[i])
			continue
		}

		inner := path[i+1 : i+end]
		if _, label, labelled := strings.Cut(inner, ":"); labelled {
			b.WriteString("[" + label + "]")
		} else {
			b.WriteString("[]")
		}
		i += end
	}
	return b.String()
}

// recordedByType indexes an array of typed objects by their Type.
//
// It returns false unless every recorded element is an object carrying a
// non-empty string Type, so the rule applies only where the data really is
// heterogeneous rather than by accident.
func recordedByType(want []any) (map[string]any, bool) {
	byType := make(map[string]any, len(want))

	for _, elem := range want {
		obj, ok := elem.(map[string]any)
		if !ok {
			return nil, false
		}
		name, ok := obj["Type"].(string)
		if !ok || name == "" {
			return nil, false
		}
		if _, seen := byType[name]; !seen {
			byType[name] = obj
		}
	}
	return byType, len(byType) > 0
}

// compare walks want and got in parallel, appending a message per divergence.
//
// It collects every problem rather than stopping at the first: when a DTO is
// wrong it is usually wrong in several places, and fixing them one test run at
// a time is miserable.
func compare(problems *[]string, path string, want, got any) {
	// A recorded null constrains presence only, which the caller has already
	// established by finding the key.
	if want == nil {
		return
	}

	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*problems = append(*problems, fmt.Sprintf("%s: expected an object, got %s", path, jsonType(got)))
			return
		}

		// An object whose keys are data constrains the type and nothing more.
		if allowed(dataObjects, path) {
			return
		}

		// Sorted so failures are reported in a stable order.
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			child, present := g[k]
			if !present {
				*problems = append(*problems, fmt.Sprintf("%s.%s: missing (recorded %s)", path, k, jsonType(w[k])))
				continue
			}
			compare(problems, path+"."+k, w[k], child)
		}

	case []any:
		g, ok := got.([]any)
		if !ok {
			*problems = append(*problems, fmt.Sprintf("%s: expected an array, got %s", path, jsonType(got)))
			return
		}
		if len(w) == 0 {
			// A recorded empty array says only that the field is a list.
			return
		}

		// Heterogeneous lists are matched by Type: an audio stream must be
		// checked against the recorded audio stream, not against whichever
		// stream happened to be first.
		if byType, ok := recordedByType(w); ok {
			for i, elem := range g {
				obj, isObject := elem.(map[string]any)
				if !isObject {
					*problems = append(*problems, fmt.Sprintf("%s[%d]: expected an object, got %s",
						path, i, jsonType(elem)))
					continue
				}

				name, _ := obj["Type"].(string)
				recorded, found := byType[name]
				if !found {
					*problems = append(*problems, fmt.Sprintf(
						"%s[%d]: no recorded element of type %q to compare against", path, i, name))
					continue
				}
				// The type is carried in the path so that a failure names
				// the kind of stream, and so that an allowance can be
				// written for one type without covering the others.
				compare(problems, fmt.Sprintf("%s[%d:%s]", path, i, name), recorded, elem)
			}
			return
		}
		// Every element of got must match the recorded element's shape, not
		// just the first: a list whose later entries are malformed is exactly
		// the bug a first-element-only check would wave through.
		for i, elem := range g {
			compare(problems, fmt.Sprintf("%s[%d]", path, i), w[0], elem)
		}

	default:
		// A field Reelix cannot fill is null, and the reason is on record.
		if got == nil && allowed(absentInReelix, path) {
			return
		}

		if jsonType(want) != jsonType(got) {
			*problems = append(*problems, fmt.Sprintf("%s: recorded %s, got %s",
				path, jsonType(want), jsonType(got)))
		}
	}
}

// jsonType names a decoded JSON value's type.
//
// encoding/json decodes every number as float64, so int and float are one type
// here by construction rather than by choice.
func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// decodeBody decodes a handler's JSON response for comparison.
func decodeBody(t *testing.T, raw []byte) any {
	t.Helper()

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decoding Reelix response: %v\nbody was: %s", err, raw)
	}
	return v
}

// TestAssertSupersetRules pins the comparison helper's own behaviour.
//
// Steps 6 and 7 rest on this being right, so it is tested rather than trusted.
func TestAssertSupersetRules(t *testing.T) {
	tests := []struct {
		name    string
		want    string
		got     string
		wantErr bool
	}{
		{name: "identical", want: `{"A":1}`, got: `{"A":1}`},
		{name: "extra field allowed", want: `{"A":1}`, got: `{"A":1,"B":2}`},
		{name: "missing field rejected", want: `{"A":1,"B":2}`, got: `{"A":1}`, wantErr: true},

		{name: "type mismatch rejected", want: `{"A":1}`, got: `{"A":"1"}`, wantErr: true},
		{name: "bool for string rejected", want: `{"A":"x"}`, got: `{"A":true}`, wantErr: true},
		{name: "int and float are one type", want: `{"A":1}`, got: `{"A":1.5}`},

		{name: "null requires presence only", want: `{"A":null}`, got: `{"A":"anything"}`},
		{name: "null still requires the key", want: `{"A":null}`, got: `{}`, wantErr: true},
		{name: "null accepts null", want: `{"A":null}`, got: `{"A":null}`},

		{name: "empty recorded array", want: `{"A":[]}`, got: `{"A":[{"X":1}]}`},
		{name: "array must stay an array", want: `{"A":[]}`, got: `{"A":"nope"}`, wantErr: true},
		{
			name:    "every element is checked, not just the first",
			want:    `{"A":[{"X":1}]}`,
			got:     `{"A":[{"X":1},{"X":2},{"Y":3}]}`,
			wantErr: true,
		},
		{
			name: "all elements matching passes",
			want: `{"A":[{"X":1}]}`,
			got:  `{"A":[{"X":1},{"X":2}]}`,
		},
		{name: "shorter array is fine", want: `{"A":[{"X":1}]}`, got: `{"A":[]}`},

		{name: "nested missing field", want: `{"A":{"B":{"C":1}}}`, got: `{"A":{"B":{}}}`, wantErr: true},
		{name: "nested ok", want: `{"A":{"B":{"C":1}}}`, got: `{"A":{"B":{"C":9,"D":0}}}`},

		{name: "top-level array", want: `[]`, got: `[]`},
		{name: "top-level scalar", want: `true`, got: `false`},
		{name: "top-level scalar mismatch", want: `true`, got: `"true"`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var want, got any
			if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
				t.Fatalf("bad want json: %v", err)
			}
			if err := json.Unmarshal([]byte(tt.got), &got); err != nil {
				t.Fatalf("bad got json: %v", err)
			}

			var problems []string
			compare(&problems, "$", want, got)

			if tt.wantErr && len(problems) == 0 {
				t.Error("expected a mismatch, got none")
			}
			if !tt.wantErr && len(problems) > 0 {
				t.Errorf("expected a match, got: %s", strings.Join(problems, "; "))
			}
		})
	}
}

// TestAssertSupersetReportsPath checks failures name the offending field,
// since a bare "does not match" on a 42-field policy object is useless.
func TestAssertSupersetReportsPath(t *testing.T) {
	var want, got any
	_ = json.Unmarshal([]byte(`{"User":{"Policy":{"IsAdministrator":true}}}`), &want)
	_ = json.Unmarshal([]byte(`{"User":{"Policy":{}}}`), &got)

	var problems []string
	compare(&problems, "$", want, got)

	if len(problems) != 1 {
		t.Fatalf("expected one problem, got %d: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "$.User.Policy.IsAdministrator") {
		t.Errorf("problem does not name the field: %s", problems[0])
	}
}

// problemsFor runs the comparison and returns what it found.
func problemsFor(t *testing.T, want, got string) []string {
	t.Helper()

	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad want json: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("bad got json: %v", err)
	}

	var problems []string
	compare(&problems, "$", w, g)
	return problems
}

// TestStreamsAreMatchedByType pins the rule that fixes a real bug: MediaStreams
// holds video, audio and subtitle streams with different key sets, and the
// general array rule checked every one of them against the recorded video
// stream at index 0.
func TestStreamsAreMatchedByType(t *testing.T) {
	// A recording with a wide video stream and a narrow audio one.
	const recorded = `{"MediaStreams":[
		{"Type":"Video","Codec":"h264","Width":1920,"Height":1080,"Profile":"High"},
		{"Type":"Audio","Codec":"eac3","Channels":6}
	]}`

	t.Run("an audio stream is not judged against the video stream", func(t *testing.T) {
		// Before this rule, the audio stream was failed for missing Width,
		// Height and Profile — fields no audio stream has ever carried.
		got := `{"MediaStreams":[
			{"Type":"Video","Codec":"h264","Width":1920,"Height":1080,"Profile":"High"},
			{"Type":"Audio","Codec":"eac3","Channels":6}
		]}`
		if p := problemsFor(t, recorded, got); len(p) > 0 {
			t.Errorf("expected a match, got: %s", strings.Join(p, "; "))
		}
	})

	t.Run("a genuinely incomplete audio stream still fails", func(t *testing.T) {
		got := `{"MediaStreams":[
			{"Type":"Video","Codec":"h264","Width":1920,"Height":1080,"Profile":"High"},
			{"Type":"Audio","Codec":"eac3"}
		]}`
		p := problemsFor(t, recorded, got)
		if len(p) != 1 || !strings.Contains(p[0], "[1:Audio].Channels") {
			t.Errorf("expected the audio stream's missing Channels, got: %v", p)
		}
	})

	t.Run("an incomplete video stream still fails", func(t *testing.T) {
		// The rule must not have made the video stream easier to satisfy.
		got := `{"MediaStreams":[{"Type":"Video","Codec":"h264","Width":1920,"Height":1080}]}`
		p := problemsFor(t, recorded, got)
		if len(p) != 1 || !strings.Contains(p[0], "[0:Video].Profile") {
			t.Errorf("expected the video stream's missing Profile, got: %v", p)
		}
	})

	t.Run("a type the recording never carried is reported", func(t *testing.T) {
		got := `{"MediaStreams":[{"Type":"EmbeddedImage","Codec":"mjpeg"}]}`
		p := problemsFor(t, recorded, got)
		if len(p) != 1 || !strings.Contains(p[0], `no recorded element of type "EmbeddedImage"`) {
			t.Errorf("expected the unrecorded type to be reported, got: %v", p)
		}
	})
}

// TestDataObjectsConstrainTheTypeOnly checks the rule for objects whose keys
// are data — requiring them would mean inventing an image id Reelix would
// then have to 404 on.
func TestDataObjectsConstrainTheTypeOnly(t *testing.T) {
	t.Run("keys need not match", func(t *testing.T) {
		want := `{"ImageTags":{"Primary":"8c613f9d","Logo":"b1609847"}}`
		if p := problemsFor(t, want, `{"ImageTags":{}}`); len(p) > 0 {
			t.Errorf("expected an empty tag map to pass, got: %s", strings.Join(p, "; "))
		}
	})

	t.Run("it must still be an object", func(t *testing.T) {
		want := `{"ImageTags":{"Primary":"8c613f9d"}}`
		p := problemsFor(t, want, `{"ImageTags":null}`)
		if len(p) != 1 || !strings.Contains(p[0], "expected an object") {
			t.Errorf("expected a null tag map to fail, got: %v", p)
		}
	})

	t.Run("an ordinary object still needs its keys", func(t *testing.T) {
		want := `{"UserData":{"PlayCount":0,"Key":"x"}}`
		p := problemsFor(t, want, `{"UserData":{}}`)
		if len(p) != 2 {
			t.Errorf("expected both missing fields to be reported, got: %v", p)
		}
	})
}

// TestAbsenceNeedsAnAllowance checks the third rule, and its limits.
func TestAbsenceNeedsAnAllowance(t *testing.T) {
	t.Run("an allowed field may be null", func(t *testing.T) {
		if p := problemsFor(t, `{"Overview":"a film"}`, `{"Overview":null}`); len(p) > 0 {
			t.Errorf("expected the allowance to apply, got: %s", strings.Join(p, "; "))
		}
	})

	t.Run("an unlisted field may not", func(t *testing.T) {
		// Container is data Reelix genuinely has; a null here is a bug.
		p := problemsFor(t, `{"Container":"mkv"}`, `{"Container":null}`)
		if len(p) != 1 || !strings.Contains(p[0], "$.Container") {
			t.Errorf("expected a null Container to fail, got: %v", p)
		}
	})

	t.Run("an allowance does not excuse a missing key", func(t *testing.T) {
		// The distinction matters: the client's generated type has the field
		// either way, but a key Reelix forgot to emit is exactly what this
		// helper exists to catch. Null is a stated answer; absence is not.
		p := problemsFor(t, `{"Overview":"a film"}`, `{}`)
		if len(p) != 1 || !strings.Contains(p[0], "$.Overview: missing") {
			t.Errorf("expected a missing Overview to fail, got: %v", p)
		}
	})

	t.Run("an allowance scoped to a type does not cover the others", func(t *testing.T) {
		const recorded = `{"MediaStreams":[
			{"Type":"Video","Width":1920},
			{"Type":"Subtitle","Width":1920}
		]}`
		// Subtitle geometry is allowed to be null; a video stream's is not.
		got := `{"MediaStreams":[
			{"Type":"Video","Width":null},
			{"Type":"Subtitle","Width":null}
		]}`
		p := problemsFor(t, recorded, got)
		if len(p) != 1 || !strings.Contains(p[0], "[0:Video].Width") {
			t.Errorf("expected only the video stream to fail, got: %v", p)
		}
	})
}

// TestEveryAllowanceStatesAReason guards the list most likely to erode.
//
// because() panics on an empty reason, so an entry cannot be added without
// one; this checks the reasons are sentences rather than placeholders, and
// that no entry has been added by editing the struct directly.
func TestEveryAllowanceStatesAReason(t *testing.T) {
	for name, list := range map[string]map[string]allowance{
		"absentInReelix": absentInReelix,
		"dataObjects":    dataObjects,
	} {
		for path, a := range list {
			if len(strings.TrimSpace(a.reason)) < 20 {
				t.Errorf("%s[%q] does not state why: %q", name, path, a.reason)
			}
		}
	}
}

// TestBecauseRequiresAReason checks the requirement is enforced rather than
// merely documented.
func TestBecauseRequiresAReason(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("an allowance without a reason was accepted")
		}
	}()

	because("   ")
}

// TestNormalizePath pins the path forms the allowance keys are written
// against.
func TestNormalizePath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"$.Items[0].Overview", "$.Items[].Overview"},
		{"$.MediaStreams[2:Subtitle].Width", "$.MediaStreams[Subtitle].Width"},
		{"$.MediaSources[0:Default].MediaStreams[11:Audio].Language",
			"$.MediaSources[Default].MediaStreams[Audio].Language"},
		{"$.Container", "$.Container"},
	} {
		if got := normalizePath(tc.in); got != tc.want {
			t.Errorf("normalizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
