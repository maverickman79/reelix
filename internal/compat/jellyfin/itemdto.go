package jellyfin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"uuid"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/repository"
	"github.com/maverickman79/reelix/internal/service"
)

// The item DTOs and their translation.
//
// Shapes come from the recorded traffic in testdata/. Two rules govern what
// goes in a field Reelix cannot fill:
//
//   - A field Reelix has no data for is null, never a fabricated value.
//     0.0.1 excludes metadata scraping and artwork downloading, so ratings,
//     overviews, release dates and image tags are genuinely unknown. A
//     CommunityRating of 0 renders as a zero-star rating and a PremiereDate
//     of 0001-01-01 as year 1: a fabricated value is a visible lie, where
//     null is exactly what Jellyfin itself reports for an unscraped library.
//
//   - An ENUM-valued field is the one exception. Its "unknown" member is a
//     real value — "Unknown", "None", "FileSystem" — and emitting null or ""
//     where the SDK expects an enum is a deserialization exception rather
//     than an empty screen. Enum fields therefore always carry a valid
//     member, even when the honest answer is that we did not probe it.
//
// ticksPerSecond is .NET's unit: one ten-millionth of a second.
const ticksPerSecond = 10_000_000

// itemDTO is a movie as it appears in a list.
//
// The fields are those present on every recorded Movie item, plus the ones
// Reelix can genuinely fill. Nine further fields appear on some recorded items
// and not others — the client rendered both — so their absence here is proven
// safe rather than assumed.
type itemDTO struct {
	Name         string `json:"Name"`
	ServerID     string `json:"ServerId"`
	ID           string `json:"Id"`
	SortName     string `json:"SortName"`
	CanDelete    bool   `json:"CanDelete"`
	HasSubtitles bool   `json:"HasSubtitles"`
	Container    string `json:"Container"`

	// Unknown without metadata. See the file comment.
	PremiereDate    *string  `json:"PremiereDate"`
	ChannelID       *string  `json:"ChannelId"`
	Overview        *string  `json:"Overview"`
	CommunityRating *float64 `json:"CommunityRating"`
	CriticRating    *float64 `json:"CriticRating"`
	OfficialRating  *string  `json:"OfficialRating"`

	// No artwork, so there is no primary image to have a ratio.
	PrimaryImageAspectRatio *float64 `json:"PrimaryImageAspectRatio"`

	RunTimeTicks   *int64 `json:"RunTimeTicks"`
	ProductionYear *int   `json:"ProductionYear"`

	IsFolder bool        `json:"IsFolder"`
	Type     string      `json:"Type"`
	UserData userDataDTO `json:"UserData"`

	// A movie contains nothing, so this is genuinely zero rather than
	// unknown. The recorded server sent it on the latest-items row.
	ChildCount int `json:"ChildCount"`

	VideoType string `json:"VideoType"`

	// Empty until artwork exists. A tag is how a client builds an image URL,
	// so advertising one Reelix cannot serve would produce a broken request
	// rather than the placeholder it shows for an item with no images.
	ImageTags         map[string]string            `json:"ImageTags"`
	BackdropImageTags []string                     `json:"BackdropImageTags"`
	ImageBlurHashes   map[string]map[string]string `json:"ImageBlurHashes"`

	LocationType string `json:"LocationType"`
	MediaType    string `json:"MediaType"`
}

