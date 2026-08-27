// Package jellyfin implements the Jellyfin-compatible API surface.
//
// This package exists solely to serve existing Jellyfin clients. It is a
// translation layer: requests are converted into calls on native services, and
// native models are converted into the DTOs below. Nothing outside this
// package imports these types, and no Jellyfin-shaped structure is ever
// persisted.
//
// The DTOs are shaped from recorded traffic (see testdata/) and Jellyfin's
// published OpenAPI specification. No Jellyfin server source has been read.
//
// Wholphin is built on an SDK generated from that specification, so its
// deserialization is strict: omitting a non-nullable field produces a hard
// client-side exception, not a degraded screen. Every field below is therefore
// always emitted, and pointers are avoided where the recorded response had a
// concrete value.
package jellyfin

import "time"

// jellyfinTime formats a timestamp the way .NET does, with exactly seven
// fractional digits.
//
// Go's RFC3339Nano trims trailing zeros, producing "…:48Z" where the reference
// server produced "…:48.0000000Z". No deserialization failure has been
// observed from that, but matching the recorded format costs nothing and
// removes a variable if a client does turn out to be strict about it.
const jellyfinTime = "2006-01-02T15:04:05.0000000Z07:00"

// zeroTime is .NET's DateTime.MinValue, which the reference server emits for
// "this has never happened". A real zero Go time would render as year 1 with a
// different offset, so it is written out literally.
const zeroTime = "0001-01-01T00:00:00.0000000Z"

func formatTime(t time.Time) string {
	return t.UTC().Format(jellyfinTime)
}

// publicSystemInfo is GET /System/Info/Public.
//
// The first thing any client requests, and what "add server by address"
// succeeds or fails on.
type publicSystemInfo struct {
	LocalAddress           string `json:"LocalAddress"`
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ProductName            string `json:"ProductName"`
	OperatingSystem        string `json:"OperatingSystem"`
	ID                     string `json:"Id"`
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
}

// systemInfo is GET /System/Info.
//
// UNVALIDATED. There is no fixture for this route: Wholphin never called it
// during the Step 0 capture, so this shape comes from Jellyfin's published
// OpenAPI specification alone and has never been compared against a real
// response or exercised by a real client. If a client fails somewhere
// unexpected, this is the first place to look.
type systemInfo struct {
	publicSystemInfo

	OperatingSystemDisplayName string `json:"OperatingSystemDisplayName"`
	PackageName                string `json:"PackageName"`
	HasPendingRestart          bool   `json:"HasPendingRestart"`
	IsShuttingDown             bool   `json:"IsShuttingDown"`
	SupportsLibraryMonitor     bool   `json:"SupportsLibraryMonitor"`
	WebSocketPortNumber        int    `json:"WebSocketPortNumber"`
	CompletedInstallations     []any  `json:"CompletedInstallations"`
	CanSelfRestart             bool   `json:"CanSelfRestart"`
	CanLaunchWebBrowser        bool   `json:"CanLaunchWebBrowser"`
	ProgramDataPath            string `json:"ProgramDataPath"`
	WebPath                    string `json:"WebPath"`
	ItemsByNamePath            string `json:"ItemsByNamePath"`
	CachePath                  string `json:"CachePath"`
	LogPath                    string `json:"LogPath"`
	InternalMetadataPath       string `json:"InternalMetadataPath"`
	TranscodingTempPath        string `json:"TranscodingTempPath"`
	HasUpdateAvailable         bool   `json:"HasUpdateAvailable"`
	EncoderLocation            string `json:"EncoderLocation"`
	SystemArchitecture         string `json:"SystemArchitecture"`
}

// userDTO is the user shape returned by /Users/Me and inside the
// authentication response.
type userDTO struct {
	Name                      string            `json:"Name"`
	ServerID                  string            `json:"ServerId"`
	ID                        string            `json:"Id"`
	HasPassword               bool              `json:"HasPassword"`
	HasConfiguredPassword     bool              `json:"HasConfiguredPassword"`
	HasConfiguredEasyPassword bool              `json:"HasConfiguredEasyPassword"`
	EnableAutoLogin           bool              `json:"EnableAutoLogin"`
	LastLoginDate             string            `json:"LastLoginDate"`
	LastActivityDate          string            `json:"LastActivityDate"`
	Configuration             userConfiguration `json:"Configuration"`
	Policy                    userPolicy        `json:"Policy"`
}

