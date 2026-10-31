package gitsource

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// Cred is the credential for every repo under Prefix. Value is a token, or
// "user:token". When Resolve is set it is called on every fetch instead, so a
// short-lived or rotated token (a minted GitHub App token, a re-run command)
// is always current. It is never formatted: String and GoString omit it.
type Cred struct {
	Prefix, Name, Value string
	Resolve             func() (string, error)
	// User, when set, is the username sent with the token (oauth2 for a
	// GitLab token on a host whose name doesn't say gitlab); else basicAuth's.
	User string
	// Key is the serve-config key the credential comes from, for messages
	// ("" = kits.git).
	Key string
}

// key is where c is configured, as messages name it.
func (c Cred) key() string {
	if c.Key == "" {
		return "kits.git"
	}
	return c.Key
}

// value returns the credential's current token.
func (c Cred) value() (string, error) {
	if c.Resolve == nil {
		return c.Value, nil
	}
	return c.Resolve()
}

// Token is the credential's current token (Resolve called now).
func (c Cred) Token() (string, error) { return c.value() }

// String names the credential without its value.
func (c Cred) String() string { return fmt.Sprintf("cred %s for %s", c.Name, c.Prefix) }

// GoString keeps %#v from printing the value.
func (c Cred) GoString() string {
	return fmt.Sprintf("gitsource.Cred{Prefix:%q, Name:%q, Value:REDACTED}", c.Prefix, c.Name)
}

// Creds maps URL prefixes to credentials.
type Creds []Cred

// For returns the credential with the longest prefix matching repo (scheme
// and host case-insensitively, the path exactly). A prefix
// matches only at a boundary — it ends in "/", or repo continues with "/" or
// ends — so "https://git.example.com" never matches
// "https://git.example.com.evil.net/r" and ".../acme" never ".../acmeevil".
func (c Creds) For(repo string) (Cred, bool) {
	var best Cred
	found := false
	repo = normalizeSchemeHost(repo)
	for _, cr := range c {
		prefix := normalizeSchemeHost(cr.Prefix) // scheme and host match case-insensitively
		if prefix == "" || !strings.HasPrefix(repo, prefix) {
			continue
		}
		if rest := repo[len(prefix):]; !strings.HasSuffix(prefix, "/") && rest != "" && rest[0] != '/' {
			continue
		}
		if !found || len(cr.Prefix) > len(best.Prefix) {
			best, found = cr, true
		}
	}
	return best, found
}

// basicAuth is the HTTP Basic auth go-git sends for c: username x-access-token
// on github.com hosts, oauth2 on any host whose name contains "gitlab"
// (password: the whole value for both), else the user of a "user:token"
// value, else "git".
func basicAuth(repo string, c Cred) *githttp.BasicAuth {
	if c.User != "" {
		return &githttp.BasicAuth{Username: c.User, Password: c.Value}
	}
	host := ""
	if u, err := url.Parse(repo); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		return &githttp.BasicAuth{Username: "x-access-token", Password: c.Value}
	case strings.Contains(host, "gitlab"):
		return &githttp.BasicAuth{Username: "oauth2", Password: c.Value}
	}
	if user, token, ok := strings.Cut(c.Value, ":"); ok && user != "" {
		return &githttp.BasicAuth{Username: user, Password: token}
	}
	return &githttp.BasicAuth{Username: "git", Password: c.Value}
}

// urlInText matches a URL in free text: scheme, optional userinfo, the
// host and path, and an optional query or fragment.
var urlInText = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)(?:[^/?#@\s"']*@)?([^?#\s"']*)(?:[?#][^\s"']*)?`)

// redactURL strips the userinfo, query and fragment from every URL in s,
// which may be a URL or an error message that quotes one.
func redactURL(s string) string {
	return urlInText.ReplaceAllString(s, "$1$2")
}

// AuthFor is the HTTP auth go-git sends to repo: its credential's current
// value (Resolve called now), as basicAuth names it; nil with no credential.
// An error names the credential, never its value.
func (c Creds) AuthFor(repo string) (transport.AuthMethod, error) {
	cred, ok := c.For(repo)
	if !ok {
		return nil, nil
	}
	v, err := cred.value()
	if err != nil {
		return nil, fmt.Errorf("cred %s could not be resolved", cred.Name)
	}
	cred.Value = v
	return basicAuth(repo, cred), nil
}

// Redact strips the userinfo, query and fragment from every URL in s.
func Redact(s string) string { return redactURL(s) }