// itemDetailDTO is a movie as it appears on its own detail screen.
//
// All 55 fields of the recorded detail response, which was identical across
// every recording of it.
type itemDetailDTO struct {
	itemDTO

	CanDownload              bool           `json:"CanDownload"`
	Chapters                 []any          `json:"Chapters"`
	DateCreated              string         `json:"DateCreated"`
	DisplayPreferencesID     string         `json:"DisplayPreferencesId"`
	EnableMediaSourceDisplay bool           `json:"EnableMediaSourceDisplay"`
	Etag                     string         `json:"Etag"`
	ExternalUrls             []any          `json:"ExternalUrls"`
	GenreItems               []any          `json:"GenreItems"`
	Genres                   []string       `json:"Genres"`
	LocalTrailerCount        int            `json:"LocalTrailerCount"`
	LockData                 bool           `json:"LockData"`
	LockedFields             []string       `json:"LockedFields"`
	ParentID                 *string        `json:"ParentId"`
	Path                     string         `json:"Path"`
	People                   []any          `json:"People"`
	PlayAccess               string         `json:"PlayAccess"`
	ProductionLocations      []string       `json:"ProductionLocations"`
	ProviderIds              map[string]any `json:"ProviderIds"`
	RemoteTrailers           []any          `json:"RemoteTrailers"`
	SpecialFeatureCount      int            `json:"SpecialFeatureCount"`
	Studios                  []any          `json:"Studios"`
	Taglines                 []string       `json:"Taglines"`
	Tags                     []string       `json:"Tags"`
	Trickplay                map[string]any `json:"Trickplay"`

	// Unknown without metadata.
	OriginalTitle *string `json:"OriginalTitle"`

	// Probed.
	Width  *int `json:"Width"`
	Height *int `json:"Height"`
	IsHD   bool `json:"IsHD"`

	MediaSources []mediaSourceDTO `json:"MediaSources"`
	MediaStreams []mediaStreamDTO `json:"MediaStreams"`
}

// userDataDTO is a client's per-user state for an item.
//
// PlaybackPositionTicks is the resume position, already judged against the
// thresholds: an item watched for two minutes reports zero, which is what the
// reference server did in the same situation. LastPlayedDate is null until
// something has been played — the recorded server omitted the field entirely
// in that state, and the client rendered both.
type userDataDTO struct {
	PlaybackPositionTicks int64   `json:"PlaybackPositionTicks"`
	PlayCount             int     `json:"PlayCount"`
	IsFavorite            bool    `json:"IsFavorite"`
	Played                bool    `json:"Played"`
	LastPlayedDate        *string `json:"LastPlayedDate"`
	Key                   string  `json:"Key"`
	ItemID                string  `json:"ItemId"`
}

// mediaSourceDTO describes the file behind an item.
//
// This is what Step 7's direct-play decision is built on: the client compares
// the container and streams against its own profile before asking to play.
type mediaSourceDTO struct {
	Protocol     string `json:"Protocol"`
	ID           string `json:"Id"`
	Path         string `json:"Path"`
	Type         string `json:"Type"`
	Container    string `json:"Container"`
	Size         *int64 `json:"Size"`
	Name         string `json:"Name"`
	IsRemote     bool   `json:"IsRemote"`
	ETag         string `json:"ETag"`
	RunTimeTicks *int64 `json:"RunTimeTicks"`

	ReadAtNativeFramerate bool `json:"ReadAtNativeFramerate"`
	IgnoreDts             bool `json:"IgnoreDts"`
	IgnoreIndex           bool `json:"IgnoreIndex"`
	GenPtsInput           bool `json:"GenPtsInput"`

	SupportsDirectPlay   bool `json:"SupportsDirectPlay"`
	SupportsDirectStream bool `json:"SupportsDirectStream"`
	// False, unlike the reference server. Reelix cannot transcode in 0.0.1,
	// and advertising a capability it would then fail to deliver is worse
	// than declining it: the client falls back to direct play, which works.
	SupportsTranscoding bool `json:"SupportsTranscoding"`
	SupportsProbing     bool `json:"SupportsProbing"`

	IsInfiniteStream bool `json:"IsInfiniteStream"`
	RequiresOpening  bool `json:"RequiresOpening"`
	RequiresClosing  bool `json:"RequiresClosing"`
	RequiresLooping  bool `json:"RequiresLooping"`
	HasSegments      bool `json:"HasSegments"`

	UseMostCompatibleTranscodingProfile bool   `json:"UseMostCompatibleTranscodingProfile"`
	TranscodingSubProtocol              string `json:"TranscodingSubProtocol"`

	VideoType               string           `json:"VideoType"`
	Bitrate                 *int             `json:"Bitrate"`
	DefaultAudioStreamIndex *int             `json:"DefaultAudioStreamIndex"`
	MediaStreams            []mediaStreamDTO `json:"MediaStreams"`
	MediaAttachments        []any            `json:"MediaAttachments"`
	Formats                 []string         `json:"Formats"`
	RequiredHTTPHeaders     map[string]any   `json:"RequiredHttpHeaders"`
}

