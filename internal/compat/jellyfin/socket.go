package jellyfin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/maverickman79/reelix/internal/logging"
)

// The socket exists in 0.0.1 for one reason: Wholphin retries /socket
// indefinitely with backoff until it gets an upgrade. Observed on the SK1 —
// refusing the upgrade leaves the client reconnecting forever, so accepting
// and holding the connection is a requirement, not polish.
//
// Reelix sends nothing over it. The Step 0 capture recorded only the 101
// handshake: the proxy never saw a frame, so any server-to-client message
// shape would be a guess, and Wholphin's SDK deserialization is strict enough
// that a wrong guess is a hard client-side exception. Silence cannot be
// misparsed. Inbound messages are logged by type at debug, which is how the
// next hardware run turns this unknown into evidence.
const (
	// socketPingInterval keeps NAT and firewall paths open and detects a peer
	// that has vanished without closing. A protocol-level ping carries no
	// Jellyfin semantics, so it is safe to send without knowing the message
	// protocol.
	socketPingInterval = 60 * time.Second

	// socketPingTimeout bounds the wait for a pong before the connection is
	// considered dead.
	socketPingTimeout = 10 * time.Second

	// socketReadLimit caps an inbound message. Jellyfin socket messages are
	// small JSON objects; anything approaching this is not one. The library
	// closes the connection with 1009 when it is exceeded.
	socketReadLimit = 32 << 10
)

// handleSocket serves GET /socket.
//
// It runs for the lifetime of the connection: the handler goroutine is the
// read loop, so the connection owns exactly one goroutine plus its ping
// timer, and both end when the peer goes away. Wholphin reconnects often
// enough that anything else would show up as a steadily growing goroutine
// count.
func (a *API) handleSocket(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	log := logging.FromContext(r.Context())

	// Compression is declined rather than negotiated. Wholphin offers
	// permessage-deflate; not echoing the extension is a legal answer that
	// means no compression, and it keeps the inflate path out of a
	// connection that carries no data in this milestone.
	//
	// Origin is deliberately verified (the default): Wholphin sends no Origin
	// header, which passes, while a browser page on another origin does not.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		// Accept has already written the failure status to the client.
		a.fail(r, "socket_accept", err)
		return
	}
	defer conn.CloseNow()

	conn.SetReadLimit(socketReadLimit)

	ctx, cancel := context.WithCancel(r.Context())

	pings := make(chan struct{})
	go func() {
		defer close(pings)
		keepSocketAlive(ctx, conn)
	}()

	// Registered after the CloseNow above so it runs first: the ping
	// goroutine is stopped and waited for before the connection is torn
	// down, which is what makes the goroutine count return to baseline.
	defer func() {
		cancel()
		<-pings
	}()

	log.Info("socket opened",
		slog.String(logging.KeyOperation, "socket"),
		slog.String(logging.KeyUserID, session.UserID.String()),
		slog.String("device", session.DeviceName),
		slog.String("client", session.Client))

	opened := time.Now()
	err = readSocket(ctx, conn, log)

	log.Info("socket closed",
		slog.String(logging.KeyOperation, "socket"),
		slog.String(logging.KeyUserID, session.UserID.String()),
		slog.String("close_status", closeStatus(err)),
		slog.Int64("duration_ms", time.Since(opened).Milliseconds()))
}

// readSocket consumes inbound messages until the connection ends.
//
// Every message is discarded. Reelix implements no socket protocol in 0.0.1,
// and a client that expects no reply is better served by silence than by an
// invented one.
func readSocket(ctx context.Context, conn *websocket.Conn, log *slog.Logger) error {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			// Every exit lands here: a clean close, a dead peer, a message
			// over the read limit, or the ping loop giving up.
			return err
		}

		if typ != websocket.MessageText {
			continue
		}

		// Only the message type is logged, never the payload: a client is
		// free to put anything in it, including a token, and the
		// constitution forbids that reaching the log.
		log.Debug("socket message received",
			slog.String(logging.KeyOperation, "socket"),
			slog.String("message_type", socketMessageType(data)),
			slog.Int("bytes", len(data)))
	}
}

// keepSocketAlive pings the peer until the connection ends.
//
// A failed ping closes the connection, which is the point: without it a
// half-open path — a client that vanished without a FIN — would leave the
// read loop blocked forever on a socket nothing is ever going to send on.
func keepSocketAlive(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(socketPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, socketPingTimeout)
			err := conn.Ping(pingCtx)
			cancel()

			if err != nil {
				// Unblocks the read loop, which then ends the connection and
				// this goroutine with it.
				conn.CloseNow()
				return
			}
		}
	}
}

// socketMessageType reads the MessageType field out of a client message.
//
// Jellyfin socket messages are JSON objects tagged with a MessageType. A
// message that is not one is not an error here — it is logged as unknown,
// because knowing that a client sent something unparseable is more useful
// than dropping it silently.
func socketMessageType(data []byte) string {
	var msg struct {
		MessageType string `json:"MessageType"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || msg.MessageType == "" {
		return "unknown"
	}
	return msg.MessageType
}

// closeStatus names how a connection ended, for the log.
//
// A connection that dies without a close handshake — the client's network
// went away, the process was killed — has no status code, and rendering the
// library's sentinel value as "StatusCode(-1)" tells an operator nothing.
func closeStatus(err error) string {
	status := websocket.CloseStatus(err)
	if status == -1 {
		return "abnormal"
	}
	return status.String()
}
