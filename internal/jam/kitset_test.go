package jam

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/studio"
)

const setRepo = "https://github.com/acme/kits"

// appKit is a kit whose parent is the sibling dir web (webKit, web-dev).
const appKit = "kind: studio\nbase:\n  kit: ../web\nprompt: app\n"

func setSrc(dir string) gitsource.Source {
	return gitsource.Source{Repo: setRepo, Dir: dir, Ref: "main"}
}

// setFixture is a fetcher serving files at main = c1.
func setFixture(t *testing.T, files map[string]string) *fakeFetcher {
	t.Helper()
	return newFakeFetcher(t, map[string]string{"main": "c1"}, map[string]map[string]string{"c1": files})
}

func storedKit(t *testing.T, st Store, name string, version int) studio.StudioKit {
	t.Helper()
	text, ok := st.KitConfig(name, version)
	if !ok {
		t.Fatalf("no %s@v%d", name, version)
	}
	sk, err := studio.ParseStudioKit([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return sk
}

// putRecorder records the kit names of each PutKitDocs call, in order.
type putRecorder struct {
	Store
	puts [][]string
}

func (s *putRecorder) PutKitDocs(kits []Kit) error {
	var names []string
	for _, k := range kits {
		names = append(names, k.Name)
	}
	s.puts = append(s.puts, names)
	return s.Store.PutKitDocs(kits)
}

func TestSetImportTwoKits(t *testing.T) {
	st := NewMemStore()
	f := setFixture(t, map[string]string{"web/kit.yml": webKit, "app/kit.yml": appKit})
	res, err := ImportKit(context.Background(), st, f, nil, "", setSrc("app"), time.Unix(10, 0))
	want := KitSourceResult{Name: "app", Version: 1, Commit: "c1",
		Also: []KitSourceResult{{Name: "web-dev", Version: 1, Commit: "c1"}}}
	if err != nil || !reflect.DeepEqual(res, want) {
		t.Fatalf("%+v %v", res, err)
	}
	if got := storedKit(t, st, "app", 1).Base.Kit; got != "web-dev@v1" {
		t.Fatalf("app base.kit = %q, want web-dev@v1", got)
	}
	for name, dir := range map[string]string{"web-dev": "web", "app": "app"} {
		k, _ := st.GetKit(name)
		m := k.Meta[1]
		if m.Source == nil || *m.Source != (KitSource{Repo: setRepo, Dir: dir, Ref: "main", Commit: "c1"}) || k.Source != nil {
			t.Fatalf("%s: meta %+v source %+v", name, m.Source, k.Source)
		}
	}
	if k, _ := st.GetKit("app"); k.Meta[1].Parent == nil || *k.Meta[1].Parent != (KitParentRef{Name: "web-dev", Version: 1}) {
		t.Fatalf("app parent = %+v", k.Meta[1].Parent)
	}
	// The same commit again: both unchanged, nothing written.
	rec := &putRecorder{Store: st}
	res, err = ImportKit(context.Background(), rec, f, nil, "", setSrc("app"), time.Unix(11, 0))
	if err != nil || !res.Unchanged || res.Version != 1 || len(res.Also) != 1 || !res.Also[0].Unchanged || len(rec.puts) != 0 {
		t.Fatalf("re-import: %+v %v puts=%v", res, err, rec.puts)
	}
}

func TestSetImportThreeLevels(t *testing.T) {
	st := &putRecorder{Store: NewMemStore()}
	f := setFixture(t, map[string]string{
		"lib/base/kit.yml": "kind: studio\nprompt: base\n",
		"web/kit.yml":      "kind: studio\nname: web-dev\nbase:\n  kit: ../lib/base\n",
		"app/kit.yml":      appKit,
	})
	res, err := ImportKit(context.Background(), st, f, nil, "", setSrc("app"), time.Unix(10, 0))
	if err != nil || res.Name != "app" || len(res.Also) != 2 || res.Also[0].Name != "base" || res.Also[1].Name != "web-dev" {
		t.Fatalf("%+v %v", res, err)
	}
	if want := [][]string{{"base", "web-dev", "app"}}; !reflect.DeepEqual(st.puts, want) {
		t.Fatalf("puts = %v, want %v", st.puts, want)
	}
	if got := storedKit(t, st, "web-dev", 1).Base.Kit; got != "base@v1" {
		t.Fatalf("web-dev base.kit = %q", got)
	}
	if k, _ := st.GetKit("base"); k.Meta[1].Source.Dir != "lib/base" {
		t.Fatalf("base dir = %q", k.Meta[1].Source.Dir)
	}
	def, err := StudioKitDefinition(st, "app", harnessinstall.Default())
	if err != nil || len(def.Chain) != 3 || def.Kit.Prompt == "" {
		t.Fatalf("definition: %+v %v", def, err)
	}
}

// trackedSet tracks //app (web-dev v1, app v1 pinned web-dev@v1) at c1.
func trackedSet(t *testing.T) (*MemStore, *fakeFetcher) {
	t.Helper()
	st := NewMemStore()
	f := setFixture(t, map[string]string{"web/kit.yml": webKit, "app/kit.yml": appKit})
	if _, err := TrackKit(context.Background(), st, f, nil, "", setSrc("app"), time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	return st, f
}

func TestSetTrackSetsEverySource(t *testing.T) {
	st, _ := trackedSet(t)
	for name, dir := range map[string]string{"web-dev": "web", "app": "app"} {
		k, _ := st.GetKit(name)
		if k.Source == nil || *k.Source != (KitSource{Repo: setRepo, Dir: dir, Ref: "main", Commit: "c1"}) {
			t.Fatalf("%s source = %+v", name, k.Source)
		}
	}
}

func TestSetSyncParentUnchanged(t *testing.T) {
	st, f := trackedSet(t)
	f.commit("main", "c2", map[string]string{"web/kit.yml": webKit, "app/kit.yml": strings.Replace(appKit, "prompt: app", "prompt: app2", 1)})
	res, err := SyncKit(context.Background(), st, f, nil, "app", time.Unix(20, 0))
	want := KitSourceResult{Name: "app", Version: 2, Commit: "c2",
		Also: []KitSourceResult{{Name: "web-dev", Version: 1, Unchanged: true, Commit: "c2"}}}
	if err != nil || !reflect.DeepEqual(res, want) {
		t.Fatalf("%+v %v", res, err)
	}
	web, _ := st.GetKit("web-dev")
	if len(web.Versions) != 1 || web.Source.Commit != "c2" {
		t.Fatalf("web-dev: versions=%d source=%+v", len(web.Versions), web.Source)
	}
	if got := storedKit(t, st, "app", 2).Base.Kit; got != "web-dev@v1" {
		t.Fatalf("app v2 base.kit = %q", got)
	}
	if app, _ := st.GetKit("app"); app.Source.Commit != "c2" || app.Meta[2].Source.Commit != "c2" {
		t.Fatalf("app: %+v", app)
	}
}

func TestSetSyncParentChangedChildRepinned(t *testing.T) {
	st, f := trackedSet(t)
	f.commit("main", "c2", map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: two\n", "app/kit.yml": appKit})
	res, err := SyncKit(context.Background(), st, f, nil, "app", time.Unix(20, 0))
	want := KitSourceResult{Name: "app", Version: 2, Commit: "c2",
		Also: []KitSourceResult{{Name: "web-dev", Version: 2, Commit: "c2"}}}
	if err != nil || !reflect.DeepEqual(res, want) {
		t.Fatalf("%+v %v", res, err)
	}
	if got := storedKit(t, st, "app", 2).Base.Kit; got != "web-dev@v2" {
		t.Fatalf("app v2 base.kit = %q", got)
	}
	if app, _ := st.GetKit("app"); *app.Meta[2].Parent != (KitParentRef{Name: "web-dev", Version: 2}) {
		t.Fatalf("app v2 parent = %+v", app.Meta[2].Parent)
	}
	// The same commit: the fast path, nothing fetched.
	fetched := f.fetches
	if res, err := SyncKit(context.Background(), st, f, nil, "app", time.Unix(21, 0)); err != nil || !res.Unchanged || f.fetches != fetched {
		t.Fatalf("second sync: %+v %v", res, err)
	}
	// A new commit with identical kits: every kit unchanged.
	f.commit("main", "c3", map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: two\n", "app/kit.yml": appKit})
	res, err = SyncKit(context.Background(), st, f, nil, "app", time.Unix(22, 0))
	if err != nil || !res.Unchanged || res.Version != 2 || !res.Also[0].Unchanged || res.Also[0].Version != 2 {
		t.Fatalf("identical sync: %+v %v", res, err)
	}
}

func TestSetAtomicOnFailure(t *testing.T) {
	st, f := trackedSet(t)
	before := st.ListKits()
	// web-dev's new prompt is valid on its own; merged with app's it is over budget.
	f.commit("main", "c2", map[string]string{
		"web/kit.yml": "kind: studio\nname: web-dev\nprompt: " + strings.Repeat("x", 500) + "\n",
		"app/kit.yml": "kind: studio\nbase:\n  kit: ../web\nprompt: " + strings.Repeat("y", 500) + "\n",
	})
	if _, err := SyncKit(context.Background(), st, f, nil, "app", time.Unix(20, 0)); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("want a merged-budget error, got %v", err)
	}
	if after := st.ListKits(); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed sync wrote:\n%+v\nwant\n%+v", after, before)
	}
	// A fresh store: nothing at all is written.
	fresh := NewMemStore()
	if _, err := ImportKit(context.Background(), fresh, f, nil, "", setSrc("app"), time.Unix(21, 0)); err == nil {
		t.Fatal("expected error")
	}
	if n := len(fresh.ListKits()); n != 0 {
		t.Fatalf("%d kits written", n)
	}
}

// setRefused imports src from f into st and requires exactly want, with st
// untouched.
func setRefused(t *testing.T, st Store, f KitFetcher, name string, src gitsource.Source, want string) {
	t.Helper()
	before := st.ListKits()
	_, err := ImportKit(context.Background(), st, f, nil, name, src, time.Unix(30, 0))
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	if after := st.ListKits(); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused import wrote: %+v", after)
	}
}

func TestSetRefusesForeignKit(t *testing.T) {
	st := NewMemStore()
	if _, _, err := PushStudioKit(st, "web-dev", "kind: studio\nprompt: mine\n"); err != nil {
		t.Fatal(err)
	}
	f := setFixture(t, map[string]string{"web/kit.yml": webKit, "app/kit.yml": appKit})
	setRefused(t, st, f, "", setSrc("app"),
		"kit web-dev exists and is not from https://github.com/acme/kits//web; rename the kit or untrack it first")
}

func TestSetRefusesOtherTrackedSource(t *testing.T) {
	st := NewMemStore()
	f := setFixture(t, map[string]string{"web/kit.yml": webKit, "app/kit.yml": appKit})
	other := gitsource.Source{Repo: "https://github.com/other/kits", Dir: "web", Ref: "main"}
	otherF := setFixture(t, map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: other\n"})
	if _, err := TrackKit(context.Background(), st, otherF, nil, "", other, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	setRefused(t, st, f, "", setSrc("app"),
		"kit web-dev exists and is not from https://github.com/acme/kits//web; rename the kit or untrack it first")
	// Imported (untracked) from this {repo, dir} but tracking another one.
	if res, err := ImportKit(context.Background(), st, f, nil, "", setSrc("web"), time.Unix(2, 0)); err != nil || res.Version != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	setRefused(t, st, f, "", setSrc("app"),
		"kit web-dev exists and is not from https://github.com/acme/kits//web; rename the kit or untrack it first")
}

func TestSetRefusesNewerLocalPush(t *testing.T) {
	st := NewMemStore()
	f := setFixture(t, map[string]string{"web/kit.yml": webKit, "app/kit.yml": appKit})
	if _, err := ImportKit(context.Background(), st, f, nil, "", setSrc("app"), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PushStudioKit(st, "web-dev", "kind: studio\nprompt: local\n"); err != nil {
		t.Fatal(err)
	}
	setRefused(t, st, f, "", setSrc("app"),
		"kit web-dev has a local push newer than its last import from https://github.com/acme/kits//web; re-push or untrack it first")
}

func TestSetRefusesBadPaths(t *testing.T) {
	kit := func(name, parent string) string {
		s := "kind: studio\n"
		if name != "" {
			s += "name: " + name + "\n"
		}
		return s + "base:\n  kit: " + parent + "\n"
	}
	for label, c := range map[string]struct {
		files map[string]string
		name  string
		want  string
	}{
		"escaping": {map[string]string{"app/kit.yml": kit("", "../../..")}, "",
			"parent path ../../.. (of app) leaves the checkout"},
		"missing kit.yml": {map[string]string{"app/kit.yml": kit("", "../nope"), "nope/README": "x"}, "",
			"parent path ../nope (of app) has no kit.yml"},
		"cycle": {map[string]string{"app/kit.yml": kit("", "../web"), "web/kit.yml": kit("web-dev", "../app")}, "",
			"cycle: app → web-dev → app"},
		"self cycle": {map[string]string{"app/kit.yml": kit("", "../app")}, "",
			"cycle: app → app"},
		"self": {map[string]string{"app/kit.yml": kit("", "./")}, "",
			"studio kit: base.kit: not a valid parent path (a path parent must be a non-empty relative path of at most 200 bytes, with no backslash)"},
		"depth 5": {map[string]string{
			"a/kit.yml": kit("", "../b"), "b/kit.yml": kit("", "../c"), "c/kit.yml": kit("", "../d"),
			"d/kit.yml": kit("", "../e"), "e/kit.yml": "kind: studio\n",
		}, "", "kit chain too deep (max 4): a → b → c → d → e"},
		"duplicate ancestor names": {map[string]string{
			"app/kit.yml": kit("", "../x"), "x/kit.yml": kit("dup", "../y"), "y/kit.yml": "kind: studio\nname: dup\n",
		}, "", "kit name dup is used by both x and y in this set"},
		"top name reused": {map[string]string{"app/kit.yml": appKit, "web/kit.yml": webKit}, "web-dev",
			"kit name web-dev is used by both app and web in this set"},
	} {
		t.Run(label, func(t *testing.T) {
			top := "app"
			if label == "depth 5" {
				top = "a"
			}
			setRefused(t, NewMemStore(), setFixture(t, c.files), c.name, setSrc(top), c.want)
		})
	}
}

func TestSetMixedParents(t *testing.T) {
	st := NewMemStore()
	if _, err := EnsureDefaultStudioKit(st); err != nil {
		t.Fatal(err)
	}
	f := setFixture(t, map[string]string{
		"web/kit.yml": "kind: studio\nname: web-dev\nbase:\n  kit: " + studio.DefaultStudioKitID + "\n",
		"app/kit.yml": appKit,
	})
	if _, err := ImportKit(context.Background(), st, f, nil, "", setSrc("app"), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if got := storedKit(t, st, "web-dev", 1).Base.Kit; got != studio.DefaultStudioKitID {
		t.Fatalf("a registry parent must be stored as written, got %q", got)
	}
	links, _, err := KitChainView(st, "app", mustConfig(t, st, "app", 1))
	want := []KitChainLink{{Name: studio.DefaultStudioKitID, Version: 1, Follow: true}, {Name: "web-dev", Version: 1}}
	if err != nil || !reflect.DeepEqual(links, want) {
		t.Fatalf("chain %+v %v, want %+v", links, err, want)
	}
	if k, _ := st.GetKit("web-dev"); k.Meta[1].Parent == nil || k.Meta[1].Parent.Name != studio.DefaultStudioKitID {
		t.Fatalf("web-dev parent = %+v", k.Meta[1].Parent)
	}
}

func mustConfig(t *testing.T, st Store, name string, version int) string {
	t.Helper()
	text, ok := st.KitConfig(name, version)
	if !ok {
		t.Fatalf("no %s@v%d", name, version)
	}
	return text
}

// Two sets naming the same two kits in opposite parent order (a→z at ref x,
// z→a at ref y), imported and tracked concurrently, by explicit and defaulted
// name: each operation holds at most one kit lock, so none deadlocks.
func TestSetCrossRefCycleNoDeadlock(t *testing.T) {
	st := NewMemStore()
	f := newFakeFetcher(t, map[string]string{"x": "cx", "y": "cy"}, map[string]map[string]string{
		"cx": {"a/kit.yml": "kind: studio\nbase:\n  kit: ../z\n", "z/kit.yml": "kind: studio\n"},
		"cy": {"z/kit.yml": "kind: studio\nbase:\n  kit: ../a\n", "a/kit.yml": "kind: studio\n"},
	})
	x := gitsource.Source{Repo: setRepo, Dir: "a", Ref: "x"}
	y := gitsource.Source{Repo: setRepo, Dir: "z", Ref: "y"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := range 20 {
			now := time.Unix(int64(i), 0)
			wg.Add(4)
			go func() { defer wg.Done(); _, _ = ImportKit(context.Background(), st, f, nil, "", x, now) }()
			go func() { defer wg.Done(); _, _ = ImportKit(context.Background(), st, f, nil, "", y, now) }()
			go func() { defer wg.Done(); _, _ = TrackKit(context.Background(), st, f, nil, "a", x, now) }()
			go func() { defer wg.Done(); _, _ = TrackKit(context.Background(), st, f, nil, "z", y, now) }()
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("concurrent set imports deadlocked")
	}
	kitLocks.mu.Lock()
	defer kitLocks.mu.Unlock()
	if len(kitLocks.m) != 0 {
		t.Fatalf("leaked %d lock entries", len(kitLocks.m))
	}
}

// gatedFetcher blocks a Fetch of commit gate inside its callback (after the
// ref resolved) until release closes, signalling entered first.
type gatedFetcher struct {
	*fakeFetcher
	gate             string
	entered, release chan struct{}
}

func (g *gatedFetcher) Fetch(ctx context.Context, src gitsource.Source, fn func(root, commit string) error) error {
	return g.fakeFetcher.Fetch(ctx, src, func(root, commit string) error {
		if commit == g.gate {
			close(g.entered)
			<-g.release
		}
		return fn(root, commit)
	})
}

// A sync held up fetching c2 while main moves to c3 must not land c2 over a
// concurrent sync's c3.
func TestSyncDoesNotRegress(t *testing.T) {
	st, ff := trackedSet(t)
	kit := func(p string) map[string]string {
		return map[string]string{"web/kit.yml": webKit, "app/kit.yml": strings.Replace(appKit, "prompt: app", "prompt: "+p, 1)}
	}
	ff.commit("main", "c2", kit("two"))
	f := &gatedFetcher{fakeFetcher: ff, gate: "c2", entered: make(chan struct{}), release: make(chan struct{})}
	aDone, bDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := SyncKit(context.Background(), st, f, nil, "app", time.Unix(20, 0))
		aDone <- err
	}()
	<-f.entered
	ff.commit("main", "c3", kit("three"))
	go func() {
		_, err := SyncKit(context.Background(), st, f, nil, "app", time.Unix(21, 0))
		bDone <- err
	}()
	// Give B the chance to finish first; with app locked across A's fetch it
	// cannot, and waits.
	var bErr error
	bFinished := false
	select {
	case bErr = <-bDone:
		bFinished = true
	case <-time.After(200 * time.Millisecond):
	}
	close(f.release)
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	if !bFinished {
		bErr = <-bDone
	}
	if bErr != nil {
		t.Fatal(bErr)
	}
	k, _ := st.GetKit("app")
	if cfg, _ := st.KitConfig("app", k.Current); !strings.Contains(cfg, "three") || k.Source.Commit != "c3" {
		t.Fatalf("app current v%d = %s, tracked %s; want c3's content", k.Current, cfg, k.Source.Commit)
	}
	c3 := 0
	for v, m := range k.Meta {
		if m.Source != nil && m.Source.Commit == "c3" {
			c3 = v
		}
	}
	for v, m := range k.Meta {
		if m.Source != nil && m.Source.Commit == "c2" && v > c3 {
			t.Fatalf("c2 written as v%d after c3's v%d", v, c3)
		}
	}
}

// planKitSet rewrites each path reference to the version its parent ends at.
func TestPlanKitSet(t *testing.T) {
	st, f := trackedSet(t)
	files := map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: two\n", "app/kit.yml": appKit}
	f.commit("main", "c2", files)
	err := f.Fetch(context.Background(), setSrc("app"), func(root, _ string) error {
		entries, err := planKitSet(st, root, setSrc("app"), "")
		if err != nil {
			return err
		}
		var got []string
		for _, e := range entries {
			got = append(got, fmt.Sprintf("%s %s v%d unchanged=%v base=%s", e.Name, e.Dir, e.Version, e.Unchanged, e.Kit.Base.Kit))
		}
		if want := []string{"web-dev web v2 unchanged=false base=", "app app v2 unchanged=false base=web-dev@v2"}; !reflect.DeepEqual(got, want) {
			t.Errorf("plan = %q, want %q", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if k, _ := st.GetKit("web-dev"); len(k.Versions) != 1 {
		t.Fatal("planning wrote")
	}
}
