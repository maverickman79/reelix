package jellyfin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"uuid"

	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/service"
)

// playbackInfoRequest is what a client sends before playing something.
//
// Only the fields Reelix acts on are declared. The recorded body also carries
// transcoding profiles, codec conditions and subtitle profiles, none of which
// mean anything to a server that either hands over the original file or
// nothing at all.
type playbackInfoRequest struct {
	MediaSourceID    string         `json:"MediaSourceId"`
	AudioStreamIndex *int           `json:"AudioStreamIndex"`
	DeviceProfile    *deviceProfile `json:"DeviceProfile"`
}

// deviceProfile is the part of a client's profile Reelix reads.
type deviceProfile struct {
	DirectPlayProfiles []directPlayProfile `json:"DirectPlayProfiles"`
}

// directPlayProfile is one "I can play this as it is" entry. Each field is a
// comma-separated list, and an empty one states no constraint.
type directPlayProfile struct {
	Container  string `json:"Container"`
	AudioCodec string `json:"AudioCodec"`
	VideoCodec string `json:"VideoCodec"`
	Type       string `json:"Type"`
}

// playbackInfoResponse is what the client reads back.
//
// The recorded response carries exactly these two fields — no DirectStreamUrl
// and no TranscodingUrl. The client builds the stream URL itself from the item
// id, the media source ETag and the play session id.
type playbackInfoResponse struct {
	MediaSources  []mediaSourceDTO `json:"MediaSources"`
	PlaySessionID string           `json:"PlaySessionId"`
}

// playbackReport is a client telling the server what it is doing.
//
// One shape covers start, progress and stop; the recorded bodies differ only
// in which fields they carry, and a field a client omits is simply zero.
type playbackReport struct {
	ItemID           string `json:"ItemId"`
	PlaySessionID    string `json:"PlaySessionId"`
	PositionTicks    int64  `json:"PositionTicks"`
	PlayMethod       string `json:"PlayMethod"`
	AudioStreamIndex *int   `json:"AudioStreamIndex"`
	IsPaused         bool   `json:"IsPaused"`
	CanSeek          bool   `json:"CanSeek"`
	Failed           bool   `json:"Failed"`
}

