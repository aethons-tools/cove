// Package preview names studio previews (studio collaboration slice 2,
// docs/usage/jam/collaboration.md#previews): which ports and names an agent
// may expose and the host each is served at.
package preview

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"net"
	"regexp"
	"strings"
)

const (
	// SSHPort is the in-studio sshd port; it can never be exposed.
	SSHPort = 2222
	// MaxExposures is the most previews one session may expose at once.
	MaxExposures = 8
	// ReservedLabel is the login host's label and so not a usable preview name.
	ReservedLabel = "jam"

	labelLen      = 26
	sessionPrefix = "ses_"
)

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$`)

// ValidName reports whether name is usable as a preview name.
func ValidName(name string) error {
	if !nameRE.MatchString(name) || strings.Contains(name, "--") || name == ReservedLabel {
		return fmt.Errorf("preview name %q: want 1-30 of [a-z0-9-], not starting/ending with '-', no '--', not %q", name, ReservedLabel)
	}
	return nil
}

// ValidPort reports whether port may be exposed.
func ValidPort(port int) error {
	if port < 1024 || port > 65535 || port == SSHPort {
		return fmt.Errorf("port %d: want 1024-65535, not %d (sshd)", port, SSHPort)
	}
	return nil
}

func validLabel(label string) bool {
	return len(label) == labelLen && !strings.ContainsFunc(label, func(r rune) bool {
		return !(('0' <= r && r <= '9') || ('a' <= r && r <= 'z'))
	})
}

// Label is the DNS label that stands for an actor in a preview host. A session
// id (ses_ + 26 chars of [0-9a-z]) yields its 26-char body; any other id
// (legacy standing, manual) yields the first 26 chars of the lowercase
// unpadded base32 of its SHA-256. Always 26 chars of [a-z0-9].
func Label(actorID string) string {
	if body, ok := strings.CutPrefix(actorID, sessionPrefix); ok && validLabel(body) {
		return body
	}
	sum := sha256.Sum256([]byte(actorID))
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return strings.ToLower(enc[:labelLen])
}

// ActorForLabel finds the actor whose Label is label.
func ActorForLabel(label string, actorIDs []string) (string, bool) {
	for _, id := range actorIDs {
		if Label(id) == label {
			return id, true
		}
	}
	return "", false
}

// Host is the preview host for the named exposure of actorID under domain.
func Host(domain, name, actorID string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	return name + "--" + Label(actorID) + "." + domain, nil
}

func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// Parse splits a preview host into its name and actor label (see Label); the
// caller maps the label back to an actor (ActorForLabel). A :port is ignored
// and case is folded; the host must be exactly one label under domain.
func Parse(host, domain string) (name, label string, ok bool) {
	h := strings.ToLower(stripPort(host))
	first, ok := strings.CutSuffix(h, "."+strings.ToLower(domain))
	if !ok || strings.Contains(first, ".") {
		return "", "", false
	}
	name, label, ok = strings.Cut(first, "--")
	if !ok || ValidName(name) != nil || !validLabel(label) {
		return "", "", false
	}
	return name, label, true
}

// LoginHost is the host previews' login flow is served at.
func LoginHost(domain string) string { return ReservedLabel + "." + strings.ToLower(domain) }

// IsPreviewHost reports whether host is a preview host or the login host.
func IsPreviewHost(host, domain string) bool {
	h := strings.ToLower(stripPort(host))
	if h == LoginHost(domain) {
		return true
	}
	_, _, ok := Parse(h, domain)
	return ok
}
