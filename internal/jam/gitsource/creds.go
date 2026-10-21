package gitsource

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// Cred is a static credential for every repo under Prefix. Value is a token,
// or "user:token". It is never formatted: String and GoString omit it.
type Cred struct{ Prefix, Name, Value string }

// String names the credential without its value.
func (c Cred) String() string { return fmt.Sprintf("cred %s for %s", c.Name, c.Prefix) }

// GoString keeps %#v from printing the value.
func (c Cred) GoString() string {
	return fmt.Sprintf("gitsource.Cred{Prefix:%q, Name:%q, Value:REDACTED}", c.Prefix, c.Name)
}

// Creds maps URL prefixes to credentials.
type Creds []Cred

// For returns the credential with the longest prefix matching repo. A prefix
// matches only at a boundary — it ends in "/", or repo continues with "/" or
// ends — so "https://git.example.com" never matches
// "https://git.example.com.evil.net/r" and ".../acme" never ".../acmeevil".
func (c Creds) For(repo string) (Cred, bool) {
	var best Cred
	found := false
	for _, cr := range c {
		if cr.Prefix == "" || !strings.HasPrefix(repo, cr.Prefix) {
			continue
		}
		if rest := repo[len(cr.Prefix):]; !strings.HasSuffix(cr.Prefix, "/") && rest != "" && rest[0] != '/' {
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
