package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// draftCLI is a Jam holding kit app v1, a runner for `at-jam kit draft …`
// against it (args[0] is the verb), the store and the admin URL.
func draftCLI(t *testing.T) (func(args ...string) (int, string, string), *jam.MemStore, string) {
	t.Helper()
	store := jam.NewMemStore()
	if _, err := store.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	t.Cleanup(ts.Close)
	return func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		argv := append([]string{"kit", "draft", args[0], "--admin-url", ts.URL}, args[1:]...)
		code := run(argv, func(string) string { return "" }, &out, &errb)
		return code, out.String(), errb.String()
	}, store, ts.URL
}

func TestKitDraftVerbs(t *testing.T) {
	kit, _, _ := draftCLI(t)
	code, out, errs := kit("open", "app")
	if code != 0 || !strings.HasPrefix(out, `opened draft app from v1 (etag "`) {
		t.Fatalf("open: %d %q %q", code, out, errs)
	}
	script := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(script, []byte("echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errs = kit("put", "app", "run.sh", script, "--mode", "755"); code != 0 || !strings.HasPrefix(out, `saved run.sh to draft app (etag "`) {
		t.Fatalf("put: %d %q %q", code, out, errs)
	}
	if code, out, _ = kit("get", "app", "run.sh"); code != 0 || out != "echo hi\n" {
		t.Fatalf("get: %d %q", code, out)
	}
	dst := filepath.Join(t.TempDir(), "got.sh")
	if code, _, _ = kit("get", "app", "run.sh", "-o", dst); code != 0 {
		t.Fatalf("get -o: %d", code)
	}
	if b, _ := os.ReadFile(dst); string(b) != "echo hi\n" {
		t.Fatalf("get -o wrote %q", b)
	}
	code, out, _ = kit("status", "app")
	for _, want := range []string{"draft app: base v1, current v1\n", "etag \"", "files (2):", "0755", "run.sh", "changes vs v1:\n  added    run.sh\n", "problems: none\n"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("status lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = kit("status", "app", "--json")
	var st jam.KitDraftStatus
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil || len(st.Changes) != 1 || st.Base != 1 {
		t.Fatalf("status --json: %d %s", code, out)
	}
	// --etag writes against the given etag: a stale one fails, nothing written (Review Focus 1).
	if code, _, errs = kit("put", "app", "x.txt", script, "--etag", strings.Repeat("0", 64)); code != 1 || !strings.Contains(errs, "draft app changed since you loaded it (reload)") {
		t.Fatalf("stale --etag: %d %q", code, errs)
	}
	if code, out, _ = kit("put", "app", "y.txt", script, "--etag", st.ETag); code != 0 {
		t.Fatalf("put --etag: %d %q", code, out)
	}
	if code, out, _ = kit("rm", "app", "y.txt"); code != 0 || !strings.HasPrefix(out, "removed y.txt from draft app (etag ") {
		t.Fatalf("rm: %d %q", code, out)
	}
	if code, out, _ = kit("publish", "app"); code != 0 || out != "published draft app as v2\n" {
		t.Fatalf("publish: %d %q", code, out)
	}
	if code, _, errs = kit("status", "app"); code != 1 || !strings.Contains(errs, "kit app has no draft") {
		t.Fatalf("status after publish: %d %q", code, errs)
	}
	// A draft of current with no edit has nothing to publish.
	if code, _, errs = kit("open", "app"); code != 0 {
		t.Fatal(errs)
	}
	if code, _, errs = kit("publish", "app"); code != 1 || !strings.Contains(errs, "draft app has no changes") {
		t.Fatalf("publish unchanged: %d %q", code, errs)
	}
	if code, out, _ = kit("discard", "app"); code != 0 || out != "discarded draft app\n" {
		t.Fatalf("discard: %d %q", code, out)
	}
	if code, out, _ = kit("open", "app", "--from", "v1"); code != 0 || !strings.HasPrefix(out, "opened draft app from v1 (etag ") {
		t.Fatalf("open --from v1: %d %q", code, out)
	}
	if code, out, _ = kit("discard", "app"); code != 0 {
		t.Fatalf("discard: %d %q", code, out)
	}
}

func TestKitVersionsShowsDraftOrigin(t *testing.T) {
	kit, _, url := draftCLI(t)
	f := filepath.Join(t.TempDir(), "n.md")
	if err := os.WriteFile(f, []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"open", "app"}, {"put", "app", "n.md", f}, {"publish", "app"}} {
		if code, _, errs := kit(args...); code != 0 {
			t.Fatalf("%v: %s", args, errs)
		}
	}
	var out, errb bytes.Buffer
	if code := run([]string{"kit", "versions", "--admin-url", url, "app"}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[2], "v2") || !strings.Contains(lines[2], "draft") || strings.Contains(lines[1], "draft") {
		t.Fatalf("versions:\n%s", out.String())
	}
}

func TestKitDraftStatusBaseSuperseded(t *testing.T) {
	kit, store, _ := draftCLI(t)
	if code, _, errs := kit("open", "app"); code != 0 {
		t.Fatal(errs)
	}
	if _, err := store.PushKit("app", "kind: studio\nprompt: two\n"); err != nil {
		t.Fatal(err)
	}
	code, out, _ := kit("status", "app")
	if code != 0 || !strings.Contains(out, "draft app: base v1, current v2\n") || !strings.Contains(out, "v2 landed after the draft was opened; publishing replaces it with the draft (nothing is merged)") {
		t.Fatalf("status (Review Focus 2):\n%s", out)
	}
}

func TestKitDraftArgs(t *testing.T) {
	kit, _, _ := draftCLI(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"get", "app"}, "at-jam kit draft get: expected <kit> <path> [-o file]"},
		{[]string{"put", "app", "x"}, "at-jam kit draft put: expected <kit> <path> <file|-> [--mode 755]"},
		{[]string{"open", "app", "--from", "x"}, "at-jam kit draft open: --from must be a version (vN)"},
		{[]string{"put", "app", "x", "f", "--mode", "700"}, "at-jam kit draft put: --mode must be 755 or 644"},
		{[]string{"get", "app", "a\nb"}, "at-jam kit draft get: a file has an unsafe path"},
	} {
		if code, _, errs := kit(tc.args...); code != 2 || !strings.Contains(errs, tc.want) {
			t.Fatalf("%q: %d %q, want %q", tc.args, code, errs, tc.want)
		}
	}
	var out, errb bytes.Buffer
	if code := run([]string{"kit", "draft"}, func(string) string { return "" }, &out, &errb); code != 2 || !strings.Contains(errb.String(), "expected open|status|get|put|rm|discard|publish") {
		t.Fatalf("no verb: %d %q", code, errb.String())
	}
}

// get -o writes the file with the entry's mode and replaces an existing
// symlink at the destination instead of writing through it.
func TestKitDraftGetOutFile(t *testing.T) {
	kit, _, _ := draftCLI(t)
	src := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(src, []byte("echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"open", "app"}, {"put", "app", "run.sh", src, "--mode", "0755"}} {
		if code, _, errs := kit(args...); code != 0 {
			t.Fatalf("%v: %s", args, errs)
		}
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "got.sh")
	if code, out, errs := kit("get", "app", "run.sh", "-o", dst); code != 0 || out != "" {
		t.Fatalf("get -o: %d %q %q", code, out, errs)
	}
	if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode: %v %v", fi, err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := kit("get", "app", "kit.yml", "-o", link); code != 0 {
		t.Fatalf("get -o link: %d %q", code, errs)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep\n" {
		t.Fatalf("wrote through the symlink: %q", b)
	}
	if fi, err := os.Lstat(link); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 {
		t.Fatalf("link not replaced by a 0644 file: %v %v", fi, err)
	}
}

// put - reads the file from stdin.
func TestKitDraftPutStdin(t *testing.T) {
	kit, _, _ := draftCLI(t)
	if code, _, errs := kit("open", "app"); code != 0 {
		t.Fatal(errs)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	go func() { _, _ = w.Write([]byte("from stdin\n")); _ = w.Close() }()
	if code, _, errs := kit("put", "app", "in.txt", "-"); code != 0 {
		t.Fatalf("put -: %d %q", code, errs)
	}
	if code, out, _ := kit("get", "app", "in.txt"); code != 0 || out != "from stdin\n" {
		t.Fatalf("get: %d %q", code, out)
	}
}
