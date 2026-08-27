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