// userConfiguration mirrors the reference response field for field.
//
// Reelix has no per-user display preferences in 0.0.1; these are the
// reference server's defaults, emitted so the client deserializes cleanly.
type userConfiguration struct {
	PlayDefaultAudioTrack      bool     `json:"PlayDefaultAudioTrack"`
	SubtitleLanguagePreference string   `json:"SubtitleLanguagePreference"`
	DisplayMissingEpisodes     bool     `json:"DisplayMissingEpisodes"`
	GroupedFolders             []string `json:"GroupedFolders"`
	SubtitleMode               string   `json:"SubtitleMode"`
	DisplayCollectionsView     bool     `json:"DisplayCollectionsView"`
	EnableLocalPassword        bool     `json:"EnableLocalPassword"`
	OrderedViews               []string `json:"OrderedViews"`
	LatestItemsExcludes        []string `json:"LatestItemsExcludes"`
	MyMediaExcludes            []string `json:"MyMediaExcludes"`
	HidePlayedInLatest         bool     `json:"HidePlayedInLatest"`
	RememberAudioSelections    bool     `json:"RememberAudioSelections"`
	RememberSubtitleSelections bool     `json:"RememberSubtitleSelections"`
	EnableNextEpisodeAutoPlay  bool     `json:"EnableNextEpisodeAutoPlay"`
	CastReceiverID             string   `json:"CastReceiverId"`
}

// userPolicy mirrors the reference response field for field.
//
// Reelix's permission model is a single is_admin boolean in 0.0.1. These
// fields are the translation of that into the closest Jellyfin
// representation, as the constitution requires — not a permission model Reelix
// actually implements. Anything a client might use to hide UI is set
// permissively, because refusing a capability here produces a confusing
// half-disabled client rather than a clear error.
type userPolicy struct {
	IsAdministrator                  bool     `json:"IsAdministrator"`
	IsHidden                         bool     `json:"IsHidden"`
	EnableCollectionManagement       bool     `json:"EnableCollectionManagement"`
	EnableSubtitleManagement         bool     `json:"EnableSubtitleManagement"`
	EnableLyricManagement            bool     `json:"EnableLyricManagement"`
	IsDisabled                       bool     `json:"IsDisabled"`
	BlockedTags                      []string `json:"BlockedTags"`
	AllowedTags                      []string `json:"AllowedTags"`
	EnableUserPreferenceAccess       bool     `json:"EnableUserPreferenceAccess"`
	AccessSchedules                  []any    `json:"AccessSchedules"`
	BlockUnratedItems                []string `json:"BlockUnratedItems"`
	EnableRemoteControlOfOtherUsers  bool     `json:"EnableRemoteControlOfOtherUsers"`
	EnableSharedDeviceControl        bool     `json:"EnableSharedDeviceControl"`
	EnableRemoteAccess               bool     `json:"EnableRemoteAccess"`
	EnableLiveTvManagement           bool     `json:"EnableLiveTvManagement"`
	EnableLiveTvAccess               bool     `json:"EnableLiveTvAccess"`
	EnableMediaPlayback              bool     `json:"EnableMediaPlayback"`
	EnableAudioPlaybackTranscoding   bool     `json:"EnableAudioPlaybackTranscoding"`
	EnableVideoPlaybackTranscoding   bool     `json:"EnableVideoPlaybackTranscoding"`
	EnablePlaybackRemuxing           bool     `json:"EnablePlaybackRemuxing"`
	ForceRemoteSourceTranscoding     bool     `json:"ForceRemoteSourceTranscoding"`
	EnableContentDeletion            bool     `json:"EnableContentDeletion"`
	EnableContentDeletionFromFolders []string `json:"EnableContentDeletionFromFolders"`
	EnableContentDownloading         bool     `json:"EnableContentDownloading"`
	EnableSyncTranscoding            bool     `json:"EnableSyncTranscoding"`
	EnableMediaConversion            bool     `json:"EnableMediaConversion"`
	EnabledDevices                   []string `json:"EnabledDevices"`
	EnableAllDevices                 bool     `json:"EnableAllDevices"`
	EnabledChannels                  []string `json:"EnabledChannels"`
	EnableAllChannels                bool     `json:"EnableAllChannels"`
	EnabledFolders                   []string `json:"EnabledFolders"`
	EnableAllFolders                 bool     `json:"EnableAllFolders"`
	InvalidLoginAttemptCount         int      `json:"InvalidLoginAttemptCount"`
	LoginAttemptsBeforeLockout       int      `json:"LoginAttemptsBeforeLockout"`
	MaxActiveSessions                int      `json:"MaxActiveSessions"`
	EnablePublicSharing              bool     `json:"EnablePublicSharing"`
	BlockedMediaFolders              []string `json:"BlockedMediaFolders"`
	BlockedChannels                  []string `json:"BlockedChannels"`
	RemoteClientBitrateLimit         int      `json:"RemoteClientBitrateLimit"`
	AuthenticationProviderID         string   `json:"AuthenticationProviderId"`
	PasswordResetProviderID          string   `json:"PasswordResetProviderId"`
	SyncPlayAccess                   string   `json:"SyncPlayAccess"`
}

