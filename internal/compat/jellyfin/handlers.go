package jellyfin

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/service"
)

// handlePublicSystemInfo serves GET /System/Info/Public.
//
// The first request any client makes and the one "add server by address"
// depends on. It is unauthenticated by necessity.
func (a *API) handlePublicSystemInfo(w http.ResponseWriter, r *http.Request) {
	settings, err := a.sessions.ServerSettings(r.Context())
	if err != nil {
		a.fail(r, "public_system_info", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	a.writeJSON(w, r, http.StatusOK, a.publicInfo(r, settings))
}

// publicInfo builds the public system info payload.
func (a *API) publicInfo(r *http.Request, settings domain.ServerSettings) publicSystemInfo {
	return publicSystemInfo{
		LocalAddress:    localAddress(r),
		ServerName:      settings.ServerName,
		Version:         jellyfinVersion,
		ProductName:     productName,
		OperatingSystem: "",
		ID:              settings.ServerID,
		// Reelix has no setup wizard. Reporting false would make a client
		// offer to run one that does not exist.
		StartupWizardCompleted: true,
	}
}

// handleSystemInfo serves GET /System/Info.
//
// UNVALIDATED: no fixture exists for this route and the Wholphin flow never
// calls it. See the systemInfo type for the full caveat.
func (a *API) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	settings, err := a.sessions.ServerSettings(r.Context())
	if err != nil {
		a.fail(r, "system_info", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	a.writeJSON(w, r, http.StatusOK, systemInfo{
		publicSystemInfo:           a.publicInfo(r, settings),
		OperatingSystemDisplayName: "Linux",
		PackageName:                "reelix",
		HasPendingRestart:          false,
		IsShuttingDown:             false,
		SupportsLibraryMonitor:     false,
		WebSocketPortNumber:        8080,
		CompletedInstallations:     emptyList(),
		CanSelfRestart:             false,
		CanLaunchWebBrowser:        false,
		// Paths are reported as empty rather than as real filesystem
		// locations: the constitution forbids leaking filesystem detail
		// through an API, and no client needs them to play a file.
		ProgramDataPath:      "",
		WebPath:              "",
		ItemsByNamePath:      "",
		CachePath:            "",
		LogPath:              "",
		InternalMetadataPath: "",
		TranscodingTempPath:  "",
		HasUpdateAvailable:   false,
		EncoderLocation:      "External",
		SystemArchitecture:   "X64",
	})
}

// handlePublicUsers serves GET /Users/Public.
//
// Returns an empty array, matching the reference server, which listed no
// public users. That sends the client to a username and password form, which
// is the only login Reelix supports. Advertising users here would also
// disclose account names to anyone who can reach the port.
func (a *API) handlePublicUsers(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, emptyList())
}

// handleQuickConnectEnabled serves GET /QuickConnect/Enabled.
//
// Reelix does not implement QuickConnect, so this answers false. The reference
// server answered true and the client went on to call Initiate; advertising a
// flow that then fails in the user's hands is worse than declining it here.
//
// Wholphin's behaviour on false is not covered by the capture.
func (a *API) handleQuickConnectEnabled(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, false)
}

// handleQuickConnectInitiate serves POST /QuickConnect/Initiate.
//
// 401 is what Jellyfin returns when QuickConnect is not active. A client that
// reaches here has ignored the Enabled probe, so the honest answer is the same
// one it would get from a real server with the feature switched off.
func (a *API) handleQuickConnectInitiate(w http.ResponseWriter, r *http.Request) {
	writeStatus(w, http.StatusUnauthorized)
}

// handleAuthenticateByName serves POST /Users/AuthenticateByName.
func (a *API) handleAuthenticateByName(w http.ResponseWriter, r *http.Request) {
	var req authenticateByNameRequest
	if err := decodeJSON(r, &req); err != nil {
		writeStatus(w, http.StatusBadRequest)
		return
	}

	// Pw is current, Password is legacy; some clients still send the latter.
	password := req.Pw
	if password == "" {
		password = req.Password
	}

	clientInfo := ParseAuthorization(r)

	session, user, token, err := a.sessions.Authenticate(r.Context(), req.Username, password,
		service.ClientInfo{
			Client:     clientInfo.Client,
			Device:     clientInfo.Device,
			DeviceID:   clientInfo.DeviceID,
			Version:    clientInfo.Version,
			RemoteAddr: remoteAddr(r),
		})
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) {
			writeStatus(w, http.StatusUnauthorized)
			return
		}
		a.fail(r, "authenticate_by_name", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	settings, err := a.sessions.ServerSettings(r.Context())
	if err != nil {
		a.fail(r, "authenticate_by_name", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	// The user id is safe to log. The token is not, and neither is the
	// authorization header it arrived in.
	logging.FromContext(r.Context()).Info("client authenticated",
		slog.String(logging.KeyOperation, "authenticate_by_name"),
		slog.String(logging.KeyUserID, user.ID.String()),
		slog.String("client", clientInfo.Client),
		slog.String("device", clientInfo.Device))

	a.writeJSON(w, r, http.StatusOK, authenticationResult{
		User:        newUserDTO(user, settings),
		SessionInfo: newSessionInfoDTO(session, user, settings, remoteAddr(r)),
		AccessToken: token,
		ServerID:    settings.ServerID,
	})
}

// handleUsersMe serves GET /Users/Me.
func (a *API) handleUsersMe(w http.ResponseWriter, r *http.Request) {
	settings, err := a.sessions.ServerSettings(r.Context())
	if err != nil {
		a.fail(r, "users_me", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	a.writeJSON(w, r, http.StatusOK, newUserDTO(userFrom(r.Context()), settings))
}

// handleSessionCapabilities serves POST /Sessions/Capabilities.
//
// Wholphin sends these as query parameters with an empty body, and the
// reference server answers 204. This is the bare route; its JSON-body sibling
// is handleSessionCapabilitiesFull.
func (a *API) handleSessionCapabilities(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	caps := domain.Session{
		PlayableMediaTypes:           trimmed(q.Get("playableMediaTypes")),
		SupportedCommands:            trimmed(q.Get("supportedCommands")),
		SupportsMediaControl:         q.Get("supportsMediaControl") == "true",
		SupportsPersistentIdentifier: q.Get("supportsPersistentIdentifier") == "true",
	}

	if err := a.sessions.SetCapabilities(r.Context(), sessionFrom(r.Context()).ID, caps); err != nil {
		a.fail(r, "session_capabilities", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	writeStatus(w, http.StatusNoContent)
}

// handleSessionCapabilitiesFull serves POST /Sessions/Capabilities/Full.
//
// The same information as the bare route, sent as a JSON body instead of
// query parameters. jellyfin-web reports its capabilities only this way — its
// api client posts here directly after authenticating.
//
// IT DOES NOT BLOCK ANYTHING. jellyfin-web neither awaits this call nor
// attaches a rejection handler to it, so a 404 here leaves an unhandled
// promise rejection in the console and nothing else. It is implemented
// because it belongs to the same login exchange as the routes that do block,
// and because it lands on a service method that already exists — not because
// the client is waiting on it.
//
// Decoding is deliberately lenient where the reference is strict. Probing
// showed the reference rejects an unrecognised SupportedCommands value with
// 400; Reelix stores the strings as sent. Reelix does not act on these
// commands in 0.0.1 — it records what the client claims — so validating an
// enum here would reject a client for advertising a capability newer than our
// copy of the list, which is a worse failure than storing a string nobody
// reads.
func (a *API) handleSessionCapabilitiesFull(w http.ResponseWriter, r *http.Request) {
	var body clientCapabilitiesRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// 400 rather than 415: the reference distinguishes a missing
		// Content-Type from an unparseable body, and Reelix has one failure
		// mode here. A client that gets this far has sent something it
		// intended as JSON.
		writeStatus(w, http.StatusBadRequest)
		return
	}

	// Both arrays are normalised to non-nil. An absent JSON field decodes to
	// a nil slice, which reaches Postgres as NULL, and both columns are NOT
	// NULL — so a client omitting either one would get a 500 for a body the
	// reference server answers 204 to. The query-parameter route cannot hit
	// this because trimmed() never returns nil.
	caps := domain.Session{
		PlayableMediaTypes:           nonNil(body.PlayableMediaTypes),
		SupportedCommands:            nonNil(body.SupportedCommands),
		SupportsMediaControl:         body.SupportsMediaControl,
		SupportsPersistentIdentifier: body.SupportsPersistentIdentifier,
	}

	if err := a.sessions.SetCapabilities(r.Context(), sessionFrom(r.Context()).ID, caps); err != nil {
		a.fail(r, "session_capabilities_full", err)
		writeStatus(w, http.StatusInternalServerError)
		return
	}

	writeStatus(w, http.StatusNoContent)
}

// fail logs a handler failure.
//
// The error text stays in the log and never reaches the client: a Jellyfin
// client renders a 500 as an empty screen regardless of the body, and the
// constitution forbids returning internal detail.
func (a *API) fail(r *http.Request, operation string, err error) {
	logging.FromContext(r.Context()).Error("compatibility request failed",
		slog.String(logging.KeyOperation, operation),
		slog.String(logging.KeyError, err.Error()))
}

// newUserDTO translates a native user into the Jellyfin representation.
func newUserDTO(u domain.User, settings domain.ServerSettings) userDTO {
	last := formatTime(u.UpdatedAt)

	return userDTO{
		Name:     u.Username,
		ServerID: settings.ServerID,
		ID:       compatID(u.ID),
		// Reelix requires a password for every account, so these are always
		// true. EasyPassword is a Jellyfin concept Reelix does not have.
		HasPassword:               true,
		HasConfiguredPassword:     true,
		HasConfiguredEasyPassword: false,
		EnableAutoLogin:           false,
		LastLoginDate:             last,
		LastActivityDate:          last,
		Configuration: userConfiguration{
			PlayDefaultAudioTrack:      true,
			SubtitleLanguagePreference: "",
			DisplayMissingEpisodes:     false,
			GroupedFolders:             emptyStrings(),
			SubtitleMode:               "Default",
			DisplayCollectionsView:     false,
			EnableLocalPassword:        false,
			OrderedViews:               emptyStrings(),
			LatestItemsExcludes:        emptyStrings(),
			MyMediaExcludes:            emptyStrings(),
			HidePlayedInLatest:         true,
			RememberAudioSelections:    true,
			RememberSubtitleSelections: true,
			EnableNextEpisodeAutoPlay:  true,
			CastReceiverID:             "F007D354",
		},
		Policy: newUserPolicy(u),
	}
}

// newUserPolicy translates Reelix's single is_admin flag into the closest
// Jellyfin policy.
//
// Reelix has no per-user permission model in 0.0.1. Capabilities a client
// might use to hide UI are granted, because a client that believes it may
// not play shows a broken library rather than a clear error.
func newUserPolicy(u domain.User) userPolicy {
	return userPolicy{
		IsAdministrator: u.IsAdmin,
		// Hidden controls whether the account appears in the public user
		// list. Reelix returns no public users at all, so this is true.
		IsHidden:                         true,
		EnableCollectionManagement:       false,
		EnableSubtitleManagement:         false,
		EnableLyricManagement:            false,
		IsDisabled:                       false,
		BlockedTags:                      emptyStrings(),
		AllowedTags:                      emptyStrings(),
		EnableUserPreferenceAccess:       true,
		AccessSchedules:                  emptyList(),
		BlockUnratedItems:                emptyStrings(),
		EnableRemoteControlOfOtherUsers:  false,
		EnableSharedDeviceControl:        false,
		EnableRemoteAccess:               true,
		EnableLiveTvManagement:           false,
		EnableLiveTvAccess:               false,
		EnableMediaPlayback:              true,
		EnableAudioPlaybackTranscoding:   false,
		EnableVideoPlaybackTranscoding:   false,
		EnablePlaybackRemuxing:           false,
		ForceRemoteSourceTranscoding:     false,
		EnableContentDeletion:            false,
		EnableContentDeletionFromFolders: emptyStrings(),
		EnableContentDownloading:         true,
		EnableSyncTranscoding:            false,
		EnableMediaConversion:            false,
		EnabledDevices:                   emptyStrings(),
		EnableAllDevices:                 true,
		EnabledChannels:                  emptyStrings(),
		EnableAllChannels:                true,
		EnabledFolders:                   emptyStrings(),
		EnableAllFolders:                 true,
		InvalidLoginAttemptCount:         0,
		LoginAttemptsBeforeLockout:       -1,
		MaxActiveSessions:                0,
		EnablePublicSharing:              false,
		BlockedMediaFolders:              emptyStrings(),
		BlockedChannels:                  emptyStrings(),
		RemoteClientBitrateLimit:         0,
		AuthenticationProviderID:         "Reelix.NativeAuthenticationProvider",
		PasswordResetProviderID:          "Reelix.NativePasswordResetProvider",
		SyncPlayAccess:                   "None",
	}
}

// newSessionInfoDTO translates a native session into the Jellyfin
// representation. The DTO is assembled here and never persisted.
func newSessionInfoDTO(s domain.Session, u domain.User, settings domain.ServerSettings, remote string) sessionInfoDTO {
	playable := s.PlayableMediaTypes
	if playable == nil {
		playable = emptyStrings()
	}
	commands := s.SupportedCommands
	if commands == nil {
		commands = emptyStrings()
	}

	return sessionInfoDTO{
		PlayState: playState{
			CanSeek:       false,
			IsPaused:      false,
			IsMuted:       false,
			RepeatMode:    "RepeatNone",
			PlaybackOrder: "Default",
		},
		AdditionalUsers: emptyList(),
		Capabilities: sessionCapabilities{
			PlayableMediaTypes:           playable,
			SupportedCommands:            commands,
			SupportsMediaControl:         s.SupportsMediaControl,
			SupportsPersistentIdentifier: s.SupportsPersistentIdentifier,
		},
		RemoteEndPoint:     remote,
		PlayableMediaTypes: playable,
		ID:                 compatID(s.ID),
		UserID:             compatID(u.ID),
		UserName:           u.Username,
		Client:             s.Client,
		LastActivityDate:   formatTime(s.LastActivityAt),
		// Nothing has been played, so the reference server's DateTime.MinValue
		// is the honest value.
		LastPlaybackCheckIn:      zeroTime,
		DeviceName:               s.DeviceName,
		DeviceID:                 s.DeviceID,
		ApplicationVersion:       s.ClientVersion,
		IsActive:                 true,
		SupportsMediaControl:     s.SupportsMediaControl,
		SupportsRemoteControl:    false,
		NowPlayingQueue:          emptyList(),
		NowPlayingQueueFullItems: emptyList(),
		HasCustomDeviceName:      false,
		ServerID:                 settings.ServerID,
		SupportedCommands:        commands,
	}
}

// maxRequestBody bounds an unauthenticated request body.
//
// /Users/AuthenticateByName is reachable without credentials, so without a
// limit it is an invitation to stream gigabytes into the process.
const maxRequestBody = 64 << 10

// decodeJSON reads a JSON body.
//
// Unknown fields are tolerated here, unlike the native API: clients send
// fields from newer Jellyfin versions than Reelix implements, and rejecting
// those would break compatibility for no benefit.
func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(dst)
}
