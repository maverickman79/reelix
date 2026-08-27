package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maverickman79/reelix/internal/auth"
	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/repository"
	"github.com/maverickman79/reelix/internal/service"
)

const dsnEnv = "REELIX_TEST_DB_DSN"

const (
	testUser     = "test"
	testPassword = "a sufficiently long password"
	testDeviceID = "device-001"
	testDevice   = "SK1"
	testClient   = "Wholphin"
	testVersion  = "1.0.7-0-g2b9af1e8"
)

// harness is a running compatibility API over a scratch database.
type harness struct {
	t    *testing.T
	pool *pgxpool.Pool
	srv  *httptest.Server
	logs *bytes.Buffer
	user domain.User
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	adminDSN := os.Getenv(dsnEnv)
	if adminDSN == "" {
		t.Skipf("%s not set; skipping database integration test", dsnEnv)
	}

	ctx := context.Background()

	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connecting to %s: %v", dsnEnv, err)
	}
	defer admin.Close()

	name := "reelix_test_" + strings.ReplaceAll(uuid.NewV7().String(), "-", "_")
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		t.Fatalf("creating scratch database: %v", err)
	}

	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parsing %s: %v", dsnEnv, err)
	}
	u.Path = "/" + name

	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatalf("connecting to scratch database: %v", err)
	}

	// Logs are captured, not discarded: one of the tests below asserts that
	// neither the token nor the raw authorization header ever reaches them.
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))

	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrating scratch database: %v", err)
	}

	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("hashing password: %v", err)
	}
	user := domain.User{Username: testUser, PasswordHash: hash, IsAdmin: true}
	if err := repository.NewUserRepository(pool).Create(ctx, &user); err != nil {
		t.Fatalf("creating user: %v", err)
	}

	api := New(service.NewSessionService(pool), service.NewMediaService(pool), log)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.Routes().ServeHTTP(w, r.WithContext(logging.WithLogger(r.Context(), log)))
	}))

	t.Cleanup(func() {
		srv.Close()
		pool.Close()

		cleanup, err := pgxpool.New(context.Background(), adminDSN)
		if err != nil {
			t.Errorf("reconnecting to drop scratch database: %v", err)
			return
		}
		defer cleanup.Close()

		if _, err := cleanup.Exec(context.Background(),
			fmt.Sprintf("DROP DATABASE IF EXISTS %q WITH (FORCE)", name)); err != nil {
			t.Errorf("dropping scratch database %s: %v", name, err)
		}
	})

	return &harness{t: t, pool: pool, srv: srv, logs: logs, user: user}
}

// authHeader builds the header Wholphin sends, optionally carrying a token.
func authHeader(token string) string {
	h := fmt.Sprintf(`MediaBrowser Client=%q, Device=%q, DeviceId=%q, Version=%q`,
		testClient, testDevice, testDeviceID, testVersion)
	if token != "" {
		h += fmt.Sprintf(`, Token=%q`, token)
	}
	return h
}

// do issues a request carrying a Jellyfin authorization header.
func (h *harness) do(method, path, token string, body any) *http.Response {
	h.t.Helper()

	var r io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("encoding body: %v", err)
		}
		r = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, h.srv.URL+path, r)
	if err != nil {
		h.t.Fatalf("building request: %v", err)
	}
	req.Header.Set(headerAuthorization, authHeader(token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// bodyOf reads and closes a response body.
func (h *harness) bodyOf(resp *http.Response) []byte {
	h.t.Helper()
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("reading body: %v", err)
	}
	return b
}

// login authenticates and returns the access token.
func (h *harness) login() string {
	h.t.Helper()

	resp := h.do(http.MethodPost, "/Users/AuthenticateByName", "", map[string]string{
		"Username": testUser,
		"Pw":       testPassword,
	})
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("AuthenticateByName returned %d: %s", resp.StatusCode, h.bodyOf(resp))
	}

	var out struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(h.bodyOf(resp), &out); err != nil {
		h.t.Fatalf("decoding auth response: %v", err)
	}
	return out.AccessToken
}

