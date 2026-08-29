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
	// MatchedVia is "primary" or "alternative": which of the provider's titles
	// the match was made against. Provenance only — nothing branches on it.
	// It is the successor to the hand-resolved list as evidence that the
	// matcher's threshold is set correctly; see migration 0010.
	MatchedVia *string
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

// Metadata field names, as the repository, the API and the provenance table
// all spell them. Declared once so a typo is a compile error rather than a row
// nobody reads.
const (
	FieldOverview        = "overview"
	FieldCommunityRating = "community_rating"
	FieldOfficialRating  = "official_rating"
	FieldPremiereDate    = "premiere_date"
	FieldGenres          = "genres"

	// The artwork fields. Their VALUES live in media_item_images rather than
	// in a column, but their Source and Locked live in the same provenance
	// table as every other field, so they are managed fields like any other
	// and lock through the same guard.
	FieldImagePrimary  = "image_primary"
	FieldImageBackdrop = "image_backdrop"
	FieldImageLogo     = "image_logo"
)

// Image types, in Reelix's own lowercase spelling. The capitalised forms
// Jellyfin clients send and expect are a fact about those clients and are
// settled at the compatibility boundary; see canonicalImageType.
const (
	ImagePrimary  = "primary"
	ImageBackdrop = "backdrop"
	ImageLogo     = "logo"
)

// ImageTypes is every image type Reelix downloads, in the order a refresh
// attempts them.
//
// Three, matching the 0.0.2 scope. Each is attempted and stored independently,
// so a film whose logo cannot be fetched still gets its poster.
var ImageTypes = []string{ImagePrimary, ImageBackdrop, ImageLogo}

// imageFields maps an image type onto the field name its provenance is
// recorded under.
var imageFields = map[string]string{
	ImagePrimary:  FieldImagePrimary,
	ImageBackdrop: FieldImageBackdrop,
	ImageLogo:     FieldImageLogo,
}

// ImageField returns the provenance field name for an image type, reporting
// whether the type is one Reelix manages.
func ImageField(imageType string) (string, bool) {
	field, ok := imageFields[imageType]
	return field, ok
}

// MetadataSourceManual marks a value a person supplied.
const MetadataSourceManual = "manual"

// managedFields is every field the metadata layer manages, so that an unknown
// field name is refused at the edge rather than written into a provenance row
// nothing ever reads.
var managedFields = map[string]bool{
	FieldOverview:        true,
	FieldCommunityRating: true,
	FieldOfficialRating:  true,
	FieldPremiereDate:    true,
	FieldGenres:          true,
	FieldImagePrimary:    true,
	FieldImageBackdrop:   true,
	FieldImageLogo:       true,
}

// IsManagedField reports whether a field name is one the metadata layer knows.
func IsManagedField(field string) bool { return managedFields[field] }

// imageValueFields are the managed fields whose value is not a column.
//
// An image is lockable but NOT hand-settable in 0.0.2. Set takes a typed
// value, and supplying an image means supplying a URL or bytes — which is the
// image selection UI this milestone excludes. So an operator can pin the
// image an item has, and cannot yet choose a different one: the lock means
// "stop changing this", not "use this one".
//
// Stated as its own predicate rather than left to fail on a column lookup.
// Set would refuse these fields anyway, because metadataColumns has no entry
// for them — but an error by coincidence reads like a bug to whoever meets it,
// and it would stop being an error the moment somebody added a column.
var imageValueFields = map[string]bool{
	FieldImagePrimary:  true,
	FieldImageBackdrop: true,
	FieldImageLogo:     true,
}

// IsImageField reports whether a field's value is an image rather than a
// column, and so cannot be set by hand.
func IsImageField(field string) bool { return imageValueFields[field] }

// FieldProvenance is where one field's value came from and whether a person
// has pinned it.
type FieldProvenance struct {
	Field  string
	Source string
	// Locked stops a refresh overwriting the value. Set by default when a
	// person edits a field; see MetadataService.Set.
	Locked    bool
	UpdatedAt time.Time
}

// ItemMetadata is one item's managed fields and their provenance.
//
// Every value is optional and nil means the field has no value, which is not
// the same as an empty one: a film with no overview and a film whose overview
// is the empty string are different claims, and only the first is honest about
// not knowing.
//
// There is no runtime here. RunTimeTicks must describe the file, which ffprobe
// measured; see the TMDB provider's FetchMetadata for why a provider runtime
// is deliberately not collected.
type ItemMetadata struct {
	MediaItemID uuid.UUID

	Overview        *string
	CommunityRating *float64
	OfficialRating  *string
	PremiereDate    *time.Time
	Genres          []string

	// Images is keyed by image type. An entry with no StoragePath is a
	// recorded negative — the provider has no image of that type — which is
	// why callers ask Image rather than reading the map directly.
	Images map[string]ItemImage

	// Provenance is keyed by field name. A field with no entry has never been
	// written by anything.
	Provenance map[string]FieldProvenance
}

// ItemImage is one stored image, or a recorded absence of one.
//
// StoragePath is relative to the cache directory. It is empty exactly when the
// provider has no image of this type, and the zero value therefore reads
// correctly as "nothing to serve" for a type never fetched at all.
type ItemImage struct {
	MediaItemID uuid.UUID
	ImageType   string

	StoragePath string

	// Tag is 32 lowercase hex characters, the first half of the SHA-256 of the
	// file content. Clients build image URLs from it and use it to cache-bust.
	Tag         string
	ContentType string

	// Width and Height are the PROVIDER'S SOURCE dimensions, not the stored
	// file's.
	//
	// The provider publishes its original — TMDB reports 2000x3000 for a
	// poster — while the download asks for a size bucket and stores 780x1170.
	// The two differ by a scale factor, so anyone reading these as the pixel
	// size of the bytes Reelix serves is reading them wrong; measure the file.
	//
	// They are stored because their RATIO is what the compatibility layer
	// needs for PrimaryImageAspectRatio, and a ratio is invariant under the
	// bucket resize. Taking them from the provider's listing is what lets that
	// field be answered without decoding a single pixel.
	Width  int
	Height int

	SourceURL string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Stored reports whether this entry has bytes behind it, as opposed to being a
// recorded negative.
func (i ItemImage) Stored() bool { return i.StoragePath != "" }

// Image returns one stored image, reporting whether there is one to serve. A
// recorded negative and a type never fetched both answer false, which is what
// every caller wants: neither has bytes.
func (m ItemMetadata) Image(imageType string) (ItemImage, bool) {
	img, ok := m.Images[imageType]
	if !ok || !img.Stored() {
		return ItemImage{}, false
	}
	return img, true
}

// AspectRatio returns the image's width divided by its height, reporting
// whether it could be computed. Zero dimensions mean the provider did not say.
//
// Correct despite Width and Height describing the provider's original rather
// than the stored file: the download only rescales, so the ratio is the same
// number. This is the one thing those two fields are for.
func (i ItemImage) AspectRatio() (float64, bool) {
	if i.Width <= 0 || i.Height <= 0 {
		return 0, false
	}
	return float64(i.Width) / float64(i.Height), true
}

// Locked reports whether a field is pinned against refresh.
func (m ItemMetadata) Locked(field string) bool {
	return m.Provenance[field].Locked
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
	// JobKindLibraryMetadata fetches the managed fields for identified items.
	// Separate from identify because the two fail independently: a film can be
	// correctly identified and have stale fields, and refetching fields must
	// not re-run identification.
	JobKindLibraryMetadata JobKind = "library_metadata"
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
