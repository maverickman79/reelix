// Package auth implements Reelix's password hashing and native API tokens.
//
// Nothing here touches HTTP or the database. It is pure enough to test without
// either, which matters: this is the code where a subtle mistake is a security
// bug rather than a visible failure.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters.
//
// m=64MiB, t=3, p=4 follows OWASP's guidance for argon2id. The cost is
// deliberate: it is what makes an offline attack on a stolen hash expensive.
//
// Note the operational consequence — each concurrent login holds 64MiB for the
// duration of the hash, so login concurrency is bounded by the container's
// memory limit rather than by CPU.
//
// These may be raised later. Existing hashes keep verifying because every hash
// records the parameters it was produced with; see the PHC string below.
const (
	argonMemory  uint32 = 64 * 1024 // KiB
	argonTime    uint32 = 3
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	saltLen             = 16
)

// MinPasswordLength is the shortest password accepted.
//
// Length is the only rule. Composition requirements (a digit, a symbol) push
// people toward predictable substitutions without adding real entropy.
const MinPasswordLength = 12

var (
	// ErrPasswordTooShort is returned by HashPassword.
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)

	// ErrMismatch means the password does not match the hash. It is
	// deliberately indistinguishable from what a caller sees for an unknown
	// user, so neither can be used to enumerate accounts.
	ErrMismatch = errors.New("password does not match")

	// ErrInvalidHash means the stored string is not a hash this package
	// produced. It indicates a corrupted or hand-edited database row, never a
	// wrong password, and must not be reported to the caller as a failed login.
	ErrInvalidHash = errors.New("stored password hash is malformed")
)

// HashPassword derives an argon2id hash and encodes it in PHC string format:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<base64 salt>$<base64 hash>
//
// The format is self-describing, so a hash produced with today's parameters
// stays verifiable after they are raised. That is why the schema has no
// separate algorithm column.
func HashPassword(password string) (string, error) {
	if len([]rune(password)) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether password produced encoded.
//
// It returns ErrMismatch for a wrong password and ErrInvalidHash for a stored
// value this package could not have produced. Callers must treat the second as
// a server fault, not as a failed login.
func VerifyPassword(encoded, password string) error {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return err
	}

	got := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, uint32(len(want)))

	// Constant time: a byte-by-byte comparison leaks how much of the hash
	// matched through its timing.
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// dummyHash is a real argon2id hash of a value nobody knows, used to give the
// unknown-user login path the same cost as the known-user path.
//
// It is computed once at startup rather than embedded as a constant so it
// always matches the parameters currently in force.
var dummyHash = sync.OnceValue(func() string {
	h, err := HashPassword(strings.Repeat("x", MinPasswordLength))
	if err != nil {
		// HashPassword can only fail here if the CSPRNG is broken, in which
		// case nothing else in this package is trustworthy either.
		panic("auth: computing dummy hash: " + err.Error())
	}
	return h
})

// DummyVerify performs a password verification whose result is discarded.
//
// Login calls it when the username is unknown. Without it, a rejected login
// returns immediately for an unknown user and only after a full argon2 hash for
// a known one, and that timing difference tells an attacker which usernames
// exist.
//
// It costs the same 64MiB as a real verification, which is the point — and is
// worth knowing when reasoning about login concurrency, since failed logins
// against nonexistent users are exactly what a credential-stuffing attempt
// produces.
func DummyVerify(password string) {
	_ = VerifyPassword(dummyHash(), password)
}

// b64 is the encoding used inside PHC strings: raw (unpadded) standard base64.
var b64 = base64.RawStdEncoding

// hashParams are the argon2 costs recorded in a PHC string.
type hashParams struct {
	memory  uint32
	time    uint32
	threads uint8
}

// decodeHash parses a PHC string back into its parameters, salt, and digest.
func decodeHash(encoded string) (hashParams, []byte, []byte, error) {
	// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash> — six fields, the first
	// empty because the string starts with the separator.
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return hashParams{}, nil, nil, ErrInvalidHash
	}
	if parts[1] != "argon2id" {
		return hashParams{}, nil, nil, fmt.Errorf("%w: algorithm is %q", ErrInvalidHash, parts[1])
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return hashParams{}, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return hashParams{}, nil, nil, fmt.Errorf("%w: argon2 version %d", ErrInvalidHash, version)
	}

	var p hashParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return hashParams{}, nil, nil, ErrInvalidHash
	}
	if p.memory == 0 || p.time == 0 || p.threads == 0 {
		return hashParams{}, nil, nil, ErrInvalidHash
	}

	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return hashParams{}, nil, nil, ErrInvalidHash
	}

	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return hashParams{}, nil, nil, ErrInvalidHash
	}

	return p, salt, key, nil
}
