package jam

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

const unsafeDraftPath = "draft app: a file has an unsafe path (empty, absolute, a . or .. segment, invalid UTF-8, or a control or format character)"

// withDraftLimits shrinks the tree limits for one test.
func withDraftLimits(t *testing.T, entries int, total, file int64) {
	t.Helper()
	e, to, f := kittree.MaxEntries, kittree.MaxTotal, kittree.MaxFile
	kittree.MaxEntries, kittree.MaxTotal, kittree.MaxFile = entries, total, file
	t.Cleanup(func() { kittree.MaxEntries, kittree.MaxTotal, kittree.MaxFile = e, to, f })
}

func openApp(t *testing.T, st Store) string {
	t.Helper()
	d, err := OpenKitDraft(st, "app", 0, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	return DraftETag(d.Tree)
}

func TestPutKitDraftFile(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "run.sh*", "echo\n")
	tag := openApp(t, st)
	later := draftNow.Add(time.Minute)
	s, err := PutKitDraftFile(st, nil, "app", "docs/notes.md", 0, []byte("hi\n"), tag, "alice", later)
	if err != nil || s.ETag == tag || s.ETag != DraftETag(s.Tree) || !slices.Equal(s.Changed, []DraftChange{{"docs/notes.md", "added"}}) || len(s.Problems) != 0 {
		t.Fatalf("put = %+v %v", s, err)
	}
	if d, _ := st.GetKitDraft("app"); d.Tree != s.Tree || d.Actor != "alice" || !d.UpdatedAt.Equal(later) || !d.OpenedAt.Equal(draftNow) {
		t.Fatalf("draft row = %+v", d)
	}
	// Mode 0 keeps the file's mode (0755 here); a new file gets 0644.
	s, err = PutKitDraftFile(st, nil, "app", "run.sh", 0, []byte("echo two\n"), s.ETag, "alice", later)
	data, e, etag, ferr := KitDraftFile(st, "app", "run.sh")
	if err != nil || ferr != nil || string(data) != "echo two\n" || e.Mode != 0o755 || etag != s.ETag {
		t.Fatalf("get = %q %+v %q %v %v", data, e, etag, err, ferr)
	}
	if _, e, _, _ := KitDraftFile(st, "app", "docs/notes.md"); e.Mode != 0o644 {
		t.Fatalf("a new file's mode = %o", e.Mode)
	}
	s, err = PutKitDraftFile(st, nil, "app", "run.sh", 0o644, []byte("echo two\n"), s.ETag, "alice", later)
	if err != nil || !slices.Equal(s.Changed, []DraftChange{{"run.sh", "mode"}}) {
		t.Fatalf("mode change = %+v %v", s, err)
	}
	// An identical write is a no-op with the same ETag.
	same, err := PutKitDraftFile(st, nil, "app", "run.sh", 0o644, []byte("echo two\n"), s.ETag, "bob", later)
	if err != nil || same.ETag != s.ETag || len(same.Changed) != 0 {
		t.Fatalf("no-op = %+v %v", same, err)
	}
	if d, _ := st.GetKitDraft("app"); d.Actor != "alice" {
		t.Fatal("a no-op write recorded its actor")
	}
	// kit.yml may go while editing; the problem is listed.
	s, err = RemoveKitDraftFile(st, nil, "app", "kit.yml", s.ETag, "alice", later)
	if err != nil || !slices.Equal(s.Problems, []string{"kit app: kit.yml missing at the root"}) || !slices.Equal(s.Changed, []DraftChange{{"kit.yml", "removed"}}) {
		t.Fatalf("rm kit.yml = %+v %v", s, err)
	}
	_, err = RemoveKitDraftFile(st, nil, "app", "nope.txt", s.ETag, "alice", later)
	wantErr(t, err, "draft app has no file nope.txt", 404)
	_, _, _, err = KitDraftFile(st, "app", "nope.txt")
	wantErr(t, err, "draft app has no file nope.txt", 404)
	_, err = PutKitDraftFile(st, nil, "app", "x", 0o600, []byte("x"), s.ETag, "alice", later)
	wantErr(t, err, "draft app: mode must be 0644 or 0755", 400)
	if v, _ := LoadKitVersion(st, "app", 0); v.Version != 1 || v.Err != nil {
		t.Fatal("draft writes touched the published version")
	}
}

