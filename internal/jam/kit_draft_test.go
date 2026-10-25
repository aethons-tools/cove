package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

var draftNow = time.Unix(100, 0).UTC()

// pushApp pushes path → content pairs ("*" suffix = 0755) as kit app's next version.
func pushApp(t *testing.T, st Store, kv ...string) {
	t.Helper()
	if _, _, err := PushKitFiles(st, "app", kittreetest.Files(kv...)); err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err error, text string, status int) {
	t.Helper()
	if err == nil || err.Error() != text || WriteStatus(err, 500) != status {
		t.Fatalf("err = %v (status %d), want %q (%d)", err, WriteStatus(err, 500), text, status)
	}
}

func TestOpenKitDraft(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	pushApp(t, st, "kit.yml", "kind: studio\nprompt: two\n")
	d, err := OpenKitDraft(st, "app", 1, "local", draftNow)
	k, _ := st.GetKit("app")
	if err != nil || d.Base != 1 || d.Tree != k.Trees[1] || d.Actor != "local" || !d.OpenedAt.Equal(draftNow) || !d.UpdatedAt.Equal(draftNow) {
		t.Fatalf("open = %+v %v", d, err)
	}
	if got, ok := st.GetKitDraft("app"); !ok || got.Tree != d.Tree {
		t.Fatalf("stored draft = %+v %v", got, ok)
	}
	_, err = OpenKitDraft(st, "app", 0, "local", draftNow)
	wantErr(t, err, "kit app already has a draft", 409)
	_, err = OpenKitDraft(st, "app", 9, "local", draftNow)
	wantErr(t, err, "kit app has no version v9", 404)
	_, err = OpenKitDraft(st, "app", -1, "local", draftNow)
	wantErr(t, err, "from must be a version number", 400)
	_, err = OpenKitDraft(st, "nope", 0, "local", draftNow)
	wantErr(t, err, "no such kit", 404)
	_, err = OpenKitDraft(st, "a b\n", 0, "local", draftNow)
	wantErr(t, err, "kit name is not tag-safe (allowed: [A-Za-z0-9_.-])", 400)
	// A broken (migration) version has no tree to open.
	broken := Kit{Name: "old", Current: 1, Broken: map[int]string{1: "unreadable"}}
	if err := st.PutKitDocs([]Kit{broken}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	_, err = OpenKitDraft(st, "old", 0, "local", draftNow)
	wantErr(t, err, "kit old v1 is not stored as a tree", 400)
}

func TestKitDraftStatus(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "run.sh*", "echo\n")
	d, err := OpenKitDraft(st, "app", 0, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	s, err := KitDraftStatusOf(st, "app")
	if err != nil || s.Kit != "app" || s.Base != 1 || s.Current != 1 || s.Tree != d.Tree || s.ETag != `"`+d.Tree+`"` || s.Actor != "local" || s.Managed {
		t.Fatalf("status = %+v %v", s, err)
	}
	if len(s.Entries) != 2 || s.Entries[1].Path != "run.sh" || s.Entries[1].Mode != "0755" || s.Entries[1].Size != 5 || len(s.Entries[1].SHA256) != 64 {
		t.Fatalf("entries = %+v", s.Entries)
	}
	if s.Changes == nil || len(s.Changes) != 0 || s.Problems == nil || len(s.Problems) != 0 {
		t.Fatalf("changes %#v problems %#v (empty lists, not null)", s.Changes, s.Problems)
	}
	_, err = KitDraftStatusOf(st, "nope")
	wantErr(t, err, "no such kit", 404)
	if _, _, err := PushKitFiles(st, "web", kittreetest.Files("kit.yml", "kind: studio\n")); err != nil {
		t.Fatal(err)
	}
	_, err = KitDraftStatusOf(st, "web")
	wantErr(t, err, "kit web has no draft", 404)
	if err := st.ApplyConfig(ConfigWrite{Marks: []ConfigMark{{Kind: ConfigKindKit, Name: "app", Commit: "abc1234", Digest: "d", AppliedAt: time.Unix(1, 0).UTC()}}}); err != nil {
		t.Fatal(err)
	}
	if s, _ := KitDraftStatusOf(st, "app"); !s.Managed {
		t.Fatal("a config-managed kit's draft must say so")
	}
}

func TestDraftProblems(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	for name, tc := range map[string]struct {
		kv   []string
		want string
	}{
		"no kit.yml":     {[]string{"run.sh", "echo\n"}, "kit app: kit.yml missing at the root"},
		"invalid kind":   {[]string{"kit.yml", "kind: nope\n"}, `studio kit: kind must be "studio", got "nope"`},
		"context field":  {[]string{"kit.yml", "kind: studio\nbase:\n  context: H4sI\n"}, "base.context is no longer supported; put a Dockerfile next to kit.yml"},
		"missing parent": {[]string{"kit.yml", "kind: studio\nbase:\n  kit: ghost\n"}, "kit ghost (parent of app) is missing"},
		"missing note":   {[]string{"kit.yml", "kind: studio\nnotes:\n  - {name: a.md, read-when: x, file: a.md}\n"}, `note "a.md": file a.md is not in the kit`},
	} {
		t.Run(name, func(t *testing.T) {
			tr, blobs, err := kittree.BuildFiles(kittreetest.Files(tc.kv...))
			if err != nil {
				t.Fatal(err)
			}
			d := KitDraft{Kit: "app", Base: 1, Tree: tr.Hash(), OpenedAt: draftNow, UpdatedAt: draftNow, Actor: "local"}
			if err := st.PutKitDraft(d, KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}); err != nil {
				t.Fatal(err)
			}
			s, err := KitDraftStatusOf(st, "app")
			if err != nil || len(s.Problems) != 1 || s.Problems[0] != tc.want {
				t.Fatalf("problems = %q %v, want [%q]", s.Problems, err, tc.want)
			}
		})
	}
}