// handlePlaybackInfo serves POST /Items/{id}/PlaybackInfo.
//
// The answer to "can I play this, and how". Reelix has one answer to give —
// the original file — so this reports whether the client's own profile says it
// can handle that file, and nothing else.
func (a *API) handlePlaybackInfo(w http.ResponseWriter, r *http.Request) {
	settings, err := a.sessions.ServerSettings(r.Context())
	if err != nil {
		a.fail(r, "playback_info", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	id, err := parseCompatID(r.PathValue("id"))
	if err != nil {
		writeStatus(w, http.StatusNotFound)
		return
	}

	// A body is expected but not required: a client that sends none is
	// treated as one that stated no constraints.
	var req playbackInfoRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeStatus(w, http.StatusBadRequest)
			return
		}
	}

	detail, err := a.media.Item(r.Context(), id, userFrom(r.Context()).ID)
	if err != nil {
		if errors.Is(err, service.ErrItemNotFound) {
			writeStatus(w, http.StatusNotFound)
			return
		}
		a.fail(r, "playback_info", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	decision := a.playback.Decide(detail, req.DeviceProfile.capabilities())

	playSessionID, err := newPlaySessionID()
	if err != nil {
		a.fail(r, "playback_info", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	logging.FromContext(r.Context()).Info("playback info requested",
		slog.String(logging.KeyOperation, "playback_info"),
		slog.String(logging.KeyUserID, userFrom(r.Context()).ID.String()),
		slog.String("item_id", compatID(id)),
		slog.String("item", detail.Item.Title),
		slog.String("play_session_id", playSessionID),
		slog.Bool("direct_play", decision.DirectPlay),
		slog.String("decision", decision.Reason))

	item := newItemDetailDTO(detail, settings)

	sources := item.MediaSources
	for i := range sources {
		// The item detail states what Reelix can serve; this states what this
		// device can play. A false here gives the user "unsupported" instead
		// of a spinner that never resolves.
		sources[i].SupportsDirectPlay = decision.DirectPlay
		sources[i].SupportsDirectStream = decision.DirectPlay
	}

	a.writeJSON(w, r, http.StatusOK, playbackInfoResponse{
		MediaSources:  sources,
		PlaySessionID: playSessionID,
	})
}

// capabilities flattens the profile into what the playback service reads.
//
// Only video entries are considered: this server plays video files, and an
// audio-only profile states nothing about them. A nil profile — a client that
// sent none — yields no constraints, which the service treats as permission.
func (p *deviceProfile) capabilities() service.DeviceCapabilities {
	var caps service.DeviceCapabilities
	if p == nil {
		return caps
	}

	for _, entry := range p.DirectPlayProfiles {
		if !strings.EqualFold(entry.Type, "Video") {
			continue
		}
		caps.Containers = append(caps.Containers, trimmed(entry.Container)...)
		caps.VideoCodecs = append(caps.VideoCodecs, trimmed(entry.VideoCodec)...)
		caps.AudioCodecs = append(caps.AudioCodecs, trimmed(entry.AudioCodec)...)
	}
	return caps
}

// handleVideoStream serves GET /Videos/{id}/stream.
//
// Direct play: the original file, byte for byte, with range support from
// http.ServeContent. Every recorded seek was an open-ended "bytes=N-" and the
// largest was past what a 32-bit offset holds, so the whole path stays int64 —
// which ServeContent guarantees and this handler must not undo.
func (a *API) handleVideoStream(w http.ResponseWriter, r *http.Request) {
	id, err := parseCompatID(r.PathValue("id"))
	if err != nil {
		writeStatus(w, http.StatusNotFound)
		return
	}

	log := logging.FromContext(r.Context())

	// Located before it is opened: a request that turns out not to be allowed
	// must not have opened a file on the way to being refused.
	located, err := a.playback.Locate(r.Context(), id)
	if err != nil {
		a.failStream(w, r, err)
		return
	}

	if !a.authorizeStream(r, located.Item.ID, located.Item.UpdatedAt.String()) {
		writeStatus(w, http.StatusUnauthorized)
		return
	}

	playable, err := located.Open()
	if err != nil {
		a.failStream(w, r, err)
		return
	}
	defer playable.Close()

	// Set explicitly rather than letting ServeContent sniff. Go's sniffer
	// knows Matroska's EBML header only as WebM, so an .mkv would be served
	// as video/webm — the kind of mismatch that surfaces as an unexplained
	// failure inside a player rather than as an error.
	w.Header().Set("Content-Type", contentTypeFor(playable.File.Filename))

	log.Debug("streaming media file",
		slog.String(logging.KeyOperation, "video_stream"),
		slog.String("item_id", compatID(id)),
		slog.String("item", playable.Item.Title),
		slog.String("range", r.Header.Get("Range")),
		slog.Int64("size", playable.Info.Size()))

	// ServeContent owns the range arithmetic, If-Range, HEAD and 416. It is
	// int64 throughout, which is the property the recorded 5255045235-byte
	// seek offset depends on.
	http.ServeContent(w, r, playable.File.Filename, playable.Info.ModTime(), playable.Handle)
}

// failStream maps a playback failure onto a status.
func (a *API) failStream(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrItemNotFound), errors.Is(err, service.ErrNoPlayableFile):
		writeStatus(w, http.StatusNotFound)
	case errors.Is(err, service.ErrFileOutsideLibrary):
		// Refusing to serve, not failing to find: the path recorded for this
		// file does not resolve to somewhere inside its library.
		a.fail(r, "video_stream", err)
		writeStatus(w, http.StatusForbidden)
	default:
		a.fail(r, "video_stream", err)
		writeStatus(w, http.StatusInternalServerError)
	}
}

// authorizeStream decides whether a bare stream request may proceed.
//
// The recorded client fetches this URL from ExoPlayer's own HTTP stack, which
// sends no Authorization header and no api_key — nine recorded requests, not
// one of them carrying a credential. Requiring a session token would mean
// playback never starts, so a request is allowed when it carries EITHER:
//
//   - a valid session token, in the header or as api_key. This narrows the
//     Step 5 decision to refuse query-string credentials, and narrowly: the
//     access log omits query strings, so a token cannot leak through it.
//
//   - the media source's ETag as "tag". A client can only have learned that
//     from an authenticated PlaybackInfo call, which makes the URL a
//     capability rather than an open endpoint.
func (a *API) authorizeStream(r *http.Request, id uuid.UUID, updatedAt string) bool {
	// Read case-insensitively, and without regard to the underscore: Wholphin
	// sends "tag" and jellyfin-web sends "Tag" and "ApiKey". A real server
	// accepts every one of those spellings; reading exact names meant not
	// seeing a credential the client had sent, and answering 401 to a request
	// that carried a perfectly good one. See queryValue.
	if tag := queryValue(r, "tag"); tag != "" && tag == etagOf(compatID(id), updatedAt) {
		return true
	}

	token := ParseAuthorization(r).Token
	if token == "" {
		token = queryValue(r, "api_key")
	}
	if token == "" {
		return false
	}

	_, _, err := a.sessions.Resolve(r.Context(), token)
	return err == nil
}

// videoContentTypes maps the containers the scanner indexes onto the types the
// reference server sent for them.
var videoContentTypes = map[string]string{
	".mkv":  "video/x-matroska",
	".mp4":  "video/mp4",
	".m4v":  "video/x-m4v",
	".avi":  "video/x-msvideo",
	".mov":  "video/quicktime",
	".wmv":  "video/x-ms-wmv",
	".ts":   "video/mp2t",
	".m2ts": "video/mp2t",
	".mpg":  "video/mpeg",
	".mpeg": "video/mpeg",
	".webm": "video/webm",
}

// contentTypeFor names a file's media type from its extension.
func contentTypeFor(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))

	if t, ok := videoContentTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	// Better an honest "some bytes" than a wrong claim: a player that is told
	// nothing sniffs the container itself, which works.
	return "application/octet-stream"
}

