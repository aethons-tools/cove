package gitsource

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseSource(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for in, want := range map[string]Source{
		"https://github.com/acme/kits@main":          {Repo: "https://github.com/acme/kits", Ref: "main"},
		"https://github.com/acme/kits//web/dev@v1.2": {Repo: "https://github.com/acme/kits", Dir: "web/dev", Ref: "v1.2"},
		"http://127.0.0.1:9/r.git@" + sha:            {Repo: "http://127.0.0.1:9/r.git", Ref: sha},
		"http://127.0.0.1:9/r.git@main":              {Repo: "http://127.0.0.1:9/r.git", Ref: "main"},
		"http://127.0.0.1/r@feature/x":               {Repo: "http://127.0.0.1/r", Ref: "feature/x"},
		"http://localhost/r.git@release/2026":        {Repo: "http://localhost/r.git", Ref: "release/2026"},
		"https://github.com/a/b@feature/x":           {Repo: "https://github.com/a/b", Ref: "feature/x"},
		"https://github.com/a/b@feature/x/y":         {Repo: "https://github.com/a/b", Ref: "feature/x/y"},
		"https://github.com/a/b@feature/v1.2":        {Repo: "https://github.com/a/b", Ref: "feature/v1.2"},
		"https://gitlab.com/g/k@release/2026":        {Repo: "https://gitlab.com/g/k", Ref: "release/2026"},
		"https://github.com/a/b@v1.2.3":              {Repo: "https://github.com/a/b", Ref: "v1.2.3"},
		"https://github.com/a/b@" + sha:              {Repo: "https://github.com/a/b", Ref: sha},
		"https://github.com/a/b//dir/sub@main":       {Repo: "https://github.com/a/b", Dir: "dir/sub", Ref: "main"},
		"https://github.com/a/b//.@main":             {Repo: "https://github.com/a/b", Ref: "main"},
		"https://git.example.com:8443/a/b@main":      {Repo: "https://git.example.com:8443/a/b", Ref: "main"},
		"https://git.example.com:8443/a/b@v1.2.3":    {Repo: "https://git.example.com:8443/a/b", Ref: "v1.2.3"},
		"HTTPS://GitHub.COM/Acme/Kits@main":          {Repo: "https://github.com/Acme/Kits", Ref: "main"},
		"https://gitserver.corp/a/b@release/1":       {Repo: "https://gitserver.corp/a/b", Ref: "release/1"},
		"https://10.0.0.5/a/b@main":                  {Repo: "https://10.0.0.5/a/b", Ref: "main"},
		"http://[::1]:8080/r.git//.@main":            {Repo: "http://[::1]:8080/r.git", Ref: "main"},
		"http://[::1]/r.git@feature/x":               {Repo: "http://[::1]/r.git", Ref: "feature/x"},
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "https://github.com/acme/kits", "http://github.com/acme/kits@main", "https://u:p@github.com/a/b@main",
		"https://github.com/a/b//../x@main", "https://github.com/a/b///abs@main", "ssh://git@github.com/a/b@main", "file:///tmp/r@main",
		// Beyond the brief: refs that could rewrite a refspec or option, odd dirs, and URL tricks.
		"https://github.com/a/b@", "https://github.com/a/b@-upload-pack=x", "https://github.com/a/b@a:refs/heads/x",
		"https://github.com/a/b@+main", "https://github.com/a/b@ma..in", "https://github.com/a/b@main.lock", "https://github.com/a/b@ma in",
		"https://github.com/a/b@main^", "https://github.com/a/b//@main", "https://github.com/a/b//web/../..@main", "https://github.com/a/b//web\\x@main",
		"https://github.com/a/b?x=1@main", "https://github.com/a/b#frag@main", "https:///a/b@main", "http://127.0.0.2/r@main",
		"https://u@github.com/a/b@main", "HTTP://example.com/r@main",
		"https://github.com/acme/../evil/r@main", "https://github.com/acme/%2e%2e/evil/r@main", "https://github.com/./a@main",
		// Pasted credentials, with and without a ref, must be refused and never echoed.
		"https://bob:s3cretTok@github.com/a/b", "https://bob:123456@github.com/a/b", "https://bob:s3cretTok@github.com/a/b@main",
		"https://bob:s3cretTok/x@github.com/a/b@main", "https://bob:123456/x@github.com/a/b", "https://s3cretTok@github.com/a/b@main",
		"https://github.com/a/b?access_token=s3cretTok@main", "https://github.com/a/b#s3cretTok@main", "https://github.com@main", "https://github.com/@main",
		"https://bob:1234/x@github.com/a/b", "https://bob:s3cretTok/x@localhost/a", "https://github.com:123456/a/b@main",
		// Round 2: '://' after the '@' (used to panic), dotless-host pastes, quoted tails.
		"x@y://z", "bob:pw@gitserver/a://b", "@://", "a@://", "https://bob:123456/x@gitserver/a/b", "https://bob:1234/x@gitserver/a/b",
		"https://github.com/a b?access_token=s3cretTok@main", `https://github.com/a"b?access_token=s3cretTok@main`,
		"https://github.com/a'b?access_token=s3cretTok@main", "https://github.com/a\tb?access_token=s3cretTok@main",
		"https://github.com/a\x00b?access_token=s3cretTok@main", "https://github.com/a`b?access_token=s3cretTok@main",
		// Round 3: empty ports and ported hosts before a ref with '/'.
		"https://bob:/s3cretTok@gitserver/a/b", "https://[::1]:/x@gitserver/a/b", "https://tok123456/x@gitserver/a/b", "https://s3cretTok:/@gitserver/a/b",
		"https://github.com:/a/b@main", "http://127.0.0.1:9/r@release/1", "https://git.example.com:8443/a/b@release/1",
		"https://john.doe:1234/s3cretTok@gitserver/a/b", "https://john.doe/s3cretTok@github.com/a/b",
		// Round 4: a secret in the dir of a pasted credential (dir validation used to echo it), single-label hosts
		// (a pasted "user:1234/secret" with no path, or a token as the host, reads as one), odd hosts.
		"https://bob:1/x//../s3cretTok@gitserver/a/b", "https://bob:1/x//s3cretTok/@gitserver/a/b", "https://bob:1/x//s3cretTok/./z@gitserver/a/b",
		"https://bob:1/x//s3cretTok\\z@gitserver/a/b", "HTTPS://bob:1/x//../s3cretTok@gitserver/a/b", "https://john.doe:1/x//../s3cretTok@gitserver/a/b",
		"https://bob:123456/s3cretTok@gitserver", "https://s3cretTok/x@main", "https://gitserver/a/b@main", "https://s3cretTok:123456/x@main",
		"https://github.com./a/b@main", "https://-github.com/a/b@main", "https://git_hub.com/a/b@main", "https://[fe80::1]/a/b@main", "https://[a.b]/x@main",
		"https://a.b%41/x@main", "https://github.com/a/b@ma~in", "https://github.com/a/b@" + strings.Repeat("s3cretTok", 30),
		// Format, non-printable and invalid UTF-8 characters.
		"https://github.com/a\u200bb@main", "https://github.com/a/b@ma\u202ein", "https://github.com/a/b@ma\u00adin", "https://github.com/a\xffb@main",
	} {
		_, err := Parse(bad)
		if err == nil {
			t.Errorf("Parse(%q) accepted", bad)
			continue
		}
		assertNoEcho(t, "Parse("+strconv.Quote(bad)+")", err, "s3cretTok", "123456")
	}
}

