package adminui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

var bigText = strings.Repeat("line of text\n", 6000) // > 64 KiB

// seedTreeKit pushes kit "t": v1 and v2 differ in their files and prompt.
func seedTreeKit(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	df := "ARG JAM_STUDIO_BASE_IMAGE\nFROM ${JAM_STUDIO_BASE_IMAGE}\n"
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

// A version's file shows its text as stored; a binary or oversize file a
// note instead; a file the version lacks is missing, never named.
func TestKitsAPIFileView(t *testing.T) {
	store := seedTreeKit(t)
	if f := getKit(t, store, "t?v=2&file=html.txt").File; f == nil || f.Content != "<b>hi</b>\n" || f.Diff || f.Note != "" {
		t.Errorf("html.txt = %+v", f)
	}
	if f := getKit(t, store, "t?v=2&file=blob.bin").File; f == nil || f.Content != "" || !strings.Contains(f.Note, "binary file") {
		t.Errorf("blob.bin = %+v", f)
	}
	if f := getKit(t, store, "t?v=2&file=big.txt").File; f == nil || f.Content != "" || !strings.Contains(f.Note, "too large to show") {
		t.Errorf("big.txt = %+v", f)
	}
	for _, q := range []string{"t?v=1&file=new.txt", "t?v=2&diff=1&file=%3Cb%3Eevil%E2%80%AE"} {
		if f := getKit(t, store, q).File; f == nil || !f.Missing || f.Path != "" {
			t.Errorf("%s = %+v", q, f)
		}
	}
}

// A version diff lists each changed file — added, removed, modified with its
// sizes, a mode-only change — and counts the unchanged; a file's diff shows
// its lines, a binary or oversize one a note with both sides' sizes.
func TestKitsAPIFilesDiff(t *testing.T) {
	store := seedTreeKit(t)
	d := getKit(t, store, "t?v=2&diff=1")
	if d.Diff == nil || diffOps(d.Diff.Lines) != "- prompt: one|+ prompt: two" {
		t.Errorf("kit.yml diff = %+v", d.Diff)
	}
	fd := d.FilesDiff
	if fd == nil || fd.From != 1 || fd.To != 2 || fd.Unchanged != 2 || fd.More != 0 {
		t.Fatalf("files diff = %+v", fd)
	}
	rows := map[string]adminui.FileChange{}
	for _, r := range fd.Rows {
		rows[r.Path] = r
	}
	for p, want := range map[string]adminui.FileChange{
		"new.txt":  {Change: "added", Path: "new.txt", Label: "new.txt", Mode: "0644", Size: "6 B"},
		"gone.txt": {Change: "removed", Path: "gone.txt", Label: "gone.txt", Mode: "0644", Size: "4 B"},
		"mod.txt":  {Change: "modified", Path: "mod.txt", Label: "mod.txt", Mode: "0644", Size: "2 B → 5 B"},
		"run.sh":   {Change: "mode", Path: "run.sh", Label: "run.sh", Mode: "0644 → 0755", Size: "5 B"},
	} {
		if rows[p] != want {
			t.Errorf("%s = %+v, want %+v", p, rows[p], want)
		}
	}
	if f := getKit(t, store, "t?v=2&diff=1&file=mod.txt").File; f == nil || !f.Diff || f.From != 1 || f.To != 2 || diffOps(f.Lines) != "+ bb" {
		t.Errorf("mod.txt diff = %+v", f)
	}
	if f := getKit(t, store, "t?v=2&diff=1&file=new.txt").File; f == nil || diffOps(f.Lines) != "+ hello" {
		t.Errorf("new.txt diff = %+v", f)
	}
	if f := getKit(t, store, "t?v=2&diff=1&file=blob.bin").File; f == nil || f.Note != "binary file (2 B → 2 B), content not shown" || len(f.Lines) != 0 {
		t.Errorf("blob.bin diff = %+v", f)
	}
	if f := getKit(t, store, "t?v=2&diff=1&file=big.txt").File; f == nil || !strings.HasPrefix(f.Note, "too large to diff (") {
		t.Errorf("big.txt diff = %+v", f)
	}
	if f := getKit(t, store, "t?v=2&diff=1&file=html.txt").File; f == nil || diffOps(f.Lines) != "+ <b>hi</b>" {
		t.Errorf("html.txt (absent in v1) diff = %+v", f)
	}
}

// Past maxCtxEntries the file list and the files diff say how many more.
func TestKitsAPIFilesCaps(t *testing.T) {
	store := newStore(t)
	kv := []string{"kit.yml", "kind: studio\n"}
	if _, _, err := jam.PushKitFiles(store, "d", kittreetest.Files(kv...)); err != nil {
		t.Fatal(err)
	}
	for i := range 510 {
		kv = append(kv, fmt.Sprintf("f%03d", i), "x")
	}
	if _, _, err := jam.PushKitFiles(store, "d", kittreetest.Files(kv...)); err != nil {
		t.Fatal(err)
	}
	d := getKit(t, store, "d?v=2&diff=1")
	if d.FilesDiff == nil || len(d.FilesDiff.Rows) != 500 || d.FilesDiff.More != 10 {
		t.Errorf("files diff = %d rows, %d more", len(d.FilesDiff.Rows), d.FilesDiff.More)
	}
	if d.Files == nil || len(d.Files.Rows) != 500 || d.Files.Hidden != 11 || d.Files.N != 2 {
		t.Errorf("files = %d rows, %d hidden", len(d.Files.Rows), d.Files.Hidden)
	}
}

// A version the migration could not turn into a tree, or one not stored as
// a tree yet, shows only its own error — never its text; a kit whose current
// version is missing is invalid in the list.
func TestKitsAPIBrokenVersions(t *testing.T) {
	store := newStore(t)
	text := "kind: studio\nbase:\n  context: " + packCtx(t, "Dockerfile", "FROM x\n") + "\n"
	if err := store.PutKitDocs([]jam.Kit{
		{Name: "broken", Current: 1, Broken: map[int]string{1: "its base.context is not a readable tar.gz"}},
		{Name: "pretree", Current: 1, Versions: map[int]string{1: text}},
		{Name: "gap", Current: 3, Versions: map[int]string{1: `{"kind":"studio"}`}},
	}, jam.KitObjects{}); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)
	for name, want := range map[string]string{
		"broken":  "could not be migrated to a tree: its base.context is not a readable tar.gz",
		"pretree": "is not stored as a tree yet",
	} {
		rec := apiDo(t, h, "GET", "/app/admin/api/kits/"+name, nil, nil)
		v := getKit(t, store, name).View
		if !strings.Contains(v.ParseErr, want) || v.RawShown != "" || v.PushText != "" || strings.Contains(rec.Body.String(), "H4sI") {
			t.Errorf("%s view = %+v", name, v)
		}
	}
	// Two versions with no tree have no files to compare.
	if err := store.PutKitDocs([]jam.Kit{{Name: "twice", Current: 2, Broken: map[int]string{1: "bad one", 2: "bad two"}}}, jam.KitObjects{}); err != nil {
		t.Fatal(err)
	}
	if d := getKit(t, store, "twice?v=2&diff=1"); d.FilesDiff != nil || d.Files != nil || d.Diff == nil {
		t.Errorf("no-tree diff = %+v / %+v / %+v", d.Diff, d.FilesDiff, d.Files)
	}
	var list adminui.KitsResponse
	apiDo(t, h, "GET", "/app/admin/api/kits", nil, &list)
	for _, r := range list.Kits {
		if !r.Invalid {
			t.Errorf("%s should be invalid: %+v", r.Name, r)
		}
	}
}

