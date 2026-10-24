package jam

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
)

// fakeFetcher serves an in-memory repo (commit -> path -> content, ref ->
// commit) as a temp checkout.
type fakeFetcher struct {
	t        *testing.T
	refs     map[string]string
	commits  map[string]map[string]string
	mu       sync.Mutex // guards fetches and failNext: imports fetch concurrently
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
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	f.fetches++
	f.mu.Unlock()
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
	if k.Source == nil || k.Source.Commit != "c1" || len(k.VersionNumbers()) != 1 {
		t.Fatalf("track: %+v versions=%d", k.Source, len(k.VersionNumbers()))
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
	if k, _ := st.GetKit("web-dev"); len(k.VersionNumbers()) != 1 || k.Source.Commit != "c2" {
		t.Fatalf("versions=%d commit=%s", len(k.VersionNumbers()), k.Source.Commit)
	}
	fetched := f.fetches
	res, err = SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(21, 5))
	if err != nil || !res.Unchanged || res.Commit != "c2" || f.fetches != fetched {
		t.Fatalf("second sync at c2 must not fetch: %+v %v fetches=%d want %d", res, err, f.fetches, fetched)
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
	if k.Source != nil || len(k.VersionNumbers()) != 1 {
		t.Fatalf("untrack: %+v", k)
	}
	if _, err := SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(50, 0)); !errors.Is(err, ErrNotTracked) {
		t.Fatalf("got %v, want ErrNotTracked", err)
	}
	if err := UntrackKit(st, nil, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// failStore fails the writes it is told to, so a test can show an operation
// leaves nothing half-written.
type failStore struct {
	Store
	failPush, failSet bool
}

func (s *failStore) PushKitTracked(name, config string, meta KitVersionMeta, track *KitSource) (int, error) {
	if s.failPush {
		return 0, errors.New("push failed")
	}
	return s.Store.PushKitTracked(name, config, meta, track)
}

// PutKitDocs fails as the write it stands for: with failPush when any doc
// adds a version, else (a source-only move) with failSet.
func (s *failStore) PutKitDocs(kits []Kit, objs KitObjects) error {
	push := false
	for _, k := range kits {
		cur, _ := s.Store.GetKit(k.Name)
		push = push || len(k.VersionNumbers()) > len(cur.VersionNumbers())
	}
	if push && s.failPush {
		return errors.New("push failed")
	}
	if !push && s.failSet {
		return errors.New("set failed")
	}
	return s.Store.PutKitDocs(kits, objs)
}

func (s *failStore) SetKitSource(name string, src *KitSource) error {
	if s.failSet {
		return errors.New("set failed")
	}
	return s.Store.SetKitSource(name, src)
}

func TestSyncWritesVersionAndSourceTogether(t *testing.T) {
	mem, f := trackedFixture(t)
	st := &failStore{Store: mem}
	f.commit("main", "c2", map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: changed\n"})
	before, _ := mem.GetKit("web-dev")

	st.failPush = true
	if _, err := SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(60, 0)); err == nil {
		t.Fatal("expected error")
	}
	if after, _ := mem.GetKit("web-dev"); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed push half-wrote: %+v", after)
	}

	// A new version never needs a second write: a failing SetKitSource is not reached.
	st.failPush, st.failSet = false, true
	res, err := SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(61, 0))
	if err != nil || res.Version != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if k, _ := mem.GetKit("web-dev"); k.Source.Commit != "c2" || k.Meta[2].Source.Commit != "c2" {
		t.Fatalf("version and source must move together: %+v", k)
	}

	// Unchanged content at a new commit only moves the source; if that fails, nothing changed.
	f.commit("main", "c3", map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: changed\n"})
	before, _ = mem.GetKit("web-dev")
	if _, err := SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(62, 0)); err == nil {
		t.Fatal("expected error")
	}
	if after, _ := mem.GetKit("web-dev"); !reflect.DeepEqual(before, after) {
		t.Fatal("failed source update changed the kit")
	}
}

