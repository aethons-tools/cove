// Package ticketkey validates and builds the ticket keys Jam stores for
// GitHub issues: "owner/repo#<n>". Linear keys ("AET-42") are opaque to it.
// It imports only the standard library so internal/jam and the trackers can
// both use it.
//
// Keys are canonical: owner/repo lowercased (GitHub treats them
// case-insensitively) and the number without leading zeros, so every
// spelling of one issue maps to one key — and one ticket channel.
package ticketkey

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

var (
	// ErrGitHub is the fixed refusal for a malformed GitHub ticket key.
	ErrGitHub = errors.New("github ticket key must be owner/repo#<n>")
	// ErrRepo is the fixed refusal for a malformed GitHub repo.
	ErrRepo = errors.New("github repo must be owner/name")

	githubKey = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#([0-9]+)$`)
	repoRE    = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$`)
)

// ValidRepo checks an "owner/name" repo. A segment made only of dots is
// refused: GitHub allows none, and "." or ".." would walk an API URL path.
func ValidRepo(repo string) error {
	m := repoRE.FindStringSubmatch(repo)
	if m == nil || onlyDots(m[1]) || onlyDots(m[2]) {
		return ErrRepo
	}
	return nil
}

func onlyDots(s string) bool { return strings.Trim(s, ".") == "" }

// ParseGitHub splits "owner/repo#<n>" into its canonical (lowercased) repo
// and issue number (n >= 1).
func ParseGitHub(key string) (repo string, n int, err error) {
	m := githubKey.FindStringSubmatch(key)
	if m == nil || ValidRepo(m[1]) != nil {
		return "", 0, ErrGitHub
	}
	n, err = strconv.Atoi(m[2])
	if err != nil || n < 1 {
		return "", 0, ErrGitHub
	}
	return strings.ToLower(m[1]), n, nil
}

// IsGitHub reports whether key is a well-formed GitHub ticket key.
func IsGitHub(key string) bool {
	_, _, err := ParseGitHub(key)
	return err == nil
}

// GitHub builds the canonical ticket key of issue n in repo.
func GitHub(repo string, n int) string {
	return strings.ToLower(repo) + "#" + strconv.Itoa(n)
}

// Canonical returns key's canonical spelling ("Acme/Board#007" →
// "acme/board#7"), or ErrGitHub if key is not a GitHub ticket key.
func Canonical(key string) (string, error) {
	repo, n, err := ParseGitHub(key)
	if err != nil {
		return "", err
	}
	return GitHub(repo, n), nil
}