// mediaStreamDTO is one track within a file.
//
// One shape serves video, audio and subtitle streams; the recorded server
// emitted a different key set per type, and a field that does not apply is
// null. What remains null here is what the scanner still does not probe —
// colour metadata, sample rate, and the container-level detail below it.
type mediaStreamDTO struct {
	Index   int     `json:"Index"`
	Type    string  `json:"Type"`
	Codec   *string `json:"Codec"`
	Width   *int    `json:"Width"`
	Height  *int    `json:"Height"`
	BitRate *int64  `json:"BitRate"`

	Channels *int `json:"Channels"`
	// Probed. Clients read both directly rather than through DisplayTitle.
	ChannelLayout *string `json:"ChannelLayout"`
	SampleRate    *int    `json:"SampleRate"`
	Language      *string `json:"Language"`

	AspectRatio  *string `json:"AspectRatio"`
	DisplayTitle string  `json:"DisplayTitle"`

	// The track's own name, where the container carries one: "SDH",
	// "Commentary", "Latin American". Null for a track the container did not
	// name, which is most video streams.
	Title *string `json:"Title"`

	// Enum members, never null. See the file comment.
	VideoRange         string `json:"VideoRange"`
	VideoRangeType     string `json:"VideoRangeType"`
	AudioSpatialFormat string `json:"AudioSpatialFormat"`

	// Dispositions. The first three come from the container; IsInterlaced is
	// not probed and IsExternal is genuinely false, since every stream Reelix
	// knows about is inside the file.
	IsDefault              bool `json:"IsDefault"`
	IsForced               bool `json:"IsForced"`
	IsHearingImpaired      bool `json:"IsHearingImpaired"`
	IsExternal             bool `json:"IsExternal"`
	IsInterlaced           bool `json:"IsInterlaced"`
	IsTextSubtitleStream   bool `json:"IsTextSubtitleStream"`
	SupportsExternalStream bool `json:"SupportsExternalStream"`

	// Probed codec detail. Level, Profile and PixelFormat are video-only in
	// practice; ffprobe reports no level for audio or subtitle streams and
	// its -99 sentinel is mapped to null well before here.
	Level       *float64 `json:"Level"`
	Profile     *string  `json:"Profile"`
	PixelFormat *string  `json:"PixelFormat"`

	// Two measured rates. ReferenceFrameRate stays null: the captures show
	// it equal to both of the others, but what the reference server means by
	// it is not something the traffic reveals, and a value that happens to
	// be right for constant-frame-rate content is still a guess.
	AverageFrameRate   *float64 `json:"AverageFrameRate"`
	RealFrameRate      *float64 `json:"RealFrameRate"`
	ReferenceFrameRate *float64 `json:"ReferenceFrameRate"`

	// Unprobed detail.
	IsAVC                    *bool   `json:"IsAVC"`
	IsAnamorphic             *bool   `json:"IsAnamorphic"`
	BitDepth                 *int    `json:"BitDepth"`
	RefFrames                *int    `json:"RefFrames"`
	NalLengthSize            *string `json:"NalLengthSize"`
	TimeBase                 *string `json:"TimeBase"`
	ColorSpace               *string `json:"ColorSpace"`
	ColorTransfer            *string `json:"ColorTransfer"`
	ColorPrimaries           *string `json:"ColorPrimaries"`
	LocalizedDefault         *string `json:"LocalizedDefault"`
	LocalizedExternal        *string `json:"LocalizedExternal"`
	LocalizedForced          *string `json:"LocalizedForced"`
	LocalizedHearingImpaired *string `json:"LocalizedHearingImpaired"`
	LocalizedUndefined       *string `json:"LocalizedUndefined"`
}

