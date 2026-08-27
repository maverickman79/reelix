package jellyfin

import (
	"encoding/hex"
	"fmt"
	"strings"
	"uuid"
)

// compatID renders a UUID as Jellyfin clients expect it: 32 lowercase
// hexadecimal characters with no dashes.
//
// This conversion exists ONLY at this boundary. Native services and the native
// API use standard dashed UUIDs; the constitution is explicit about that, and
// the dashless form must not leak out of this package. Clients string-compare
// these values and build URLs from them, so a dashed id produces failures that
// look like missing content rather than malformed identifiers.
func compatID(id uuid.UUID) string {
	return hex.EncodeToString(id[:])
}

// parseCompatID converts an id received from a client back into a UUID.
//
// It accepts both the dashless form clients send and the dashed form, because
// a client echoing back an id it read elsewhere is not worth rejecting.
func parseCompatID(s string) (uuid.UUID, error) {
	s = strings.TrimSpace(s)

	switch len(s) {
	case 32:
		raw, err := hex.DecodeString(s)
		if err != nil {
			return uuid.UUID{}, fmt.Errorf("invalid id %q", s)
		}
		var id uuid.UUID
		copy(id[:], raw)
		return id, nil

	case 36:
		id, err := uuid.Parse(s)
		if err != nil {
			return uuid.UUID{}, fmt.Errorf("invalid id %q", s)
		}
		return id, nil

	default:
		return uuid.UUID{}, fmt.Errorf("invalid id %q", s)
	}
}
