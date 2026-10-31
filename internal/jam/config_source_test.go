package jam

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

const cfgRepo = "https://github.com/acme/jam"

var cfgFiles = map[string]string{
	"cfg/users/bob.yml":    "kind: user\nversion: 1\n",
	"cfg/kits/web/kit.yml": "kind: studio\n",
	"README.md":            "not config",
}

// cfgFetcher serves the config repo cfgRepo at main = c1 holding files.
func cfgFetcher(t *testing.T, files map[string]string) (repoFetcher, *fakeFetcher) {
	t.Helper()
	cfg := newFakeFetcher(t, map[string]string{"main": "c1", "v1": "c1"}, map[string]map[string]string{"c1": files})
	return repoFetcher{cfgRepo: cfg}, cfg
}

func syncOpts() ConfigApplyOptions { return ConfigApplyOptions{Now: planNow} }

// followEnv is allCreds with cfgSrc as the followed config repo.
var followEnv = func() ConfigEnv { e := allCreds; e.Followed = cfgSrc; return e }()

func TestSyncTheFollowedSource(t *testing.T) {
	st := NewMemStore()
	f, _ := cfgFetcher(t, cfgFiles)
	ctx := context.Background()
	if _, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, allCreds, syncOpts(), nil, "alice"); !errors.Is(err, ErrConfigNotTracked) ||
		err.Error() != "no config repo is configured (config.github in the serve config)" {
		t.Fatalf("sync without a followed source: %v", err)
	}
	if ConfigFollowConfirmed(st, followEnv) {
		t.Fatal("confirmed before any apply")
	}
	res, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, followEnv, ConfigApplyOptions{DryRun: true, Now: planNow}, nil, "alice")
	if err != nil || res.Applied || res.Commit != "c1" || res.Plan.Counts()[ConfigCreate] != 2 || len(st.ListUsers()) != 0 {
		t.Fatalf("diff: %+v %v", res, err)
	}
	if _, ok := st.GetConfigSource(); ok {
		t.Fatal("a dry run recorded the source")
	}
	res, err = ApplyConfigSource(ctx, st, f, gitsource.Source{}, followEnv, syncOpts(), nil, "alice")
	if err != nil || !res.Applied || res.Source != "https://github.com/acme/jam//cfg@main" || res.Commit != "c1" {
		t.Fatalf("sync: %+v %v", res, err)
	}
	if got, _ := st.GetConfigSource(); got != (ConfigSource{Repo: cfgRepo, Dir: "cfg", Ref: "main", Commit: "c1", AppliedAt: planNow}) {
		t.Fatalf("record %+v", got)
	}
	if !ConfigFollowConfirmed(st, followEnv) {
		t.Fatal("not confirmed after the first apply")
	}
	for _, mk := range st.ListConfigMarks() {
		if mk.Commit != "c1" {
			t.Fatalf("mark %+v: want the config commit", mk)
		}
	}
	k, _ := st.GetKit("web")
	if m := k.Meta[1].Source; m == nil || *m != (KitSource{Repo: cfgRepo, Dir: "cfg/kits/web", Ref: "main", Commit: "c1"}) {
		t.Fatalf("provenance %+v", m)
	}
}

// A record of another repo (the serve config moved) is not confirmed, and
// the first apply of the new one replaces it.
func TestSyncReplacesAnotherSourcesRecord(t *testing.T) {
	st := NewMemStore()
	f, _ := cfgFetcher(t, cfgFiles)
	if err := st.SetConfigSource(&ConfigSource{Repo: "https://github.com/acme/old", Ref: "main", Commit: "c0"}); err != nil {
		t.Fatal(err)
	}
	if ConfigFollowConfirmed(st, followEnv) {
		t.Fatal("another repo's record confirms")
	}
	if _, err := ApplyConfigSource(context.Background(), st, f, gitsource.Source{}, followEnv, syncOpts(), nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetConfigSource(); got.Source() != cfgSrc || got.Commit != "c1" {
		t.Fatalf("record %+v", got)
	}
}

func TestSyncCommitOnlyMoveRecordsCommit(t *testing.T) {
	st := NewMemStore()
	f, cfg := cfgFetcher(t, cfgFiles)
	ctx := context.Background()
	if _, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, followEnv, syncOpts(), nil, "alice"); err != nil {
		t.Fatal(err)
	}
	moved := map[string]string{"README.md": "edited"}
	for p, c := range cfgFiles {
		if p != "README.md" {
			moved[p] = c
		}
	}
	cfg.commit("main", "c2", moved)
	var buf bytes.Buffer
	res, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, followEnv, syncOpts(), slog.New(slog.NewJSONHandler(&buf, nil)), "alice")
	if err != nil || res.Applied || ConfigChanges(res.Plan) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got, _ := st.GetConfigSource(); got.Commit != "c2" {
		t.Fatalf("the tracked commit did not advance: %+v", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("a no-change sync logged:\n%s", buf.String())
	}
	if mk := st.ListConfigMarks()[0]; mk.Commit != "c1" {
		t.Fatalf("an unchanged object's mark moved: %+v", mk)
	}
}