// handlePlaybackStarted serves POST /Sessions/Playing.
//
// Logged but not stored. The recorded start body carries no PositionTicks at
// all, so storing it would mean writing a position of zero — which, judged
// against the thresholds, clears any resume position the user already had.
// Pressing play on a half-watched film would wipe the very thing it is about
// to resume from. The first progress report arrives five seconds later and
// carries a real position.
func (a *API) handlePlaybackStarted(w http.ResponseWriter, r *http.Request) {
	a.recordPlayback(w, r, "playback started", slog.LevelInfo, playbackStart)
}

// handlePlaybackProgress serves POST /Sessions/Playing/Progress.
//
// Logged at debug: a client reports every few seconds — twenty-four times in
// the recorded session for one film — and at info that would bury everything
// operationally useful.
func (a *API) handlePlaybackProgress(w http.ResponseWriter, r *http.Request) {
	a.recordPlayback(w, r, "playback progress", slog.LevelDebug, playbackTick)
}

// handlePlaybackStopped serves POST /Sessions/Playing/Stopped.
//
// The only report that can complete a viewing, which is what makes the play
// count increment once per playback rather than once every few seconds
// through the closing credits.
func (a *API) handlePlaybackStopped(w http.ResponseWriter, r *http.Request) {
	a.recordPlayback(w, r, "playback stopped", slog.LevelInfo, playbackEnd)
}

// playbackEvent is which of the three reports arrived.
type playbackEvent int

const (
	// playbackStart is logged only; see handlePlaybackStarted for why.
	playbackStart playbackEvent = iota
	// playbackTick is a position during a playback.
	playbackTick
	// playbackEnd is the end of one, and the only report that can complete a
	// viewing.
	playbackEnd
)

