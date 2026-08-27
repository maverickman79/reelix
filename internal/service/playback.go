package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/repository"
)

var (
	// ErrNoPlayableFile means the item exists but has no file behind it,
	// which a scan interrupted between its two writes can produce.
	ErrNoPlayableFile = errors.New("item has no playable file")

	// ErrFileOutsideLibrary means the path recorded for a file is not inside
	// any of its library's configured paths. It is a refusal to serve, not a
	// missing file.
	ErrFileOutsideLibrary = errors.New("file is outside its library")
)

// Resume thresholds.
//
// These are Reelix's values. The Step 0 capture bounds the lower one but does
// not pin it: the reference server was watched to 2.5% of a film and reported
// no resume position and an empty Continue Watching list, so the threshold is
// somewhere above that. Jellyfin exposes the same three settings in its
// dashboard with these defaults, which is published configuration rather than
// anything read from its source, and they are sensible numbers on their own
// terms — two minutes into a two-hour film is not something to resume, and
// the last few minutes are the credits.
const (
	// resumeMinFraction is how far in playback must have reached before the
	// position is worth keeping.
	resumeMinFraction = 0.05

	// resumeMaxFraction is the point past which an item counts as finished
	// rather than in progress.
	resumeMaxFraction = 0.90

	// resumeMinRuntime is the shortest item that can be resumed at all. It
	// gates resuming only: a short item watched to the end is still played.
	resumeMinRuntime = 5 * time.Minute
)

// Progress is what a reported position means.
type Progress struct {
	// ResumePosition is where playback should start next time, in seconds,
	// or zero for an item that is not in progress.
	ResumePosition float64

	// Completed reports that this position is far enough through to count as
	// having watched the item.
	Completed bool
}

// Evaluate judges a reported position against the item's runtime.
//
// Pure, and deliberately the only place the thresholds are applied: the value
// stored, the value returned to a client, and the answer to "is this in
// progress" all come from here, so they cannot drift apart.
func Evaluate(positionSeconds, runtimeSeconds float64) Progress {
	if positionSeconds <= 0 || runtimeSeconds <= 0 {
		// Nothing to resume, and with no runtime there is no fraction to
		// judge: an unprobed file is not evidence that it was finished.
		return Progress{}
	}

	fraction := positionSeconds / runtimeSeconds

	if fraction > resumeMaxFraction {
		// Finished. The position is dropped as well as marked: a client
		// offered a resume point in the closing credits would be wrong.
		return Progress{Completed: true}
	}

	if fraction < resumeMinFraction {
		return Progress{}
	}

	// The runtime floor gates resuming only, which is why it is checked here
	// and not above: a three-minute item watched to the end is still played.
	if runtimeSeconds < resumeMinRuntime.Seconds() {
		return Progress{}
	}

	return Progress{ResumePosition: positionSeconds}
}

// PlaybackService owns playback decisions and opens the files behind them.
//
// It knows nothing about Jellyfin: the compatibility layer translates a
// client's device profile into DeviceCapabilities and translates the decision
// back into whatever the client expects to read.
type PlaybackService struct {
	pool *pgxpool.Pool
}

// NewPlaybackService returns a service backed by pool.
func NewPlaybackService(pool *pgxpool.Pool) *PlaybackService {
	return &PlaybackService{pool: pool}
}

// DeviceCapabilities is what a client says it can play, flattened.
//
// An empty list means "no constraint stated", not "nothing supported": a
// client that tells Reelix nothing gets the benefit of the doubt, because
// direct play is the only thing Reelix can offer and a refusal it cannot act
// on helps nobody.
type DeviceCapabilities struct {
	Containers  []string
	VideoCodecs []string
	AudioCodecs []string
}

// Decision is the answer to "can this device play this file as it is".
type Decision struct {
	DirectPlay bool

	// Reason names what decided it, for the log and for the client's benefit
	// when the answer is no.
	Reason string
}

// LocatedFile is an item and the file recorded for it, from the database only.
//
// Locating and opening are separate steps so that a caller can decide whether
// a request is allowed before any of it reaches the filesystem. Nothing here
// has been resolved or opened yet: Open does the containment check and the
// open together, so neither can be skipped.
type LocatedFile struct {
	Item domain.MediaItem
	File domain.MediaFile

	// roots are the library's configured paths, which the file must be
	// inside. Unexported: nothing outside this package should be able to
	// hand Open a different set.
	roots []domain.LibraryPath
}