// assertNoEcho fails if err holds one of the secrets (case-insensitively:
// Parse lowercases hosts) or any digit: every Parse/check message is fixed,
// digit-free text, so a digit proves part of the input was echoed.
func assertNoEcho(t *testing.T, what string, err error, secrets ...string) {
	t.Helper()
	msg := strings.ToLower(err.Error())
	for _, secret := range secrets {
		if strings.Contains(msg, strings.ToLower(secret)) {
			t.Errorf("%s error echoes the secret: %v", what, err)
		}
	}
	if strings.ContainsAny(msg, "0123456789") {
		t.Errorf("%s error holds a digit (echoed input?): %v", what, err)
	}
}

func TestSourceStringRoundTrips(t *testing.T) {
	for _, in := range []string{"https://github.com/acme/kits@main", "https://github.com/acme/kits//web/dev@v1.2"} {
		s, err := Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if s.String() != in {
			t.Errorf("String() = %q, want %q", s.String(), in)
		}
	}
}

func TestCredsLongestPrefixAndUsername(t *testing.T) {
	c := Creds{{Prefix: "https://github.com/", Name: "gh", Value: "t1"}, {Prefix: "https://github.com/acme/", Name: "acme", Value: "t2"},
		{Prefix: "https://gitlab.example.com/", Name: "gl", Value: "t3"}, {Prefix: "https://git.example.org/", Name: "plain", Value: "bob:t4"}}
	if got, _ := c.For("https://github.com/acme/kits"); got.Name != "acme" {
		t.Fatalf("got %s", got.Name)
	}
	for repo, user := range map[string]string{"https://github.com/acme/kits": "x-access-token", "https://gitlab.example.com/g/k": "oauth2", "https://git.example.org/k": "bob"} {
		cr, _ := c.For(repo)
		if a := basicAuth(repo, cr); a.Username != user {
			t.Errorf("%s: user %q", repo, a.Username)
		}
	}
	if a := basicAuth("https://git.example.org/k", Cred{Value: "bob:t4"}); a.Password != "t4" {
		t.Errorf("user:token password = %q", a.Password)
	}
	if a := basicAuth("https://git.other.net/k", Cred{Value: "tok"}); a.Username != "git" || a.Password != "tok" {
		t.Errorf("plain token: %+v", a)
	}
}

