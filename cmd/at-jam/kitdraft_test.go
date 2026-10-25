package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
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
	kit, store, _ := draftCLI(t)
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
	if code, _, errs = kit("get", "app", "x.txt"); code != 1 || !strings.Contains(errs, "draft app has no file x.txt") {
		t.Fatalf("stale --etag wrote x.txt: %d %q", code, errs)
	}
	if code, out, _ = kit("put", "app", "y.txt", script, "--etag", st.ETag); code != 0 {
		t.Fatalf("put --etag: %d %q", code, out)
	}
	if code, out, _ = kit("rm", "app", "y.txt"); code != 0 || !strings.HasPrefix(out, "removed y.txt from draft app (etag ") {
		t.Fatalf("rm: %d %q", code, out)
	}
	pre, _ := store.GetKitDraft("app")
	if code, out, _ = kit("publish", "app"); code != 0 || out != "published draft app as v2 (tree "+pre.Tree[:12]+")\n" {
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
	if code := run([]string{"kit", "draft"}, func(string) string { return "" }, &out, &errb); code != 2 || !strings.Contains(errb.String(), "expected open|status|get|put|rm|discard|publish|sync") {
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

// Strings a server answers with (etag, mode, kind, warning, origin) reach
// the terminal escaped: no raw ESC or U+202E.
func TestKitDraftEscapesServerStrings(t *testing.T) {
	const bad = "\x1b[2J\u202e"
	st := jam.KitDraftStatus{Kit: "app", Base: 1, Current: 1, ETag: bad, Actor: bad,
		Entries:  []jam.DraftEntry{{Path: "a", Mode: bad, Size: 1}},
		Changes:  []jam.DraftChange{{Path: "a", Kind: bad}},
		Problems: []string{bad}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var v any
		switch {
		case r.URL.Path == "/admin/kits/app/history":
			v = []jam.KitVersionInfo{{Version: 1, Origin: bad, Current: true}}
		case r.URL.Path == "/admin/kits/app/draft/publish":
			v = jam.KitDraftPublish{Kit: "app", Version: 2, Pinned: []string{bad}, Drift: true, Warning: bad}
		case r.Method == "GET" || r.Method == "POST":
			v = st
		default:
			v = jam.KitDraftSave{Kit: "app", ETag: bad, Changed: st.Changes, Problems: st.Problems}
		}
		_ = json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(ts.Close)
	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"kit", "draft", "open", "app"}, {"kit", "draft", "status", "app"},
		{"kit", "draft", "put", "app", "a", f, "--etag", "e"}, {"kit", "draft", "rm", "app", "a", "--etag", "e"},
		{"kit", "draft", "publish", "app", "--etag", "e"}, {"kit", "versions", "app"},
	} {
		var out, errb bytes.Buffer
		code := run(append(argv, "--admin-url", ts.URL), func(string) string { return "" }, &out, &errb)
		all := out.String() + errb.String()
		if code != 0 || strings.ContainsAny(all, "\x1b\u202e") {
			t.Fatalf("%v: %d %q", argv, code, all)
		}
	}
}

// A flag the verb does not take is a usage error; --help is not.
func TestKitDraftFlagsPerVerb(t *testing.T) {
	kit, _, _ := draftCLI(t)
	for _, args := range [][]string{
		{"open", "app", "--mode", "755"}, {"status", "app", "--pin"}, {"put", "app", "a", "f", "--from", "v1"},
		{"status", "app", "-o", "x"}, {"open", "app", "--json"}, {"status", "app", "--etag", "e"},
	} {
		if code, _, errs := kit(args...); code != 2 || !strings.Contains(errs, "flag provided but not defined") {
			t.Fatalf("%q: %d %q", args, code, errs)
		}
	}
	for _, argv := range [][]string{{"kit", "draft", "--help"}, {"kit", "draft", "-h"}, {"kit", "draft", "put", "-h"}} {
		var out, errb bytes.Buffer
		if code := run(argv, func(string) string { return "" }, &out, &errb); code != 0 || !strings.Contains(out.String(), "draft") {
			t.Fatalf("%q: %d %q %q", argv, code, out.String(), errb.String())
		}
	}
}

func TestKitDraftSync(t *testing.T) {
	kit, store, _ := draftCLI(t)
	dir := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{"kit.yml": "kind: studio\nprompt: synced\n", "bin/run": "echo\n", ".dockerignore": "*.log\n", "debug.log": "x"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "bin/run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := kit("sync", "app", dir); code != 1 || !strings.Contains(errs, "kit app has no draft") {
		t.Fatalf("sync without a draft: %d %q", code, errs)
	}
	if code, _, errs := kit("open", "app"); code != 0 {
		t.Fatal(errs)
	}
	code, out, errs := kit("sync", "app", dir)
	want := "  added    .dockerignore\n  added    bin/run\n  modified kit.yml\n"
	if code != 0 || !strings.HasPrefix(out, "synced "+dir+" to draft app (etag \"") || !strings.HasSuffix(out, want) {
		t.Fatalf("sync: %d %q %q", code, out, errs)
	}
	d, _ := store.GetKitDraft("app")
	tree, _ := store.KitTree(d.Tree)
	if _, ok := tree.Lookup("debug.log"); ok {
		t.Fatal(".dockerignore must filter the sync")
	}
	if e, _ := tree.Lookup("bin/run"); e.Mode != 0o755 {
		t.Fatal("the exec bit was lost")
	}
	if code, out, _ = kit("sync", "app", dir); code != 0 || !strings.HasPrefix(out, "draft app unchanged (etag ") {
		t.Fatalf("second sync: %d %q", code, out)
	}
	// A symlink in the directory is refused client-side; nothing is sent.
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "leak")); err != nil {
		t.Fatal(err)
	}
	if code, _, errs = kit("sync", "app", dir); code != 1 || !strings.Contains(errs, "leak: symlinks are not allowed") {
		t.Fatalf("symlink: %d %q", code, errs)
	}
	if again, _ := store.GetKitDraft("app"); again.Tree != d.Tree {
		t.Fatal("a refused sync changed the draft")
	}
	if code, _, errs = kit("sync", "app", filepath.Join(dir, "nope")); code != 1 || !strings.Contains(errs, "no such directory") {
		t.Fatalf("missing dir: %d %q", code, errs)
	}
	// --etag with the current etag writes; a sync that drops files lists
	// them removed, and the problems it leaves are printed.
	if err := os.Remove(filepath.Join(dir, "leak")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "bin/run")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kit.yml"), []byte("kind: nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs = kit("sync", "app", dir, "--etag", strings.Trim(draftETagOf(store), `"`))
	if code != 0 || !strings.Contains(out, "  removed  bin/run\n") || !strings.Contains(out, "  modified kit.yml\n") || !strings.Contains(out, "problems:\n  - studio kit: kind must be \"studio\", got \"nope\"\n") {
		t.Fatalf("sync --etag: %d %q %q", code, out, errs)
	}
	// A stale --etag is refused (Review Focus 1).
	if code, _, errs = kit("sync", "app", t.TempDir(), "--etag", strings.Repeat("0", 64)); code != 1 || !strings.Contains(errs, "draft app changed since you loaded it (reload)") {
		t.Fatalf("stale sync: %d %q", code, errs)
	}
}

// draftETagOf is kit app's open draft's ETag.
func draftETagOf(store *jam.MemStore) string {
	d, _ := store.GetKitDraft("app")
	return jam.DraftETag(d.Tree)
}

// A warning without drift (the draft could not be closed) is printed too.
func TestKitDraftPublishWarns(t *testing.T) {
	const warn = "published v2; the draft could not be closed \u2014 discard it"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jam.KitDraftPublish{Kit: "app", Version: 2, Tree: strings.Repeat("ab", 32), Warning: warn})
	}))
	t.Cleanup(ts.Close)
	var out, errb bytes.Buffer
	code := run([]string{"kit", "draft", "publish", "app", "--etag", "e", "--admin-url", ts.URL}, func(string) string { return "" }, &out, &errb)
	if code != 0 || out.String() != "published draft app as v2 (tree abababababab)\n" || errb.String() != "warning: "+warn+"\n" {
		t.Fatalf("publish: %d %q %q", code, out.String(), errb.String())
	}
}