// viewDTO is a library presented as something to browse.
//
// Jellyfin calls it a CollectionFolder. It is the response Wholphin cannot
// render a home screen without.
type viewDTO struct {
	Name                     string                       `json:"Name"`
	ServerID                 string                       `json:"ServerId"`
	ID                       string                       `json:"Id"`
	Etag                     string                       `json:"Etag"`
	DateCreated              string                       `json:"DateCreated"`
	DateLastMediaAdded       string                       `json:"DateLastMediaAdded"`
	CanDelete                bool                         `json:"CanDelete"`
	CanDownload              bool                         `json:"CanDownload"`
	SortName                 string                       `json:"SortName"`
	ExternalUrls             []any                        `json:"ExternalUrls"`
	Path                     string                       `json:"Path"`
	EnableMediaSourceDisplay bool                         `json:"EnableMediaSourceDisplay"`
	ChannelID                *string                      `json:"ChannelId"`
	Taglines                 []string                     `json:"Taglines"`
	Genres                   []string                     `json:"Genres"`
	PlayAccess               string                       `json:"PlayAccess"`
	RemoteTrailers           []any                        `json:"RemoteTrailers"`
	ProviderIds              map[string]any               `json:"ProviderIds"`
	IsFolder                 bool                         `json:"IsFolder"`
	ParentID                 *string                      `json:"ParentId"`
	Type                     string                       `json:"Type"`
	People                   []any                        `json:"People"`
	Studios                  []any                        `json:"Studios"`
	GenreItems               []any                        `json:"GenreItems"`
	LocalTrailerCount        int                          `json:"LocalTrailerCount"`
	UserData                 userDataDTO                  `json:"UserData"`
	ChildCount               int                          `json:"ChildCount"`
	SpecialFeatureCount      int                          `json:"SpecialFeatureCount"`
	DisplayPreferencesID     string                       `json:"DisplayPreferencesId"`
	Tags                     []string                     `json:"Tags"`
	CollectionType           string                       `json:"CollectionType"`
	ImageTags                map[string]string            `json:"ImageTags"`
	BackdropImageTags        []string                     `json:"BackdropImageTags"`
	ImageBlurHashes          map[string]map[string]string `json:"ImageBlurHashes"`
	LocationType             string                       `json:"LocationType"`
	MediaType                string                       `json:"MediaType"`
	LockedFields             []string                     `json:"LockedFields"`
	LockData                 bool                         `json:"LockData"`
}

// newViewDTO translates a library into a collection folder.
func newViewDTO(v service.View, settings domain.ServerSettings) viewDTO {
	id := compatID(v.Library.ID)

	return viewDTO{
		Name:        v.Library.Name,
		ServerID:    settings.ServerID,
		ID:          id,
		Etag:        etagOf(id, v.Library.UpdatedAt.String()),
		DateCreated: formatTime(v.Library.CreatedAt),
		// Reelix does not track when a library last gained an item. The
		// reference server reported "never" here for the same reason.
		DateLastMediaAdded: zeroTime,
		// A client must not offer to delete or download a whole library
		// through an API that implements neither.
		CanDelete:                false,
		CanDownload:              false,
		SortName:                 strings.ToLower(v.Library.Name),
		ExternalUrls:             emptyList(),
		Path:                     "",
		EnableMediaSourceDisplay: true,
		ChannelID:                nil,
		Taglines:                 emptyStrings(),
		Genres:                   emptyStrings(),
		PlayAccess:               "Full",
		RemoteTrailers:           emptyList(),
		ProviderIds:              map[string]any{},
		IsFolder:                 true,
		// Reelix has no folder above a library. The reference server's views
		// hang off a media root Reelix does not have, and inventing an id
		// that resolves to nothing would be worse than saying "none".
		ParentID:             nil,
		Type:                 "CollectionFolder",
		People:               emptyList(),
		Studios:              emptyList(),
		GenreItems:           emptyList(),
		LocalTrailerCount:    0,
		UserData:             newUserDataDTO(v.Library.ID, domain.PlaybackState{}),
		ChildCount:           v.ItemCount,
		SpecialFeatureCount:  0,
		DisplayPreferencesID: id,
		Tags:                 emptyStrings(),
		CollectionType:       "movies",
		ImageTags:            map[string]string{},
		BackdropImageTags:    emptyStrings(),
		ImageBlurHashes:      map[string]map[string]string{},
		LocationType:         "FileSystem",
		MediaType:            "Unknown",
		LockedFields:         emptyStrings(),
		LockData:             false,
	}
}

