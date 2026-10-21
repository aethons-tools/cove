package jam

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/studio"
)

func chainKit(parent, prompt string) studio.StudioKit {
	sk := studio.StudioKit{Kind: studio.Kind, Prompt: prompt}
	sk.Base.Kit = parent
	return sk
}

func mustEnsure(t *testing.T, st Store, name string, sk studio.StudioKit) KitRef {
	t.Helper()
	ref, _, err := ensureStudioKit(st, name, sk)
	if err != nil {
		t.Fatalf("ensure %s: %v", name, err)
	}
	return ref
}

func TestResolveKitChainThreeLinks(t *testing.T) {
	st := NewMemStore()
	mustEnsure(t, st, "root", chainKit("", "r"))
	mustEnsure(t, st, "mid", chainKit("root@v1", "m"))
	mustEnsure(t, st, "mid", chainKit("root@v1", "m2")) // mid is now v2
	chain, err := ResolveKitChain(st, "leaf", chainKit("mid", "l"))
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain len %d", len(chain))
	}
	want := []struct {
		id     string
		ver    int
		follow bool
	}{{"root", 1, false}, {"mid", 2, true}, {"leaf", 0, false}}
	for i, w := range want {
		l := chain[i]
		if l.Ref.ID != w.id || l.Ref.Version != w.ver || l.Follow != w.follow || l.Ref.Digest == "" {
			t.Errorf("link %d = %+v, want %+v", i, l, w)
		}
	}
	// root is reached by mid's pinned reference, mid by leaf's following one.
	if chain[0].Follow || !chain[1].Follow {
		t.Error("follow flags wrong")
	}
}

