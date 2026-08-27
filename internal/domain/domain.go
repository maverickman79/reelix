// Package domain holds Reelix's internal models.
//
// These types are the project's own architecture. They are deliberately free
// of struct tags in both directions: they are not database rows and they are
// not API responses. The repository layer maps them to and from PostgreSQL;
// the API layers map them to and from their own DTOs. Neither mapping is
// allowed to reach in here and add a tag for its own convenience.
//
// Nullable database columns are pointers. A pointer distinguishes "ffprobe has
// not run yet" from "ffprobe reported zero", which matters to the scanner.
package domain

import (
	"time"
	"uuid"
)

// LibraryKind is the type of media a library holds. 0.0.1 supports movies.
type LibraryKind string

const LibraryKindMovie LibraryKind = "movie"

// MediaItemKind is the type of a single media item.
type MediaItemKind string

const MediaItemKindMovie MediaItemKind = "movie"

// StreamKind is the type of a track within a media file.
type StreamKind string

const (
	StreamKindVideo    StreamKind = "video"
	StreamKindAudio    StreamKind = "audio"
	StreamKindSubtitle StreamKind = "subtitle"
)

// User is an account.
//
// PasswordHash is opaque here: choosing and applying the hashing algorithm
// belongs to the authentication service, not to persistence. It must never be
// logged or serialised into any API response.
type User struct {
	ID           uuid.UUID
	Username     string
	PasswordHash string
	IsAdmin      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// APIToken is a native API credential belonging to a user.
//
// The plaintext token is never stored and never appears in this struct; only
// its SHA-256 is persisted. See internal/auth.
type APIToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	TokenHash string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Library is a logical collection of media. Its filesystem locations are
// LibraryPath values, not a field on the library itself.
type Library struct {
	ID        uuid.UUID
	Name      string
	Kind      LibraryKind
	CreatedAt time.Time
	UpdatedAt time.Time
}

// LibraryPath is one filesystem location belonging to a library.
type LibraryPath struct {
	ID        uuid.UUID
	LibraryID uuid.UUID
	Path      string
	CreatedAt time.Time
}

// MediaItem is one logical piece of media, backed by one or more MediaFiles.
type MediaItem struct {
	ID        uuid.UUID
	LibraryID uuid.UUID
	Kind      MediaItemKind
	Title     string
	Year      *int
	// SourcePath is where this item came from on disk: the movie's directory,
	// or the file's own path when the file sits directly in a library root.
	// Unique within a library, and what makes a re-scan update rather than
	// duplicate.
	SourcePath string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ServerSettings is the server's own identity.
//
// ServerID is 32 lowercase hex characters. Clients cache it and treat a change
// as a different server, so it is generated once and never rewritten.
type ServerSettings struct {
	ServerID   string
	ServerName string
}

// PlaybackState is one user's progress through one media item.
//
// PositionSeconds is where playback should resume, already judged against the
// resume thresholds: zero means the item is not in progress. RawPositionSeconds
// is what the client last reported, kept unjudged so that changing those
// thresholds later reinterprets history rather than discarding it.
type PlaybackState struct {
	UserID      uuid.UUID
	MediaItemID uuid.UUID

	PositionSeconds    float64
	RawPositionSeconds float64

	Played    bool
	PlayCount int

	LastPlayedAt *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// InProgress reports whether the item belongs in a resume list.
func (p PlaybackState) InProgress() bool { return p.PositionSeconds > 0 }

// Session is a client session bound to a device.
//
// This is the native record. The Jellyfin SessionInfo DTO is assembled from it
// at the compatibility boundary and never persisted.
type Session struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	TokenHash string

	DeviceID      string
	DeviceName    string
	Client        string
	ClientVersion string

	PlayableMediaTypes           []string
	SupportedCommands            []string
	SupportsMediaControl         bool
	SupportsPersistentIdentifier bool

	CreatedAt      time.Time
	LastActivityAt time.Time
}

// JobKind is the type of background work. 0.0.1 has one.
type JobKind string

const JobKindLibraryScan JobKind = "library_scan"

// JobState is where a job is in its lifecycle.
type JobState string

const (
	JobStateQueued    JobState = "queued"
	JobStateRunning   JobState = "running"
	JobStateCompleted JobState = "completed"
	JobStateFailed    JobState = "failed"
	JobStateCancelled JobState = "cancelled"
)

// Terminal reports whether a job has finished and will not change again.
func (s JobState) Terminal() bool {
	switch s {
	case JobStateCompleted, JobStateFailed, JobStateCancelled:
		return true
	}
	return false
}

// Job is one unit of long-running background work.
//
// Progress is files probed out of files discovered. ProgressTotal is 0 until
// the walk finishes, because the count is not known before then.
type Job struct {
	ID              uuid.UUID
	Kind            JobKind
	State           JobState
	LibraryID       *uuid.UUID
	ProgressCurrent int
	ProgressTotal   int
	CurrentItem     *string
	Error           *string
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// MediaFile is one physical file on disk.
//
// Everything from Container onward is ffprobe output and is nil until the file
// has been probed; ProbedAt is what distinguishes "discovered" from "probed".
type MediaFile struct {
	ID              uuid.UUID
	MediaItemID     uuid.UUID
	Path            string
	Filename        string
	SizeBytes       int64
	Container       *string
	DurationSeconds *float64
	ProbedAt        *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// MediaStream is one track within a MediaFile.
//
// Width and Height are video-only; Channels is audio-only. The alternative,
// a table per stream kind, buys nothing at this size.
type MediaStream struct {
	ID          uuid.UUID
	MediaFileID uuid.UUID
	StreamIndex int
	Kind        StreamKind
	Codec       *string
	Width       *int
	Height      *int
	Channels    *int
	BitRate     *int64
}