// sessionCapabilities is what a client reported it can do.
type sessionCapabilities struct {
	PlayableMediaTypes           []string `json:"PlayableMediaTypes"`
	SupportedCommands            []string `json:"SupportedCommands"`
	SupportsMediaControl         bool     `json:"SupportsMediaControl"`
	SupportsPersistentIdentifier bool     `json:"SupportsPersistentIdentifier"`
}

// brandingOptions is GET /Branding/Configuration.
//
// LoginDisclaimer and CustomCss are POINTERS so that a nil value is OMITTED
// rather than serialised as null, which is what the reference server does.
// That was established by setting branding on a reference instance and
// reading it back: a null string is dropped from the object field by field,
// while an empty string is emitted. Reelix configures no branding, so the
// whole object it serves is {"SplashscreenEnabled":false}.
//
// Do not "complete" this by emitting the two fields as null. That is a
// different response from the one a real server sends, and it was the shape
// this would have been given by inference rather than by probing.
type brandingOptions struct {
	LoginDisclaimer     *string `json:"LoginDisclaimer,omitempty"`
	CustomCSS           *string `json:"CustomCss,omitempty"`
	SplashscreenEnabled bool    `json:"SplashscreenEnabled"`
}

// endpointInfo is GET /System/Endpoint.
//
// Both fields describe the CALLER, not the server: whether the request came
// from the same machine, and whether it came from a private network.
type endpointInfo struct {
	IsLocal     bool `json:"IsLocal"`
	IsInNetwork bool `json:"IsInNetwork"`
}

// clientCapabilitiesRequest is the body POST /Sessions/Capabilities/Full
// carries.
//
// The bare /Sessions/Capabilities route takes the same information as query
// parameters; this one takes it as JSON. Wholphin uses the query form,
// jellyfin-web uses this one, and a real server serves both.
//
// The reference also accepts DeviceProfile, AppStoreUrl and IconUrl here.
// They are deliberately absent: 0.0.1 stores none of them, transcoding is
// excluded from the milestone so a device profile has nothing to influence,
// and encoding/json ignores fields it was not given a home for. Probing
// confirmed the reference answers 204 to a body carrying unknown fields, so
// being the more permissive of the two costs nothing.
type clientCapabilitiesRequest struct {
	PlayableMediaTypes           []string `json:"PlayableMediaTypes"`
	SupportedCommands            []string `json:"SupportedCommands"`
	SupportsMediaControl         bool     `json:"SupportsMediaControl"`
	SupportsPersistentIdentifier bool     `json:"SupportsPersistentIdentifier"`
}

// playState is the playback state of a session.
//
// 0.0.1 tracks no playback state, so this is the idle shape the reference
// server returns for a session that has never played anything.
type playState struct {
	CanSeek       bool   `json:"CanSeek"`
	IsPaused      bool   `json:"IsPaused"`
	IsMuted       bool   `json:"IsMuted"`
	RepeatMode    string `json:"RepeatMode"`
	PlaybackOrder string `json:"PlaybackOrder"`
}