func TestConcurrentSyncsDoNotDuplicate(t *testing.T) {
	st, f := trackedFixture(t)
	f.commit("main", "c2", map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: changed\n"})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = SyncKit(context.Background(), st, f, nil, "web-dev", time.Unix(70, 0))
		}()
	}
	wg.Wait()
	if k, _ := st.GetKit("web-dev"); len(k.VersionNumbers()) != 2 {
		t.Fatalf("versions=%d, want 2", len(k.VersionNumbers()))
	}
	// One fetch for the track, one for the first sync: the rest wait on the
	// kit lock, then see the commit is current.
	if f.fetches != 2 {
		t.Fatalf("fetches=%d, want 2", f.fetches)
	}
}

// An untracked import of the tracked source moves the tracked commit with it, so
// the next sync does not mistake the kit for current.
func TestImportOfTrackedSourceAdvancesTrackedCommit(t *testing.T) {
	st, f := trackedFixture(t) // tracked at c1 (prompt: hi)
	ctx := context.Background()
	src := gitsource.Source{Repo: "https://github.com/acme/kits", Dir: "web", Ref: "main"}
	kit := func(p string) map[string]string {
		return map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: " + p + "\n"}
	}
	f.commit("main", "c2", kit("two"))
	if _, err := SyncKit(ctx, st, f, nil, "web-dev", time.Unix(80, 0)); err != nil {
		t.Fatal(err)
	}
	f.refs["main"] = "c1" // force-moved back
	res, err := ImportKit(ctx, st, f, nil, "web-dev", src, time.Unix(81, 0))
	if err != nil || res.Commit != "c1" {
		t.Fatalf("%+v %v", res, err)
	}
	if k, _ := st.GetKit("web-dev"); k.Source.Commit != "c1" {
		t.Fatalf("tracked commit = %s, want c1", k.Source.Commit)
	}
	f.refs["main"] = "c2"
	res, err = SyncKit(ctx, st, f, nil, "web-dev", time.Unix(82, 0))
	if err != nil || res.Unchanged {
		t.Fatalf("sync must see c2 as changed: %+v %v", res, err)
	}
	k, _ := st.GetKit("web-dev")
	if cfg, _ := st.KitConfig("web-dev", k.Current); !strings.Contains(cfg, "two") || k.Source.Commit != "c2" {
		t.Fatalf("not on c2 content: %s %+v", cfg, k.Source)
	}
	// The unchanged path too: importing identical content still moves the commit.
	f.commit("main", "c3", kit("two"))
	if _, err := ImportKit(ctx, st, f, nil, "web-dev", src, time.Unix(83, 0)); err != nil {
		t.Fatal(err)
	}
	if k, _ := st.GetKit("web-dev"); k.Source.Commit != "c3" {
		t.Fatalf("unchanged import left tracked commit %s", k.Source.Commit)
	}
}

func TestKitLocksPruned(t *testing.T) {
	lockKit("a")()
	kitLocks.mu.Lock()
	defer kitLocks.mu.Unlock()
	if len(kitLocks.m) != 0 {
		t.Fatalf("leaked %d lock entries", len(kitLocks.m))
	}
}

// A kit whose context-dir is the repo root must not pack Jam's clone
// metadata: refetching the same commit is then `unchanged`, not a new version.
func TestImportRootContextDirIsDeterministic(t *testing.T) {
	url, _, _ := gittest.Serve(t, map[string]string{
		"kit.yml":    "kind: studio\nname: rootkit\nbase:\n  context-dir: .\n",
		"Dockerfile": "FROM ${COVE_BASE_IMAGE}\nRUN true\n",
	})
	st := NewMemStore()
	f := &gitsource.Fetcher{Timeout: 30 * time.Second}
	src := gitsource.Source{Repo: url, Dir: ".", Ref: "main"}
	res, err := ImportKit(context.Background(), st, f, nil, "", src, time.Unix(10, 0))
	if err != nil || res.Version != 1 || res.Unchanged {
		t.Fatalf("first import: %+v %v", res, err)
	}
	res, err = ImportKit(context.Background(), st, f, nil, "", src, time.Unix(11, 0))
	if err != nil || !res.Unchanged || res.Version != 1 {
		t.Fatalf("second import of the same commit: %+v %v, want unchanged v1", res, err)
	}
}
