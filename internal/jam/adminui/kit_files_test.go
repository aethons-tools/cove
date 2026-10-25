package adminui_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// section is the card with id in body ("" when absent).
func section(body, id string) string {
	i := strings.Index(body, `id="`+id+`"`)
	if i < 0 {
		return ""
	}
	j := strings.Index(body[i:], "</section>")
	if j < 0 {
		return body[i:]
	}
	return body[i : i+j]
}

var bigText = strings.Repeat("line of text\n", 6000) // > 64 KiB

// packCtx is a base64 tar.gz of one file: a packed base.context, as a
// pre-tree Jam stored it.
func packCtx(t *testing.T, name, body string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// seedTreeKit pushes kit "t": v1 and v2 differ in their files and prompt.
func seedTreeKit(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	df := "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n"
	for _, kv := range [][]string{
		{"kit.yml", "kind: studio\nprompt: one\n", "Dockerfile", df, "keep.txt", "same\n", "mod.txt", "a\n", "gone.txt", "bye\n", "run.sh", "echo\n", "blob.bin", "\x00\x01", "big.txt", bigText},
		{"kit.yml", "kind: studio\nprompt: two\n", "Dockerfile", df, "keep.txt", "same\n", "mod.txt", "a\nbb\n", "new.txt", "hello\n", "run.sh*", "echo\n", "blob.bin", "\x00\x02", "big.txt", bigText + "more\n", "html.txt", "<b>hi</b>\n"},
	} {
		if _, _, err := jam.PushKitFiles(store, "t", kittreetest.Files(kv...)); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestKitPageListsFiles(t *testing.T) {
	body := get(t, kitsHandler(seedTreeKit(t)), "/app/admin/kits/t?v=2").Body.String()
	files := section(body, "ctx-files")
	for _, want := range []string{"<h2>Files</h2>", `file=run.sh"`, "0755", `file=kit.yml"`, `file=Dockerfile"`, "kit.yml included"} {
		if !strings.Contains(files, want) {
			t.Errorf("files card missing %q:\n%s", want, files)
		}
	}
	yaml := section(body, "kit-yaml")
	if !strings.Contains(body, "prompt: two") || strings.Contains(yaml, "H4sI") {
		t.Errorf("kit.yml card:\n%s", yaml)
	}
	if !strings.Contains(body, "kit.yml, as stored") {
		t.Errorf("YAML card sub missing")
	}
	if !strings.Contains(body, "Dockerfile") || !strings.Contains(body, "Base") {
		t.Errorf("base label missing")
	}
}

func TestKitPageShowsTreeFile(t *testing.T) {
	h := kitsHandler(seedTreeKit(t))
	f := section(get(t, h, "/app/admin/kits/t?v=2&file=html.txt").Body.String(), "ctx-file")
	if !strings.Contains(f, "&lt;b&gt;hi&lt;/b&gt;") || strings.Contains(f, "<b>hi") {
		t.Errorf("file content not escaped:\n%s", f)
	}
	f = section(get(t, h, "/app/admin/kits/t?v=2&file=blob.bin").Body.String(), "ctx-file")
	if !strings.Contains(f, "binary") {
		t.Errorf("binary file:\n%s", f)
	}
	f = section(get(t, h, "/app/admin/kits/t?v=2&file=big.txt").Body.String(), "ctx-file")
	if !strings.Contains(f, "too large") || strings.Contains(f, "line of text") {
		t.Errorf("big file:\n%.400s", f)
	}
}

func TestKitPageDiffsFiles(t *testing.T) {
	h := kitsHandler(seedTreeKit(t))
	body := get(t, h, "/app/admin/kits/t?v=2&diff=1").Body.String()
	if d := section(body, "kit-diff"); !strings.Contains(d, `<span class="d-del">- prompt: one</span>`) || !strings.Contains(d, `<span class="d-add">+ prompt: two</span>`) {
		t.Errorf("kit.yml diff:\n%s", d)
	}
	tbl := section(body, "ctx-diff")
	for _, want := range []string{
		"<h2>Files v1 → v2</h2>",
		`data-change="added"`, `file=new.txt"`,
		`data-change="removed"`, `file=gone.txt"`,
		`data-change="modified"`, `file=mod.txt"`, "2 B → 5 B",
		`data-change="mode"`, `file=run.sh"`, "0644 → 0755",
		"2 unchanged",
	} {
		if !strings.Contains(tbl, want) {
			t.Errorf("files diff missing %q:\n%s", want, tbl)
		}
	}
	f := section(get(t, h, "/app/admin/kits/t?v=2&diff=1&file=mod.txt").Body.String(), "ctx-file")
	if !strings.Contains(f, `<span class="d-add">+ bb</span>`) {
		t.Errorf("file diff:\n%s", f)
	}
	f = section(get(t, h, "/app/admin/kits/t?v=2&diff=1&file=blob.bin").Body.String(), "ctx-file")
	if !strings.Contains(f, "binary") {
		t.Errorf("binary file:\n%s", f)
	}
}

// A tree version's kit.yml is shown as stored, its format characters made
// visible in the YAML card and the version diff.
func TestKitPageKitYMLEscaped(t *testing.T) {
	store := newStore(t)
	for _, p := range []string{"one", "t\u202ewo"} {
		if _, _, err := jam.PushKitFiles(store, "e", kittreetest.Files("kit.yml", "kind: studio\nprompt: "+p+"\n")); err != nil {
			t.Fatal(err)
		}
	}
	body := get(t, kitsHandler(store), "/app/admin/kits/e?v=2&diff=1").Body.String()
	for _, id := range []string{"kit-yaml", "kit-diff"} {
		if s := section(body, id); !strings.Contains(s, `t\u202ewo`) || strings.Contains(s, "\u202e") {
			t.Errorf("%s not escaped:\n%s", id, s)
		}
	}
}

// A version the migration could not turn into a tree shows its own error.
func TestKitPageBrokenVersion(t *testing.T) {
	store := newStore(t)
	if err := store.PutKitDocs([]jam.Kit{{Name: "old", Current: 1, Broken: map[int]string{1: "its base.context is not a readable tar.gz"}}}, jam.KitObjects{}); err != nil {
		t.Fatal(err)
	}
	body := get(t, kitsHandler(store), "/app/admin/kits/old").Body.String()
	if !strings.Contains(body, "could not be migrated to a tree: its base.context is not a readable tar.gz") || strings.Contains(body, "EOF") {
		t.Errorf("broken version page:\n%s", body)
	}
}

// preTexts is the text of every <pre> in body.
func preTexts(body string) string {
	var out []string
	for _, part := range strings.Split(body, "<pre")[1:] {
		out = append(out, part[:strings.Index(part, "</pre>")])
	}
	return strings.Join(out, "\n")
}

// A stored kit.yml that does not parse (kept verbatim by the migration) may
// still hold a packed base.context: the page shows a fixed placeholder, not
// the base64, in the raw card, the diff, the push form and ?file=kit.yml.
func TestKitPageUnparsableTreeHidesContextBlob(t *testing.T) {
	store := newStore(t)
	b64 := packCtx(t, "Dockerfile", "FROM x\n")
	if _, err := store.PushKit("bad", "kind: studio\nbogus: 1\nbase:\n  context: "+b64+"\n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jam.PushKitFiles(store, "bad", kittreetest.Files("kit.yml", "kind: studio\n")); err != nil {
		t.Fatal(err)
	}
	h := kitsHandler(store)
	for _, path := range []string{"/app/admin/kits/bad?v=1", "/app/admin/kits/bad?v=2&diff=1", "/app/admin/kits/bad?v=1&file=kit.yml", "/app/admin/kits/bad?v=2&diff=1&file=kit.yml"} {
		body := get(t, h, path).Body.String()
		if strings.Contains(body, "H4sI") {
			t.Errorf("%s shows the base64:\n%s", path, preTexts(body))
		}
		if !strings.Contains(body, "«packed context, not shown»") {
			t.Errorf("%s: no placeholder", path)
		}
	}
}

// The kits list marks a kit whose current version is missing invalid.
func TestKitListMissingCurrentInvalid(t *testing.T) {
	store := newStore(t)
	if err := store.PutKitDocs([]jam.Kit{{Name: "gap", Current: 3, Versions: map[int]string{1: `{"kind":"studio"}`}}}, jam.KitObjects{}); err != nil {
		t.Fatal(err)
	}
	body := get(t, kitsHandler(store), "/app/admin/kits").Body.String()
	if !strings.Contains(body, ">invalid</span>") {
		t.Errorf("kits list:\n%s", body)
	}
}

// An unknown file is a 404 that does not echo the requested name; under a
// diff its card names both versions and follows the files diff.
func TestKitPageUnknownFile(t *testing.T) {
	h := kitsHandler(seedTreeKit(t))
	for _, path := range []string{
		"/app/admin/kits/t?v=2&diff=1&file=%3Cb%3Eevil%E2%80%AE",
		"/app/admin/kits/t?v=1&file=%3Cb%3Eevil%E2%80%AE",
	} {
		rec := get(t, h, path)
		body := rec.Body.String()
		if rec.Code != http.StatusNotFound || !strings.Contains(body, "No such file") {
			t.Errorf("%s = %d, want 404 with a no-such-file note", path, rec.Code)
		}
		if strings.Contains(body, "evil") {
			t.Errorf("%s: the requested name is echoed", path)
		}
	}
	body := get(t, h, "/app/admin/kits/t?v=2&diff=1&file=nope").Body.String()
	if f := section(body, "ctx-file"); !strings.Contains(f, "either version") {
		t.Errorf("diff-mode missing file:\n%s", f)
	}
	if i, j := strings.Index(body, `id="ctx-diff"`), strings.Index(body, `id="ctx-file"`); i < 0 || j < i {
		t.Errorf("missing-file card should follow the files diff (%d, %d)", i, j)
	}
	if f := section(get(t, h, "/app/admin/kits/t?v=1&file=nope").Body.String(), "ctx-file"); !strings.Contains(f, "this version") {
		t.Errorf("view-mode missing file:\n%s", f)
	}
}

// A path with HTML in it is shown escaped and linked query-escaped.
func TestKitPageHTMLPathEscaped(t *testing.T) {
	const p = "x<script>alert(1)</script>z"
	store := newStore(t)
	if _, _, err := jam.PushKitFiles(store, "h", kittreetest.Files("kit.yml", "kind: studio\n", p, "<b>hi</b>\n")); err != nil {
		t.Fatal(err)
	}
	h := kitsHandler(store)
	for _, path := range []string{"/app/admin/kits/h", "/app/admin/kits/h?file=" + url.QueryEscape(p)} {
		body := get(t, h, path).Body.String()
		if strings.Contains(body, "<script>alert") || strings.Contains(body, "<b>hi") {
			t.Errorf("%s: name or content rendered raw", path)
		}
		if !strings.Contains(body, "x&lt;script&gt;alert(1)&lt;/script&gt;z") {
			t.Errorf("%s: name not shown escaped", path)
		}
	}
}

// Past maxCtxEntries changed files, the files diff says how many more.
func TestKitPageFilesDiffCap(t *testing.T) {
	store := newStore(t)
	kv := []string{"kit.yml", "kind: studio\n"}
	if _, _, err := jam.PushKitFiles(store, "d", kittreetest.Files(kv...)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 510; i++ {
		kv = append(kv, fmt.Sprintf("f%03d", i), "x")
	}
	if _, _, err := jam.PushKitFiles(store, "d", kittreetest.Files(kv...)); err != nil {
		t.Fatal(err)
	}
	c := section(get(t, kitsHandler(store), "/app/admin/kits/d?v=2&diff=1").Body.String(), "ctx-diff")
	if n := strings.Count(c, "data-change="); n != 500 || !strings.Contains(c, "… 10 more") {
		t.Errorf("files diff rows = %d, want 500 and a note of 10 more", n)
	}
}

// A pre-tree version (a store whose migration failed) shows only its own
// error: never its stored text, which may hold a packed context.
func TestKitPagePreTreeVersion(t *testing.T) {
	store := newStore(t)
	text := "kind: studio\nbase:\n  context: " + packCtx(t, "Dockerfile", "FROM x\n") + "\n"
	if err := store.PutKitDocs([]jam.Kit{{Name: "old", Current: 1, Versions: map[int]string{1: text}}}, jam.KitObjects{}); err != nil {
		t.Fatal(err)
	}
	body := get(t, kitsHandler(store), "/app/admin/kits/old").Body.String()
	if !strings.Contains(body, "is not stored as a tree yet") || strings.Contains(body, "H4sI") {
		t.Errorf("pre-tree version page:\n%s", body)
	}
}