// newItemDTO translates a media item into its list representation.
func newItemDTO(row repository.ItemWithFile, settings domain.ServerSettings) itemDTO {
	id := compatID(row.Item.ID)

	dto := itemDTO{
		Name:         row.Item.Title,
		ServerID:     settings.ServerID,
		ID:           id,
		SortName:     strings.ToLower(row.Item.Title),
		CanDelete:    false,
		HasSubtitles: row.HasSubtitles,
		// Unknown without metadata; see the file comment.
		PremiereDate:      nil,
		ChannelID:         nil,
		Overview:          nil,
		CommunityRating:   nil,
		CriticRating:      nil,
		OfficialRating:    nil,
		ProductionYear:    row.Item.Year,
		IsFolder:          false,
		Type:              "Movie",
		UserData:          newUserDataDTO(row.Item.ID, row.State),
		VideoType:         "VideoFile",
		ImageTags:         map[string]string{},
		BackdropImageTags: emptyStrings(),
		ImageBlurHashes:   map[string]map[string]string{},
		LocationType:      "FileSystem",
		MediaType:         "Video",
	}

	if row.File != nil {
		dto.Container = containerName(row.File.Container)
		dto.RunTimeTicks = runtimeTicks(row.File.DurationSeconds)
	}
	return dto
}

// newItemDetailDTO translates a media item into its detail representation.
func newItemDetailDTO(detail service.ItemDetail, settings domain.ServerSettings) itemDetailDTO {
	row := repository.ItemWithFile{
		Item:         detail.Item,
		File:         detail.File,
		HasSubtitles: detail.HasSubtitles,
		State:        detail.State,
	}

	id := compatID(detail.Item.ID)
	parent := compatID(detail.Item.LibraryID)
	streams := newStreamDTOs(detail.Streams)

	dto := itemDetailDTO{
		itemDTO:                  newItemDTO(row, settings),
		CanDownload:              false,
		Chapters:                 emptyList(),
		DateCreated:              formatTime(detail.Item.CreatedAt),
		DisplayPreferencesID:     id,
		EnableMediaSourceDisplay: true,
		Etag:                     etagOf(id, detail.Item.UpdatedAt.String()),
		ExternalUrls:             emptyList(),
		GenreItems:               emptyList(),
		Genres:                   emptyStrings(),
		LocalTrailerCount:        0,
		LockData:                 false,
		LockedFields:             emptyStrings(),
		ParentID:                 &parent,
		// Emptied deliberately: the constitution forbids leaking filesystem
		// layout through an API, and no client needs a path to play a file.
		Path:                "",
		People:              emptyList(),
		PlayAccess:          "Full",
		ProductionLocations: emptyStrings(),
		ProviderIds:         map[string]any{},
		RemoteTrailers:      emptyList(),
		SpecialFeatureCount: 0,
		Studios:             emptyList(),
		Taglines:            emptyStrings(),
		Tags:                emptyStrings(),
		Trickplay:           map[string]any{},
		OriginalTitle:       nil,
		MediaStreams:        streams,
		MediaSources:        emptySources(),
	}

	if video := videoStream(detail.Streams); video != nil {
		dto.Width = video.Width
		dto.Height = video.Height
		dto.IsHD = video.Height != nil && *video.Height >= 720
	}

	if detail.File != nil {
		dto.MediaSources = []mediaSourceDTO{newMediaSourceDTO(detail, dto.Etag, streams)}
	}
	return dto
}