// An unparsable kit.yml: only base's packed context is masked (another key
// named context is kept), and a text that cannot be analysed is hidden whole,
// with no push prefill — in the view and in ?file=kit.yml.
func TestKitsAPIUnparsableMasking(t *testing.T) {
	store := newStore(t)
	b64 := packCtx(t, "Dockerfile", "FROM x\n")
	for name, text := range map[string]string{
		"keep":   "kind: studio\nbogus: 1\nbuild-args: {context: x-kept}\n",
		"masked": "kind: studio\nbogus: 1\nbuild-args: {context: x-kept}\nbase:\n  context: " + b64 + "\n",
		"whole":  "kind: studio\nbogus: 1\n---\nbase: {context: " + b64 + "}\n",
	} {
		if _, err := store.PushKit(name, text); err != nil {
			t.Fatal(err)
		}
	}
	if v := getKit(t, store, "keep").View; v.PushText != "kind: studio\nbogus: 1\nbuild-args: {context: x-kept}\n" || v.Masked || v.Hidden {
		t.Errorf("keep = %+v", v)
	}
	if v := getKit(t, store, "masked").View; !v.Masked || v.Hidden || !strings.Contains(v.PushText, "context: x-kept") || !strings.Contains(v.PushText, "«packed context, not shown»") || strings.Contains(v.PushText, "H4sI") {
		t.Errorf("masked = %+v", v)
	}
	if v := getKit(t, store, "whole").View; !v.Hidden || v.PushText != "" || strings.Contains(v.RawShown, "H4sI") {
		t.Errorf("whole = %+v", v)
	}
	h := uiHandler(t, store)
	for _, name := range []string{"masked", "whole"} {
		rec := apiDo(t, h, "GET", "/app/admin/api/kits/"+name+"?file=kit.yml", nil, nil)
		f := getKit(t, store, name+"?file=kit.yml").File
		if strings.Contains(rec.Body.String(), "H4sI") || f == nil || (name == "masked") != f.Masked || (name == "whole") != f.Hidden {
			t.Errorf("%s file = %+v", name, f)
		}
	}
}
