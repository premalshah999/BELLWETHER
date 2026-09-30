// Package auth issues and checks API keys.
//
// One key per person, each carrying a profile. A shared passphrase cannot say
// who is making a request, whose access to withdraw when a laptop goes
// missing, or who was signed in when something changed; a key answers all
// three for the same cost per request.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// Role decides what a key may do.
type Role string

const (
	// RoleOwner may do everything, including issuing and revoking keys.
	RoleOwner Role = "owner"
	// RoleOperator may use the whole application but not manage keys.
	RoleOperator Role = "operator"
	// RoleViewer may read but not change anything, and may not spend the
	// token budget: an AI call is a write as far as cost is concerned.
	RoleViewer Role = "viewer"
)

// Valid reports whether a role is one this system knows.
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleOperator, RoleViewer:
		return true
	}
	return false
}

// keyPrefix marks a TradeSys key in logs, config files and secret scanners.
//
// A recognisable prefix is what lets a leaked key be spotted — by its owner,
// by a scanner watching a repository, or by whoever reads a log by accident.
const keyPrefix = "tsk_"

// prefixLength is how much of the key is stored in clear for lookup.
//
// Enough to identify a row without scanning every digest, short enough that
// knowing it brings an attacker no closer to the rest.
const prefixLength = 12

// Key is a newly issued credential.
//
// Secret is returned exactly once, at issue. Nothing stores it, so it cannot
// be recovered — a lost key is reissued, never looked up.
type Key struct {
	Secret string
	Prefix string
	Hash   []byte
}

// Profile is who a key belongs to.
type Profile struct {
	ID         int64      `json:"id"`
	Prefix     string     `json:"prefix"`
	Name       string     `json:"name"`
	Role       Role       `json:"role"`
	Note       string     `json:"note,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// CanWrite reports whether this profile may change anything.
func (p Profile) CanWrite() bool { return p.Role == RoleOwner || p.Role == RoleOperator }

// Generate mints a new key.
//
// The secret is 32 bytes from crypto/rand — 256 bits, so guessing is not a
// threat model and the digest need not be slow to compute.
func Generate() (Key, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Key{}, fmt.Errorf("auth: no source of randomness: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	secret := keyPrefix + body

	sum := sha256.Sum256([]byte(secret))
	return Key{
		Secret: secret,
		Prefix: secret[:prefixLength],
		Hash:   sum[:],
	}, nil
}

// PrefixOf returns the lookup handle for a presented key.
//
// Returns false for anything that is not shaped like one of our keys, so a
// malformed credential is rejected before it reaches the database.
func PrefixOf(presented string) (string, bool) {
	presented = strings.TrimSpace(presented)
	if !strings.HasPrefix(presented, keyPrefix) || len(presented) < prefixLength+8 {
		return "", false
	}
	return presented[:prefixLength], true
}

// Matches reports whether a presented key hashes to a stored digest.
//
// Constant time. A comparison that returns on the first differing byte leaks
// how much of a guess was right, which is enough to reconstruct the rest one
// byte at a time.
func Matches(presented string, hash []byte) bool {
	sum := sha256.Sum256([]byte(strings.TrimSpace(presented)))
	return subtle.ConstantTimeCompare(sum[:], hash) == 1
}
