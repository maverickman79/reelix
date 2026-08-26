package v1_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maverickman79/reelix/internal/auth"
)

func TestSetupCreatesFirstAdmin(t *testing.T) {
	h := newHarness(t)

	resp := h.do(http.MethodPost, "/setup", "", map[string]string{
		"username": testUser,
		"password": testPassword,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup returned %d: %s", resp.StatusCode, h.body(resp))
	}

	var user struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		IsAdmin  bool   `json:"isAdmin"`
	}
	h.decode(resp, &user)

	if user.Username != testUser {
		t.Errorf("username is %q, want %q", user.Username, testUser)
	}
	if !user.IsAdmin {
		t.Error("the first user was not created as an administrator")
	}
	// Dashed UUID: the 32-character dashless form is a Jellyfin convention and
	// must not leak into the native API.
	if len(user.ID) != 36 || strings.Count(user.ID, "-") != 4 {
		t.Errorf("id %q is not a dashed UUID", user.ID)
	}
}

// TestSetupIsRefusedTwice is the property that makes an unauthenticated setup
// endpoint safe to expose.
func TestSetupIsRefusedTwice(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodPost, "/setup", "", map[string]string{
		"username": "intruder",
		"password": "another sufficiently long password",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second setup returned %d, want 409", resp.StatusCode)
	}
	if code := h.errorCode(resp); code != "already_set_up" {
		t.Errorf("error code is %q, want already_set_up", code)
	}

	// And the second account must not exist.
	var n int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("counting users: %v", err)
	}
	if n != 1 {
		t.Errorf("database holds %d users after a refused setup, want 1", n)
	}
}

// TestSetupIsRefusedConcurrently covers the race an attacker would try on a
// freshly started server: several simultaneous claims on the admin account.
func TestSetupIsRefusedConcurrently(t *testing.T) {
	h := newHarness(t)

	const attempts = 8
	codes := make(chan int, attempts)

	for range attempts {
		go func() {
			resp := h.do(http.MethodPost, "/setup", "", map[string]string{
				"username": "claimant",
				"password": "a sufficiently long password",
			})
			resp.Body.Close()
			codes <- resp.StatusCode
		}()
	}

	created := 0
	for range attempts {
		if <-codes == http.StatusCreated {
			created++
		}
	}

	if created != 1 {
		t.Errorf("%d concurrent setup requests created %d administrators, want exactly 1",
			attempts, created)
	}

	var n int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("counting users: %v", err)
	}
	if n != 1 {
		t.Errorf("database holds %d users, want 1", n)
	}
}

func TestSetupRejectsShortPassword(t *testing.T) {
	h := newHarness(t)

	resp := h.do(http.MethodPost, "/setup", "", map[string]string{
		"username": testUser,
		"password": "short",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("setup with a short password returned %d, want 400", resp.StatusCode)
	}
	if code := h.errorCode(resp); code != "invalid_request" {
		t.Errorf("error code is %q, want invalid_request", code)
	}
}

func TestLogin(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodPost, "/auth/login", "", map[string]string{
		"username": testUser,
		"password": testPassword,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login returned %d: %s", resp.StatusCode, h.body(resp))
	}

	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expiresAt"`
		User      struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	h.decode(resp, &out)

	if !strings.HasPrefix(out.Token, auth.TokenPrefix) {
		t.Errorf("token %q lacks the %q prefix", out.Token, auth.TokenPrefix)
	}
	if out.User.Username != testUser {
		t.Errorf("login returned user %q", out.User.Username)
	}
	if time.Until(out.ExpiresAt) < 24*time.Hour {
		t.Errorf("token expires at %s, sooner than expected", out.ExpiresAt)
	}
}

// TestLoginStoresOnlyTheHash checks the database never holds a usable
// credential.
func TestLoginStoresOnlyTheHash(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.login()

	ctx := context.Background()

	var stored string
	if err := h.pool.QueryRow(ctx, `SELECT token_hash FROM api_tokens`).Scan(&stored); err != nil {
		t.Fatalf("reading the stored token: %v", err)
	}

	if stored == token {
		t.Fatal("the plaintext token was stored in the database")
	}
	if strings.Contains(stored, token) {
		t.Fatal("the stored value contains the plaintext token")
	}
	if stored != auth.HashToken(token) {
		t.Error("the stored value is not the SHA-256 of the token")
	}

	// The password must not be recoverable either.
	var hash string
	if err := h.pool.QueryRow(ctx, `SELECT password_hash FROM users`).Scan(&hash); err != nil {
		t.Fatalf("reading the stored password hash: %v", err)
	}
	if strings.Contains(hash, testPassword) {
		t.Fatal("the stored password hash contains the plaintext password")
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("password hash is not argon2id: %q", hash)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	h := newHarness(t)
	h.setup()

	tests := []struct {
		name     string
		username string
		password string
	}{
		{"wrong password", testUser, "a completely different password"},
		{"unknown user", "nobody", testPassword},
		{"empty password", testUser, ""},
		{"empty username", "", testPassword},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.do(http.MethodPost, "/auth/login", "", map[string]string{
				"username": tt.username,
				"password": tt.password,
			})
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("login returned %d, want 401", resp.StatusCode)
			}
			// The same code and message for an unknown user as for a wrong
			// password: the endpoint must not be usable to enumerate accounts.
			if code := h.errorCode(resp); code != "invalid_credentials" {
				t.Errorf("error code is %q, want invalid_credentials", code)
			}
		})
	}
}