// TestPublicSystemInfoMatchesFixture validates the first request any client
// makes — the one "add server by address" succeeds or fails on.
func TestPublicSystemInfoMatchesFixture(t *testing.T) {
	h := newHarness(t)

	for _, name := range fixtureNames(t, "GET_System_Info_Public") {
		t.Run(name, func(t *testing.T) {
			f := loadFixture(t, "GET_System_Info_Public", name)

			resp := h.do(http.MethodGet, f.Request.Path, "", nil)
			if resp.StatusCode != f.Response.Status {
				t.Fatalf("status %d, recorded %d", resp.StatusCode, f.Response.Status)
			}

			assertSuperset(t, f.recordedJSON(t), decodeBody(t, h.bodyOf(resp)))
		})
	}
}

func TestPublicUsersMatchesFixture(t *testing.T) {
	h := newHarness(t)
	f := loadFixture(t, "GET_Users_Public", "00")

	resp := h.do(http.MethodGet, f.Request.Path, "", nil)
	if resp.StatusCode != f.Response.Status {
		t.Fatalf("status %d, recorded %d", resp.StatusCode, f.Response.Status)
	}

	assertSuperset(t, f.recordedJSON(t), decodeBody(t, h.bodyOf(resp)))
}

// TestQuickConnectDeclines checks Reelix declines cleanly rather than
// advertising a flow it does not implement.
//
// The reference server had QuickConnect enabled and returned true, so the
// recorded response is deliberately NOT matched here: the fixture records a
// server with the feature on. What is asserted is the shape — a JSON boolean,
// which is what the client's generated type expects — and the value false.
func TestQuickConnectDeclines(t *testing.T) {
	h := newHarness(t)

	f := loadFixture(t, "GET_QuickConnect_Enabled", "00")

	resp := h.do(http.MethodGet, "/QuickConnect/Enabled", "", nil)
	if resp.StatusCode != f.Response.Status {
		t.Fatalf("status %d, recorded %d", resp.StatusCode, f.Response.Status)
	}

	// Same JSON type as the recording, opposite value.
	got := decodeBody(t, h.bodyOf(resp))
	if jsonType(got) != jsonType(f.recordedJSON(t)) {
		t.Errorf("returned %s, recorded %s", jsonType(got), jsonType(f.recordedJSON(t)))
	}
	if got != false {
		t.Errorf("QuickConnect/Enabled returned %v, want false", got)
	}

	// And Initiate declines, as Jellyfin does when the feature is off.
	initiate := h.do(http.MethodPost, "/QuickConnect/Initiate", "", nil)
	defer initiate.Body.Close()
	if initiate.StatusCode != http.StatusUnauthorized {
		t.Errorf("QuickConnect/Initiate returned %d, want 401", initiate.StatusCode)
	}
}

