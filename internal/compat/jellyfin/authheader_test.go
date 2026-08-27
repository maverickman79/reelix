package jellyfin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// This file is the entire safety net for the authorization parser.
//
// Every Authorization value in the Step 0 capture was replaced with
// "REDACTED" before the fixtures were committed, and X-Emby-Authorization and
// X-MediaBrowser-Token appear nowhere in it. So unlike every other part of the
// compatibility layer, this code has NO recorded reference to be validated
// against — these cases are written from Jellyfin's published API
// documentation and the format the Kotlin SDK is known to emit.
//
// Treat a failure here as a real signal and a pass as weaker evidence than it
// looks.

func requestWith(headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestParseAuthorization(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    Authorization
	}{
		{
			name: "wholphin shape",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client="Wholphin", Device="SK1", DeviceId="device-001", Version="1.0.7-0-g2b9af1e8", Token="abc123"`,
			},
			want: Authorization{
				Client: "Wholphin", Device: "SK1", DeviceID: "device-001",
				Version: "1.0.7-0-g2b9af1e8", Token: "abc123",
			},
		},
		{
			name: "pre-authentication, no token",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client="Wholphin", Device="SK1", DeviceId="device-001", Version="1.0.7"`,
			},
			want: Authorization{
				Client: "Wholphin", Device: "SK1", DeviceID: "device-001", Version: "1.0.7",
			},
		},
		{
			name: "legacy emby header",
			headers: map[string]string{
				headerEmbyAuth: `MediaBrowser Client="VidHub", Device="iPad", DeviceId="d2", Version="2.0", Token="t2"`,
			},
			want: Authorization{
				Client: "VidHub", Device: "iPad", DeviceID: "d2", Version: "2.0", Token: "t2",
			},
		},
		{
			name: "token in its own header",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client="Wholphin", Device="SK1", DeviceId="d1", Version="1.0"`,
				headerToken:         "separate-token",
			},
			want: Authorization{
				Client: "Wholphin", Device: "SK1", DeviceID: "d1", Version: "1.0",
				Token: "separate-token",
			},
		},
		{
			name: "separate token header wins over an inline one",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client="C", DeviceId="d", Token="inline"`,
				headerToken:         "explicit",
			},
			want: Authorization{Client: "C", DeviceID: "d", Token: "explicit"},
		},
		{
			name: "standard header wins over the legacy one",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client="Standard", DeviceId="d1"`,
				headerEmbyAuth:      `MediaBrowser Client="Legacy", DeviceId="d2"`,
			},
			want: Authorization{Client: "Standard", DeviceID: "d1"},
		},

		// A device name containing a comma is the case a naive
		// strings.Split(",") gets wrong, and "Living Room, TV" is a name a
		// real person types into a real television.
		{
			name: "quoted value containing a comma",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client="Wholphin", Device="Living Room, TV", DeviceId="d1", Token="t"`,
			},
			want: Authorization{
				Client: "Wholphin", Device: "Living Room, TV", DeviceID: "d1", Token: "t",
			},
		},
		{
			name: "quoted value containing an equals sign",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client="C", Device="a=b", DeviceId="d"`,
			},
			want: Authorization{Client: "C", Device: "a=b", DeviceID: "d"},
		},

		// Spacing and casing vary between clients and Jellyfin versions.
		{
			name: "no spaces after commas",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client="C",Device="D",DeviceId="I",Version="V",Token="T"`,
			},
			want: Authorization{Client: "C", Device: "D", DeviceID: "I", Version: "V", Token: "T"},
		},
		{
			name: "generous spacing",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser   Client = "C" ,  DeviceId = "I"`,
			},
			want: Authorization{Client: "C", DeviceID: "I"},
		},
		{
			name: "lowercase scheme",
			headers: map[string]string{
				headerAuthorization: `mediabrowser Client="C", DeviceId="I"`,
			},
			want: Authorization{Client: "C", DeviceID: "I"},
		},
		{
			name: "lowercase keys",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser client="C", deviceid="I", token="T"`,
			},
			want: Authorization{Client: "C", DeviceID: "I", Token: "T"},
		},
		{
			name: "mixed-case DeviceID spelling",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client="C", DeviceID="I"`,
			},
			want: Authorization{Client: "C", DeviceID: "I"},
		},
		{
			name: "unquoted values",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client=C, DeviceId=I, Token=T`,
			},
			want: Authorization{Client: "C", DeviceID: "I", Token: "T"},
		},
		{
			name: "empty quoted value",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client="C", Device="", DeviceId="I"`,
			},
			want: Authorization{Client: "C", Device: "", DeviceID: "I"},
		},
		{
			name: "unknown keys are ignored",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client="C", DeviceId="I", FutureField="X"`,
			},
			want: Authorization{Client: "C", DeviceID: "I"},
		},
		{
			name: "trailing comma",
			headers: map[string]string{
				headerAuthorization: `MediaBrowser Client="C", DeviceId="I",`,
			},
			want: Authorization{Client: "C", DeviceID: "I"},
		},

		// Nothing usable.
		{name: "no headers", headers: nil, want: Authorization{}},
		{
			name:    "wrong scheme",
			headers: map[string]string{headerAuthorization: `Bearer rlx_abc`},
			want:    Authorization{},
		},
		{
			name:    "native reelix token is not accepted here",
			headers: map[string]string{headerAuthorization: `Bearer rlx_sometoken`},
			want:    Authorization{},
		},
		{
			name:    "scheme only",
			headers: map[string]string{headerAuthorization: `MediaBrowser`},
			want:    Authorization{},
		},
		{
			name:    "scheme with empty parameters",
			headers: map[string]string{headerAuthorization: `MediaBrowser `},
			want:    Authorization{},
		},
		{
			name:    "garbage",
			headers: map[string]string{headerAuthorization: `!!!!`},
			want:    Authorization{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAuthorization(requestWith(tt.headers))
			if got != tt.want {
				t.Errorf("ParseAuthorization() = %+v\n                   want %+v", got, tt.want)
			}
		})
	}
}

// TestParseAuthorizationDoesNotPanic throws malformed input at the parser.
//
// This header arrives from the network before any authentication, so a panic
// here is a denial of service reachable by anyone who can open a socket.
func TestParseAuthorizationDoesNotPanic(t *testing.T) {
	values := []string{
		`MediaBrowser "`,
		`MediaBrowser ""`,
		`MediaBrowser """"`,
		`MediaBrowser =`,
		`MediaBrowser ="unclosed`,
		`MediaBrowser Client="`,
		`MediaBrowser Client="a", Device="b`,
		`MediaBrowser ,,,,,,`,
		`MediaBrowser ====`,
		`MediaBrowser Client=="x"`,
		"MediaBrowser Client=\"a\x00b\"",
		`MediaBrowser ` + string(make([]byte, 4096)),
	}

	for _, v := range values {
		t.Run(v[:min(len(v), 30)], func(t *testing.T) {
			_ = ParseAuthorization(requestWith(map[string]string{headerAuthorization: v}))
		})
	}
}