// TestDraftFileRefusals: hostile paths are refused unechoed; graphic unicode
// is a fine name; the limits and .dockerignore hold (Review Focus 4).
func TestDraftFileRefusals(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", ".dockerignore", "*.log\n", "sub/x", "x")
	tag := openApp(t, st)
	for name, tc := range map[string]struct{ p, want string }{
		"newline":         {"a\nb", unsafeDraftPath},
		"ansi escape":     {"a\x1b[31mb", unsafeDraftPath},
		"bidi override":   {"\u202etxt.exe", unsafeDraftPath},
		"dot dot":         {"../x", unsafeDraftPath},
		"dot segment":     {"a/./b", unsafeDraftPath},
		"invalid utf-8":   {"a\xffb", unsafeDraftPath},
		"absolute":        {"/etc/passwd", unsafeDraftPath},
		"empty":           {"", unsafeDraftPath},
		"file over a dir": {"sub", "draft app: sub: is both a file and a directory"},
		"ignored":         {"debug.log", "draft app: debug.log: excluded by .dockerignore"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := PutKitDraftFile(st, nil, "app", tc.p, 0, []byte("x"), tag, "local", draftNow)
			wantErr(t, err, tc.want, 400)
		})
	}
	_, err := RemoveKitDraftFile(st, nil, "app", "a\nb", tag, "local", draftNow)
	wantErr(t, err, unsafeDraftPath, 400)
	_, _, _, err = KitDraftFile(st, "app", "a\nb")
	wantErr(t, err, unsafeDraftPath, 400)
	if d, _ := st.GetKitDraft("app"); DraftETag(d.Tree) != tag {
		t.Fatal("a refused write changed the draft")
	}
	uni := "docs/ünïcødé 名前.md"
	s, err := PutKitDraftFile(st, nil, "app", uni, 0, []byte("ok\n"), tag, "local", draftNow)
	if err != nil || s.Changed[0].Path != uni {
		t.Fatalf("unicode put = %+v %v", s, err)
	}
	if b, _, _, err := KitDraftFile(st, "app", uni); err != nil || string(b) != "ok\n" {
		t.Fatalf("unicode get = %q %v", b, err)
	}
	// Limits: 4 entries now (kit.yml, .dockerignore, sub/x, the unicode file).
	withDraftLimits(t, 5, 1<<20, 16)
	_, err = PutKitDraftFile(st, nil, "app", "big.bin", 0, make([]byte, 17), s.ETag, "local", draftNow)
	wantErr(t, err, "draft app: big.bin: more than 16 bytes (limit 16 bytes per file)", 413)
	s, err = PutKitDraftFile(st, nil, "app", "five.txt", 0, []byte("5"), s.ETag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PutKitDraftFile(st, nil, "app", "six.txt", 0, []byte("6"), s.ETag, "local", draftNow)
	wantErr(t, err, "draft app: 6 files (limit 5)", 400)
}

func TestDraftDockerignoreDropsFiles(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "debug.log", "x", "run.sh", "y")
	tag := openApp(t, st)
	s, err := PutKitDraftFile(st, nil, "app", ".dockerignore", 0, []byte("*.log\n"), tag, "local", draftNow)
	want := []DraftChange{{".dockerignore", "added"}, {"debug.log", "removed"}}
	if err != nil || !slices.Equal(s.Changed, want) {
		t.Fatalf("save .dockerignore = %+v %v, want %+v", s.Changed, err, want)
	}
	if _, _, _, err := KitDraftFile(st, "app", "debug.log"); err == nil {
		t.Fatal("an ignored file stayed in the draft")
	}
}

// TestDraftTwoTabsStaleETag: two editors load one ETag; the second save is
// refused, never merged (Review Focus 1).
func TestDraftTwoTabsStaleETag(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "notes.md", "v1\n")
	loaded := openApp(t, st) // both tabs load the draft
	a, err := PutKitDraftFile(st, nil, "app", "notes.md", 0, []byte("tab A\n"), loaded, "alice", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PutKitDraftFile(st, nil, "app", "notes.md", 0, []byte("tab B\n"), loaded, "bob", draftNow)
	wantErr(t, err, "draft app changed since you loaded it (reload)", 412)
	if b, _, _, _ := KitDraftFile(st, "app", "notes.md"); string(b) != "tab A\n" {
		t.Fatalf("tab B's stale save got through: %q", b)
	}
	for name, call := range map[string]func(tag string) error{
		"put": func(tag string) error {
			_, err := PutKitDraftFile(st, nil, "app", "other.md", 0, []byte("b"), tag, "bob", draftNow)
			return err
		},
		"rm": func(tag string) error {
			_, err := RemoveKitDraftFile(st, nil, "app", "notes.md", tag, "bob", draftNow)
			return err
		},
		"discard": func(tag string) error {
			_, err := DiscardKitDraft(st, nil, "app", tag)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			wantErr(t, call(loaded), "draft app changed since you loaded it (reload)", 412)
			wantErr(t, call(""), "draft app: If-Match is required (send the etag from the draft status)", 428)
		})
	}
	if d, _ := st.GetKitDraft("app"); DraftETag(d.Tree) != a.ETag {
		t.Fatal("a refused write changed the draft")
	}
	// Tab B reloads (a.ETag) and saves.
	if _, err := PutKitDraftFile(st, nil, "app", "notes.md", 0, []byte("tab B\n"), a.ETag, "bob", draftNow); err != nil {
		t.Fatal(err)
	}
}

