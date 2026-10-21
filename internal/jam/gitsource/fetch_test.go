package gitsource

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
)

func randomString(n int) string {
	b := make([]byte, n*3/4)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func TestFetchBranchTagCommitDir(t *testing.T) {
	url, c1, update := gittest.Serve(t, map[string]string{"web/kit.yml": "kind: studio\nprompt: one\n"})
	c2 := update(map[string]string{"web/kit.yml": "kind: studio\nprompt: two\n"})
	f := &Fetcher{MaxBytes: 50 << 20, Timeout: 60 * time.Second}
	for ref, want := range map[string]struct{ commit, prompt string }{"main": {c2, "two"}, "v1": {c1, "one"}, c1: {c1, "one"}} {
		var gotCommit, gotBody string
		err := f.Fetch(context.Background(), Source{Repo: url, Dir: "web", Ref: ref}, func(root, commit string) error {
			b, _ := os.ReadFile(filepath.Join(root, "web", "kit.yml"))
			gotCommit, gotBody = commit, string(b)
			return nil
		})
		if err != nil || gotCommit != want.commit || !strings.Contains(gotBody, want.prompt) {
			t.Errorf("ref %s: %v %s %q", ref, err, gotCommit, gotBody)
		}
	}
	if c, err := f.Resolve(context.Background(), Source{Repo: url, Ref: "main"}); err != nil || c != c2 {
		t.Fatalf("Resolve: %s %v", c, err)
	}
	if c, err := f.Resolve(context.Background(), Source{Repo: url, Ref: "v1"}); err != nil || c != c1 {
		t.Fatalf("Resolve annotated tag: %s %v", c, err)
	}
	if err := f.Fetch(context.Background(), Source{Repo: url, Ref: "nope"}, func(string, string) error { return nil }); err == nil || !strings.Contains(err.Error(), "ref nope not found") {
		t.Fatalf("missing ref: %v", err)
	}
	missing := strings.Repeat("b", 40)
	if err := f.Fetch(context.Background(), Source{Repo: url, Ref: missing}, func(string, string) error { return nil }); err == nil || !strings.Contains(err.Error(), "commit "+missing+" not found in "+url+" (or the server does not serve commits by id)") {
		t.Fatalf("missing commit: %v", err)
	}
	if _, err := f.Resolve(context.Background(), Source{Repo: url, Ref: "nope"}); err == nil || !strings.Contains(err.Error(), "ref nope not found") {
		t.Fatalf("Resolve missing ref: %v", err)
	}
}

func TestFetchAuth(t *testing.T) {
	url, _, _ := gittest.Serve(t, map[string]string{"kit.yml": "kind: studio\n"}, gittest.WithAuth("x-access-token", "good-token"))
	// A loopback host is neither github.com nor gitlab, so the username comes
	// from the user:token value; the server checks both halves.
	ok := &Fetcher{Creds: Creds{{Prefix: url, Name: "gh-kits", Value: "x-access-token:good-token"}}, MaxBytes: 50 << 20, Timeout: 30 * time.Second}
	if err := ok.Fetch(context.Background(), Source{Repo: url, Ref: "main"}, func(string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	bad := &Fetcher{Creds: Creds{{Prefix: url, Name: "gh-kits", Value: "WRONG-SECRET"}}, MaxBytes: 50 << 20, Timeout: 30 * time.Second}
	err := bad.Fetch(context.Background(), Source{Repo: url, Ref: "main"}, func(string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "check kits.git cred gh-kits") || strings.Contains(err.Error(), "WRONG-SECRET") {
		t.Fatalf("bad auth: %v", err)
	}
	anon := &Fetcher{MaxBytes: 50 << 20, Timeout: 30 * time.Second}
	err = anon.Fetch(context.Background(), Source{Repo: url, Ref: "main"}, func(string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "auth required for 127.0.0.1") || !strings.Contains(err.Error(), "add a kits.git entry") {
		t.Fatalf("anonymous: %v", err)
	}
	if _, err := bad.Resolve(context.Background(), Source{Repo: url, Ref: "main"}); err == nil || !strings.Contains(err.Error(), "check kits.git cred gh-kits") {
		t.Fatalf("Resolve bad auth: %v", err)
	}
}

// The token is sent only as an in-memory Basic auth header: no file in the
// checkout (including .git/config) and no error ever holds it.
func TestFetchCredentialNeverOnDisk(t *testing.T) {
	const token = "tok-7f3a9c-SECRET"
	url, c1, _ := gittest.Serve(t, map[string]string{"kit.yml": "kind: studio\n"}, gittest.WithAuth("x-access-token", token))
	f := &Fetcher{Creds: Creds{{Prefix: url, Name: "gh", Value: "x-access-token:" + token}}, MaxBytes: 50 << 20, Timeout: 30 * time.Second}
	for _, ref := range []string{"main", "v1", c1} {
		var files int
		err := f.Fetch(context.Background(), Source{Repo: url, Ref: ref}, func(root, _ string) error {
			return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				files++
				if strings.Contains(string(b), token) || strings.Contains(string(b), base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))) {
					t.Errorf("ref %s: token in %s", ref, p)
				}
				return nil
			})
		})
		if err != nil {
			t.Fatalf("ref %s: %v", ref, err)
		}
		if files == 0 {
			t.Fatalf("ref %s: walked no files", ref)
		}
	}
	for _, src := range []Source{{Repo: url, Ref: "nope"}, {Repo: url + "x", Ref: "main"}, {Repo: url, Ref: strings.Repeat("b", 40)}} {
		err := f.Fetch(context.Background(), src, func(string, string) error { return nil })
		if err == nil || strings.Contains(err.Error(), token) {
			t.Errorf("%s: %v", src, err)
		}
		if _, err := f.Resolve(context.Background(), src); err != nil && strings.Contains(err.Error(), token) {
			t.Errorf("Resolve %s: %v", src, err)
		}
	}
}

// A redirect to another host is refused before any request reaches it, so a
// credential can never follow it.
func TestFetchRefusesCrossHostRedirect(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("credential followed the redirect to %s", r.Host)
		}
		http.NotFound(w, r)
	}))
	defer other.Close()
	target := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+r.URL.RequestURI(), http.StatusFound)
	}))
	defer origin.Close()
	f := &Fetcher{Creds: Creds{{Prefix: origin.URL, Name: "c", Value: "tok"}}, MaxBytes: 1 << 20, Timeout: 10 * time.Second}
	err := f.Fetch(context.Background(), Source{Repo: origin.URL + "/r.git", Ref: "main"}, func(string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("cross-host redirect: %v", err)
	}
	if _, err := f.Resolve(context.Background(), Source{Repo: origin.URL + "/r.git", Ref: "main"}); err == nil {
		t.Fatal("Resolve followed a cross-host redirect")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("redirect target got %d requests", n)
	}
}

