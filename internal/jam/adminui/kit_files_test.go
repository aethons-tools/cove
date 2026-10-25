package adminui_test

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

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