// TestAuthenticationRejectsBadTokens covers every shape of missing or
// malformed credential.
func TestAuthenticationRejectsBadTokens(t *testing.T) {
	h := newHarness(t)
	h.setup()
	valid := h.login()

	tests := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"empty bearer", "Bearer "},
		{"unknown token", "Bearer " + auth.TokenPrefix + "notarealtoken"},
		{"no scheme", valid},
		{"wrong scheme", "Basic " + valid},
		{"jellyfin scheme", `MediaBrowser Token="` + valid + `"`},
		{"token without prefix", "Bearer " + strings.TrimPrefix(valid, auth.TokenPrefix)},
		{"truncated token", "Bearer " + valid[:len(valid)-4]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, h.srv.URL+"/api/v1/me", nil)
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}

			resp, err := h.srv.Client().Do(req)
			if err != nil {
				t.Fatalf("GET /me: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("GET /me returned %d, want 401", resp.StatusCode)
			}
		})
	}
}

// TestExpiredTokenIsRejectedWhileItsRowExists is the case that motivated
// filtering on expires_at inside the lookup query.
//
// Nothing deletes expired tokens — there is no sweeper, and DeleteExpired is
// not scheduled — so an expired token's row stays in the table indefinitely. A
// lookup that matched on token_hash alone would authenticate it happily. This
// test leaves the row in place and only moves its expiry into the past.
func TestExpiredTokenIsRejectedWhileItsRowExists(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.login()

	ctx := context.Background()

	// It works before expiry.
	resp := h.do(http.MethodGet, "/me", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /me with a fresh token returned %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Expire it in place, without deleting anything.
	tag, err := h.pool.Exec(ctx,
		`UPDATE api_tokens SET expires_at = now() - interval '1 second'`)
	if err != nil {
		t.Fatalf("expiring the token: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("expired %d rows, want 1", tag.RowsAffected())
	}

	// The row must still be present — otherwise this test would pass for the
	// wrong reason.
	var n int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM api_tokens`).Scan(&n); err != nil {
		t.Fatalf("counting tokens: %v", err)
	}
	if n != 1 {
		t.Fatalf("api_tokens holds %d rows, want the expired row to still be there", n)
	}

	// And it must now be rejected.
	resp = h.do(http.MethodGet, "/me", token, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an expired token returned %d, want 401", resp.StatusCode)
	}
	if code := h.errorCode(resp); code != "unauthenticated" {
		t.Errorf("error code is %q, want unauthenticated", code)
	}
}

// TestTokenExpiringLaterStillWorks guards against the expiry comparison being
// written the wrong way round, which the test above alone would not catch.
func TestTokenExpiringLaterStillWorks(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.login()

	if _, err := h.pool.Exec(context.Background(),
		`UPDATE api_tokens SET expires_at = now() + interval '1 second'`); err != nil {
		t.Fatalf("adjusting expiry: %v", err)
	}

	resp := h.do(http.MethodGet, "/me", token, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("a token expiring in one second returned %d, want 200", resp.StatusCode)
	}
}

func TestMe(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.login()

	resp := h.do(http.MethodGet, "/me", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /me returned %d", resp.StatusCode)
	}

	var user struct {
		Username string `json:"username"`
		IsAdmin  bool   `json:"isAdmin"`
	}
	h.decode(resp, &user)

	if user.Username != testUser || !user.IsAdmin {
		t.Errorf("GET /me returned %+v", user)
	}
}

// TestNoResponseLeaksSecrets sweeps every endpoint for the password and the
// password hash.
func TestNoResponseLeaksSecrets(t *testing.T) {
	h := newHarness(t)

	bodies := []string{}

	resp := h.do(http.MethodPost, "/setup", "", map[string]string{
		"username": testUser,
		"password": testPassword,
	})
	bodies = append(bodies, h.body(resp))

	resp = h.do(http.MethodPost, "/auth/login", "", map[string]string{
		"username": testUser,
		"password": testPassword,
	})
	loginBody := h.body(resp)
	bodies = append(bodies, loginBody)

	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(loginBody), &login); err != nil {
		t.Fatalf("decoding login response: %v", err)
	}

	resp = h.do(http.MethodGet, "/me", login.Token, nil)
	bodies = append(bodies, h.body(resp))

	resp = h.do(http.MethodPost, "/libraries", login.Token, map[string]any{
		"name": "Movies", "kind": "movie", "paths": []string{"/media/movies"},
	})
	bodies = append(bodies, h.body(resp))

	resp = h.do(http.MethodGet, "/libraries", login.Token, nil)
	bodies = append(bodies, h.body(resp))

	var passwordHash string
	if err := h.pool.QueryRow(context.Background(),
		`SELECT password_hash FROM users`).Scan(&passwordHash); err != nil {
		t.Fatalf("reading the password hash: %v", err)
	}

	for i, body := range bodies {
		if strings.Contains(body, testPassword) {
			t.Errorf("response %d contains the plaintext password: %s", i, body)
		}
		if strings.Contains(body, passwordHash) {
			t.Errorf("response %d contains the password hash: %s", i, body)
		}
		if strings.Contains(strings.ToLower(body), "password") {
			t.Errorf("response %d mentions a password field: %s", i, body)
		}
	}
}
