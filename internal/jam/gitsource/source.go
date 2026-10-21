package gitsource

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// Source names a kit in a git repository: "<repo>[//<dir>]@<ref>".
type Source struct {
	Repo string // https URL (http only for loopback), no userinfo
	Dir  string // clean relative slash path; "" = the repository root
	Ref  string // branch, tag or 40-hex commit
}

// Parse reads "<repo>[//<dir>]@<ref>" and refuses anything Fetch would: see
// Source.check. A pasted credential is refused before anything else and is
// never echoed: errors quote the input only once it is known to have no
// userinfo, and then without query or fragment.
func Parse(s string) (Source, error) {
	scheme := strings.Index(s, "://")
	if scheme < 0 {
		return Source{}, errors.New("source: want <repo>[//<dir>]@<ref> with an https repo URL")
	}
	authority := s[scheme+3:]
	if i := strings.IndexByte(authority, '/'); i >= 0 {
		authority = authority[:i]
	}
	if strings.Contains(authority, "@") {
		return Source{}, errors.New("source: credentials in the repo URL are refused (use a kits.git cred)")
	}
	// A ref never contains '@', and nor may the repo URL: exactly one '@'
	// separates them. (An '@' anywhere else is most likely a pasted
	// credential whose password holds a '/'.)
	switch strings.Count(s, "@") {
	case 0:
		return Source{}, fmt.Errorf("source %q: missing @<ref> (want <repo>[//<dir>]@<ref>)", redactURL(s))
	case 1:
	default:
		return Source{}, errors.New("source: '@' may appear only once, before the ref (credentials belong in a kits.git cred)")
	}
	at := strings.IndexByte(s, '@')
	repo, ref := s[:at], s[at+1:]
	// "https://user:pa/ss@host/path" (a password holding a raw '/') slips
	// past the authority check and leaves "host/path" as the ref. A ref whose
	// first component looks like a host name is refused, without echoing.
	if first, _, ok := strings.Cut(ref, "/"); ok && (strings.Contains(first, ".") || first == "localhost") {
		return Source{}, errors.New("source: the ref looks like <host>/<path>; is a credential pasted into the repo URL? (use a kits.git cred)")
	}
	src := Source{Ref: ref}
	// The dir separator is the first "//" after the scheme's.
	if i := strings.Index(repo[scheme+3:], "//"); i >= 0 {
		i += scheme + 3
		repo, src.Dir = repo[:i], repo[i+2:]
		if src.Dir == "" {
			return Source{}, fmt.Errorf("source %q: empty dir after //", redactURL(s))
		}
		if src.Dir == "." {
			src.Dir = ""
		}
	}
	src.Repo = repo
	if err := src.check(); err != nil {
		return Source{}, err
	}
	return src, nil
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
	return checkRef(s.Ref)
}

// checkRepoURL accepts https URLs, and http only to a loopback host, with a
// path and no userinfo, query, fragment or dot segments. Errors never quote
// a URL that failed to parse or carries userinfo, and quote others without
// query or fragment.
func checkRepoURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("repo: not a valid URL")
	}
	if u.User != nil || strings.Contains(raw, "@") {
		return errors.New("repo: credentials in the URL are refused (use a kits.git cred)")
	}
	shown := redactURL(raw)
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopback(u.Hostname()):
	case u.Scheme == "http":
		return fmt.Errorf("repo %q: http is allowed only for loopback hosts; use https", shown)
	default:
		return fmt.Errorf("repo %q: unsupported scheme %q; use https", shown, u.Scheme)
	}
	if u.Host == "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("repo %q: want https://<host>/<path> with no query or fragment", shown)
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return errors.New("repo: invalid port")
		}
	}
	if strings.Trim(u.Path, "/") == "" {
		return fmt.Errorf("repo %q: missing the repository path", shown)
	}
	// Dot segments would let a repo URL match one credential prefix while
	// naming a path outside it.
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("repo %q: '.' and '..' path segments are refused", shown)
		}
	}
	return nil
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
	if path.IsAbs(dir) || path.Clean(dir) != dir || dir == ".." || strings.HasPrefix(dir, "../") ||
		strings.ContainsAny(dir, "\\\x00") {
		return fmt.Errorf("dir %q: want a clean relative path inside the repository", dir)
	}
	return nil
}

// checkRef accepts a branch or tag name git would (a conservative subset of
// git check-ref-format), or a 40-hex commit. Anything that could read as an
// option or rewrite a refspec (leading '-', '+', ':') is refused.
func checkRef(ref string) error {
	bad := func(why string) error { return fmt.Errorf("ref %q: %s", ref, why) }
	switch {
	case ref == "":
		return errors.New("ref: missing (want <repo>[//<dir>]@<ref>)")
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
