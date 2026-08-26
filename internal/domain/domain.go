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
	CreatedAt time.Time
	UpdatedAt time.Time
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