// TestAuthenticateByNameMatchesFixture is the core of this step: the response
// carries a 42-field Policy and a 15-field Configuration, and the SDK will
// throw on any one of them being absent.
func TestAuthenticateByNameMatchesFixture(t *testing.T) {
	h := newHarness(t)
	f := loadFixture(t, "POST_Users_AuthenticateByName", "00")

	resp := h.do(http.MethodPost, f.Request.Path, "", map[string]string{
		"Username": testUser,
		"Pw":       testPassword,
	})
	if resp.StatusCode != f.Response.Status {
		t.Fatalf("status %d, recorded %d", resp.StatusCode, f.Response.Status)
	}

	body := h.bodyOf(resp)
	assertSuperset(t, f.recordedJSON(t), decodeBody(t, body))

	var out struct {
		AccessToken string `json:"AccessToken"`
		ServerID    string `json:"ServerId"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
		SessionInfo struct {
			ID       string `json:"Id"`
			UserID   string `json:"UserId"`
			DeviceID string `json:"DeviceId"`
			Client   string `json:"Client"`
		} `json:"SessionInfo"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	// Ids must be 32-char dashless hex at this boundary. A dashed id produces
	// failures that look like missing content rather than malformed input.
	for label, id := range map[string]string{
		"ServerId":           out.ServerID,
		"User.Id":            out.User.ID,
		"SessionInfo.Id":     out.SessionInfo.ID,
		"SessionInfo.UserId": out.SessionInfo.UserID,
	} {
		if len(id) != 32 || strings.Contains(id, "-") {
			t.Errorf("%s = %q, want 32 dashless hex characters", label, id)
		}
		if strings.ToLower(id) != id {
			t.Errorf("%s = %q, want lowercase", label, id)
		}
	}

	// The user id must be this user's, in dashless form.
	if out.User.ID != compatID(h.user.ID) {
		t.Errorf("User.Id = %s, want %s", out.User.ID, compatID(h.user.ID))
	}

	// The device identity from the authorization header must reach the session.
	if out.SessionInfo.DeviceID != testDeviceID || out.SessionInfo.Client != testClient {
		t.Errorf("session did not carry the client's identity: %+v", out.SessionInfo)
	}

	if out.AccessToken == "" {
		t.Error("no access token issued")
	}
}

// TestUsersMeMatchesFixture validates the authenticated user shape.
func TestUsersMeMatchesFixture(t *testing.T) {
	h := newHarness(t)
	token := h.login()

	for _, name := range fixtureNames(t, "GET_Users_Me") {
		t.Run(name, func(t *testing.T) {
			f := loadFixture(t, "GET_Users_Me", name)

			resp := h.do(http.MethodGet, f.Request.Path, token, nil)
			if resp.StatusCode != f.Response.Status {
				t.Fatalf("status %d, recorded %d", resp.StatusCode, f.Response.Status)
			}

			assertSuperset(t, f.recordedJSON(t), decodeBody(t, h.bodyOf(resp)))
		})
	}
}

// TestSessionCapabilitiesMatchesFixture replays the recorded query string.
func TestSessionCapabilitiesMatchesFixture(t *testing.T) {
	h := newHarness(t)
	token := h.login()

	f := loadFixture(t, "POST_Sessions_Capabilities", "00")

	q := url.Values{}
	for k, v := range f.Request.Query {
		q.Set(k, v)
	}

	resp := h.do(http.MethodPost, f.Request.Path+"?"+q.Encode(), token, nil)
	defer resp.Body.Close()

	if resp.StatusCode != f.Response.Status {
		t.Fatalf("status %d, recorded %d", resp.StatusCode, f.Response.Status)
	}

	// The reported capabilities must actually be recorded, not merely accepted.
	var playable []string
	var supportsPersistent bool
	if err := h.pool.QueryRow(context.Background(),
		`SELECT playable_media_types, supports_persistent_identifier FROM sessions`).
		Scan(&playable, &supportsPersistent); err != nil {
		t.Fatalf("reading session: %v", err)
	}

	if len(playable) != 1 || playable[0] != "Video" {
		t.Errorf("playable_media_types = %v, want [Video]", playable)
	}
	if !supportsPersistent {
		t.Error("supports_persistent_identifier was not recorded")
	}
}

// TestCapturedFlowInOrder replays the recorded login sequence end to end.
//
// Each request is issued in the order Wholphin issued it, because the flow is
// stateful: /Users/Me and /Sessions/Capabilities only work with the token that
// /Users/AuthenticateByName returned.
func TestCapturedFlowInOrder(t *testing.T) {
	h := newHarness(t)

	var token string

	steps := []struct {
		order  int
		method string
		path   string
		route  string
		status int
	}{
		{1, http.MethodGet, "/System/Info/Public", "GET_System_Info_Public", 200},
		{2, http.MethodGet, "/QuickConnect/Enabled", "GET_QuickConnect_Enabled", 200},
		{3, http.MethodGet, "/Users/Public", "GET_Users_Public", 200},
		{4, http.MethodGet, "/System/Info/Public", "GET_System_Info_Public", 200},
		// Step 5 in the capture is QuickConnect/Initiate, which Reelix
		// declines; Wholphin should not reach it, having been told the
		// feature is off.
		{6, http.MethodPost, "/Users/AuthenticateByName", "POST_Users_AuthenticateByName", 200},
		{7, http.MethodGet, "/Users/Me", "GET_Users_Me", 200},
		{8, http.MethodGet, "/System/Info/Public", "GET_System_Info_Public", 200},
		{9, http.MethodPost, "/Sessions/Capabilities", "POST_Sessions_Capabilities", 204},
		// The home screen. /UserImage answers 404 by design; see its handler.
		{10, http.MethodGet, "/UserImage", "GET_UserImage", 404},
		{11, http.MethodGet, "/DisplayPreferences/default", "GET_DisplayPreferences_default", 200},
		{16, http.MethodGet, "/LiveTv/Recordings/Folders", "GET_LiveTv_Recordings_Folders", 200},
		{19, http.MethodGet, "/UserItems/Resume", "GET_UserItems_Resume", 200},
		{21, http.MethodGet, "/Items/Latest", "GET_Items_Latest", 200},
		{13, http.MethodGet, "/UserViews", "GET_UserViews", 200},
		{22, http.MethodGet, "/Shows/NextUp", "GET_Shows_NextUp", 200},
		// Call 33 browses the library. This harness seeds no media, so the
		// answer is an empty page; the populated comparison against every
		// recorded /Items call lives in items_test.go.
		{33, http.MethodGet, "/Items", "GET_Items", 200},
		// Call 123 in the capture is the WebSocket upgrade, which cannot be
		// replayed through this HTTP client. See socket_test.go.
	}

	for _, s := range steps {
		t.Run(fmt.Sprintf("%02d_%s", s.order, s.route), func(t *testing.T) {
			path := s.path
			var body any

			switch s.path {
			case "/Users/AuthenticateByName":
				body = map[string]string{"Username": testUser, "Pw": testPassword}
			case "/Sessions/Capabilities":
				path += "?playableMediaTypes=Video&supportedCommands=SendString" +
					"&supportsMediaControl=true&supportsPersistentIdentifier=true"
			}

			resp := h.do(s.method, path, token, body)
			raw := h.bodyOf(resp)

			if resp.StatusCode != s.status {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, s.status, raw)
			}

			if s.path == "/Users/AuthenticateByName" {
				var out struct {
					AccessToken string `json:"AccessToken"`
				}
				if err := json.Unmarshal(raw, &out); err != nil {
					t.Fatalf("decoding auth response: %v", err)
				}
				token = out.AccessToken
			}
		})
	}

	if token == "" {
		t.Fatal("the flow completed without issuing a token")
	}
}

