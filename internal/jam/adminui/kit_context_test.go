package adminui_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/studio"
)

// ctxFile is one file of a test context (mode 0 → 0644).
type ctxFile struct {
	name, body string
	mode       int64
}

func packCtx(t *testing.T, files ...ctxFile) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		mode := f.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(f.body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

const hostilePath = "x<script>alert(1)</script>\u202e\nz"

var bigText = strings.Repeat("line of text\n", 6000) // > 64 KiB

// seedCtxKit pushes kit "w": v1 and v2 differ only in their packed context.
func seedCtxKit(t *testing.T) jam.Store {
	t.Helper()
	df := ctxFile{name: "Dockerfile", body: "FROM ${COVE_BASE_IMAGE}\n"}
	v1 := packCtx(t, df,
		ctxFile{name: "keep.txt", body: "same\n"},
		ctxFile{name: "mod.txt", body: "a\n"},
		ctxFile{name: "gone.txt", body: "bye\n"},
		ctxFile{name: "run.sh", body: "echo\n"},
		ctxFile{name: "blob.bin", body: "\x00\x01"},
		ctxFile{name: "big.txt", body: bigText},
	)
	v2 := packCtx(t, df,
		ctxFile{name: "keep.txt", body: "same\n"},
		ctxFile{name: "mod.txt", body: "a\nbb\n"},
		ctxFile{name: "new.txt", body: "hello\n"},
		ctxFile{name: "run.sh", body: "echo\n", mode: 0o755},
		ctxFile{name: "blob.bin", body: "\x00\x02"},
		ctxFile{name: "big.txt", body: bigText + "more\n"},
		ctxFile{name: hostilePath, body: "<b>hi</b>\u202e\n"},
	)
	store := newStore(t)
	for _, c := range []string{v1, v2} {
		seedKit(t, store, "w", "kind: studio\nbase:\n  context: "+c+"\n")
	}
	return store
}

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

func TestKitDiffYAMLOmitsContextBlob(t *testing.T) {
	body := get(t, kitsHandler(seedCtxKit(t)), "/app/admin/kits/w?v=2&diff=1").Body.String()
	diff := section(body, "kit-diff")
	if diff == "" {
		t.Fatal("no diff card")
	}
	if strings.Contains(diff, "H4sI") {
		t.Errorf("YAML diff carries the base64 context:\n%s", diff)
	}
	if !strings.Contains(diff, `<span class="d-del">-     context: «tar.gz, 7 files, `) || !strings.Contains(diff, `<span class="d-add">+     context: «tar.gz, 8 files, `) {
		t.Errorf("YAML diff should show the placeholder change:\n%s", diff)
	}
}

func TestKitDiffContextFilesTable(t *testing.T) {
	body := get(t, kitsHandler(seedCtxKit(t)), "/app/admin/kits/w?v=2&diff=1").Body.String()
	tbl := section(body, "ctx-diff")
	if tbl == "" {
		t.Fatal("no context-files diff card")
	}
	for _, want := range []string{
		`data-change="added"`, `href="/app/admin/kits/w?v=2&amp;diff=1&amp;file=new.txt"`,
		`data-change="removed"`, `file=gone.txt"`,
		`data-change="modified"`, `file=mod.txt"`, "2 B → 5 B",
		`data-change="mode"`, `file=run.sh"`, "0644 → 0755",
		"2 unchanged",
	} {
		if !strings.Contains(tbl, want) {
			t.Errorf("context diff missing %q:\n%s", want, tbl)
		}
	}
	if strings.Contains(tbl, "keep.txt") {
		t.Errorf("unchanged files are only counted")
	}
}

func TestKitDiffOneFile(t *testing.T) {
	h := kitsHandler(seedCtxKit(t))
	body := get(t, h, "/app/admin/kits/w?v=2&diff=1&file=mod.txt").Body.String()
	f := section(body, "ctx-file")
	if !strings.Contains(f, "mod.txt") || !strings.Contains(f, `<span class="d-add">+ bb</span>`) || !strings.Contains(f, `<span>  a</span>`) {
		t.Errorf("file diff:\n%s", f)
	}
	f = section(get(t, h, "/app/admin/kits/w?v=2&diff=1&file=blob.bin").Body.String(), "ctx-file")
	if !strings.Contains(f, "binary") || !strings.Contains(f, "2 B → 2 B") || strings.Contains(f, `class="diff"`) {
		t.Errorf("binary file:\n%s", f)
	}
	f = section(get(t, h, "/app/admin/kits/w?v=2&diff=1&file=big.txt").Body.String(), "ctx-file")
	if !strings.Contains(f, "too large") || !strings.Contains(f, "78000 B → 78005 B") || strings.Contains(f, "line of text") {
		t.Errorf("oversize file:\n%s", f)
	}
}

func TestKitContextHostilePathEscaped(t *testing.T) {
	h := kitsHandler(seedCtxKit(t))
	for _, path := range []string{
		"/app/admin/kits/w?v=2&diff=1",
		"/app/admin/kits/w?v=2",
		"/app/admin/kits/w?v=2&diff=1&file=" + url.QueryEscape(hostilePath),
		"/app/admin/kits/w?v=2&file=" + url.QueryEscape(hostilePath),
	} {
		rec := get(t, h, path)
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d", path, rec.Code)
		}
		if strings.Contains(body, "<script>alert") || strings.Contains(body, "\u202e") || strings.Contains(body, "<b>hi") || strings.Contains(body, "</script>\u202e\nz") {
			t.Errorf("%s: hostile name or content rendered raw", path)
		}
		if !strings.Contains(body, `x&lt;script&gt;alert(1)&lt;/script&gt;\u202e\nz`) {
			t.Errorf("%s: hostile name not shown escaped", path)
		}
		if !strings.Contains(body, "file="+url.QueryEscape(hostilePath)) && !strings.Contains(path, "file=") {
			t.Errorf("%s: no query-escaped link to the hostile file", path)
		}
	}
}

