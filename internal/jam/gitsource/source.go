package gitsource

import (
	"errors"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/studio"
)

// Source names a kit in a git repository: "<repo>[//<dir>]@<ref>".
type Source struct {
	Repo string // https URL (http only for loopback) to a dotted or loopback host, no userinfo
	Dir  string // clean relative slash path; "" = the repository root
	Ref  string // branch, tag or 40-hex commit
}

// Parse reads "<repo>[//<dir>]@<ref>" and refuses anything Fetch would: see
// Source.check. The scheme and host are lowercased.
//
// A source is operator-typed text that may be a pasted clone URL holding a
// credential, so no error from Parse or check quotes any part of the input:
// every message is fixed text naming the component at fault, and holds no
// digit (FuzzParse uses that as an echo canary). Structural refusals — the
// '@' count, credentials in the authority, the host rule, host-like refs and
// the port/slash-ref rule — run before any component is validated. A pasted
// credential can therefore reach an accepted Source only through text that
// is a valid dotted-host repo URL as far as the text can tell (the accepted
// residual: a dotted head — a token or a user name such as "john.doe" — read
// as the host, as in "https://ab.cd/x@gitserver/a/b").
func Parse(s string) (Source, error) {
	if hasUnsafeChars(s) {
		return Source{}, errors.New("source: spaces, quotes, control and non-printable characters are not allowed")
	}
	if scheme := strings.Index(s, "://"); scheme >= 0 && strings.Contains(authorityOf(s[scheme+3:]), "@") {
		return Source{}, errors.New("source: credentials in the repo URL are refused (use a kits.git cred)")
	}
	// A ref never contains '@', and nor may the repo URL: exactly one '@'
	// separates them. (An '@' anywhere else is most likely a pasted
	// credential whose password holds a '/'.)
	switch strings.Count(s, "@") {
	case 0:
		return Source{}, errors.New("source: missing @<ref> (want <repo>[//<dir>]@<ref>)")
	case 1:
	default:
		return Source{}, errors.New("source: '@' may appear only once, before the ref (credentials belong in a kits.git cred)")
	}
	at := strings.IndexByte(s, '@')
	repo, ref := s[:at], s[at+1:]
	scheme := strings.Index(repo, "://")
	if scheme < 0 {
		return Source{}, errors.New("source: want <repo>[//<dir>]@<ref> with an https repo URL")
	}
	src := Source{Ref: ref}
	// The dir separator is the first "//" after the scheme's.
	if i := strings.Index(repo[scheme+3:], "//"); i >= 0 {
		i += scheme + 3
		repo, src.Dir = repo[:i], repo[i+2:]
		if src.Dir == "" {
			return Source{}, errors.New("source: empty dir after //")
		}
		if src.Dir == "." {
			src.Dir = ""
		}
	}
	src.Repo = normalizeSchemeHost(repo)
	if err := src.check(); err != nil {
		return Source{}, err
	}
	return src, nil
}

// authorityOf is the text of s up to its first '/' (s starts after "://").
func authorityOf(s string) string {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
}

// normalizeSchemeHost lowercases the scheme and authority of a URL string
// and leaves the rest (the path, which is case-sensitive) as is.
func normalizeSchemeHost(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return raw
	}
	end := i + 3 + len(authorityOf(raw[i+3:]))
	return strings.ToLower(raw[:end]) + raw[end:]
}

// hasUnsafeChars reports whether s is invalid UTF-8 or holds whitespace, a
// quote, a backtick, or a control, format (Cf) or non-printable character.
func hasUnsafeChars(s string) bool { return studio.HasUnsafeChars(s) }

// maxQuoted caps how much of an accepted Source's repo a fetch error quotes.
const maxQuoted = 200

