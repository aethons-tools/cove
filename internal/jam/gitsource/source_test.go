package gitsource

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseSource(t *testing.T) {
	for in, want := range map[string]Source{
		"https://github.com/acme/kits@main":                   {Repo: "https://github.com/acme/kits", Ref: "main"},
		"https://github.com/acme/kits//web/dev@v1.2":          {Repo: "https://github.com/acme/kits", Dir: "web/dev", Ref: "v1.2"},
		"http://127.0.0.1:9/r.git@" + strings.Repeat("a", 40): {Repo: "http://127.0.0.1:9/r.git", Ref: strings.Repeat("a", 40)},
		"http://localhost/r.git@release/2026":                 {Repo: "http://localhost/r.git", Ref: "release/2026"},
		"https://github.com/a/b@feature/v1.2":                 {Repo: "https://github.com/a/b", Ref: "feature/v1.2"},
		"https://git.example.com:8443/a/b@main":               {Repo: "https://git.example.com:8443/a/b", Ref: "main"},
		"http://[::1]:8080/r.git//.@main":                     {Repo: "http://[::1]:8080/r.git", Ref: "main"},
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
	} {
		_, err := Parse(bad)
		if err == nil {
			t.Errorf("Parse(%q) accepted", bad)
			continue
		}
		for _, secret := range []string{"s3cretTok", "123456"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("Parse(%q) error echoes the secret: %v", bad, err)
			}
		}
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
	// A refusal after parsing quotes only scheme, host and escaped path.
	if err := checkRepoURL("http://example.com/a%20b?access_token=s3cretTok"); err == nil || strings.Contains(err.Error(), "s3cretTok") || !strings.Contains(err.Error(), "http://example.com/a%20b") {
		t.Errorf("checkRepoURL: %v", err)
	}
}

// Parse never panics, and an error never quotes what was planted after a
// ':' in the authority (a pasted password).
func FuzzParse(f *testing.F) {
	for _, seed := range [][3]string{
		{"https://github.com/a/b@main", "s3cretTok", "github.com/a/b@main"},
		{"x@y://z", "123456", "gitserver/a/b"},
		{"bob:pw@gitserver/a://b", "pw/x", "github.com/a b?x=1@main"},
		{"https://u:p@h//d@", "p", "@@"},
		{"http://[::1]:8080/r.git//.@main", "tok:en", "host:99/x@" + strings.Repeat("a", 40)},
	} {
		f.Add(seed[0], seed[1], seed[2])
	}
	f.Fuzz(func(t *testing.T, raw, secret, rest string) {
		_, _ = Parse(raw)
		_, _ = Parse(raw + "@" + rest)
		// The planted secret lives in the authority: no '/' (that would end
		// it), and long enough not to match fixed message text by chance.
		if len(secret) < 6 || strings.ContainsAny(secret, "/") {
			return
		}
		for _, in := range []string{"https://bob:" + secret + "@" + rest, "https://bob:" + secret + "@" + rest + "@main", "http://x:" + secret + "@127.0.0.1/" + rest} {
			if _, err := Parse(in); err != nil && strings.Contains(err.Error(), secret) {
				t.Fatalf("Parse(%q) error quotes the secret: %v", in, err)
			}
		}
	})
}
