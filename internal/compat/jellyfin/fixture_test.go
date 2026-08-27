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
func assertSuperset(t *testing.T, want, got any) {
	t.Helper()

	var problems []string
	compare(&problems, "$", want, got)

	if len(problems) > 0 {
		t.Errorf("response is not a structural superset of the recorded Jellyfin response:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
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
		// Every element of got must match the recorded element's shape, not
		// just the first: a list whose later entries are malformed is exactly
		// the bug a first-element-only check would wave through.
		for i, elem := range g {
			compare(problems, fmt.Sprintf("%s[%d]", path, i), w[0], elem)
		}

	default:
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