// Fetch re-checks the source itself, so a hand-built Source cannot reach a
// scheme or ref Parse would refuse.
func TestFetchRefusesBadSource(t *testing.T) {
	tmp := t.TempDir()
	f := &Fetcher{MaxBytes: 1 << 20, Timeout: time.Second, TempDir: tmp}
	for _, src := range []Source{{Repo: "http://example.com/r", Ref: "main"}, {Repo: "file:///tmp/r", Ref: "main"}, {Repo: "ssh://git@github.com/a/b", Ref: "main"},
		{Repo: "https://u:p@github.com/a/b", Ref: "main"}, {Repo: "https://github.com/a/b", Ref: "a:b"}, {Repo: "https://github.com/a/b", Dir: "../x", Ref: "main"},
		{Repo: "https://github.com/a/b?access_token=SEKRIT", Ref: "main"}, {Repo: "http://example.com/r?access_token=SEKRIT", Ref: "main"},
		{Repo: "https://bob:SEKRIT@github.com/a/b", Ref: "main"}, {Repo: "https://bob:SEKRIT", Ref: "main"}} {
		called := false
		err := f.Fetch(context.Background(), src, func(string, string) error { called = true; return nil })
		if err == nil || called {
			t.Errorf("Fetch(%+v) accepted", src)
		} else if strings.Contains(err.Error(), "SEKRIT") {
			t.Errorf("Fetch error echoes the secret: %v", err)
		}
		if _, err := f.Resolve(context.Background(), src); err == nil {
			t.Errorf("Resolve(%+v) accepted", src)
		} else if strings.Contains(err.Error(), "SEKRIT") {
			t.Errorf("Resolve error echoes the secret: %v", err)
		}
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Fatalf("temp dir used: %v", ents)
	}
}