func TestCredsMatchSchemeHostCaseInsensitively(t *testing.T) {
	c := Creds{{Prefix: "HTTPS://GitHub.com/Acme/", Name: "acme", Value: "t"}}
	if got, ok := c.For("https://github.com/Acme/kits"); !ok || got.Name != "acme" {
		t.Fatalf("scheme/host case: %v %v", got, ok)
	}
	if _, ok := c.For("https://github.com/acme/kits"); ok {
		t.Fatal("path matched case-insensitively")
	}
}

// clip (used by fetch errors on accepted repos) caps quoted text at
// maxQuoted bytes on a rune boundary.
func TestClip(t *testing.T) {
	short := strings.Repeat("a", maxQuoted)
	if clip(short) != short {
		t.Errorf("clip shortened %d bytes", len(short))
	}
	long := strings.Repeat("é", maxQuoted) // 2-byte runes: byte maxQuoted is mid-rune
	got := clip(long)
	if !strings.HasSuffix(got, "…") || len(got) > maxQuoted+len("…") || !utf8.ValidString(got) {
		t.Errorf("clip(long) = %q", got)
	}
}

// check (so Fetch/Resolve on a hand-built Source) never echoes the Source:
// every refusal is fixed text naming the component.
func TestCheckErrorsNeverEcho(t *testing.T) {
	const tok = "s3cretTok"
	for _, src := range []Source{
		{Repo: "https://github.com/a/b", Ref: tok + ".lock"}, {Repo: "https://github.com/a/b", Ref: tok + "^"}, {Repo: "https://github.com/a/b", Ref: "-" + tok},
		{Repo: "https://github.com/a/b", Ref: strings.Repeat(tok, 30)}, {Repo: "https://github.com/a/b", Dir: "../" + tok, Ref: "main"},
		{Repo: "https://github.com/a/b", Dir: tok + "/", Ref: "main"}, {Repo: "https://github.com/a/b", Dir: "/" + tok, Ref: "main"},
		{Repo: "http://example.com/" + tok, Ref: "main"}, {Repo: "https://" + tok + "/x", Ref: "main"}, {Repo: "https://bob:" + tok + "@github.com/a", Ref: "main"},
		{Repo: "https://github.com/a/b?access_token=" + tok, Ref: "main"}, {Repo: "https://github.com/a/b#" + tok, Ref: "main"},
		{Repo: "https://github.com:123456/a/b", Ref: "main"}, {Repo: "https://github.com:1/a/b", Ref: tok + "/x"}, {Repo: "ftp://github.com/" + tok, Ref: "main"},
		{Repo: "https://github.com/" + tok + "/../x", Ref: "main"}, {Repo: "https://github.com", Ref: tok}, {Repo: "https://github.com/a/b", Ref: "github.com/" + tok},
	} {
		err := src.check()
		if err == nil {
			t.Errorf("check(%+v) accepted", src)
			continue
		}
		assertNoEcho(t, fmt.Sprintf("check(%+v)", src), err, tok)
	}
}