// TestAuthenticationRejectsBadCredentials checks a wrong password is a 401.
func TestAuthenticationRejectsBadCredentials(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct {
		name     string
		username string
		password string
	}{
		{"wrong password", testUser, "not the password at all"},
		{"unknown user", "nobody", testPassword},
		{"empty password", testUser, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.do(http.MethodPost, "/Users/AuthenticateByName", "", map[string]string{
				"Username": tc.username,
				"Pw":       tc.password,
			})
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("returned %d, want 401", resp.StatusCode)
			}
		})
	}
}

// TestAuthenticatedRoutesRequireAToken checks the protected routes are shut.
func TestAuthenticatedRoutesRequireAToken(t *testing.T) {
	h := newHarness(t)
	valid := h.login()

	for _, path := range []string{"/Users/Me", "/System/Info"} {
		t.Run(path, func(t *testing.T) {
			resp := h.do(http.MethodGet, path, "", nil)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("without a token: %d, want 401", resp.StatusCode)
			}

			bad := h.do(http.MethodGet, path, "not-a-real-token", nil)
			defer bad.Body.Close()
			if bad.StatusCode != http.StatusUnauthorized {
				t.Errorf("with a bogus token: %d, want 401", bad.StatusCode)
			}

			ok := h.do(http.MethodGet, path, valid, nil)
			defer ok.Body.Close()
			if ok.StatusCode != http.StatusOK {
				t.Errorf("with a valid token: %d, want 200", ok.StatusCode)
			}
		})
	}
}

// TestNativeTokenIsRejectedByCompat checks the two authentication schemes are
// genuinely separate.
//
// A native /api/v1 bearer token must not open the compatibility surface, and
// vice versa. The constitution requires the schemes to be independent; sharing
// a credential would make that independence nominal.
func TestNativeTokenIsRejectedByCompat(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	native, err := auth.NewToken(h.user.CreatedAt)
	if err != nil {
		t.Fatalf("minting a native token: %v", err)
	}
	record := domain.APIToken{
		UserID:    h.user.ID,
		TokenHash: native.Hash,
		ExpiresAt: native.ExpiresAt,
	}
	if err := repository.NewTokenRepository(h.pool).Create(ctx, &record); err != nil {
		t.Fatalf("storing the native token: %v", err)
	}

	resp := h.do(http.MethodGet, "/Users/Me", native.Plaintext, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a native API token opened the compatibility surface: %d", resp.StatusCode)
	}
}

