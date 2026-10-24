package jam

import (
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
	"github.com/aethons-tools/cove/internal/studio"
)

func harnessDefault() harnessinstall.Install { return harnessinstall.Default() }

// seedKit stores sk under name the way a pre-tree Jam would have: unchecked,
// as kit.yml text (Store.PushKit). For fixtures of packed-context kits.
func seedKit(t *testing.T, st Store, name string, sk studio.StudioKit) {
	t.Helper()
	text, err := sk.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PushKit(name, string(text)); err != nil {
		t.Fatal(err)
	}
}

// pushKit pushes sk under name as a one-file tree (its rendered kit.yml),
// the way EnsureStudioKit does, reporting whether the version was unchanged.
func pushKit(st Store, name string, sk studio.StudioKit) (KitRef, bool, error) {
	yml, err := studio.RenderKitYAML(sk)
	if err != nil {
		return KitRef{}, false, err
	}
	return PushKitFiles(st, name, []kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: yml}})
}

func TestPushKitFilesStoresTheDirectory(t *testing.T) {
	st := NewMemStore()
	ref, unchanged, err := PushKitFiles(st, "app", kittreetest.Files("kit.yml", "kind: studio\n# a comment\n", "Dockerfile", dockerfile, "run.sh*", "echo\n", ".dockerignore", "*.log\n", "debug.log", "x"))
	if err != nil || unchanged || ref.Version != 1 {
		t.Fatalf("push = %+v %v %v", ref, unchanged, err)
	}
	v, _ := LoadKitVersion(st, "app", 0)
	if string(v.KitYML) != "kind: studio\n# a comment\n" {
		t.Fatalf("kit.yml not stored verbatim: %q", v.KitYML)
	}
	if _, ok := v.Tree.Lookup("debug.log"); ok {
		t.Fatal(".dockerignore must filter the push")
	}
	if e, _ := v.Tree.Lookup("run.sh"); e.Mode != 0o755 {
		t.Fatal("exec bit lost")
	}
	if ref.Digest != studio.ChainDigest([]studio.StudioKit{v.Kit}, harnessDefault()) || v.Kit.BuildContext == "" {
		t.Fatalf("digest %s, build context %q", ref.Digest, v.Kit.BuildContext)
	}
	again, unchanged, err := PushKitFiles(st, "app", kittreetest.Files("kit.yml", "kind: studio\n# a comment\n", "Dockerfile", dockerfile, "run.sh*", "echo\n", ".dockerignore", "*.log\n"))
	if err != nil || !unchanged || again.Version != 1 {
		t.Fatalf("same tree = %+v %v %v", again, unchanged, err)
	}
}

func TestPushKitFilesRefusals(t *testing.T) {
	st := NewMemStore()
	for name, tc := range map[string]struct {
		files []kittree.File
		want  string
	}{
		"no kit.yml":     {kittreetest.Files("Dockerfile", "FROM x"), "kit app: kit.yml missing at the root"},
		"context":        {kittreetest.Files("kit.yml", "kind: studio\nbase:\n  context: H4sI\n"), "base.context is no longer supported; put a Dockerfile next to kit.yml"},
		"context-dir":    {kittreetest.Files("kit.yml", "kind: studio\nbase:\n  context-dir: ctx\n"), "base.context-dir is no longer supported; put a Dockerfile next to kit.yml"},
		"path parent":    {kittreetest.Files("kit.yml", "kind: studio\nbase: {kit: ../web}\n"), "path parents resolve only in a git import (kit import/track --git)"},
		"invalid kit":    {kittreetest.Files("kit.yml", "kind: nope\n"), `studio kit: kind must be "studio", got "nope"`},
		"missing note":   {kittreetest.Files("kit.yml", "kind: studio\nnotes:\n  - {name: a.md, read-when: x, file: a.md}\n"), `note "a.md": file a.md is not in the kit`},
		"image + Docker": {kittreetest.Files("kit.yml", "kind: studio\nbase: {image: ghcr.io/x@sha256:1}\n", "Dockerfile", dockerfile), "studio kit: base.image and a Dockerfile next to kit.yml are mutually exclusive"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := PushKitFiles(st, "app", tc.files)
			if err == nil || err.Error() != tc.want || WriteStatus(err, 500) != 400 {
				t.Fatalf("err = %v (status %d), want %q", err, WriteStatus(err, 500), tc.want)
			}
		})
	}
	if _, ok := st.GetKit("app"); ok {
		t.Fatal("a refused push stored a kit")
	}
}

func TestReplaceKitYMLKeepsTheOtherFiles(t *testing.T) {
	st := NewMemStore()
	if _, _, err := PushKitFiles(st, "app", kittreetest.Files("kit.yml", "kind: studio\n", "run.sh*", "echo\n")); err != nil {
		t.Fatal(err)
	}
	ref, _, err := ReplaceKitYML(st, "app", "kind: studio\nprompt: hi\n")
	if err != nil || ref.Version != 2 {
		t.Fatalf("replace = %+v %v", ref, err)
	}
	v, _ := LoadKitVersion(st, "app", 0)
	if e, ok := v.Tree.Lookup("run.sh"); !ok || e.Mode != 0o755 || v.Kit.Prompt != "hi" {
		t.Fatalf("tree = %+v", v.Tree)
	}
}

func TestEnsureDefaultStudioKitIsAOneFileTree(t *testing.T) {
	st := NewMemStore()
	ref, err := EnsureDefaultStudioKit(st)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := LoadKitVersion(st, studio.DefaultStudioKitID, ref.Version)
	want, _ := studio.RenderKitYAML(studio.DefaultStudioKit())
	if len(v.Tree.Entries) != 1 || string(v.KitYML) != string(want) {
		t.Fatalf("default kit tree %+v kit.yml %q", v.Tree, v.KitYML)
	}
	if again, _ := EnsureDefaultStudioKit(st); again.Version != ref.Version {
		t.Fatal("seeding the default kit twice made a new version")
	}
}

// blobHookStore runs onBlob (once) the first time a blob is read: a push
// racing a reader of the current files.
type blobHookStore struct {
	Store
	once   sync.Once
	onBlob func()
}

func (s *blobHookStore) KitBlob(sha string) ([]byte, bool) {
	s.once.Do(s.onBlob)
	return s.Store.KitBlob(sha)
}

// A push that lands while ReplaceKitYML reads the current files is not
// reverted: the read and the push are one step under kitMu, so the racing
// push comes after it and keeps its file.
func TestReplaceKitYMLDoesNotRevertARacingPush(t *testing.T) {
	mem := NewMemStore()
	if _, _, err := PushKitFiles(mem, "app", kittreetest.Files("kit.yml", "kind: studio\n", "a.sh", "a\n")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	st := &blobHookStore{Store: mem}
	st.onBlob = func() {
		go func() {
			_, _, err := PushKitFiles(mem, "app", kittreetest.Files("kit.yml", "kind: studio\n", "a.sh", "a\n", "b.sh", "b\n"))
			done <- err
		}()
		select { // the racing push either lands now (no lock held) or waits for the lock
		case err := <-done:
			done <- err
		case <-time.After(100 * time.Millisecond):
		}
	}
	if _, _, err := ReplaceKitYML(st, "app", "kind: studio\nprompt: hi\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	v, _ := LoadKitVersion(mem, "app", 0)
	if _, ok := v.Tree.Lookup("b.sh"); !ok || v.Version != 3 {
		t.Fatalf("current v%d %+v: the racing push was reverted", v.Version, v.Tree)
	}
}
