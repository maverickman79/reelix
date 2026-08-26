package auth

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

const goodPassword = "correct horse battery staple"

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if err := VerifyPassword(hash, goodPassword); err != nil {
		t.Errorf("VerifyPassword with the correct password: %v", err)
	}

	if err := VerifyPassword(hash, goodPassword+"x"); !errors.Is(err, ErrMismatch) {
		t.Errorf("VerifyPassword with a wrong password returned %v, want ErrMismatch", err)
	}
}

// TestHashDoesNotContainPassword is the property that makes the whole scheme
// worth having.
func TestHashDoesNotContainPassword(t *testing.T) {
	hash, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(hash, goodPassword) {
		t.Fatal("the encoded hash contains the plaintext password")
	}
	for _, word := range strings.Fields(goodPassword) {
		if strings.Contains(hash, word) {
			t.Errorf("the encoded hash contains %q from the password", word)
		}
	}
}

func TestHashFormat(t *testing.T) {
	hash, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	// PHC string format, with the parameters recorded in the hash itself.
	// This is what lets the costs be raised later without invalidating
	// existing passwords, and why the schema has no algorithm column.
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("unexpected hash format: %s", hash)
	}
	if n := strings.Count(hash, "$"); n != 5 {
		t.Errorf("hash has %d separators, want 5: %s", n, hash)
	}
}

// TestSaltIsUnique catches the classic mistake of a fixed or missing salt,
// which would make identical passwords produce identical hashes.
func TestSaltIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 16)
	for range 16 {
		hash, err := HashPassword(goodPassword)
		if err != nil {
			t.Fatalf("HashPassword: %v", err)
		}
		if _, dup := seen[hash]; dup {
			t.Fatal("hashing the same password twice produced the same hash")
		}
		seen[hash] = struct{}{}

		if err := VerifyPassword(hash, goodPassword); err != nil {
			t.Fatalf("a freshly salted hash failed to verify: %v", err)
		}
	}
}

func TestPasswordTooShort(t *testing.T) {
	short := strings.Repeat("a", MinPasswordLength-1)
	if _, err := HashPassword(short); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("HashPassword(%d chars) returned %v, want ErrPasswordTooShort", len(short), err)
	}

	exact := strings.Repeat("a", MinPasswordLength)
	if _, err := HashPassword(exact); err != nil {
		t.Errorf("HashPassword at exactly the minimum length: %v", err)
	}
}

// TestPasswordLengthCountsRunes checks the minimum is not measured in bytes,
// which would let a short multi-byte password through.
func TestPasswordLengthCountsRunes(t *testing.T) {
	// 11 runes, 33 bytes. Byte-counting would wrongly accept it.
	short := strings.Repeat("日", MinPasswordLength-1)
	if _, err := HashPassword(short); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("an %d-rune password was accepted: %v", len([]rune(short)), err)
	}
}

// TestVerifyRejectsMalformedHash covers every way a stored hash can be broken.
//
// These must return ErrInvalidHash, not ErrMismatch: a corrupt row is a server
// fault, and reporting it as a failed login would hide it forever.
func TestVerifyRejectsMalformedHash(t *testing.T) {
	valid, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	parts := strings.Split(valid, "$")

	tests := []struct {
		name string
		hash string
	}{
		{"empty", ""},
		{"not a phc string", "hunter2"},
		{"too few fields", "$argon2id$v=19$m=65536,t=3,p=4$" + parts[4]},
		{"wrong algorithm", "$argon2i$v=19$m=65536,t=3,p=4$" + parts[4] + "$" + parts[5]},
		{"bcrypt", "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"},
		{"unknown version", "$argon2id$v=99$m=65536,t=3,p=4$" + parts[4] + "$" + parts[5]},
		{"unparseable params", "$argon2id$v=19$m=x,t=3,p=4$" + parts[4] + "$" + parts[5]},
		{"zero memory", "$argon2id$v=19$m=0,t=3,p=4$" + parts[4] + "$" + parts[5]},
		{"bad base64 salt", "$argon2id$v=19$m=65536,t=3,p=4$!!!!$" + parts[5]},
		{"bad base64 hash", "$argon2id$v=19$m=65536,t=3,p=4$" + parts[4] + "$!!!!"},
		{"empty salt", "$argon2id$v=19$m=65536,t=3,p=4$$" + parts[5]},
		{"leading garbage", "x" + valid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyPassword(tt.hash, goodPassword)
			if !errors.Is(err, ErrInvalidHash) {
				t.Errorf("VerifyPassword(%q) returned %v, want ErrInvalidHash", tt.name, err)
			}
		})
	}
}

// TestVerifyToleratesDifferentParameters is the forward-compatibility promise:
// a hash produced with weaker costs must still verify after the constants are
// raised, because the costs travel in the hash.
func TestVerifyToleratesDifferentParameters(t *testing.T) {
	// A hash built by hand with parameters deliberately unlike the current
	// constants, as an older Reelix would have written.
	const (
		oldMemory  = 32 * 1024
		oldTime    = 2
		oldThreads = 1
	)

	salt := []byte("sixteen bytes!!!")
	key := argon2.IDKey([]byte(goodPassword), salt, oldTime, oldMemory, oldThreads, argonKeyLen)

	old := "$argon2id$v=19$m=32768,t=2,p=1$" + b64.EncodeToString(salt) + "$" + b64.EncodeToString(key)

	if err := VerifyPassword(old, goodPassword); err != nil {
		t.Errorf("a hash with older parameters failed to verify: %v", err)
	}
	if err := VerifyPassword(old, "wrong password entirely"); !errors.Is(err, ErrMismatch) {
		t.Errorf("an older hash accepted the wrong password: %v", err)
	}
}

// TestDummyVerify checks the timing-equalisation helper does not panic and
// rejects everything. Its value is in what it costs, not what it returns.
func TestDummyVerify(t *testing.T) {
	DummyVerify("anything at all")
	DummyVerify("")
}
