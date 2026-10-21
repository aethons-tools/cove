package gitsource

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Source names a kit in a git repository: "<repo>[//<dir>]@<ref>".
type Source struct {
	Repo string // https URL (http only for loopback), no userinfo
	Dir  string // clean relative slash path; "" = the repository root
	Ref  string // branch, tag or 40-hex commit
}

// Parse reads "<repo>[//<dir>]@<ref>" and refuses anything Fetch would: see
// Source.check. The scheme and host are lowercased. A pasted credential is
// refused and never echoed: Parse's own errors quote nothing, and the
// checks it runs quote a repo URL only when its host is a plain host name.
func Parse(s string) (Source, error) {
	// Nothing is quoted until it is known to hold no space, quote, control,
	// format or non-printable character.
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
	// "https://user:pa/ss@host/path" (a password holding a raw '/') slips
	// past the authority check and leaves "host/path" as the ref. A ref whose
	// first component looks like a host name is refused; Source.check closes
	// the dotless-host and port variants.
	if first, _, ok := strings.Cut(ref, "/"); ok && (strings.Contains(first, ".") || first == "localhost") {
		return Source{}, errors.New("source: the ref looks like <host>/<path>; is a credential pasted into the repo URL? (use a kits.git cred)")
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
func hasUnsafeChars(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r <= 0x20 || r == 0x7f || r == '"' || r == '\'' || r == '`' || unicode.IsSpace(r) || unicode.IsControl(r) ||
			unicode.Is(unicode.Cf, r) || !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}

// maxQuoted caps how much of any input an error quotes.
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

// check validates every part of s. Parse and Fetch/Resolve both run it, so a
// hand-built Source gets no further than a parsed one.
func (s Source) check() error {
	if err := checkRepoURL(s.Repo); err != nil {
		return err
	}
	if err := checkDir(s.Dir); err != nil {
		return err
	}
	if err := checkRef(s.Ref); err != nil {
		return err
	}
	// "https://user:1234/x@gitserver/a/b" or "https://tok123/x@gitserver/a/b"
	// is a pasted credential that reads as a repo on host "user" port 1234 (or
	// host "tok123") at ref "gitserver/a/b"; the text cannot tell them apart.
	// So a ref holding '/' needs a dotted (or loopback) host with no port.
	if strings.Contains(s.Ref, "/") {
		u, _ := url.Parse(s.Repo) // parsed fine in checkRepoURL
		if h := u.Hostname(); u.Port() != "" || !strings.Contains(h, ".") && !isLoopback(h) {
			return errors.New("source: refs with '/' need a dotted, portless https host (a port or a dotless host here could be a pasted credential)")
		}
	}
	return nil
}

// checkRepoURL accepts https URLs, and http only to a loopback host, with a
// path and no userinfo, empty port, query, fragment or dot segments. Errors
// quote the URL only when its host is a plain host name (see quoteRepo).
func checkRepoURL(raw string) error {
	if hasUnsafeChars(raw) {
		return errors.New("repo: spaces, quotes, control and non-printable characters are not allowed")
	}
	if strings.Contains(raw, "@") {
		return errors.New("repo: credentials in the URL are refused (use a kits.git cred)")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("repo: not a valid URL")
	}
	if u.User != nil {
		return errors.New("repo: credentials in the URL are refused (use a kits.git cred)")
	}
	if strings.HasSuffix(u.Host, ":") {
		return errors.New("repo: empty port (a ':' with nothing after it) is refused")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return errors.New("repo: invalid port")
		}
	}
	shown := quoteRepo(u)
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopback(u.Hostname()):
	case u.Scheme == "http":
		return fmt.Errorf("repo %s: http is allowed only for loopback hosts; use https", shown)
	default:
		return fmt.Errorf("repo %s: unsupported scheme; use https", shown)
	}
	if u.Host == "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("repo %s: want https://<host>/<path> with no query or fragment", shown)
	}
	if strings.Trim(u.Path, "/") == "" {
		return fmt.Errorf("repo %s: missing the repository path", shown)
	}
	// Dot segments would let a repo URL match one credential prefix while
	// naming a path outside it.
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("repo %s: '.' and '..' path segments are refused", shown)
		}
	}
	return nil
}

// quoteRepo renders u for an error: the quoted scheme, host, port and
// escaped path when the host is a plain host name, else a placeholder (the
// host might be a token: "https://tok123/x@…").
func quoteRepo(u *url.URL) string {
	if !plainHost(u.Hostname()) {
		return "(URL not shown)"
	}
	return strconv.Quote(clip(u.Scheme + "://" + u.Host + u.EscapedPath()))
}

// plainHost reports whether h is loopback or a dotted DNS name of
// [a-z0-9-] labels.
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
		return fmt.Errorf("dir %q: want a clean relative path inside the repository", clip(dir))
	}
	return nil
}

// checkRef accepts a branch or tag name git would (a conservative subset of
// git check-ref-format), or a 40-hex commit. Anything that could read as an
// option or rewrite a refspec (leading '-', '+', ':') is refused.
func checkRef(ref string) error {
	bad := func(why string) error { return fmt.Errorf("ref %q: %s", clip(ref), why) }
	switch {
	case ref == "":
		return errors.New("ref: missing (want <repo>[//<dir>]@<ref>)")
	case hasUnsafeChars(ref):
		return errors.New("ref: spaces, quotes and control characters are not allowed")
	case len(ref) > 255:
		return bad("too long")
	case strings.HasPrefix(ref, "-"), strings.HasPrefix(ref, "+"):
		return bad("must not start with '-' or '+'")
	case strings.HasPrefix(ref, "/"), strings.HasSuffix(ref, "/"), strings.Contains(ref, "//"):
		return bad("bad '/'")
	case strings.Contains(ref, ".."), strings.HasSuffix(ref, "."), strings.HasSuffix(ref, ".lock"):
		return bad("bad '.'")
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\@", r) {
			return bad(fmt.Sprintf("character %q not allowed", r))
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return bad("bad path component")
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
