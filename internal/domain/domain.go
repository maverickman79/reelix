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

// IdentityStatus is how far identification has got with one media item.
//
// Three states rather than a nullable id, because "no external id" would
// otherwise mean both "never attempted" and "attempted and found nothing" —
// and the watch-history importer has to retry the first and leave the second
// alone. A null cannot say which is which.
type IdentityStatus string

const (
	// IdentityPending has never been attempted, or is queued for another try.
	IdentityPending IdentityStatus = "pending"
	// IdentityMatched has an identity, attributed to a provider.
	IdentityMatched IdentityStatus = "matched"
	// IdentityUnmatched was attempted and deliberately declined. It is a
	// success: a visible, fixable gap rather than a silent wrong answer.
	IdentityUnmatched IdentityStatus = "unmatched"
	// IdentityManual was set by a person. No pass may overwrite it.
	IdentityManual IdentityStatus = "manual"
)

// Identity is what a media item was identified as, and how.
//
// It carries no title, overview, rating or artwork. Those hang off an identity
// once one exists; keeping them out is what lets the importer and the artwork
// fetcher depend on this without depending on each other.
type Identity struct {
	MediaItemID uuid.UUID
	Status      IdentityStatus

	// Provider is the lowercase internal name that decided, e.g. "tmdb". Nil
	// while pending and for an unmatched item, because nothing claimed it.
	Provider *string
	// Confidence is how the match was reached: exact, year_near, title_only.
	// Nil unless Status is matched.
	Confidence *string
	// Reason explains a decline in operator-facing words. Nil unless Status is
	// unmatched. Nothing branches on its contents.
	Reason *string
	// AttemptedAt is when identification last ran. Nil while pending.
	AttemptedAt *time.Time

	// ExternalIDs are keyed by lowercase provider name — "tmdb", "imdb". The
	// capitalised spellings Jellyfin clients expect are decided at the
	// compatibility boundary, like DisplayTitle and ChannelLayout.
	ExternalIDs map[string]string

	CreatedAt time.Time
	UpdatedAt time.Time
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

// JobKind is the type of background work.
type JobKind string

const (
	// JobKindLibraryScan walks a library, probing what changed. Local: it
	// works with the network unplugged.
	JobKindLibraryScan JobKind = "library_scan"
	// JobKindLibraryIdentify asks an external provider which films a
	// library's items are. Remote, which is exactly why it is not a step
	// inside a scan — a scan must not fail because someone else's API is down.
	JobKindLibraryIdentify JobKind = "library_identify"
)

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
// Width, Height, Profile, Level, PixelFormat and the frame rates are
// video-only; Channels is audio-only. The alternative, a table per stream
// kind, buys nothing at this size.
//
// Everything here is as ffprobe reported it. Language is an ISO 639-2 code,
// not a display name: turning "eng" into "English" is presentation, and it
// happens at the boundary that has an audience to present to.
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

	Language    *string
	Title       *string
	Profile     *string
	Level       *int
	PixelFormat *string

	// Audio-only, and stored as ffprobe reported it — "5.1(side)" keeps its
	// qualifier. Rendering it for a client is presentation and happens at the
	// compatibility boundary.
	ChannelLayout *string
	SampleRate    *int

	AverageFrameRate *float64
	RealFrameRate    *float64

	// Dispositions the container set. Booleans rather than pointers: ffprobe
	// always reports them, so false means "not flagged" rather than unknown.
	IsDefault         bool
	IsForced          bool
	IsHearingImpaired bool
}