// PlayableFile is an open file, ready to be streamed.
//
// The caller must Close it. The item and file records travel with it because
// a caller serving the bytes also needs the container and the name.
type PlayableFile struct {
	LocatedFile

	Handle *os.File
	Info   fs.FileInfo
}

// Close releases the file handle.
func (p *PlayableFile) Close() error {
	if p == nil || p.Handle == nil {
		return nil
	}
	return p.Handle.Close()
}

// PlaybackReport is a client telling the server where it has got to.
type PlaybackReport struct {
	UserID uuid.UUID
	ItemID uuid.UUID

	PositionSeconds float64

	// Stopped marks the end of a playback rather than a point during one.
	// Only a stop can complete a viewing, which is what makes the play count
	// increment once per playback instead of once per report.
	Stopped bool

	// Failed reports that playback did not work. Nothing is recorded for
	// one: a playback that failed is not progress.
	Failed bool
}

// Record stores a client's progress and reports what it meant.
//
// Two round trips: one to read the runtime the thresholds are judged against,
// one to write. The thresholds stay in Evaluate rather than being folded into
// the write to save a query — policy belongs in the service, and this is the
// wrong thing to trade it for.
func (s *PlaybackService) Record(ctx context.Context, report PlaybackReport) (Progress, error) {
	if report.Failed {
		return Progress{}, nil
	}

	media := repository.NewMediaRepository(s.pool)

	runtime, err := media.ItemRuntime(ctx, report.ItemID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Progress{}, fmt.Errorf("%w: %s", ErrItemNotFound, report.ItemID)
		}
		return Progress{}, err
	}

	var runtimeSeconds float64
	if runtime != nil {
		runtimeSeconds = *runtime
	}
	progress := Evaluate(report.PositionSeconds, runtimeSeconds)

	// A viewing is counted when a completed playback ends. Counting on every
	// report past the threshold would add one every few seconds through the
	// closing credits; counting on start would count a fifteen-second sample
	// as a viewing.
	playCount := 0
	if report.Stopped && progress.Completed {
		playCount = 1
	}

	now := time.Now().UTC()
	state := domain.PlaybackState{
		UserID:             report.UserID,
		MediaItemID:        report.ItemID,
		PositionSeconds:    progress.ResumePosition,
		RawPositionSeconds: report.PositionSeconds,
		Played:             progress.Completed,
		LastPlayedAt:       &now,
	}

	if err := repository.NewPlaybackRepository(s.pool).Report(ctx, state, playCount); err != nil {
		return Progress{}, err
	}
	return progress, nil
}

// State returns one user's progress through one item, zero when there is none.
func (s *PlaybackService) State(ctx context.Context, userID, itemID uuid.UUID) (domain.PlaybackState, error) {
	state, err := repository.NewPlaybackRepository(s.pool).Get(ctx, userID, itemID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Never played is not an error; it is the common case.
			return domain.PlaybackState{UserID: userID, MediaItemID: itemID}, nil
		}
		return domain.PlaybackState{}, err
	}
	return state, nil
}

// Decide reports whether a device can direct-play an item as it stands.
//
// Only container and codec membership are checked. Bitrate ceilings, codec
// profiles, levels and reference-frame limits are the transcoding decision
// engine, which 0.0.1 does not have: Reelix either hands over the original
// file or nothing at all, so a finer-grained "no" would change no outcome
// while risking a false refusal on a file the device would have played.
func (s *PlaybackService) Decide(detail ItemDetail, caps DeviceCapabilities) Decision {
	if detail.File == nil {
		return Decision{Reason: "the item has no file"}
	}

	if len(caps.Containers) == 0 && len(caps.VideoCodecs) == 0 && len(caps.AudioCodecs) == 0 {
		return Decision{DirectPlay: true, Reason: "no device profile supplied"}
	}

	if !containerSupported(detail.File.Container, caps.Containers) {
		return Decision{Reason: fmt.Sprintf("container %s is not in the device profile",
			describe(detail.File.Container))}
	}

	for _, stream := range detail.Streams {
		switch stream.Kind {
		case domain.StreamKindVideo:
			if !codecSupported(stream.Codec, caps.VideoCodecs) {
				return Decision{Reason: fmt.Sprintf("video codec %s is not in the device profile",
					describe(stream.Codec))}
			}
		case domain.StreamKindAudio:
			if !codecSupported(stream.Codec, caps.AudioCodecs) {
				return Decision{Reason: fmt.Sprintf("audio codec %s is not in the device profile",
					describe(stream.Codec))}
			}
		}
	}
	return Decision{DirectPlay: true, Reason: "container and codecs are in the device profile"}
}

