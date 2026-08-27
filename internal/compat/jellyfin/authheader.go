package jellyfin

import (
	"net/http"
	"strings"
)

// Authorization carries what a Jellyfin client says about itself.
//
// Every field is optional. Clients omit Token before authenticating and
// populate it afterwards; some omit Version entirely.
type Authorization struct {
	Client   string
	Device   string
	DeviceID string
	Version  string
	Token    string
}

// Header names carrying client identity.
//
// Jellyfin has accepted the credential under two names over its history and
// both remain in the wild. Wholphin sends the standard Authorization header;
// older and third-party clients send X-Emby-Authorization.
const (
	headerAuthorization = "Authorization"
	headerEmbyAuth      = "X-Emby-Authorization"
	headerToken         = "X-MediaBrowser-Token"
)

// authScheme is the scheme both authorization headers use.
const authScheme = "MediaBrowser"

// ParseAuthorization extracts client identity from a request.
//
// WARNING: none of this may be logged. The parsed Token is a live credential
// and the raw header contains it. The constitution forbids logging
// authentication headers and session tokens, and there is a test asserting
// neither reaches the log.
//
// This parser has NO recorded reference. Every Authorization value in the
// Step 0 capture was replaced with "REDACTED" before the fixtures were
// committed, and X-Emby-Authorization and X-MediaBrowser-Token appear nowhere
// in it. The format below comes from Jellyfin's published API documentation
// and from what the Kotlin SDK is known to emit — both permitted sources — but
// it is not validated against observed traffic. It is the most likely place in
// the compatibility layer to be subtly wrong.
func ParseAuthorization(r *http.Request) Authorization {
	// X-Emby-Authorization is checked second so that a client sending both
	// has its standard header win.
	auth := parseAuthValue(r.Header.Get(headerAuthorization))
	if auth == (Authorization{}) {
		auth = parseAuthValue(r.Header.Get(headerEmbyAuth))
	}

	// A token supplied in its own header wins: a client that bothers to send
	// it there is telling us which credential it means.
	if t := strings.TrimSpace(r.Header.Get(headerToken)); t != "" {
		auth.Token = t
	}

	return auth
}

// parseAuthValue parses one header value of the form:
//
//	MediaBrowser Client="Wholphin", Device="SK1", DeviceId="abc", Version="1.0.7", Token="xyz"
//
// It returns the zero Authorization for anything that is not this scheme.
func parseAuthValue(value string) Authorization {
	value = strings.TrimSpace(value)
	if value == "" {
		return Authorization{}
	}

	scheme, rest, found := strings.Cut(value, " ")
	if !found || !strings.EqualFold(scheme, authScheme) {
		return Authorization{}
	}

	var auth Authorization
	for key, val := range splitPairs(rest) {
		// Keys vary in case between clients and Jellyfin versions;
		// DeviceId and Deviceid both appear in the wild.
		switch strings.ToLower(key) {
		case "client":
			auth.Client = val
		case "device":
			auth.Device = val
		case "deviceid":
			auth.DeviceID = val
		case "version":
			auth.Version = val
		case "token":
			auth.Token = val
		}
	}
	return auth
}

// splitPairs walks comma-separated Key="Value" pairs, yielding each key and
// its unquoted value.
//
// It cannot be a strings.Split on ",": a quoted value may itself contain a
// comma, and device names routinely do — "Living Room, TV" is a real device
// name. The scan therefore tracks whether it is inside quotes.
func splitPairs(s string) func(func(string, string) bool) {
	return func(yield func(string, string) bool) {
		var (
			start    int
			inQuotes bool
		)

		emit := func(pair string) bool {
			key, val, found := strings.Cut(pair, "=")
			if !found {
				return true
			}

			key = strings.TrimSpace(key)
			val = strings.TrimSpace(val)

			// Unquote only a properly balanced pair; a lone quote is data.
			if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
				val = val[1 : len(val)-1]
			}

			if key == "" {
				return true
			}
			return yield(key, val)
		}

		for i := range len(s) {
			switch s[i] {
			case '"':
				inQuotes = !inQuotes
			case ',':
				if inQuotes {
					continue
				}
				if !emit(s[start:i]) {
					return
				}
				start = i + 1
			}
		}
		emit(s[start:])
	}
}