func TestResolveKitChainErrors(t *testing.T) {
	st := NewMemStore()
	mustEnsure(t, st, "web-dev", chainKit("", ""))
	for name, tc := range map[string]struct {
		self, parent, want string
	}{
		"missing":         {"my-app", "nope", "kit nope (parent of my-app) is missing"},
		"missing version": {"my-app", "web-dev@v9", "kit web-dev has no version v9 (parent of my-app)"},
		"self":            {"a", "a", "cycle: a → a"},
	} {
		_, err := ResolveKitChain(st, tc.self, chainKit(tc.parent, ""))
		if !errors.Is(err, ErrKitChain) || err.Error() != tc.want {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestResolveKitChainInvalidParent(t *testing.T) {
	st := NewMemStore()
	if _, err := st.PushKit("web-dev", "kind: [unterminated"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PushKit("web-dev", "kind: studio\nbogus: 1\n"); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveKitChain(st, "my-app", chainKit("web-dev@v2", ""))
	if !errors.Is(err, ErrKitChain) || !strings.HasPrefix(err.Error(), "kit web-dev@v2 (parent of my-app) is invalid: ") {
		t.Fatalf("%v", err)
	}
}

func TestResolveKitChainCycle(t *testing.T) {
	st := NewMemStore()
	mustEnsure(t, st, "a", chainKit("", ""))
	mustEnsure(t, st, "b", chainKit("a", ""))
	_, _, err := ensureStudioKit(st, "a", chainKit("b", ""))
	if err == nil || err.Error() != "cycle: a → b → a" || WriteStatus(err, 500) != http.StatusBadRequest {
		t.Fatalf("%v", err)
	}
	if k, _ := st.GetKit("a"); k.Current != 1 {
		t.Error("cyclic push recorded a version")
	}
}

func TestResolveKitChainTooDeep(t *testing.T) {
	st := NewMemStore()
	mustEnsure(t, st, "d", chainKit("", ""))
	mustEnsure(t, st, "c", chainKit("d", ""))
	mustEnsure(t, st, "b", chainKit("c", ""))
	mustEnsure(t, st, "a", chainKit("b", "")) // four links: allowed
	_, _, err := ensureStudioKit(st, "top", chainKit("a", ""))
	if err == nil || err.Error() != "kit chain too deep (max 4): top → a → b → c → d" || WriteStatus(err, 500) != http.StatusBadRequest {
		t.Fatalf("%v", err)
	}
	if _, err := ResolveKitChain(st, "top", chainKit("a", "")); !errors.Is(err, ErrKitChain) {
		t.Fatalf("not ErrKitChain: %v", err)
	}
}

func TestEnsureKitMergedBudgets(t *testing.T) {
	st := NewMemStore()
	mustEnsure(t, st, "p", chainKit("", strings.Repeat("x", 500)))
	_, _, err := ensureStudioKit(st, "c", chainKit("p", strings.Repeat("y", 500)))
	want := studio.Merge([]studio.StudioKit{chainKit("", strings.Repeat("x", 500)), chainKit("", strings.Repeat("y", 500))}).CheckPrompt()
	if want == nil || err == nil || err.Error() != want.Error() || WriteStatus(err, 500) != http.StatusBadRequest {
		t.Fatalf("prompt: %v (want %v)", err, want)
	}

	parent := chainKit("", "")
	child := chainKit("n", "")
	for i := range 11 {
		parent.Notes = append(parent.Notes, studio.KitNote{Name: "p" + string(rune('a'+i)) + ".md", ReadWhen: "w", Body: "b"})
		child.Notes = append(child.Notes, studio.KitNote{Name: "c" + string(rune('a'+i)) + ".md", ReadWhen: "w", Body: "b"})
	}
	mustEnsure(t, st, "n", parent)
	_, _, err = ensureStudioKit(st, "nc", child)
	want = studio.Merge([]studio.StudioKit{parent, child}).CheckNotes()
	if want == nil || err == nil || err.Error() != want.Error() || WriteStatus(err, 500) != http.StatusBadRequest {
		t.Fatalf("notes: %v (want %v)", err, want)
	}
}

func TestEnsureKitRecordsParent(t *testing.T) {
	st := NewMemStore()
	mustEnsure(t, st, "p", chainKit("", "a"))
	mustEnsure(t, st, "p", chainKit("", "b")) // p v2
	ref := mustEnsure(t, st, "c", chainKit("p", ""))
	k, _ := st.GetKit("c")
	if m := k.Meta[ref.Version]; m.Parent == nil || *m.Parent != (KitParentRef{Name: "p", Version: 2}) {
		t.Fatalf("meta = %+v", m)
	}
	// pinned parent records the pinned version
	ref = mustEnsure(t, st, "c2", chainKit("p@v1", ""))
	k, _ = st.GetKit("c2")
	if m := k.Meta[ref.Version]; m.Parent == nil || m.Parent.Version != 1 {
		t.Fatalf("pinned meta = %+v", m)
	}
	// metadata passed by the caller keeps its fields
	m := KitVersionMeta{Source: &KitSource{Repo: "r", Ref: "main"}}
	ref, _, err := ensureStudioKitMeta(st, "c3", chainKit("p", ""), m, nil)
	if err != nil {
		t.Fatal(err)
	}
	k, _ = st.GetKit("c3")
	if got := k.Meta[ref.Version]; got.Parent == nil || got.Source == nil || got.Source.Repo != "r" {
		t.Fatalf("meta = %+v", got)
	}
	// the child is not re-pushed when its parent moves
	mustEnsure(t, st, "p", chainKit("", "c"))
	if _, unchanged, err := ensureStudioKit(st, "c", chainKit("p", "")); err != nil || !unchanged {
		t.Fatalf("unchanged=%v err=%v", unchanged, err)
	}
	if k, _ := st.GetKit("c"); k.Current != 1 {
		t.Errorf("child current v%d", k.Current)
	}
}

func TestEnsureKitUnchainedHasNoParent(t *testing.T) {
	st := NewMemStore()
	ref := mustEnsure(t, st, "solo", chainKit("", "x"))
	k, _ := st.GetKit("solo")
	if m := k.Meta[ref.Version]; m.Parent != nil {
		t.Fatalf("parent = %+v", m.Parent)
	}
	sk := chainKit("", "x")
	if ref.Digest != studio.BuildDigest(sk, harnessinstall.Default()) {
		t.Error("unchained digest changed")
	}
}

func TestKitsReferencing(t *testing.T) {
	st := NewMemStore()
	mustEnsure(t, st, "p", chainKit("", ""))
	mustEnsure(t, st, "f2", chainKit("p", ""))
	mustEnsure(t, st, "f1", chainKit("p", ""))
	mustEnsure(t, st, "pin", chainKit("p@v1", ""))
	mustEnsure(t, st, "other", chainKit("", ""))
	// a kit that no longer references p in its current version is not listed
	mustEnsure(t, st, "gone", chainKit("p", ""))
	mustEnsure(t, st, "gone", chainKit("", "x"))
	follow, pinned := KitsReferencing(st, "p")
	if strings.Join(follow, ",") != "f1,f2" || strings.Join(pinned, ",") != "pin" {
		t.Fatalf("follow=%v pinned=%v", follow, pinned)
	}
}
