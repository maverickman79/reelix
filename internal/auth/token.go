package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// TokenPrefix marks a string as a Reelix API token.
//
// It makes a leaked token recognisable in a log or a paste, and gives secret
// scanners something to match on.
const TokenPrefix = "rlx_"

// tokenBytes is the entropy behind each token. 256 bits is far beyond
// brute-force, which is what justifies storing only a fast hash of it.
const tokenBytes = 32

// TokenLifetime is how long an issued token stays valid.
//
// Nothing sweeps expired rows; every lookup filters on expires_at instead, so
// an expired token is rejected while its row is still present.
const TokenLifetime = 30 * 24 * time.Hour

// Token is a freshly minted credential.
//
// Plaintext is returned to the caller exactly once, at login. Only Hash is
// persisted, so a database disclosure yields no usable credentials.
type Token struct {
	Plaintext string
	Hash      string
	ExpiresAt time.Time
}

// NewToken mints a token valid for TokenLifetime from now.
func NewToken(now time.Time) (Token, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, fmt.Errorf("generating token: %w", err)
	}

	plaintext := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)

	return Token{
		Plaintext: plaintext,
		Hash:      HashToken(plaintext),
		ExpiresAt: now.Add(TokenLifetime),
	}, nil
}

// HashToken returns the hex-encoded SHA-256 of a token.
//
// SHA-256 rather than argon2 is deliberate and is not an inconsistency with
// password hashing: a token is 256 bits of CSPRNG output with no guessable
// structure, so a slow hash protects nothing, while paying argon2's 64MiB on
// every authenticated request would be a self-inflicted denial of service.
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// ParseBearer extracts a token from an Authorization header value.
//
// It accepts "Bearer <token>" with a case-insensitive scheme, as RFC 7235
// requires. It returns "" for anything else, including a bare token with no
// scheme — accepting that would be a second, undocumented way in.
func ParseBearer(header string) string {
	scheme, rest, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}

	token := strings.TrimSpace(rest)
	if !strings.HasPrefix(token, TokenPrefix) {
		return ""
	}
	return token
}