// TestSessionIsReusedPerDevice checks re-authentication from one device
// replaces its session rather than accumulating rows.
func TestSessionIsReusedPerDevice(t *testing.T) {
	h := newHarness(t)

	first := h.login()
	second := h.login()

	if first == second {
		t.Error("re-authentication returned the same token; it should be reissued")
	}

	var n int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("counting sessions: %v", err)
	}
	if n != 1 {
		t.Errorf("two logins from one device produced %d sessions, want 1", n)
	}

	// The old token must stop working — that is what "log in again" means.
	old := h.do(http.MethodGet, "/Users/Me", first, nil)
	defer old.Body.Close()
	if old.StatusCode != http.StatusUnauthorized {
		t.Errorf("the superseded token still works: %d", old.StatusCode)
	}
}

// TestNoSecretsInLogs is the counterpart to the password check in Step 3.
//
// The authorization header and the access token are live credentials, and the
// constitution forbids logging either. This matters more here than anywhere
// else: the header parser is the one piece of the compatibility layer with no
// recorded reference, so it is the most likely to be handled carelessly.
func TestNoSecretsInLogs(t *testing.T) {
	h := newHarness(t)

	token := h.login()

	// Exercise every route, including the failure paths.
	h.do(http.MethodGet, "/System/Info/Public", "", nil).Body.Close()
	h.do(http.MethodGet, "/Users/Public", "", nil).Body.Close()
	h.do(http.MethodGet, "/QuickConnect/Enabled", "", nil).Body.Close()
	h.do(http.MethodPost, "/QuickConnect/Initiate", "", nil).Body.Close()
	h.do(http.MethodGet, "/Users/Me", token, nil).Body.Close()
	h.do(http.MethodGet, "/System/Info", token, nil).Body.Close()
	h.do(http.MethodPost, "/Sessions/Capabilities?playableMediaTypes=Video", token, nil).Body.Close()
	h.do(http.MethodGet, "/Users/Me", "a-bogus-token-value", nil).Body.Close()
	h.do(http.MethodPost, "/Users/AuthenticateByName", "", map[string]string{
		"Username": testUser, "Pw": testPassword,
	}).Body.Close()

	logs := h.logs.String()

	for _, secret := range []struct {
		name  string
		value string
	}{
		{"the access token", token},
		{"the raw authorization header", authHeader(token)},
		{"the password", testPassword},
		{"the stored password hash", h.user.PasswordHash},
		{"the token's stored hash", auth.HashToken(token)},
	} {
		if secret.value == "" {
			continue
		}
		if strings.Contains(logs, secret.value) {
			t.Errorf("%s appears in the logs", secret.name)
		}
	}

	// The scheme name alone would indicate a header being logged wholesale.
	if strings.Contains(logs, authScheme+" Client=") {
		t.Error("an authorization header was logged")
	}
}

// TestCompatIDRoundTrip pins the boundary conversion.
func TestCompatIDRoundTrip(t *testing.T) {
	id := uuid.NewV7()

	encoded := compatID(id)
	if len(encoded) != 32 {
		t.Fatalf("compatID produced %d characters, want 32", len(encoded))
	}
	if strings.Contains(encoded, "-") {
		t.Errorf("compatID produced dashes: %s", encoded)
	}
	if strings.ToLower(encoded) != encoded {
		t.Errorf("compatID produced uppercase: %s", encoded)
	}

	back, err := parseCompatID(encoded)
	if err != nil {
		t.Fatalf("parseCompatID: %v", err)
	}
	if back != id {
		t.Errorf("round trip changed the id: %s became %s", id, back)
	}

	// The dashed form a client might echo back is also accepted.
	dashed, err := parseCompatID(id.String())
	if err != nil {
		t.Fatalf("parseCompatID on the dashed form: %v", err)
	}
	if dashed != id {
		t.Errorf("dashed round trip changed the id")
	}

	for _, bad := range []string{"", "xyz", strings.Repeat("z", 32), id.String() + "extra"} {
		if _, err := parseCompatID(bad); err == nil {
			t.Errorf("parseCompatID(%q) returned no error", bad)
		}
	}
}
