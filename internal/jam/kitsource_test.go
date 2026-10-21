package jam

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// fakeFetcher serves an in-memory repo (commit -> path -> content, ref ->
// commit) as a temp checkout.
type fakeFetcher struct {
	t        *testing.T
	refs     map[string]string
	commits  map[string]map[string]string
	fetches  int
	failNext error
}

func newFakeFetcher(t *testing.T, refs map[string]string, commits map[string]map[string]string) *fakeFetcher {
	return &fakeFetcher{t: t, refs: refs, commits: commits}
}

func (f *fakeFetcher) commit(ref, sha string, files map[string]string) {
	f.commits[sha] = files
	f.refs[ref] = sha
}

func (f *fakeFetcher) fail() error {
	err := f.failNext
	f.failNext = nil
	return err
}

func (f *fakeFetcher) Resolve(_ context.Context, src gitsource.Source) (string, error) {
	if err := f.fail(); err != nil {
		return "", err
	}
	sha, ok := f.refs[src.Ref]
	if !ok {
		return "", fmt.Errorf("no ref %q", src.Ref)
	}
	return sha, nil
}

func (f *fakeFetcher) Fetch(ctx context.Context, src gitsource.Source, fn func(root, commit string) error) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.fetches++
	sha, err := f.Resolve(ctx, src)
	if err != nil {
		return err
	}
	root := f.t.TempDir()
	for p, c := range f.commits[sha] {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			return err
		}
	}
	return fn(root, sha)
}

const webKit = "kind: studio\nname: web-dev\nprompt: hi\n"

func trackedFixture(t *testing.T) (*MemStore, *fakeFetcher) {
	t.Helper()
	st := NewMemStore()
	f := newFakeFetcher(t, map[string]string{"main": "c1"}, map[string]map[string]string{"c1": {"web/kit.yml": webKit}})
	src := gitsource.Source{Repo: "https://github.com/acme/kits", Dir: "web", Ref: "main"}
	if _, err := TrackKit(context.Background(), st, f, nil, "web-dev", src, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	return st, f
}

func TestImportAndTrackRecordProvenance(t *testing.T) {
	st := NewMemStore()
	f := newFakeFetcher(t, map[string]string{"main": "c1"}, map[string]map[string]string{"c1": {"web/kit.yml": webKit}})
	src := gitsource.Source{Repo: "https://github.com/acme/kits", Dir: "web", Ref: "main"}
	res, err := ImportKit(context.Background(), st, f, nil, "", src, time.Unix(10, 0))
	if err != nil || res.Name != "web-dev" || res.Version != 1 || res.Commit != "c1" {
		t.Fatalf("%+v %v", res, err)
	}
	k, _ := st.GetKit("web-dev")
	if m := k.Meta[1]; m.Source == nil || m.Source.Commit != "c1" || m.Source.Dir != "web" || k.Source != nil {
		t.Fatalf("import must record version provenance and not track: %+v %+v", m, k.Source)
	}
	if _, err := TrackKit(context.Background(), st, f, nil, "web-dev", src, time.Unix(11, 0)); err != nil {
		t.Fatal(err)
	}
	k, _ = st.GetKit("web-dev")
	if k.Source == nil || k.Source.Commit != "c1" || len(k.Versions) != 1 {
		t.Fatalf("track: %+v versions=%d", k.Source, len(k.Versions))
	}
}

func TestImportNameFallbacks(t *testing.T) {
	f := newFakeFetcher(t, map[string]string{"main": "c1"}, map[string]map[string]string{"c1": {"kit.yml": "kind: studio\nprompt: hi\n"}})
	for repo, want := range map[string]string{
		"https://github.com/acme/my-kit.git": "my-kit",
		"https://github.com/acme/other/":     "other",
	} {
		res, err := ImportKit(context.Background(), NewMemStore(), f, nil, "", gitsource.Source{Repo: repo, Dir: ".", Ref: "main"}, time.Unix(1, 0))
		if err != nil || res.Name != want {
			t.Errorf("%s: %+v %v, want name %q", repo, res, err, want)
		}
	}
}

func TestImportRefusals(t *testing.T) {
	src := gitsource.Source{Repo: "https://github.com/acme/kits", Dir: "web", Ref: "main"}
	for name, c := range map[string]struct {
		kit, name string
	}{
		"invalid kit.yml": {"kind: studio\nbogus: [", ""},
		"bad name":        {webKit, "Bad Name!"},
	} {
		st := NewMemStore()
		f := newFakeFetcher(t, map[string]string{"main": "c1"}, map[string]map[string]string{"c1": {"web/kit.yml": c.kit}})
		if _, err := ImportKit(context.Background(), st, f, nil, c.name, src, time.Unix(1, 0)); err == nil {
			t.Errorf("%s: expected error", name)
		}
		if len(st.ListKits()) != 0 {
			t.Errorf("%s: store touched", name)
		}
	}
}

func TestSyncUnchanged(t *testing.T) {
	st, f := trackedFixture(t) // web-dev tracked at c1
	res, err := SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(20, 0))
	if err != nil || !res.Unchanged || f.fetches != 1 /* only the track's */ {
		t.Fatalf("same commit: %+v %v fetches=%d", res, err, f.fetches)
	}
	f.commit("main", "c2", map[string]string{"web/kit.yml": webKit}) // new commit, identical kit
	res, err = SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(21, 0))
	if err != nil || !res.Unchanged || res.Commit != "c2" {
		t.Fatalf("identical content: %+v %v", res, err)
	}
	if k, _ := st.GetKit("web-dev"); len(k.Versions) != 1 || k.Source.Commit != "c2" {
		t.Fatalf("versions=%d commit=%s", len(k.Versions), k.Source.Commit)
	}
	f.commit("main", "c3", map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: changed\n"})
	if res, _ := SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(22, 0)); res.Unchanged || res.Version != 2 {
		t.Fatalf("changed: %+v", res)
	}
}

func TestSyncFailureLeavesKit(t *testing.T) {
	st, f := trackedFixture(t)
	f.failNext = errors.New("auth failed for https://github.com/acme/ (check kits.git cred gh-kits)")
	before, _ := st.GetKit("web-dev")
	if _, err := SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(30, 0)); err == nil {
		t.Fatal("expected error")
	}
	f.refs["main"] = "c5"
	f.commits["c5"] = map[string]string{"web/kit.yml": "kind: studio\nbogus: ["}
	if _, err := SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(31, 0)); err == nil {
		t.Fatal("expected error")
	}
	after, _ := st.GetKit("web-dev")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed sync changed the kit")
	}
}

func TestMixedProvenance(t *testing.T) {
	st, f := trackedFixture(t)
	if _, _, err := PushStudioKit(st, "web-dev", `{"kind":"studio","prompt":"local edit"}`); err != nil {
		t.Fatal(err)
	}
	k, _ := st.GetKit("web-dev")
	if k.Meta[2].Source != nil {
		t.Fatal("local push must be local")
	}
	f.commit("main", "c9", map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: from git\n"})
	if res, err := SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(40, 0)); err != nil || res.Version != 3 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestSyncUntrackedAndUntrack(t *testing.T) {
	st, f := trackedFixture(t)
	if err := UntrackKit(st, nil, "web-dev"); err != nil {
		t.Fatal(err)
	}
	k, _ := st.GetKit("web-dev")
	if k.Source != nil || len(k.Versions) != 1 {
		t.Fatalf("untrack: %+v", k)
	}
	if _, err := SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(50, 0)); !errors.Is(err, ErrNotTracked) {
		t.Fatalf("got %v, want ErrNotTracked", err)
	}
	if err := UntrackKit(st, nil, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