func TestKitContextUnknownFile(t *testing.T) {
	h := kitsHandler(seedCtxKit(t))
	for _, path := range []string{
		"/app/admin/kits/w?v=2&diff=1&file=%3Cb%3Eevil%E2%80%AE",
		"/app/admin/kits/w?v=1&file=%3Cb%3Eevil%E2%80%AE",
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
	// under a diff, the card sits below the context diff and names both versions
	body := get(t, h, "/app/admin/kits/w?v=2&diff=1&file=nope").Body.String()
	if f := section(body, "ctx-file"); !strings.Contains(f, "either version") {
		t.Errorf("diff-mode missing file:\n%s", f)
	}
	if i, j := strings.Index(body, `id="ctx-diff"`), strings.Index(body, `id="ctx-file"`); i < 0 || j < i {
		t.Errorf("missing-file card should follow the context diff (%d, %d)", i, j)
	}
	if f := section(get(t, h, "/app/admin/kits/w?v=1&file=nope").Body.String(), "ctx-file"); !strings.Contains(f, "this version") {
		t.Errorf("view-mode missing file:\n%s", f)
	}
}

// Two contexts differing in one same-length byte still diff: the
// placeholder carries a short hash of the stored base64.
func TestKitDiffPlaceholderHash(t *testing.T) {
	df := ctxFile{name: "Dockerfile", body: "FROM x\n"}
	store := newStore(t)
	for _, b := range []string{"a\n", "b\n"} {
		seedKit(t, store, "h", "kind: studio\nbase:\n  context: "+packCtx(t, df, ctxFile{name: "a.txt", body: b})+"\n")
	}
	d := section(get(t, kitsHandler(store), "/app/admin/kits/h?v=2&diff=1").Body.String(), "kit-diff")
	if !strings.Contains(d, `<span class="d-del">-     context: «tar.gz, 2 files, `) || !strings.Contains(d, `<span class="d-add">+     context: «tar.gz, 2 files, `) || strings.Count(d, ", sha256:") != 2 {
		t.Errorf("a same-size context change should show in the YAML diff:\n%s", d)
	}
}

// A name packed twice is listed once, marked; a context diff past
// maxCtxEntries rows says how many more changed.
func TestKitContextDuplicatesAndDiffCap(t *testing.T) {
	df := ctxFile{name: "Dockerfile", body: "FROM x\n"}
	store := newStore(t)
	dup := packCtx(t, df, ctxFile{name: "dup.txt", body: "1\n"}, ctxFile{name: "dup.txt", body: "22\n"})
	many := []ctxFile{df}
	for i := 0; i < 510; i++ {
		many = append(many, ctxFile{name: fmt.Sprintf("f%03d", i), body: "x"})
	}
	for _, c := range []string{dup, packCtx(t, many...)} {
		seedKit(t, store, "d", "kind: studio\nbase:\n  context: "+c+"\n")
	}
	h := kitsHandler(store)
	c := section(get(t, h, "/app/admin/kits/d?v=1").Body.String(), "ctx-files")
	if strings.Count(c, ">dup.txt</a>") != 1 || !strings.Contains(c, "duplicate entries in tar") || !strings.Contains(c, "<td>3 B</td>") {
		t.Errorf("duplicate names:\n%s", c)
	}
	c = section(get(t, h, "/app/admin/kits/d?v=2&diff=1").Body.String(), "ctx-diff")
	if n := strings.Count(c, "data-change="); n != 500 || !strings.Contains(c, "… 11 more") {
		t.Errorf("context diff rows = %d, want 500 and a note of 11 more", n)
	}
}

func TestKitVersionContextFiles(t *testing.T) {
	h := kitsHandler(seedCtxKit(t))
	body := get(t, h, "/app/admin/kits/w?v=1").Body.String()
	tbl := section(body, "ctx-files")
	for _, want := range []string{`href="/app/admin/kits/w?v=1&amp;file=mod.txt"`, "gone.txt", "0644", "2 B", "Dockerfile"} {
		if !strings.Contains(tbl, want) {
			t.Errorf("version context files missing %q:\n%s", want, tbl)
		}
	}
	f := section(get(t, h, "/app/admin/kits/w?v=1&file=gone.txt").Body.String(), "ctx-file")
	if !strings.Contains(f, "gone.txt") || !strings.Contains(f, "bye") {
		t.Errorf("file content:\n%s", f)
	}
	f = section(get(t, h, "/app/admin/kits/w?v=1&file=blob.bin").Body.String(), "ctx-file")
	if !strings.Contains(f, "binary") || strings.Contains(f, "\x00") {
		t.Errorf("binary content:\n%s", f)
	}
}

func TestKitContextUnreadable(t *testing.T) {
	store := newStore(t)
	// readable at push (the first header), refused by the scan
	bad := packCtx(t, ctxFile{name: "Dockerfile", body: "FROM x\n"}, ctxFile{name: "../evil", body: "x"})
	seedKit(t, store, "u", "kind: studio\nbase:\n  context: "+bad+"\n")
	seedKitText(t, store, "u", "kind: studio\nbase:\n  context: '!!not-base64'\n")
	h := kitsHandler(store)
	body := get(t, h, "/app/admin/kits/u?v=1").Body.String()
	if c := section(body, "ctx-files"); !strings.Contains(c, "context unreadable: not a safe, readable tar.gz") || strings.Contains(c, "evil") {
		t.Errorf("bad tar:\n%s", c)
	}
	body = get(t, h, "/app/admin/kits/u?v=2").Body.String()
	if c := section(body, "ctx-files"); !strings.Contains(c, "context unreadable: not valid base64") {
		t.Errorf("bad base64:\n%s", c)
	}
	body = get(t, h, "/app/admin/kits/u?v=2&diff=1").Body.String()
	if d := section(body, "kit-diff"); strings.Contains(d, "H4sI") || !strings.Contains(d, "«tar.gz, unreadable") {
		t.Errorf("diff with an unreadable side:\n%s", d)
	}
	if c := section(body, "ctx-diff"); !strings.Contains(c, "context unreadable") {
		t.Errorf("context diff with an unreadable side:\n%s", c)
	}
	// a file asked of contexts that can't be read says so, not "no such file"
	for _, path := range []string{"/app/admin/kits/u?v=2&diff=1&file=Dockerfile", "/app/admin/kits/u?v=1&file=Dockerfile"} {
		rec := get(t, h, path)
		f := section(rec.Body.String(), "ctx-file")
		if rec.Code != http.StatusOK || !strings.Contains(f, "context unreadable") || strings.Contains(f, "No such file") {
			t.Errorf("%s = %d:\n%s", path, rec.Code, f)
		}
	}
}

// seedKit stores config under name the way a pre-tree Jam would have:
// parsed, as canonical JSON config text (Kit.Versions, not a tree), unchecked,
// as the new current version. For fixtures of packed-context kits, which a
// push now refuses.
func seedKit(t *testing.T, st jam.Store, name, config string) {
	t.Helper()
	sk, err := studio.ParseStudioKit([]byte(config))
	if err != nil {
		t.Fatal(err)
	}
	text, err := sk.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	seedKitText(t, st, name, string(text))
}

// seedKitText stores text verbatim as name's new current pre-tree version.
func seedKitText(t *testing.T, st jam.Store, name, text string) {
	t.Helper()
	k, ok := st.GetKit(name)
	if !ok {
		k = jam.Kit{Name: name}
	}
	n := 1
	if vs := k.VersionNumbers(); len(vs) > 0 {
		n = vs[len(vs)-1] + 1
	}
	versions := maps.Clone(k.Versions)
	if versions == nil {
		versions = map[int]string{}
	}
	versions[n] = text
	k.Versions, k.Current = versions, n
	if err := st.PutKitDocs([]jam.Kit{k}, jam.KitObjects{}); err != nil {
		t.Fatal(err)
	}
}