// TestDraftWritesSurviveConcurrentGC: GC running alongside saves never takes
// a blob the draft references (Review Focus 5; -race in CI).
func TestDraftWritesSurviveConcurrentGC(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	tag := openApp(t, st)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := st.GCKitObjects(); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}()
	for i := range 50 {
		s, err := PutKitDraftFile(st, nil, "app", fmt.Sprintf("f%02d.txt", i), 0, []byte(fmt.Sprintf("content %d\n", i)), tag, "local", draftNow)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatal(err)
		}
		tag = s.ETag
	}
	close(stop)
	wg.Wait()
	if _, err := st.GCKitObjects(); err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetKitDraft("app")
	tree, ok := st.KitTree(d.Tree)
	if !ok || len(tree.Entries) != 51 {
		t.Fatalf("draft tree = %d entries, %v", len(tree.Entries), ok)
	}
	for _, e := range tree.Entries {
		if _, ok := st.KitBlob(e.SHA256); !ok {
			t.Fatalf("GC took %s's blob from the draft", e.Path)
		}
	}
}

func TestReplaceKitDraft(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "old.sh", "old\n")
	tag := openApp(t, st)
	files := kittreetest.Files("kit.yml", "kind: studio\nprompt: synced\n", "run.sh*", "echo\n", ".dockerignore", "*.log\n", "debug.log", "x")
	s, err := ReplaceKitDraft(st, nil, "app", files, tag, "local", draftNow)
	want := []DraftChange{{".dockerignore", "added"}, {"kit.yml", "modified"}, {"old.sh", "removed"}, {"run.sh", "added"}}
	if err != nil || !slices.Equal(s.Changed, want) || len(s.Problems) != 0 {
		t.Fatalf("replace = %+v %v, want %+v", s, err, want)
	}
	if _, _, _, err := KitDraftFile(st, "app", "debug.log"); err == nil {
		t.Fatal(".dockerignore must filter the sync")
	}
	same, err := ReplaceKitDraft(st, nil, "app", files, s.ETag, "local", draftNow)
	if err != nil || same.ETag != s.ETag || len(same.Changed) != 0 {
		t.Fatalf("same files = %+v %v (unchanged)", same, err)
	}
	// Stale or missing If-Match (Review Focus 1), nothing written.
	_, err = ReplaceKitDraft(st, nil, "app", kittreetest.Files("kit.yml", "kind: studio\n"), tag, "local", draftNow)
	wantErr(t, err, "draft app changed since you loaded it (reload)", 412)
	_, err = ReplaceKitDraft(st, nil, "app", kittreetest.Files("kit.yml", "kind: studio\n"), "", "local", draftNow)
	wantErr(t, err, "draft app: If-Match is required (send the etag from the draft status)", 428)
	// No kit.yml is a problem, not a refusal.
	s, err = ReplaceKitDraft(st, nil, "app", kittreetest.Files("notes.md", "n\n"), s.ETag, "local", draftNow)
	if err != nil || !slices.Equal(s.Problems, []string{"kit app: kit.yml missing at the root"}) {
		t.Fatalf("no kit.yml = %+v %v", s, err)
	}
	// The file rules hold.
	_, err = ReplaceKitDraft(st, nil, "app", []kittree.File{{Path: "a\nb"}}, s.ETag, "local", draftNow)
	wantErr(t, err, unsafeDraftPath, 400)
	withDraftLimits(t, 1, kittree.MaxTotal, kittree.MaxFile)
	_, err = ReplaceKitDraft(st, nil, "app", kittreetest.Files("a", "1", "b", "2"), s.ETag, "local", draftNow)
	wantErr(t, err, "draft app: 2 files (limit 1)", 400)
}

