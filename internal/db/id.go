package db

import (
	"encoding/base32"
	"strings"

	"github.com/google/uuid"
)

// NewID generates a fresh club identifier: a UUIDv4 encoded with lowercase
// base32hex (no padding). 26 chars, URL-safe.
func NewID() (string, error) {
	u, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	s := base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(u[:])
	return strings.ToLower(s), nil
}

// ValidID checks whether s looks like a club ID we issued.
func ValidID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'v')) {
			return false
		}
	}
	return true
}
