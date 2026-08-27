package jellyfin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// dialSocket opens an authenticated WebSocket against the harness.
func dialSocket(t *testing.T, h *harness, token string) *websocket.Conn {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	header := http.Header{}
	header.Set(headerAuthorization, authHeader(token))

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.srv.URL, "http")+"/socket",
		&websocket.DialOptions{
			HTTPClient: h.srv.Client(),
			HTTPHeader: header,
		})
	if err != nil {
		t.Fatalf("dialling /socket: %v", err)
	}
	return conn
}

// TestSocketHandshakeMatchesRecordedAccept validates the upgrade against the
// Step 0 capture at the byte level.
//
// The recording is a usable test vector: it carries the client's
// Sec-WebSocket-Key and the exact Sec-WebSocket-Accept a real Jellyfin server
// computed from it. Replaying that key must produce that value.
//
// The handshake is done by hand rather than through the library so this
// asserts what actually goes on the wire.
func TestSocketHandshakeMatchesRecordedAccept(t *testing.T) {
	h := newHarness(t)
	token := h.login()

	f := loadFixture(t, "GET_socket", "00")

	key := f.Request.Headers["Sec-WebSocket-Key"]
	wantAccept := f.Response.Headers["Sec-WebSocket-Accept"]
	if key == "" || wantAccept == "" {
		t.Fatal("the recorded handshake is missing its key or accept header")
	}

	addr := strings.TrimPrefix(h.srv.URL, "http://")

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dialling %s: %v", addr, err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting deadline: %v", err)
	}

	// The request Wholphin sent, replayed: the same upgrade headers, the same
	// key, and the same offer of permessage-deflate.
	request := "GET /socket HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Extensions: permessage-deflate\r\n" +
		headerAuthorization + ": " + authHeader(token) + "\r\n" +
		"\r\n"

	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("writing the handshake: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, h.srv.URL+"/socket", nil)
	if err != nil {
		t.Fatalf("building a request to read the response against: %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("reading the handshake response: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d, want 101 — Wholphin reconnects forever without the upgrade", resp.StatusCode)
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != wantAccept {
		t.Errorf("Sec-WebSocket-Accept = %q, the recorded server answered %q", got, wantAccept)
	}
	if got := resp.Header.Get("Upgrade"); !strings.EqualFold(got, "websocket") {
		t.Errorf("Upgrade = %q, want websocket", got)
	}
	if got := resp.Header.Get("Connection"); !strings.EqualFold(got, "Upgrade") {
		t.Errorf("Connection = %q, want Upgrade", got)
	}

	// Compression is declined deliberately: not echoing the extension means
	// no compression, which keeps the inflate path out of the connection.
	if got := resp.Header.Get("Sec-WebSocket-Extensions"); got != "" {
		t.Errorf("Sec-WebSocket-Extensions = %q, want the offer declined", got)
	}
}

// TestSocketHoldsOpen is the behaviour Wholphin needs: the connection must
// stay up with no traffic on it. A server that accepts and then hangs up puts
// the client straight back into its reconnect loop.
func TestSocketHoldsOpen(t *testing.T) {
	h := newHarness(t)
	conn := dialSocket(t, h, h.login())
	defer conn.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The read runs in the background rather than against a short deadline:
	// a read context expiring closes the connection in this library, which
	// would destroy the very thing under test. It also gives Ping below the
	// concurrent reader it needs to see a pong.
	reads := make(chan error, 1)
	go func() {
		_, _, err := conn.Read(ctx)
		reads <- err
	}()

	// Nothing should arrive, and nothing should end the connection. A read
	// returning at all here — data, an EOF, a close frame — means the server
	// did not simply hold the connection.
	select {
	case err := <-reads:
		t.Fatalf("the socket did not stay open and silent: %v", err)
	case <-time.After(750 * time.Millisecond):
	}

	// Still alive afterwards: a ping proves the server is reading and
	// answering control frames rather than sitting on a dead connection.
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelPing()

	if err := conn.Ping(pingCtx); err != nil {
		t.Fatalf("the socket did not answer a ping: %v", err)
	}
}

// TestSocketDiscardsClientMessages checks a message from the client neither
// closes the connection nor draws a reply.
//
// Reelix implements no socket protocol in 0.0.1, and inventing a reply the
// SDK would then have to deserialize is the failure this avoids.
func TestSocketDiscardsClientMessages(t *testing.T) {
	h := newHarness(t)
	conn := dialSocket(t, h, h.login())
	defer conn.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"MessageType":"KeepAlive"}`)); err != nil {
		t.Fatalf("writing a message: %v", err)
	}

	readCtx, cancelRead := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancelRead()

	if _, _, err := conn.Read(readCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the server answered or closed after a client message: %v", err)
	}
}

// TestSocketRequiresAToken checks the upgrade is authenticated. An anonymous
// upgrade would leave a goroutine and an open connection available to anyone
// who can reach the port.
func TestSocketRequiresAToken(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct{ name, token string }{
		{"no token", ""},
		{"bogus token", "not-a-real-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, h.srv.URL+"/socket", nil)
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			req.Header.Set(headerAuthorization, authHeader(tc.token))
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Sec-WebSocket-Key", "oxAlS+8MmMXA2phvBF6tMw==")
			req.Header.Set("Sec-WebSocket-Version", "13")

			resp, err := h.srv.Client().Do(req)
			if err != nil {
				t.Fatalf("GET /socket: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status %d, want 401", resp.StatusCode)
			}
		})
	}
}

// TestSocketRejectsANonUpgradeRequest checks a plain GET does not hijack the
// connection or hang.
func TestSocketRejectsANonUpgradeRequest(t *testing.T) {
	h := newHarness(t)

	resp := h.do(http.MethodGet, "/socket", h.login(), nil)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusSwitchingProtocols || resp.StatusCode == http.StatusOK {
		t.Errorf("a plain GET was upgraded: %d", resp.StatusCode)
	}
}

// TestSocketGoroutinesReturnToBaseline is the leak check.
//
// Every connection owns a read loop and a ping timer. Wholphin reconnects
// often — on resume, on network change, on its own backoff — so a goroutine
// that outlives its connection would not stay a rounding error for long: it
// would show up as a count that only ever climbs.
func TestSocketGoroutinesReturnToBaseline(t *testing.T) {
	h := newHarness(t)
	token := h.login()

	// One connection first, so that whatever the library and the HTTP client
	// allocate lazily is already allocated when the baseline is taken.
	warmup := dialSocket(t, h, token)
	closeSocket(t, warmup)

	baseline := settledGoroutines(t, 0)

	const connections = 20
	for i := range connections {
		conn := dialSocket(t, h, token)

		// Prove the connection is genuinely up before closing it: counting
		// goroutines after a handshake that failed would prove nothing. A
		// write is the check rather than a ping, because a pong can only be
		// seen by a concurrent reader and this test deliberately runs none.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := conn.Write(ctx, websocket.MessageText, []byte(`{"MessageType":"KeepAlive"}`))
		cancel()

		if err != nil {
			conn.CloseNow()
			t.Fatalf("connection %d was not usable: %v", i, err)
		}

		closeSocket(t, conn)
	}

	// Teardown is asynchronous on both sides, so the count is allowed to
	// settle rather than being read once and trusted.
	if got := settledGoroutines(t, baseline); got > baseline {
		t.Errorf("goroutine count did not return to baseline after %d connections: %d, was %d\n%s",
			connections, got, baseline, goroutineDump())
	}
}

// closeSocket closes a connection cleanly and fails the test if it cannot.
func closeSocket(t *testing.T, conn *websocket.Conn) {
	t.Helper()

	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("closing the socket: %v", err)
	}
}

// settledGoroutines waits for the goroutine count to stop falling, and
// returns as soon as it is back at or below want.
func settledGoroutines(t *testing.T, want int) int {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	count := runtime.NumGoroutine()

	for time.Now().Before(deadline) {
		if count <= want {
			return count
		}
		time.Sleep(25 * time.Millisecond)

		next := runtime.NumGoroutine()
		if next == count && want == 0 {
			// Taking a baseline: two identical readings is settled enough.
			return count
		}
		count = next
	}
	return count
}

// goroutineDump renders the live stacks, so a leak names itself instead of
// leaving the next reader to guess which goroutine stayed behind.
func goroutineDump() string {
	buf := make([]byte, 64<<10)
	return fmt.Sprintf("goroutines:\n%s", buf[:runtime.Stack(buf, true)])
}