// TestRenameKitDraftFile: a rename moves the content and mode in one save,
// refuses a missing source, a taken, unsafe, ignored or file-over-directory
// target (unechoed) and a stale ETag, leaves a rename to itself a no-op, and
// applies a file renamed to .dockerignore.
func TestRenameKitDraftFile(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "run.sh*", "echo\n", "sub/x", "x", ".dockerignore", "*.log\n", "rules", "sub\n")
	tag := openApp(t, st)
	later := draftNow.Add(time.Minute)
	s, err := RenameKitDraftFile(st, nil, "app", "run.sh", "bin/run.sh", tag, "alice", later)
	if err != nil || !slices.Equal(s.Changed, []DraftChange{{"bin/run.sh", "added"}, {"run.sh", "removed"}}) || len(s.Problems) != 0 {
		t.Fatalf("rename = %+v %v", s, err)
	}
	data, e, etag, err := KitDraftFile(st, "app", "bin/run.sh")
	if err != nil || string(data) != "echo\n" || e.Mode != 0o755 || etag != s.ETag {
		t.Fatalf("moved file = %q %+v %q %v", data, e, etag, err)
	}
	_, _, _, err = KitDraftFile(st, "app", "run.sh")
	wantErr(t, err, "draft app has no file run.sh", 404)
	if d, _ := st.GetKitDraft("app"); d.Actor != "alice" || !d.UpdatedAt.Equal(later) {
		t.Fatalf("draft row = %+v", d)
	}
	same, err := RenameKitDraftFile(st, nil, "app", "bin/run.sh", "bin/run.sh", s.ETag, "bob", later)
	if err != nil || same.ETag != s.ETag || len(same.Changed) != 0 {
		t.Fatalf("rename to itself = %+v %v", same, err)
	}
	if d, _ := st.GetKitDraft("app"); d.Actor != "alice" {
		t.Fatal("a no-op rename recorded its actor")
	}
	for name, tc := range map[string]struct {
		from, to, want string
		status         int
	}{
		"no such file":    {"nope", "x2", "draft app has no file nope", 404},
		"taken":           {"bin/run.sh", "kit.yml", "draft app already has a file kit.yml", 409},
		"bidi target":     {"bin/run.sh", "\u202etxt.exe", unsafeDraftPath, 400},
		"dot dot target":  {"bin/run.sh", "../x", unsafeDraftPath, 400},
		"unsafe source":   {"a\nb", "x", unsafeDraftPath, 400},
		"file over a dir": {"bin/run.sh", "sub", "draft app: sub: is both a file and a directory", 400},
		"ignored":         {"bin/run.sh", "run.log", "draft app: run.log: excluded by .dockerignore", 400},
		"stale":           {"bin/run.sh", "b.sh", "draft app changed since you loaded it (reload)", 412},
	} {
		t.Run(name, func(t *testing.T) {
			ifMatch := s.ETag
			if name == "stale" {
				ifMatch = tag
			}
			_, err := RenameKitDraftFile(st, nil, "app", tc.from, tc.to, ifMatch, "alice", later)
			wantErr(t, err, tc.want, tc.status)
		})
	}
	if d, _ := st.GetKitDraft("app"); DraftETag(d.Tree) != s.ETag {
		t.Fatal("a refused rename changed the draft")
	}
	// A file renamed to .dockerignore applies it: "sub" now drops sub/x.
	s, err = RemoveKitDraftFile(st, nil, "app", ".dockerignore", s.ETag, "alice", later)
	if err != nil {
		t.Fatal(err)
	}
	s, err = RenameKitDraftFile(st, nil, "app", "rules", ".dockerignore", s.ETag, "alice", later)
	if err != nil || !slices.Equal(s.Changed, []DraftChange{{".dockerignore", "added"}, {"rules", "removed"}, {"sub/x", "removed"}}) {
		t.Fatalf("rename to .dockerignore = %+v %v", s, err)
	}
	// kit.yml renamed away is a problem, not a refusal.
	s, err = RenameKitDraftFile(st, nil, "app", "kit.yml", "old.yml", s.ETag, "alice", later)
	if err != nil || !slices.Equal(s.Problems, []string{"kit app: kit.yml missing at the root"}) {
		t.Fatalf("rename kit.yml = %+v %v", s, err)
	}
	if v, _ := LoadKitVersion(st, "app", 0); v.Version != 1 || v.Err != nil {
		t.Fatal("renames touched the published version")
	}
}