// newMediaSourceDTO describes the file a client would play.
func newMediaSourceDTO(detail service.ItemDetail, etag string, streams []mediaStreamDTO) mediaSourceDTO {
	file := detail.File
	ticks := runtimeTicks(file.DurationSeconds)

	source := mediaSourceDTO{
		Protocol:  "File",
		ID:        compatID(detail.Item.ID),
		Path:      "",
		Type:      "Default",
		Container: mediaSourceContainer(file.Container, file.Filename),
		// The filename without its extension, as the reference server sent
		// it. This is release information rather than filesystem layout, and
		// a client displays it when a user picks between versions.
		Name:                                strings.TrimSuffix(file.Filename, filepath.Ext(file.Filename)),
		IsRemote:                            false,
		ETag:                                etag,
		RunTimeTicks:                        ticks,
		ReadAtNativeFramerate:               false,
		IgnoreDts:                           false,
		IgnoreIndex:                         false,
		GenPtsInput:                         false,
		SupportsDirectPlay:                  true,
		SupportsDirectStream:                true,
		SupportsTranscoding:                 false,
		SupportsProbing:                     true,
		IsInfiniteStream:                    false,
		RequiresOpening:                     false,
		RequiresClosing:                     false,
		RequiresLooping:                     false,
		HasSegments:                         false,
		UseMostCompatibleTranscodingProfile: false,
		// An enum member even though Reelix does not transcode: "" is not a
		// member, and the SDK deserializes this as an enum.
		TranscodingSubProtocol:  "http",
		VideoType:               "VideoFile",
		MediaStreams:            streams,
		MediaAttachments:        emptyList(),
		Formats:                 emptyStrings(),
		RequiredHTTPHeaders:     map[string]any{},
		DefaultAudioStreamIndex: defaultAudioIndex(detail.Streams),
	}

	if file.SizeBytes > 0 {
		size := file.SizeBytes
		source.Size = &size
	}

	// The overall bitrate is derived rather than probed: size over duration is
	// what a player needs to judge whether the network can keep up, and it is
	// arithmetic on two stored values rather than a guess.
	if file.SizeBytes > 0 && file.DurationSeconds != nil && *file.DurationSeconds > 0 {
		bitrate := int(float64(file.SizeBytes) * 8 / *file.DurationSeconds)
		source.Bitrate = &bitrate
	}
	return source
}