func TestFetchLimits(t *testing.T) {
	big := map[string]string{"kit.yml": "kind: studio\n", "blob.bin": randomString(3 << 20)}
	url, _, _ := gittest.Serve(t, big)
	tmp := t.TempDir()
	f := &Fetcher{MaxBytes: 1 << 20, Timeout: 30 * time.Second, TempDir: tmp}
	err := f.Fetch(context.Background(), Source{Repo: url, Ref: "main"}, func(string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("size limit: %v", err)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Fatalf("temp dir left behind: %v", ents)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) }))
	defer slow.Close()
	f2 := &Fetcher{MaxBytes: 1 << 20, Timeout: 200 * time.Millisecond, TempDir: tmp}
	if err := f2.Fetch(context.Background(), Source{Repo: slow.URL + "/r.git", Ref: "main"}, func(string, string) error { return nil }); err == nil {
		t.Fatal("timeout not enforced")
	} else if !strings.Contains(err.Error(), "timed out after the Fetcher's 200ms limit") {
		t.Fatalf("Fetcher timeout: %v", err)
	}
	// The caller's own deadline is reported as the caller's, not the Fetcher's.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	f3 := &Fetcher{MaxBytes: 1 << 20, Timeout: 30 * time.Second, TempDir: tmp}
	if err := f3.Fetch(ctx, Source{Repo: slow.URL + "/r.git", Ref: "main"}, func(string, string) error { return nil }); err == nil ||
		!strings.Contains(err.Error(), "stopped by the caller") || strings.Contains(err.Error(), "Fetcher's") {
		t.Fatalf("caller deadline: %v", err)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Fatalf("temp dir left behind: %v", ents)
	}
}

// A small pack can expand into a huge work tree: the checkout is capped in
// bytes written, on the branch and the commit paths, and cleaned up.
func TestFetchCheckoutCap(t *testing.T) {
	url, c1, _ := gittest.Serve(t, map[string]string{"kit.yml": "kind: studio\n", "zeros.bin": strings.Repeat("\x00", 4<<20)})
	tmp := t.TempDir()
	f := &Fetcher{MaxBytes: 1 << 20, MaxCheckoutBytes: 1 << 20, Timeout: 30 * time.Second, TempDir: tmp}
	for _, ref := range []string{"main", c1} {
		err := f.Fetch(context.Background(), Source{Repo: url, Ref: ref}, func(string, string) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "checkout") || !strings.Contains(err.Error(), "exceeded the 1048576-byte limit") {
			t.Fatalf("ref %s: %v", ref, err)
		}
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Fatalf("temp dir left behind: %v", ents)
	}
	roomy := &Fetcher{MaxBytes: 1 << 20, MaxCheckoutBytes: 8 << 20, Timeout: 30 * time.Second, TempDir: tmp}
	if err := roomy.Fetch(context.Background(), Source{Repo: url, Ref: "main"}, func(string, string) error { return nil }); err != nil {
		t.Fatalf("under the cap: %v", err)
	}
}