// A prefix matches only at a path boundary, so a credential for one host or
// org is never sent to a lookalike.
func TestCredsPrefixBoundary(t *testing.T) {
	c := Creds{{Prefix: "https://git.example.com", Name: "host", Value: "t1"}, {Prefix: "https://github.com/acme", Name: "acme", Value: "t2"}}
	for _, repo := range []string{"https://git.example.com.evil.net/r", "https://git.example.com:8443/r", "https://github.com/acmeevil/r", "https://github.com/other/r"} {
		if got, ok := c.For(repo); ok {
			t.Errorf("For(%q) = %s", repo, got.Name)
		}
	}
	for repo, name := range map[string]string{"https://git.example.com/r": "host", "https://git.example.com": "host", "https://github.com/acme/r": "acme"} {
		if got, ok := c.For(repo); !ok || got.Name != name {
			t.Errorf("For(%q) = %s, %v", repo, got.Name, ok)
		}
	}
}

func TestCredValueNeverFormatted(t *testing.T) {
	c := Cred{Prefix: "https://github.com/", Name: "gh", Value: "s3cret-token"}
	wrapped := struct{ C Cred }{c}
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%v %+v %#v %s", c, c, c, c), fmt.Sprintf("%+v %#v", wrapped, wrapped), fmt.Sprint(Creds{c})} {
		if strings.Contains(s, "s3cret") {
			t.Errorf("value formatted: %s", s)
		}
	}
}

func TestRedactURL(t *testing.T) {
	in := `Get "https://bob:tok@git.example.com/r.git/info/refs?access_token=SEKRIT": x; also http://u@127.0.0.1:9/r#SEKRIT2 and https://ok.example/r`
	got := redactURL(in)
	if strings.Contains(got, "tok") || strings.Contains(got, "bob") || strings.Contains(got, "u@") || strings.Contains(got, "SEKRIT") {
		t.Fatalf("redactURL = %s", got)
	}
	if !strings.Contains(got, "https://git.example.com/r.git/info/refs") || !strings.Contains(got, "https://ok.example/r") {
		t.Fatalf("redactURL mangled: %s", got)
	}
}

// Hand-built sources with quotes or spaces are refused before quoting.
func TestCheckRefusesUnsafeChars(t *testing.T) {
	for _, src := range []Source{{Repo: "https://github.com/a b?access_token=s3cretTok", Ref: "main"}, {Repo: `https://github.com/a"?access_token=s3cretTok`, Ref: "main"},
		{Repo: "https://github.com/a/b", Ref: "ma'in"}, {Repo: "https://github.com/a/b", Dir: "w eb", Ref: "main"}} {
		err := src.check()
		if err == nil || strings.Contains(err.Error(), "s3cretTok") {
			t.Errorf("check(%+v) = %v", src, err)
		}
	}
	// A refusal after parsing quotes nothing either, not even the host.
	if _, err := checkRepoURL("http://example.com/a%20b?access_token=s3cretTok"); err == nil || strings.Contains(err.Error(), "s3cretTok") || strings.Contains(err.Error(), "example.com") {
		t.Errorf("checkRepoURL: %v", err)
	}
}