// newStreamDTOs translates the probed streams.
func newStreamDTOs(streams []domain.MediaStream) []mediaStreamDTO {
	out := make([]mediaStreamDTO, 0, len(streams))

	for _, s := range streams {
		dto := mediaStreamDTO{
			Index:    s.StreamIndex,
			Type:     streamType(s.Kind),
			Codec:    s.Codec,
			Width:    s.Width,
			Height:   s.Height,
			BitRate:  s.BitRate,
			Channels: s.Channels,

			// Probed metadata, passed through as stored. Language is the raw
			// ISO 639 code the container carried, which is what the recorded
			// server sent; the English name it also composed lives only in
			// DisplayTitle.
			ChannelLayout:    displayChannelLayout(s.ChannelLayout),
			SampleRate:       s.SampleRate,
			Language:         s.Language,
			Title:            s.Title,
			Profile:          s.Profile,
			Level:            level(s.Level),
			PixelFormat:      s.PixelFormat,
			AverageFrameRate: s.AverageFrameRate,
			RealFrameRate:    s.RealFrameRate,

			// Dispositions as the container set them. Until 0.0.2 these were
			// hardcoded false, which the fixture comparison could not catch:
			// false is the same JSON type as true.
			IsDefault:         s.IsDefault,
			IsForced:          s.IsForced,
			IsHearingImpaired: s.IsHearingImpaired,

			// Enum members rather than null; the honest answer is that the
			// scanner does not read colour metadata.
			VideoRange:         "Unknown",
			VideoRangeType:     "Unknown",
			AudioSpatialFormat: "None",
			// Every stream Reelix knows about is inside the container.
			IsExternal:             false,
			SupportsExternalStream: false,
			IsTextSubtitleStream:   s.Kind == domain.StreamKindSubtitle && isTextSubtitle(s.Codec),
			AspectRatio:            aspectRatio(s.Width, s.Height),
			DisplayTitle:           displayTitle(s),

			LocalizedDefault:         &localizedLabels.def,
			LocalizedForced:          &localizedLabels.forced,
			LocalizedExternal:        &localizedLabels.external,
			LocalizedHearingImpaired: &localizedLabels.hearingImpaired,
			LocalizedUndefined:       &localizedLabels.undefined,
		}
		out = append(out, dto)
	}
	return out
}

// newUserDataDTO builds the per-user state for an item.
//
// Key is the dashed id, which is what the reference server used for items
// carrying no external provider id.
func newUserDataDTO(id uuid.UUID, state domain.PlaybackState) userDataDTO {
	dto := userDataDTO{
		PlaybackPositionTicks: secondsToTicks(state.PositionSeconds),
		PlayCount:             state.PlayCount,
		Played:                state.Played,
		Key:                   id.String(),
		ItemID:                compatID(id),
	}

	if state.LastPlayedAt != nil {
		played := formatTime(*state.LastPlayedAt)
		dto.LastPlayedDate = &played
	}
	return dto
}

// secondsToTicks converts a stored position into the unit clients speak.
func secondsToTicks(seconds float64) int64 {
	if seconds <= 0 {
		return 0
	}
	return int64(seconds * ticksPerSecond)
}

// localizedLabels are the strings the recorded server sent for these fields,
// reproduced exactly.
//
// Calling them "localized" is Jellyfin's naming, not a promise Reelix makes:
// the reference server sends English regardless of the requesting client, and
// these are the words it sent. Returning them is answering a question, not
// implementing translation. If Reelix ever localises anything, this is one of
// the places that would have to change, and it will be a deliberate change
// rather than a null quietly becoming a word.
var localizedLabels = struct {
	def, forced, external, hearingImpaired, undefined string
}{
	def:             "Default",
	forced:          "Forced",
	external:        "External",
	hearingImpaired: "Hearing Impaired",
	undefined:       "Undefined",
}

// level widens a stored codec level for the DTO, which types it as a number
// because the recorded server sent 40 for H.264 level 4.0.
func level(v *int) *float64 {
	if v == nil {
		return nil
	}
	f := float64(*v)
	return &f
}

// videoStream returns the first video stream, or nil.
func videoStream(streams []domain.MediaStream) *domain.MediaStream {
	for i := range streams {
		if streams[i].Kind == domain.StreamKindVideo {
			return &streams[i]
		}
	}
	return nil
}

// defaultAudioIndex returns the index of the first audio stream, or nil when
// the file has none.
func defaultAudioIndex(streams []domain.MediaStream) *int {
	for _, s := range streams {
		if s.Kind == domain.StreamKindAudio {
			index := s.StreamIndex
			return &index
		}
	}
	return nil
}

// streamType maps a native stream kind onto Jellyfin's enum.
func streamType(k domain.StreamKind) string {
	switch k {
	case domain.StreamKindVideo:
		return "Video"
	case domain.StreamKindAudio:
		return "Audio"
	case domain.StreamKindSubtitle:
		return "Subtitle"
	default:
		return "Unknown"
	}
}