func TestApplyOtherGitSourceLeavesTrackedRecord(t *testing.T) {
	st := NewMemStore()
	f, _ := cfgFetcher(t, cfgFiles)
	ctx := context.Background()
	other := gitsource.Source{Repo: cfgRepo, Dir: "cfg", Ref: "v1"}
	if res, err := ApplyConfigSource(ctx, st, f, other, followEnv, syncOpts(), nil, "alice"); err != nil || !res.Applied {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got, ok := st.GetConfigSource(); ok {
		t.Fatalf("an apply of another source recorded itself: %+v", got)
	}
}

// badDirFetcher's checkout holds out → a directory outside it, and a file.
type badDirFetcher struct{ t *testing.T }

func (b badDirFetcher) Resolve(context.Context, gitsource.Source) (string, error) { return "c1", nil }

func (b badDirFetcher) Fetch(_ context.Context, _ gitsource.Source, fn func(root, commit string) error) error {
	root, outside := b.t.TempDir(), b.t.TempDir()
	mustWrite(b.t, filepath.Join(outside, "users/eve.yml"), "kind: user\nversion: 1\n", 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		return err
	}
	mustWrite(b.t, filepath.Join(root, "file"), "x", 0o644)
	return fn(root, "c1")
}

func TestFetchConfigTreeRefusesBadDir(t *testing.T) {
	st := NewMemStore()
	for _, dir := range []string{"out", "file", "none"} {
		_, err := ApplyConfigSource(context.Background(), st, badDirFetcher{t}, gitsource.Source{Repo: cfgRepo, Dir: dir, Ref: "main"},
			allCreds, ConfigApplyOptions{DryRun: true, Now: planNow}, nil, "alice")
		if status, msg := ConfigStatus(err); status != http.StatusBadRequest || msg != "config source: "+dir+" is not a directory in the repo" {
			t.Errorf("%s: %d %q", dir, status, msg)
		}
	}
}

func TestConfigFetchFailureTracksNothing(t *testing.T) {
	st := NewMemStore()
	f, cfg := cfgFetcher(t, cfgFiles)
	cfg.failNext = fmt.Errorf("%w: auth failed for https://github.com/acme/", gitsource.ErrFetch)
	_, err := ApplyConfigSource(context.Background(), st, f, gitsource.Source{}, followEnv, syncOpts(), nil, "alice")
	if status, msg := ConfigStatus(err); status != http.StatusBadGateway || !strings.Contains(msg, "auth failed") {
		t.Fatalf("%d %q", status, msg)
	}
	if _, ok := st.GetConfigSource(); ok {
		t.Fatal("a failed fetch recorded the source")
	}
}

func TestConfigAuditNeverLogsUserinfo(t *testing.T) {
	var buf bytes.Buffer
	logConfigApply(slog.New(slog.NewJSONHandler(&buf, nil)), "alice",
		ConfigApplyOptions{Source: gitsource.Source{Repo: "https://tok3n@github.com/acme/jam", Ref: "main"}, Commit: "c1"}, ConfigPlan{})
	if strings.Contains(buf.String(), "tok3n") || !strings.Contains(buf.String(), `"source":"invalid"`) {
		t.Fatalf("audit:\n%s", buf.String())
	}
	if got := configSourceLabel(cfgSrc); got != "https://github.com/acme/jam//cfg@main" {
		t.Fatal(got)
	}
	if got := configSourceLabel(gitsource.Source{Repo: ConfigDirRepo}); got != "dir" {
		t.Fatal(got)
	}
}

// serialFetcher serializes fetches per repo URL, as gitsource.Fetcher does:
// a fetch nested in another of the same repo blocks until ctx ends.
type serialFetcher struct {
	KitFetcher
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (s *serialFetcher) Fetch(ctx context.Context, src gitsource.Source, fn func(root, commit string) error) error {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	l := s.locks[src.Repo]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[src.Repo] = l
	}
	s.mu.Unlock()
	l.Lock()
	defer l.Unlock()
	return s.KitFetcher.Fetch(ctx, src, fn)
}

// withFiles is cfgFiles plus extra.
func withFiles(extra map[string]string) map[string]string {
	out := map[string]string{}
	for p, c := range cfgFiles {
		out[p] = c
	}
	for p, c := range extra {
		out[p] = c
	}
	return out
}

// TestConcurrentSyncsAndKitPush: four syncs of a tree holding a .source.yml
// (kitsRepo//app@main) race a kit push and a kit sync of another kit
// tracking kitsRepo, all through one per-repo-serializing fetcher.
func TestConcurrentSyncsAndKitPush(t *testing.T) {
	st := NewMemStore()
	rf, _ := cfgFetcher(t, withFiles(map[string]string{"cfg/kits/app.source.yml": appSourceYML}))
	rf[kitsRepo] = newFakeFetcher(t, map[string]string{"main": "k1"}, map[string]map[string]string{"k1": {
		"app/kit.yml": "kind: studio\nprompt: app\n", "solo/kit.yml": "kind: studio\nprompt: solo\n",
	}})
	f := &serialFetcher{KitFetcher: rf}
	ctx := context.Background()
	if _, err := TrackKit(ctx, st, f, nil, "solo", gitsource.Source{Repo: kitsRepo, Dir: "solo", Ref: "main"}, planNow); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		wg.Go(func() {
			for i := range 30 {
				_, _, _ = PushStudioKit(st, "other", fmt.Sprintf("kind: studio\nprompt: p%d\n", i))
			}
		})
		wg.Go(func() {
			for range 30 {
				_, _ = SyncKit(ctx, st, f, nil, "solo", planNow)
			}
		})
		for range 4 {
			wg.Go(func() { _, _ = ApplyConfigSource(ctx, st, f, gitsource.Source{}, followEnv, syncOpts(), nil, "alice") })
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock between config syncs, a kit push and a kit sync")
	}
	for _, n := range []string{"web", "app", "solo"} {
		if k, _ := st.GetKit(n); len(k.VersionNumbers()) != 1 {
			t.Fatalf("racing syncs wrote %d versions of %s", len(k.VersionNumbers()), n)
		}
	}
	if res, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, followEnv, syncOpts(), nil, "alice"); err != nil || res.Applied {
		t.Fatalf("a later sync is not a no-op: %+v %v", res, err)
	}
}