// FuzzParse holds Parse to three properties for operator-typed text that may
// be a pasted clone URL:
//
//  1. Parse never panics.
//  2. No error echoes the input: a planted token-like secret never appears
//     in an error (case-insensitively; Parse lowercases hosts), and — every
//     message being fixed, digit-free text — no error holds a digit.
//  3. A Source is accepted only when the text's head is a plain host (see
//     plainHead), so a secret can reach Repo, Dir or Ref only through text
//     that is a valid dotted-host repo URL as far as the text can tell: the
//     documented residual ("https://ab.cd/x@gitserver/a/b" with a dotted
//     token as the host). Every other head must be refused outright.
//
// The secret is planted after "://" and before the last '@' in the shapes a
// paste takes: user:secret, the secret alone, with a '/' or "//" inside the
// password, with and without an appended @ref, in both schemes.
func FuzzParse(f *testing.F) {
	for _, seed := range [][5]string{
		{"https://github.com/a/b@main", "s3cretTok1", "bob", "gitserver", "a/b"},
		{"x@y://z", "123456abc", "x-access-token", "github.com", "a/b"},
		{"bob:pw@gitserver/a://b", "pw0rd99", "oauth2", "host", "x"},
		{"https://u:p@h//d@", "tok123456", "", "h", "@@"},
		{"http://[::1]:8080/r.git//.@main", "abc123def", "git", "127.0.0.1:99", "x@" + strings.Repeat("a", 40)},
		{"https://bob:1/x//../s3cretTok@gitserver/a/b", "1/x//../s3cretTok", "bob", "gitserver", "a/b"},
		{"https://bob:12/s3cretTok@gitserver", "12/s3cretTok", "bob", "gitserver", ""},
		{"https://ab.cd/x@gitserver/a/b", "ab.cd/x1", "john.doe", "gitserver", "a/b"},
		{"https://a.b/c//d@e", "YWJj/ZGVm+Zz==1", "u.v", "localhost", "r.git"},
	} {
		f.Add(seed[0], seed[1], seed[2], seed[3], seed[4])
	}
	f.Fuzz(func(t *testing.T, raw, secret, user, host, path string) {
		try := func(in string, secrets ...string) {
			src, err := Parse(in) // property 1: must not panic
			if err != nil {
				assertNoEcho(t, "Parse("+strconv.Quote(in)+")", err, secrets...)
				return
			}
			if !plainHead(in) {
				t.Fatalf("Parse(%q) accepted a source whose head is not a plain host: %+v", in, src)
			}
		}
		try(raw)
		try(raw + "@" + host + "/" + path)
		if !tokenLike(secret) {
			return
		}
		rest := host + "/" + path
		for _, head := range []string{
			user + ":" + secret,               // user:password
			secret,                            // a token alone
			secret + "/" + path,               // a token holding a raw '/'
			user + "/" + secret,               // a user holding a raw '/'
			user + ":" + secret + "//" + path, // a password holding "//" (a dir separator)
			host + ":" + secret,               // a host-like user
		} {
			for _, in := range []string{
				"https://" + head + "@" + rest,
				"HTTPS://" + head + "@" + rest,
				"http://" + head + "@" + rest,
				"https://" + head + "@" + rest + "@main",
				"https://" + head + "//" + path + "@" + host,
				"https://" + host + "/" + path + "//" + head + "@main",
			} {
				try(in, secret)
			}
		}
	})
}

// plainHead reports whether the text right after the first "://" in s reads
// as a plain host: up to the first '/', ':' or '@' it is a dotted DNS name or
// a loopback address (a bracketed literal must be exactly "[::1]"). Such text
// is a valid repo URL as far as the text can tell, so a credential pasted
// there is the accepted residual; Parse must refuse every other head.
func plainHead(s string) bool {
	i := strings.Index(s, "://")
	if i < 0 {
		return false
	}
	rest := s[i+3:]
	if strings.HasPrefix(rest, "[") {
		end := strings.IndexByte(rest, ']')
		return end > 0 && rest[1:end] == "::1"
	}
	end := strings.IndexAny(rest, "/:@")
	if end < 0 {
		end = len(rest)
	}
	return plainHost(rest[:end])
}

// tokenLike accepts a realistic secret: at least 6 characters from the token
// alphabets (base64 with '/', '+', '='; URL-safe '-', '_'; JWT '.'), with a
// letter and a digit so it can never be a word of a fixed message (which
// hold no digit), and no '@' (a paste with '@' in the password is a
// different, always refused, shape: two '@').
func tokenLike(s string) bool {
	if len(s) < 6 {
		return false
	}
	var letter, digit bool
	for _, r := range s {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z':
			letter = true
		case '0' <= r && r <= '9':
			digit = true
		case strings.ContainsRune("/+=-_.", r):
		default:
			return false
		}
	}
	return letter && digit
}