func TestDiscardKitDraft(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	d, _ := OpenKitDraft(st, "app", 0, "local", draftNow)
	_, err := DiscardKitDraft(st, nil, "app", "")
	wantErr(t, err, "draft app: If-Match is required (send the etag from the draft status)", 428)
	_, err = DiscardKitDraft(st, nil, "app", `"`+strings.Repeat("0", 64)+`"`)
	wantErr(t, err, "draft app changed since you loaded it (reload)", 412)
	// Exactly "<hex>" or bare <hex>: a weak tag, *, or unbalanced or doubled
	// quotes never match (ruling F5).
	for _, tag := range []string{`W/"` + d.Tree + `"`, "*", `"` + d.Tree, d.Tree + `"`, `""` + d.Tree + `""`, `"`} {
		_, err = DiscardKitDraft(st, nil, "app", tag)
		wantErr(t, err, "draft app changed since you loaded it (reload)", 412)
	}
	if _, err := DiscardKitDraft(st, nil, "app", d.Tree); err != nil { // the quotes are optional
		t.Fatal(err)
	}
	if _, ok := st.GetKitDraft("app"); ok {
		t.Fatal("discard left the draft")
	}
	if k, _ := st.GetKit("app"); k.Current != 1 || len(k.VersionNumbers()) != 1 {
		t.Fatal("discard touched the kit")
	}
	_, err = DiscardKitDraft(st, nil, "app", d.Tree)
	wantErr(t, err, "kit app has no draft", 404)
}

// TestRemoveKitDropsDraft: kit rm is not blocked by a draft and takes it
// (and, after GC, its tree) with it (Review Focus 3).
func TestRemoveKitDropsDraft(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	d, err := OpenKitDraft(st, "app", 0, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	tr, blobs, err := kittree.BuildFiles(kittreetest.Files("draft-only.txt", "x"))
	if err != nil {
		t.Fatal(err)
	}
	d.Tree = tr.Hash()
	if err := st.PutKitDraft(d, KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKitChecked(st, nil, "app"); err != nil {
		t.Fatalf("a draft must not block kit rm: %v", err)
	}
	if _, ok := st.GetKitDraft("app"); ok {
		t.Fatal("the draft outlived its kit")
	}
	if _, ok := st.KitTree(tr.Hash()); ok {
		t.Fatal("the draft's tree outlived its kit")
	}
	_, err = KitDraftStatusOf(st, "app")
	wantErr(t, err, "no such kit", 404)
	pushApp(t, st, "kit.yml", "kind: studio\n")
	_, err = KitDraftStatusOf(st, "app")
	wantErr(t, err, "kit app has no draft", 404)
}