// A panic inside the checkout (e.g. in go-git) still removes the temp dir.
func TestFetchPanicRemovesTempDir(t *testing.T) {
	url, _, _ := gittest.Serve(t, map[string]string{"kit.yml": "kind: studio\n"})
	tmp := t.TempDir()
	testHookCheckout = func(string) { panic("boom") }
	defer func() { testHookCheckout = nil }()
	f := &Fetcher{Timeout: 30 * time.Second, TempDir: tmp}
	func() {
		defer func() {
			if r := recover(); r != "boom" {
				t.Fatalf("recovered %v", r)
			}
		}()
		_ = f.Fetch(context.Background(), Source{Repo: url, Ref: "main"}, func(string, string) error { return nil })
	}()
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Fatalf("temp dir left behind: %v", ents)
	}
	// The repo lock was released too.
	testHookCheckout = nil
	if err := f.Fetch(context.Background(), Source{Repo: url, Ref: "main"}, func(string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// Same-host redirects are followed at most 10 times.
func TestFetchRedirectCap(t *testing.T) {
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		http.Redirect(w, r, srv.URL+"/loop"+strconv.Itoa(int(n))+"/info/refs?service=git-upload-pack", http.StatusFound)
	}))
	defer srv.Close()
	f := &Fetcher{Timeout: 10 * time.Second}
	err := f.Fetch(context.Background(), Source{Repo: srv.URL + "/r.git", Ref: "main"}, func(string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect loop: %v", err)
	}
	if n := hits.Load(); n > 11 {
		t.Fatalf("followed %d hops", n-1)
	}
}

// The checkout dir is private, under TempDir, and gone once fn returns —
// also when fn fails.
func TestFetchTempDir(t *testing.T) {
	url, _, _ := gittest.Serve(t, map[string]string{"kit.yml": "kind: studio\n"})
	tmp := t.TempDir()
	f := &Fetcher{MaxBytes: 50 << 20, Timeout: 30 * time.Second, TempDir: tmp}
	var seen string
	err := f.Fetch(context.Background(), Source{Repo: url, Ref: "main"}, func(root, _ string) error {
		seen = root
		st, err := os.Stat(root)
		if err != nil {
			return err
		}
		if st.Mode().Perm() != 0o700 || filepath.Dir(root) != tmp {
			t.Errorf("checkout %s mode %v", root, st.Mode().Perm())
		}
		return os.ErrInvalid
	})
	if err != os.ErrInvalid {
		t.Fatalf("fn error not returned as is: %v", err)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatalf("checkout %s left behind: %v", seen, err)
	}
}

// Each fetch has its own byte budget: a small-limit fetch failing does not
// limit a concurrent one, and vice versa.
func TestFetchLimitIsPerFetch(t *testing.T) {
	url, _, _ := gittest.Serve(t, map[string]string{"kit.yml": "kind: studio\n", "blob.bin": randomString(2 << 20)})
	small := &Fetcher{MaxBytes: 256 << 10, Timeout: 30 * time.Second}
	large := &Fetcher{MaxBytes: 50 << 20, Timeout: 30 * time.Second}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		f := large
		if i%2 == 0 {
			f = small
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f.Fetch(context.Background(), Source{Repo: url, Ref: "main"}, func(string, string) error { return nil })
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if i%2 == 0 && (err == nil || !strings.Contains(err.Error(), "exceeded")) {
			t.Errorf("small %d: %v", i, err)
		}
		if i%2 == 1 && err != nil {
			t.Errorf("large %d: %v", i, err)
		}
	}
}

// The clone's .git is Jam's, not the kit's: fn must not see it (a
// context-dir of "." would pack it), for a branch and for a commit id.
func TestFetchRootHasNoGitDir(t *testing.T) {
	url, c1, _ := gittest.Serve(t, map[string]string{"kit.yml": "kind: studio\n"})
	f := &Fetcher{Timeout: 30 * time.Second}
	for _, ref := range []string{"main", c1} {
		err := f.Fetch(context.Background(), Source{Repo: url, Ref: ref}, func(root, _ string) error {
			if _, err := os.Lstat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
				t.Errorf("ref %s: .git present in the checkout root (err %v)", ref, err)
			}
			if _, err := os.Stat(filepath.Join(root, "kit.yml")); err != nil {
				t.Errorf("ref %s: work tree lost: %v", ref, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