// recordPlayback stores what a client reports, logs it, and answers 204.
//
// Every progress report is a write. One report per five seconds per stream is
// a single-row upsert on a primary key — noise for Postgres beside the request
// that carried it — and the alternative, coalescing in memory, would put the
// state being added in the one place a crash destroys it, and would be wrong
// the moment there is a second process. The upsert suppresses no-op writes, so
// a paused client reporting an unchanged position writes nothing at all.
func (a *API) recordPlayback(w http.ResponseWriter, r *http.Request, message string,
	level slog.Level, event playbackEvent) {

	var report playbackReport
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &report); err != nil {
			writeStatus(w, http.StatusBadRequest)
			return
		}
	}

	attrs := []slog.Attr{
		slog.String(logging.KeyOperation, "playback_report"),
		slog.String(logging.KeyUserID, userFrom(r.Context()).ID.String()),
		slog.String("item_id", report.itemID()),
		slog.String("play_session_id", report.PlaySessionID),
		slog.String("position", formatTicks(report.PositionTicks)),
		slog.Int64("position_ticks", report.PositionTicks),
	}
	if report.PlayMethod != "" {
		attrs = append(attrs, slog.String("play_method", report.PlayMethod))
	}
	if report.IsPaused {
		attrs = append(attrs, slog.Bool("paused", true))
	}
	if report.Failed {
		attrs = append(attrs, slog.Bool("failed", true))
	}

	if event != playbackStart {
		progress, err := a.storeProgress(r, report, event == playbackEnd)
		switch {
		case errors.Is(err, service.ErrItemNotFound):
			// The client is reporting progress through something that is no
			// longer in the library. Nothing to record and nothing to fix.
			attrs = append(attrs, slog.Bool("unknown_item", true))

		case err != nil:
			a.fail(r, "playback_report", err)
			writeStatus(w, http.StatusInternalServerError)
			return

		default:
			attrs = append(attrs,
				slog.String("resume_position", formatTicks(secondsToTicks(progress.ResumePosition))),
				slog.Bool("completed", progress.Completed))
		}
	}

	logging.FromContext(r.Context()).LogAttrs(r.Context(), level, message, attrs...)

	writeStatus(w, http.StatusNoContent)
}

// storeProgress translates a client's report into a native one and records it.
func (a *API) storeProgress(r *http.Request, report playbackReport, stopped bool) (service.Progress, error) {
	id, err := parseCompatID(report.ItemID)
	if err != nil {
		// An id Reelix cannot parse names nothing it holds.
		return service.Progress{}, fmt.Errorf("%w: %q", service.ErrItemNotFound, report.ItemID)
	}

	return a.playback.Record(r.Context(), service.PlaybackReport{
		UserID:          userFrom(r.Context()).ID,
		ItemID:          id,
		PositionSeconds: float64(report.PositionTicks) / ticksPerSecond,
		Stopped:         stopped,
		Failed:          report.Failed,
	})
}

// itemID renders the reported item id in the dashless form.
//
// Clients send it dashed. Normalising here keeps one form in the log, so a
// grep for an id finds every line about it.
func (p playbackReport) itemID() string {
	id, err := parseCompatID(p.ItemID)
	if err != nil {
		return p.ItemID
	}
	return compatID(id)
}

// formatTicks renders a position as h:mm:ss, which is how anyone reading the
// log thinks about a position in a film.
func formatTicks(ticks int64) string {
	if ticks < 0 {
		return "0:00:00"
	}

	seconds := ticks / ticksPerSecond
	return fmt.Sprintf("%d:%02d:%02d", seconds/3600, (seconds%3600)/60, seconds%60)
}

// newPlaySessionID mints the identifier a client carries through one playback.
//
// 32 hex characters, the form the reference server used. It correlates
// PlaybackInfo, the stream request and every progress report in the log.
func newPlaySessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generating a play session id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
