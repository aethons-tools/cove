// Package harbor is the credential broker: it authenticates an enrolled identity,
// decides whether that identity may reach a configured destination and which
// credential to inject, and reverse-proxies the request with harbor's real
// credential swapped in. Downstream secrets live in harbor, never in the caller.
package harbor

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"
)

// Identity is one enrolled actor's record. The raw token is never stored — only
// its hash — so a leaked store yields no usable credentials.
type Identity struct {
	ID           string    `json:"id"`
	TokenHash    string    `json:"token_hash"`
	Project      string    `json:"project"`
	Role         string    `json:"role"`
	Destinations []string  `json:"destinations"` // allow-listed destination names, e.g. ["anthropic","git"]
	Repos        []string  `json:"repos"`        // allowed "owner/repo" globs for git, e.g. ["aethons-tools/*"]
	Expiry       time.Time `json:"expiry"`       // zero = no expiry
}

// MintToken returns a new high-entropy bearer token (URL-safe, no padding).
func MintToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the hex SHA-256 of a token — the stored/looked-up key.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}