// clip shortens s to maxQuoted bytes (on a rune boundary) with an ellipsis.
func clip(s string) string {
	if len(s) <= maxQuoted {
		return s
	}
	cut := maxQuoted
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// String renders s in the form Parse reads.
func (s Source) String() string {
	out := s.Repo
	if s.Dir != "" && s.Dir != "." {
		out += "//" + s.Dir
	}
	return out + "@" + s.Ref
}

// dir is s.Dir with the root spelled ".", as Load takes it.
func (s Source) dir() string {
	if s.Dir == "" {
		return "."
	}
	return s.Dir
}

// check validates every part of s without quoting any of it. Parse and
// Fetch/Resolve both run it, so a hand-built Source gets no further than a
// parsed one. The repo URL and the structural rules go first; the dir and
// ref are validated only once the text can no longer be a pasted credential.
func (s Source) check() error {
	u, err := checkRepoURL(s.Repo)
	if err != nil {
		return err
	}
	if strings.Contains(s.Ref, "/") {
		// "https://john.doe:1234/x@gitserver/a/b" is a pasted credential that
		// reads as a repo on host john.doe, port 1234, at ref "gitserver/a/b";
		// the text cannot tell them apart. So a ref with '/' needs a portless
		// host, and its first component may not look like a host name.
		if u.Port() != "" {
			return errors.New("source: a ref containing '/' needs a repo URL without a port (with a port the text could be a pasted credential)")
		}
		if first, _, _ := strings.Cut(s.Ref, "/"); strings.Contains(first, ".") || first == "localhost" {
			return errors.New("source: the ref looks like <host>/<path>; is a credential pasted into the repo URL? (use a kits.git cred)")
		}
	}
	if err := checkDir(s.Dir); err != nil {
		return err
	}
	return checkRef(s.Ref)
}

// checkRepoURL accepts https URLs, and http only to a loopback host, with a
// path and no userinfo, empty port, query, fragment or dot segments. The host
// must be a dotted DNS name or a loopback address: a single-label host is
// what a pasted "https://user:1234/x@host" or "https://token/x@host" reads
// as. Errors are fixed text; the URL is parsed, never quoted.
func checkRepoURL(raw string) (*url.URL, error) {
	if hasUnsafeChars(raw) {
		return nil, errors.New("repo: spaces, quotes, control and non-printable characters are not allowed")
	}
	if strings.Contains(raw, "@") {
		return nil, errors.New("repo: credentials in the URL are refused (use a kits.git cred)")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("repo: not a valid URL")
	}
	if u.User != nil {
		return nil, errors.New("repo: credentials in the URL are refused (use a kits.git cred)")
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("repo: empty port (a ':' with nothing after it) is refused")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, errors.New("repo: invalid port")
		}
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopback(u.Hostname()):
	case u.Scheme == "http":
		return nil, errors.New("repo: http is allowed only for loopback hosts; use https")
	default:
		return nil, errors.New("repo: unsupported scheme; use https")
	}
	scheme := strings.Index(raw, "://")
	if scheme < 0 || u.Host == "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, errors.New("repo: want https://<host>/<path> with no query or fragment")
	}
	// The host must be the text itself: nothing percent-encoded or otherwise
	// rewritten by the URL parser, so the host rule below judges what the
	// operator typed.
	if u.Host != authorityOf(raw[scheme+3:]) {
		return nil, errors.New("repo: the host holds characters that are not allowed")
	}
	host := u.Hostname()
	if strings.HasPrefix(u.Host, "[") && host != "::1" {
		return nil, errors.New("repo: a bracketed IP literal host is accepted only for loopback; use a DNS name")
	}
	if !plainHost(host) {
		return nil, errors.New("repo: the host must be a dotted DNS name or a loopback address (a single-label host could be a pasted credential)")
	}
	if strings.Trim(u.Path, "/") == "" {
		return nil, errors.New("repo: missing the repository path")
	}
	// Dot segments would let a repo URL match one credential prefix while
	// naming a path outside it.
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return nil, errors.New("repo: '.' and '..' path segments are refused")
		}
	}
	return u, nil
}

// plainHost reports whether h is loopback or a dotted DNS name of
// [a-z0-9-] labels (compared case-insensitively).
func plainHost(h string) bool {
	if isLoopback(h) {
		return true
	}
	if !strings.Contains(h, ".") || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.ToLower(h), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !('a' <= r && r <= 'z' || '0' <= r && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func isLoopback(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// checkDir accepts "" (the root) or a clean relative slash path inside the
// repository.
func checkDir(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	if hasUnsafeChars(dir) {
		return errors.New("dir: spaces, quotes and control characters are not allowed")
	}
	if path.IsAbs(dir) || path.Clean(dir) != dir || dir == ".." || strings.HasPrefix(dir, "../") ||
		strings.ContainsAny(dir, "\\\x00") {
		return errors.New("dir: want a clean relative path inside the repository (no leading '/', no '.' or '..' segments, no '\\')")
	}
	return nil
}

// checkRef accepts a branch or tag name git would (a conservative subset of
// git check-ref-format), or a 40-hex commit. Anything that could read as an
// option or rewrite a refspec (leading '-', '+', ':') is refused.
func checkRef(ref string) error {
	switch {
	case ref == "":
		return errors.New("ref: missing (want <repo>[//<dir>]@<ref>)")
	case hasUnsafeChars(ref):
		return errors.New("ref: spaces, quotes and control characters are not allowed")
	case len(ref) > 255:
		return errors.New("ref: too long")
	case strings.HasPrefix(ref, "-"), strings.HasPrefix(ref, "+"):
		return errors.New("ref: must not start with '-' or '+'")
	case strings.HasPrefix(ref, "/"), strings.HasSuffix(ref, "/"), strings.Contains(ref, "//"):
		return errors.New("ref: must not start or end with '/' or contain '//'")
	case strings.Contains(ref, ".."), strings.HasSuffix(ref, "."), strings.HasSuffix(ref, ".lock"):
		return errors.New("ref: must not contain '..' or end with '.' or '.lock'")
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\@", r) {
			return errors.New("ref: holds a character git refuses in ref names (one of ~ ^ : ? * [ \\ @)")
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return errors.New("ref: a path component must not start with '.' or end with '.lock'")
		}
	}
	return nil
}

// isCommitID reports whether ref is a full 40-hex commit id.
func isCommitID(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, r := range ref {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f' || 'A' <= r && r <= 'F') {
			return false
		}
	}
	return true
}

// ValidatePrefix checks a credential's URL prefix (a kits.git url): it ends in
// "/" and, with a path appended, is a repo URL Parse would accept — https (http
// only for loopback), a dotted DNS host or loopback, no userinfo, query or
// fragment. Errors never echo the prefix.
func ValidatePrefix(prefix string) error {
	if !strings.HasSuffix(prefix, "/") {
		return errors.New("prefix: must end with '/' (so it matches whole path segments)")
	}
	return Source{Repo: prefix + "x", Ref: "main"}.check()
}