// textSubtitleCodecs are the subtitle formats carried as text.
//
// The distinction matters to a player: a text subtitle can be rendered
// directly, where an image-based one has to be composited. Derived from the
// codec because the scanner records it, unlike the flags around it.
var textSubtitleCodecs = map[string]bool{
	"subrip": true, "srt": true, "ass": true, "ssa": true,
	"mov_text": true, "webvtt": true, "text": true,
}

func isTextSubtitle(codec *string) bool {
	if codec == nil {
		return false
	}
	return textSubtitleCodecs[strings.ToLower(*codec)]
}

// containerName renders the container the way the recorded server did.
//
// ffprobe reports a matroska file's format as "matroska,webm"; the reference
// server reported "mkv" for exactly those files, while leaving an mp4's
// "mov,mp4,m4a,3gp,3g2,mj2" untouched. The asymmetry looks arbitrary and is
// copied deliberately: a client matches this string against its direct-play
// profile, so it has to be the string the client expects.
func containerName(container *string) string {
	if container == nil {
		return ""
	}
	if *container == "matroska,webm" {
		return "mkv"
	}
	return *container
}

// mediaSourceContainer renders the container a MEDIA SOURCE reports.
//
// This is deliberately not the same string as the item-level Container, and
// the difference is the reference server's, not ours. Probed against a real
// 10.11.8 with one file per extension, all three of the mp4 family probing as
// the identical ffprobe list "mov,mp4,m4a,3gp,3g2,mj2":
//
//	extension   Item.Container              MediaSource.Container
//	.mp4        mov,mp4,m4a,3gp,3g2,mj2     mp4
//	.m4v        mov,mp4,m4a,3gp,3g2,mj2     mov
//	.mov        mov,mp4,m4a,3gp,3g2,mj2     mov
//	.mkv        mkv                         mkv
//
// So the item keeps ffprobe's raw list and the media source carries a single
// token. The rule that fits every observation: the file's extension when it
// appears in the list, otherwise the FIRST token — which is why .m4v reports
// "mov" rather than "m4v", and why this cannot be simplified to "use the
// extension".
//
// It matters because a client BUILDS A URL from this field. jellyfin-web
// requests /Videos/{id}/stream.{container}, so a raw list here produced
// /Videos/{id}/stream.mov,mp4,m4a,3gp,3g2,mj2 and playback failed. Wholphin
// never exposed it: every file in the Step 0 capture that it played was
// matroska, which containerName already collapsed to "mkv".
func mediaSourceContainer(container *string, filename string) string {
	name := containerName(container)

	// Already a single token — including "mkv", which containerName has
	// resolved from ffprobe's "matroska,webm".
	if !strings.Contains(name, ",") {
		return name
	}

	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), ".")

	tokens := strings.Split(name, ",")
	for _, token := range tokens {
		if token == ext {
			return token
		}
	}
	return tokens[0]
}

// runtimeTicks converts a duration in seconds into .NET ticks.
func runtimeTicks(seconds *float64) *int64 {
	if seconds == nil {
		return nil
	}
	ticks := int64(*seconds * ticksPerSecond)
	return &ticks
}

// aspectRatio renders a video stream's shape as "16:9".
func aspectRatio(width, height *int) *string {
	if width == nil || height == nil || *width <= 0 || *height <= 0 {
		return nil
	}

	divisor := gcd(*width, *height)
	ratio := fmt.Sprintf("%d:%d", *width/divisor, *height/divisor)
	return &ratio
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

// etagOf builds a cache tag from an item's identity and its last change.
//
// 32 hex characters, matching the form the recorded server used. It changes
// when the item changes, which is the whole contract of an etag.
func etagOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// emptySources is a non-nil empty list, for an item with no file behind it.
func emptySources() []mediaSourceDTO { return []mediaSourceDTO{} }