// sessionInfoDTO is the session shape inside the authentication response.
//
// Assembled from a domain.Session at this boundary; never stored in this form.
type sessionInfoDTO struct {
	PlayState                playState           `json:"PlayState"`
	AdditionalUsers          []any               `json:"AdditionalUsers"`
	Capabilities             sessionCapabilities `json:"Capabilities"`
	RemoteEndPoint           string              `json:"RemoteEndPoint"`
	PlayableMediaTypes       []string            `json:"PlayableMediaTypes"`
	ID                       string              `json:"Id"`
	UserID                   string              `json:"UserId"`
	UserName                 string              `json:"UserName"`
	Client                   string              `json:"Client"`
	LastActivityDate         string              `json:"LastActivityDate"`
	LastPlaybackCheckIn      string              `json:"LastPlaybackCheckIn"`
	DeviceName               string              `json:"DeviceName"`
	DeviceID                 string              `json:"DeviceId"`
	ApplicationVersion       string              `json:"ApplicationVersion"`
	IsActive                 bool                `json:"IsActive"`
	SupportsMediaControl     bool                `json:"SupportsMediaControl"`
	SupportsRemoteControl    bool                `json:"SupportsRemoteControl"`
	NowPlayingQueue          []any               `json:"NowPlayingQueue"`
	NowPlayingQueueFullItems []any               `json:"NowPlayingQueueFullItems"`
	HasCustomDeviceName      bool                `json:"HasCustomDeviceName"`
	ServerID                 string              `json:"ServerId"`
	SupportedCommands        []string            `json:"SupportedCommands"`
}

// authenticationResult is POST /Users/AuthenticateByName.
type authenticationResult struct {
	User        userDTO        `json:"User"`
	SessionInfo sessionInfoDTO `json:"SessionInfo"`
	AccessToken string         `json:"AccessToken"`
	ServerID    string         `json:"ServerId"`
}

// authenticateByNameRequest is the request body Wholphin sends.
//
// Pw is the current field. Password is the legacy one, still sent by some
// clients; both are accepted and Pw wins.
type authenticateByNameRequest struct {
	Username string `json:"Username"`
	Pw       string `json:"Pw"`
	Password string `json:"Password"`
}

// quickConnectResult is POST /QuickConnect/Initiate.
//
// Reelix does not implement QuickConnect, so this type exists only to document
// the shape the route would return if it did. The handler answers 401.
type quickConnectResult struct {
	Authenticated bool   `json:"Authenticated"`
	Secret        string `json:"Secret"`
	Code          string `json:"Code"`
	DeviceID      string `json:"DeviceId"`
	DeviceName    string `json:"DeviceName"`
	AppName       string `json:"AppName"`
	AppVersion    string `json:"AppVersion"`
	DateAdded     string `json:"DateAdded"`
}

// queryResult is the envelope Jellyfin wraps most item lists in.
//
// Generic over the item type: the routes that carry nothing use
// queryResult[any], while /Items and /UserViews carry their own DTOs.
type queryResult[T any] struct {
	Items            []T `json:"Items"`
	TotalRecordCount int `json:"TotalRecordCount"`
	StartIndex       int `json:"StartIndex"`
}

// emptyQueryResult is a well-formed empty list.
//
// Items is a non-nil slice: a nil one marshals as null, and the SDK's
// generated type declares the array non-nullable.
func emptyQueryResult() queryResult[any] {
	return queryResult[any]{Items: emptyList()}
}

// themeMediaResult is GET /Items/{id}/ThemeSongs.
//
// The same envelope with the id it was asked about attached, which is what
// the recorded response carried.
type themeMediaResult struct {
	OwnerID          string `json:"OwnerId"`
	Items            []any  `json:"Items"`
	TotalRecordCount int    `json:"TotalRecordCount"`
	StartIndex       int    `json:"StartIndex"`
}

// displayPreferences is GET /DisplayPreferences/default.
//
// Reelix stores none of this. The shape is served because the client expects
// it; see the handler for why the values are what they are.
type displayPreferences struct {
	ID                 string         `json:"Id"`
	SortBy             string         `json:"SortBy"`
	RememberIndexing   bool           `json:"RememberIndexing"`
	PrimaryImageHeight int            `json:"PrimaryImageHeight"`
	PrimaryImageWidth  int            `json:"PrimaryImageWidth"`
	CustomPrefs        map[string]any `json:"CustomPrefs"`
	ScrollDirection    string         `json:"ScrollDirection"`
	ShowBackdrop       bool           `json:"ShowBackdrop"`
	RememberSorting    bool           `json:"RememberSorting"`
	SortOrder          string         `json:"SortOrder"`
	ShowSidebar        bool           `json:"ShowSidebar"`
	Client             string         `json:"Client"`
}

// problemDetails is the RFC 9110 error body ASP.NET returns, and the shape the
// recorded 404s carry. Reelix emits it only where the reference server did.
type problemDetails struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Status  int    `json:"status"`
	TraceID string `json:"traceId"`
}
