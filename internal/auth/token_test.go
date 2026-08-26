package auth

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestNewToken(t *testing.T) {
	now := time.Now().UTC()

	tok, err := NewToken(now)
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}

	if !strings.HasPrefix(tok.Plaintext, TokenPrefix) {
		t.Errorf("token %q lacks the %q prefix", tok.Plaintext, TokenPrefix)
	}
	if tok.Hash != HashToken(tok.Plaintext) {
		t.Error("Hash does not match the plaintext it was minted with")
	}
	if !tok.ExpiresAt.Equal(now.Add(TokenLifetime)) {
		t.Errorf("ExpiresAt is %s, want %s", tok.ExpiresAt, now.Add(TokenLifetime))
	}

	// The stored hash must not contain the credential it protects.
	if strings.Contains(tok.Hash, tok.Plaintext) {
		t.Error("the token hash contains the plaintext token")
	}

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(tok.Plaintext, TokenPrefix))
	if err != nil {
		t.Fatalf("token body is not raw base64url: %v", err)
	}
	if len(raw) != tokenBytes {
		t.Errorf("token carries %d bytes of entropy, want %d", len(raw), tokenBytes)
	}
}

// TestTokensAreUnique would catch a broken or unseeded random source.
func TestTokensAreUnique(t *testing.T) {
	now := time.Now().UTC()
	seen := make(map[string]struct{}, 128)

	for range 128 {
		tok, err := NewToken(now)
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if _, dup := seen[tok.Plaintext]; dup {
			t.Fatal("NewToken produced a duplicate token")
		}
		seen[tok.Plaintext] = struct{}{}
	}
}

func TestHashTokenIsStable(t *testing.T) {
	const token = TokenPrefix + "abcdefghijklmnopqrstuvwxyz"

	if HashToken(token) != HashToken(token) {
		t.Fatal("HashToken is not deterministic")
	}
	if HashToken(token) == HashToken(token+"x") {
		t.Fatal("HashToken collides on different inputs")
	}
	if len(HashToken(token)) != 64 {
		t.Errorf("HashToken returned %d characters, want 64 hex characters", len(HashToken(token)))
	}
}

func TestParseBearer(t *testing.T) {
	const token = TokenPrefix + "abc123"

	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"standard", "Bearer " + token, token},
		{"lowercase scheme", "bearer " + token, token},
		{"mixed case scheme", "BeArEr " + token, token},
		{"trailing whitespace", "Bearer " + token + "  ", token},

		{"empty", "", ""},
		{"scheme only", "Bearer", ""},
		{"no scheme", token, ""},
		{"wrong scheme", "Basic " + token, ""},
		// The Jellyfin scheme must not be accepted here: the constitution
		// requires the two authentication systems to stay separate.
		{"jellyfin scheme", `MediaBrowser Token="` + token + `"`, ""},
		{"missing prefix", "Bearer abc123", ""},
		{"prefix only in the middle", "Bearer abc" + TokenPrefix + "123", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseBearer(tt.header); got != tt.want {
				t.Errorf("ParseBearer(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}