// containerSupported reports whether the device accepts this container.
//
// ffprobe names a format by every extension that shares it — an mp4 is
// "mov,mp4,m4a,3gp,3g2,mj2" — so the stored value is a list, and the file is
// playable if the device accepts any name in it.
func containerSupported(container *string, supported []string) bool {
	if len(supported) == 0 {
		return true
	}
	if container == nil {
		// Nothing probed the container. Refusing on that basis would fail a
		// file the device may well play.
		return true
	}

	for _, name := range strings.Split(*container, ",") {
		if contains(supported, strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// codecSupported reports whether the device accepts a stream's codec.
func codecSupported(codec *string, supported []string) bool {
	if len(supported) == 0 || codec == nil {
		return true
	}
	return contains(supported, *codec)
}

func contains(values []string, s string) bool {
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

// describe renders an optional value for a message.
func describe(v *string) string {
	if v == nil {
		return "(unknown)"
	}
	return strconv.Quote(*v)
}

// Locate resolves an item to a file on disk, or fails.
//
// The path recorded for the file must resolve to somewhere inside one of its
// library's configured paths. The scanner wrote that row, but an endpoint that
// opens whatever a database row names is one bad row away from serving
// anything on the filesystem, so the check is made on every request rather
// than trusted from the scan.
func (s *PlaybackService) Locate(ctx context.Context, id uuid.UUID) (*LocatedFile, error) {
	media := repository.NewMediaRepository(s.pool)

	item, err := media.GetItem(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrItemNotFound, id)
		}
		return nil, err
	}

	files, err := media.ListFilesByItem(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoPlayableFile, id)
	}
	file := files[0]

	roots, err := repository.NewLibraryRepository(s.pool).ListPaths(ctx, item.LibraryID)
	if err != nil {
		return nil, err
	}

	return &LocatedFile{Item: item, File: file, roots: roots}, nil
}

// Open resolves the file's real path, checks it lies inside the library, and
// opens it. The caller must Close it.
func (l *LocatedFile) Open() (*PlayableFile, error) {
	path, err := resolveInsideLibrary(l.File.Path, l.roots)
	if err != nil {
		return nil, err
	}

	handle, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNoPlayableFile, l.File.Path)
		}
		return nil, fmt.Errorf("opening media file: %w", err)
	}

	info, err := handle.Stat()
	if err != nil {
		handle.Close()
		return nil, fmt.Errorf("stat media file: %w", err)
	}
	if info.IsDir() {
		handle.Close()
		return nil, fmt.Errorf("%w: %s is a directory", ErrNoPlayableFile, l.File.Path)
	}

	return &PlayableFile{LocatedFile: *l, Handle: handle, Info: info}, nil
}

// resolveInsideLibrary returns the real path of a file, having checked that it
// lies within one of the library's roots.
//
// Symlinks are resolved on both sides before comparing. A lexical check alone
// would accept a link inside the library pointing anywhere on the host, which
// is the case this exists to stop.
func resolveInsideLibrary(path string, roots []domain.LibraryPath) (string, error) {
	if len(roots) == 0 {
		return "", fmt.Errorf("%w: its library has no configured path", ErrFileOutsideLibrary)
	}

	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrNoPlayableFile, path)
		}
		return "", fmt.Errorf("resolving media path: %w", err)
	}

	for _, root := range roots {
		realRoot, err := filepath.EvalSymlinks(root.Path)
		if err != nil {
			// A library root that has gone away cannot contain anything.
			continue
		}
		if real == realRoot || strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
			return real, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrFileOutsideLibrary, path)
}
