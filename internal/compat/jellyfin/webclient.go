package jellyfin

import (
	"crypto/rand"
	"net/http"
	"net/netip"
	"strconv"
)

// The routes in this file are the ones jellyfin-web asks for and Wholphin
// never does. None of them appears in the Step 0 capture, so every shape here
// came from probing a reference 10.11.8 directly; see docs/compat-capture.md.
//
// None of them blocks a render. Two of them nevertheless mattered enough to
// implement, because a 404 on this pair produces a PERMANENT request loop
// rather than a single failure — see handleSystemEndpoint.

// bitrateTestDefaultSize is what the reference server returns when no size is
// asked for.
const bitrateTestDefaultSize = 128 << 10

// bitrateTestMaxSize bounds the response. The reference rejects a request far
// above this with 400 rather than allocating whatever it was asked for, and
// so does Reelix: this endpoint is reachable by any authenticated client and
// the size comes straight from the query string.
const bitrateTestMaxSize = 100 << 20

// handleBranding serves GET /Branding/Configuration.
//
// Read by the login page. It is UNAUTHENTICATED on the reference server, and
// is registered that way here — a client fetches it before it has a token,
// which is the whole point of a login disclaimer.
//
// Reelix has no branding configuration and 0.0.1 adds none, so both string
// fields are nil and are omitted rather than sent as null. See brandingOptions
// for the probe that settled which of those two the reference does.
func (a *API) handleBranding(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, brandingOptions{SplashscreenEnabled: false})
}

// handleSystemEndpoint serves GET /System/Endpoint.
//
// THIS ROUTE 404ING IS A PERMANENT REQUEST LOOP, not a single failure, and
// that is the reason it is implemented in a milestone that cannot use its
// answer. jellyfin-web's api client caches the result in _endPointInfo and
// populates that cache ONLY on success. A 404 never caches, so every caller
// re-requests, forever. /Playback/BitrateTest is dragged along with it
// because the bitrate routine calls this first — which is why the two were
// observed cycling together in the access log rather than independently.
//
// Both fields describe the caller. IsInNetwork is true for a private or
// loopback address, IsLocal only for loopback. They are computed rather than
// hardcoded because a client uses them to choose its own bitrate cap, and
// answering "remote" to a machine on the LAN makes it ask for a worse stream
// than the network can carry.
//
// A caveat that will matter later: Reelix reads the transport address and
// does NOT consult X-Forwarded-For. Behind a reverse proxy — which is how
// jellyfin-web reaches Reelix today — every caller looks like the proxy, so
// this reports the proxy's network rather than the client's. That is the safe
// direction for a header nothing authenticates, and it is wrong in exactly
// one way worth writing down.
func (a *API) handleSystemEndpoint(w http.ResponseWriter, r *http.Request) {
	info := endpointInfo{}

	if addr, err := netip.ParseAddr(remoteAddr(r)); err == nil {
		info.IsLocal = addr.IsLoopback()
		info.IsInNetwork = addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
	}

	a.writeJSON(w, r, http.StatusOK, info)
}

// handleBitrateTest serves GET /Playback/BitrateTest.
//
// A block of bytes a client times itself downloading to estimate bandwidth.
// The content is meaningless; only its length and the time it takes are read.
//
// THE SIZE PARAMETER IS CAPITALISED. jellyfin-web requests ?Size=, and Go's
// query lookup is case-sensitive, so reading "size" would silently serve the
// default to every real caller while every hand-written curl appeared to
// work. Both spellings are accepted for that reason.
//
// Bytes are random rather than zeroes. A run of zeroes compresses to almost
// nothing, and any compressing hop between server and client would turn a
// bandwidth measurement into a measurement of gzip.
//
// The reference rounds the length up to a power of two; Reelix serves the
// size it was asked for. Nothing reads the difference — the client divides
// bytes received by time elapsed — and honouring the request is easier to
// reason about than reproducing an artefact of somebody else's buffer size.
//
// 0.0.1 does not transcode, so Reelix never acts on the answer. It is served
// because the client asks, and because it is the other half of the loop
// described on handleSystemEndpoint.
func (a *API) handleBitrateTest(w http.ResponseWriter, r *http.Request) {
	size := bitrateTestDefaultSize

	raw := r.URL.Query().Get("Size")
	if raw == "" {
		raw = r.URL.Query().Get("size")
	}

	if raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > bitrateTestMaxSize {
			// The reference answers 400 above its own bound rather than
			// serving something else, so a client asking for the impossible
			// is told so instead of silently measuring the wrong thing.
			writeStatus(w, http.StatusBadRequest)
			return
		}
		size = n
	}

	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		a.fail(r, "bitrate_test", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(size))
	// Explicitly uncacheable: a cached response measures the cache.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(buf); err != nil {
		a.fail(r, "bitrate_test", err)
	}
}