// A .source.yml naming the config repo itself is fetched after the config
// checkout is closed: with a per-repo-serializing fetcher, no deadlock.
func TestConfigSourceKitFromTheConfigRepo(t *testing.T) {
	st := NewMemStore()
	rf, _ := cfgFetcher(t, withFiles(map[string]string{
		"cfg/kits/lib.source.yml": "kind: kit-source\nversion: 1\ngit: " + cfgRepo + "//shared/lib@main\n",
		"shared/lib/kit.yml":      "kind: studio\nprompt: lib\n",
	}))
	type out struct {
		res ConfigApplyResult
		err error
	}
	ch := make(chan out, 1)
	go func() {
		res, err := ApplyConfigSource(context.Background(), st, &serialFetcher{KitFetcher: rf}, cfgSrc, allCreds, syncOpts(), nil, "alice")
		ch <- out{res, err}
	}()
	var got out
	select {
	case got = <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: the kit source was fetched inside the config checkout")
	}
	if res, err := got.res, got.err; err != nil || !res.Applied {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if k, _ := st.GetKit("lib"); k.Source == nil || *k.Source != (KitSource{Repo: cfgRepo, Dir: "shared/lib", Ref: "main", Commit: "c1"}) {
		t.Fatalf("tracked %+v", k.Source)
	}
}

func TestApplyConfigSourceWithoutFetcher(t *testing.T) {
	st := NewMemStore()
	_, err := ApplyConfigSource(context.Background(), st, nil, cfgSrc, allCreds, syncOpts(), nil, "alice")
	if status, msg := ConfigStatus(err); status != http.StatusServiceUnavailable || msg != "config: git sources are not available on this serve" {
		t.Fatalf("%d %q", status, msg)
	}
	_, err = ApplyConfigSource(context.Background(), st, nil, gitsource.Source{}, followEnv, syncOpts(), nil, "alice")
	if status, _ := ConfigStatus(err); status != http.StatusServiceUnavailable {
		t.Fatalf("sync: %d", status)
	}
	if _, ok := st.GetConfigSource(); ok {
		t.Fatal("tracked without a fetcher")
	}
}

// deadlineFetcher's config fetch returns a caller-stopped error; its kits
// fetch waits for the deadline.
type deadlineFetcher struct {
	repoFetcher
	failConfig bool
}

func (d deadlineFetcher) Fetch(ctx context.Context, src gitsource.Source, fn func(root, commit string) error) error {
	if src.Repo == cfgRepo && d.failConfig {
		return fmt.Errorf("%w: fetch %s: stopped by the caller: %w", gitsource.ErrFetch, cfgRepo, context.DeadlineExceeded)
	}
	if src.Repo == kitsRepo {
		<-ctx.Done()
		return fmt.Errorf("%w: fetch %s: stopped by the caller: %w", gitsource.ErrFetch, kitsRepo, context.Cause(ctx))
	}
	return d.repoFetcher.Fetch(ctx, src, fn)
}

func TestConfigFetchTimedOut(t *testing.T) {
	rf, _ := cfgFetcher(t, withFiles(map[string]string{"cfg/kits/app.source.yml": appSourceYML}))
	for _, failConfig := range []bool{true, false} {
		st := NewMemStore()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := ApplyConfigSource(ctx, st, deadlineFetcher{rf, failConfig}, cfgSrc, allCreds, syncOpts(), nil, "alice")
		cancel()
		if status, msg := ConfigStatus(err); status != http.StatusBadGateway || msg != "config fetch timed out" {
			t.Errorf("failConfig=%v: %d %q", failConfig, status, msg)
		}
		if len(st.ListUsers()) != 0 || len(st.ListConfigMarks()) != 0 {
			t.Errorf("failConfig=%v: a timed-out run wrote", failConfig)
		}
	}
}

// A kit sync that lands the content the config repo has leaves the next
// sync with nothing to apply but a stale mark: the metadata-only write
// refreshes it (never when the plan is refused), so the repo's next change
// is not drift.
func TestSyncRefreshesStaleMarks(t *testing.T) {
	st := NewMemStore()
	files := withFiles(map[string]string{"cfg/kits/app.source.yml": appSourceYML})
	rf, cfg := cfgFetcher(t, files)
	kits := newFakeFetcher(t, map[string]string{"main": "k1"}, map[string]map[string]string{"k1": {"app/kit.yml": "kind: studio\nprompt: one\n"}})
	rf[kitsRepo] = kits
	ctx := context.Background()
	if res, err := ApplyConfigSource(ctx, st, rf, gitsource.Source{}, followEnv, syncOpts(), nil, "alice"); err != nil || !res.Applied {
		t.Fatalf("sync: %+v %v", res, err)
	}
	stale := markOf(st, ConfigKindKit, "app")
	kits.commit("main", "k2", map[string]string{"app/kit.yml": "kind: studio\nprompt: two\n"})
	if _, err := SyncKit(ctx, st, rf, nil, "app", planNow); err != nil {
		t.Fatal(err)
	}
	// A refused sync (a broken file at c2) refreshes nothing.
	cfg.commit("main", "c2", withFiles(map[string]string{"cfg/kits/app.source.yml": appSourceYML, "cfg/users/eve.yml": "kind: user\nversion: 9\n"}))
	if _, err := ApplyConfigSource(ctx, st, rf, gitsource.Source{}, followEnv, syncOpts(), nil, "alice"); !errors.Is(err, ErrConfigRefused) {
		t.Fatalf("want refused: %v", err)
	}
	if mk := markOf(st, ConfigKindKit, "app"); mk != stale {
		t.Fatalf("a refused sync rewrote a mark: %+v", mk)
	}
	if got, _ := st.GetConfigSource(); got.Commit != "c1" {
		t.Fatalf("a refused sync recorded its commit: %+v", got)
	}
	cfg.commit("main", "c3", files)
	res, err := ApplyConfigSource(ctx, st, rf, gitsource.Source{}, followEnv, syncOpts(), nil, "alice")
	if err != nil || res.Applied || ConfigChanges(res.Plan) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if mk := markOf(st, ConfigKindKit, "app"); mk.Digest == stale.Digest || mk.Commit != "c3" {
		t.Fatalf("the stale mark was not refreshed: %+v (was %+v)", mk, stale)
	}
	kits.commit("main", "k3", map[string]string{"app/kit.yml": "kind: studio\nprompt: three\n"})
	res, err = ApplyConfigSource(ctx, st, rf, gitsource.Source{}, followEnv, syncOpts(), nil, "alice")
	if err != nil || !res.Applied {
		t.Fatalf("the repo's next change: %+v %v", res, err)
	}
}

// TestConfigRoundTripWithSourceKit: a Jam whose kit tracks its own source
// writes it as kits/<n>.source.yml; applying that tree onto an empty Jam
// fetches and tracks the kit, and the replica writes the same tree.
func TestConfigRoundTripWithSourceKit(t *testing.T) {
	src, kits := trackedFixture(t)
	f := repoFetcher{kitsRepo: kits}
	exp, err := ExportConfigTree(src.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeExport(t, dir, exp)
	tr, err := LoadConfigTree(dir)
	if err != nil || len(tr.Refusals) > 0 {
		t.Fatalf("%v %v", err, tr.Refusals)
	}
	dst := NewMemStore()
	if res, err := ApplyConfigTree(context.Background(), dst, f, tr, allCreds, dirOpts(), nil, "local"); err != nil || !res.Applied {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	got, _ := ExportConfigTree(dst.ExportConfig(), nil, nil, true)
	if !reflect.DeepEqual(got.Files, exp.Files) {
		t.Fatalf("replica tree differs:\n%v\n---\n%v", got.Files, exp.Files)
	}
	if k, _ := dst.GetKit("web-dev"); k.Source == nil || *k.Source != (KitSource{Repo: kitsRepo, Dir: "web", Ref: "main", Commit: "c1"}) {
		t.Fatalf("tracked %+v", k.Source)
	}
}

// ctxFetcher fails a fetch whose context is already done, as the real
// Fetcher does once a deadline passes.
type ctxFetcher struct{ KitFetcher }

func (f ctxFetcher) Fetch(ctx context.Context, src gitsource.Source, fn func(root, commit string) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: config fetch timed out", gitsource.ErrFetch)
	}
	return f.KitFetcher.Fetch(ctx, src, fn)
}

// The fetch deadline starts once configMu is taken: a run queued behind
// another longer than the deadline still fetches.
func TestConfigFetchDeadlineExcludesQueueWait(t *testing.T) {
	old := configFetchTimeout
	configFetchTimeout = 50 * time.Millisecond
	t.Cleanup(func() { configFetchTimeout = old })
	kits := newFakeFetcher(t, map[string]string{"main": "k1"}, map[string]map[string]string{"k1": {"app/kit.yml": "kind: studio\n"}})
	runs := map[string]func(st *MemStore) error{
		"apply source": func(st *MemStore) error {
			f, _ := cfgFetcher(t, cfgFiles)
			_, err := ApplyConfigSource(context.Background(), st, ctxFetcher{f}, cfgSrc, allCreds, syncOpts(), nil, "alice")
			return err
		},
		"sync": func(st *MemStore) error {
			f, _ := cfgFetcher(t, cfgFiles)
			_, err := ApplyConfigSource(context.Background(), st, ctxFetcher{f}, gitsource.Source{}, followEnv, syncOpts(), nil, "alice")
			return err
		},
		"apply tree": func(st *MemStore) error {
			tr := treeOf(t, map[string]string{"kits/app.source.yml": appSourceYML})
			_, err := ApplyConfigTree(context.Background(), st, ctxFetcher{repoFetcher{kitsRepo: kits}}, tr, allCreds, dirOpts(), nil, "local")
			return err
		},
	}
	for name, run := range runs {
		t.Run(name, func(t *testing.T) {
			configMu.Lock()
			done := make(chan error, 1)
			go func() { done <- run(NewMemStore()) }()
			time.Sleep(4 * configFetchTimeout) // queued behind a held configMu
			configMu.Unlock()
			if err := <-done; err != nil {
				t.Fatalf("a queued run timed out: %v", err)
			}
		})
	}
}

// A stored source dir (a hand-edited record, say) with a '.' or '..'
// component is refused without echoing it, even where the path exists.
func TestConfigCheckoutDirRefusesDotComponents(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(root, "cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"..", "cfg/..", "./cfg", "cfg/.", "../repo/cfg", "cfg//x", "/cfg"} {
		_, err := configCheckoutDir(root, dir)
		if err == nil || strings.Contains(err.Error(), dir) {
			t.Fatalf("dir %q: %v", dir, err)
		}
	}
	if got, err := configCheckoutDir(root, "cfg"); err != nil || got != filepath.Join(root, "cfg") {
		t.Fatalf("cfg: %q %v", got, err)
	}
}
