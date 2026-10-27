package jam

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/util"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
	"github.com/aethons-tools/cove/internal/studio"
)

// fakeDraftBuilder is a KitDraftBuilder a test scripts. Each build writes
// write to its log, calls during, signals started, waits for release (or
// its ctx), appends readFile from the draft's own files to the log, and
// returns err or id (default sha256:img).
type fakeDraftBuilder struct {
	mu       sync.Mutex
	defs     []KitDefinition
	tags     []string
	write    string
	during   func()
	started  chan string
	release  chan struct{}
	readFile string
	err      error
	id       string
	present  map[string]bool
}

func (f *fakeDraftBuilder) BuildKitDraft(ctx context.Context, def KitDefinition, tag string, log io.Writer) (string, error) {
	f.mu.Lock()
	f.defs, f.tags = append(f.defs, def), append(f.tags, tag)
	f.mu.Unlock()
	_, _ = io.WriteString(log, f.write)
	if f.during != nil {
		f.during()
	}
	if f.started != nil {
		f.started <- tag
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if f.readFile != "" {
		b, err := util.ReadFile(def.Chain[len(def.Chain)-1].Files, f.readFile)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(log, "file: "+string(b))
	}
	if f.err != nil {
		return "", f.err
	}
	if f.id != "" {
		return f.id, nil
	}
	return "sha256:img", nil
}

func (f *fakeDraftBuilder) HasKitDraftImage(id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.present[id], nil
}

func (f *fakeDraftBuilder) setPresent(id string, v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.present == nil {
		f.present = map[string]bool{}
	}
	f.present[id] = v
}

func (f *fakeDraftBuilder) builds() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.defs)
}

// draftBuildsFor is kit app v1 (kit.yml, Dockerfile) with an open draft
// whose Dockerfile was edited (so that blob is the draft's alone), test
// builds over fb under a cancellable serve context, and the draft's ETag.
func draftBuildsFor(t *testing.T, fb *fakeDraftBuilder) (*KitDraftBuilds, Store, string, context.CancelFunc) {
	t.Helper()
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "Dockerfile", "ARG JAM_STUDIO_BASE_IMAGE\nFROM ${JAM_STUDIO_BASE_IMAGE}\n")
	d, err := OpenKitDraft(st, "app", 0, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	s, err := PutKitDraftFile(st, slog.New(slog.DiscardHandler), "app", "Dockerfile", 0,
		[]byte("ARG JAM_STUDIO_BASE_IMAGE\nFROM ${JAM_STUDIO_BASE_IMAGE}\nRUN echo one\n"), DraftETag(d.Tree), "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewKitDraftBuilds(ctx, st, fb, slog.New(slog.DiscardHandler)), st, s.ETag, cancel
}

// finished waits for kit app's latest build to end and returns it.
func finished(t *testing.T, b *KitDraftBuilds) KitDraftBuild {
	t.Helper()
	b.mu.Lock()
	j := b.jobs["app"]
	b.mu.Unlock()
	if j == nil {
		t.Fatal("no build")
	}
	select {
	case <-j.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the build did not finish")
	}
	v, err := b.Status("app", 0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestKitDraftTag(t *testing.T) {
	tree := strings.Repeat("ab", 32)
	tag, err := KitDraftTag("app", tree)
	if err != nil || tag != "cove-kit-draft:app-abababababab" || KitDraftStageTag(tag) != "cove-kit-draft:app-abababababab-stage" {
		t.Fatalf("tag = %q, %v", tag, err)
	}
	if _, err := KitDraftTag(strings.Repeat("a", 109), tree); err != nil {
		t.Fatalf("a 109-character name must fit: %v", err)
	}
	for _, name := range []string{strings.Repeat("a", 110), ".app", "-app", "a b"} {
		_, err := KitDraftTag(name, tree)
		wantErr(t, err, "kit name cannot form a draft image tag (at most 109 characters of [A-Za-z0-9_.-], not starting with . or -)", 400)
	}
}

func TestDraftBuildSucceeds(t *testing.T) {
	fb := &fakeDraftBuilder{write: "#1 FROM base\n"}
	b, st, etag, _ := draftBuildsFor(t, fb)
	d, _ := st.GetKitDraft("app")
	tag := "cove-kit-draft:app-" + d.Tree[:12]
	v, err := b.Start("app", etag, "local")
	if err != nil || v.State != DraftBuildRunning || v.Tree != d.Tree || v.Tag != tag || !strings.HasPrefix(v.ID, "kdb_") || v.By != "local" {
		t.Fatalf("start = %+v, %v", v, err)
	}
	v = finished(t, b)
	if v.State != DraftBuildSucceeded || v.Digest != "sha256:img" || !v.Current || v.DraftETag != etag || v.FinishedAt.IsZero() {
		t.Fatalf("finished = %+v", v)
	}
	if !strings.Contains(v.Log, "#1 FROM base\n") || !strings.HasSuffix(v.Log, "built "+tag+" (sha256:img)\n") {
		t.Fatalf("log = %q", v.Log)
	}
	def := fb.defs[0]
	if fb.tags[0] != tag || strings.HasPrefix(fb.tags[0], "cove-kit:") || def.Ref.ID != "app" || def.Ref.Version != 0 || len(def.Chain) != 1 || def.Chain[0].Ref.Version != 0 {
		t.Fatalf("built %q from %+v", fb.tags[0], def.Ref)
	}
	if !reflect.DeepEqual(def.Harness, harnessinstall.Default()) {
		t.Fatalf("harness = %+v, want the default", def.Harness)
	}
	if want := studio.ChainDigest(chainKits(def.Chain), def.Harness); def.Ref.Digest != want {
		t.Fatalf("digest = %s, want %s", def.Ref.Digest, want)
	}
	df, err := util.ReadFile(def.Chain[0].Files, "Dockerfile")
	if err != nil || !strings.Contains(string(df), "RUN echo one") {
		t.Fatalf("the draft's files: %q %v", df, err)
	}
}

func TestDraftBuildOnAChain(t *testing.T) {
	st := NewMemStore()
	if _, _, err := PushKitFiles(st, "web", kittreetest.Files("kit.yml", "kind: studio\n", "Dockerfile", "ARG JAM_STUDIO_BASE_IMAGE\nFROM ${JAM_STUDIO_BASE_IMAGE}\n")); err != nil {
		t.Fatal(err)
	}
	pushApp(t, st, "kit.yml", "kind: studio\nbase: {kit: web}\n")
	d, err := OpenKitDraft(st, "app", 0, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	fb := &fakeDraftBuilder{}
	b := NewKitDraftBuilds(context.Background(), st, fb, slog.New(slog.DiscardHandler))
	if _, err := b.Start("app", DraftETag(d.Tree), "local"); err != nil {
		t.Fatal(err)
	}
	finished(t, b)
	def := fb.defs[0]
	if len(def.Chain) != 2 || def.Chain[0].Ref.ID != "web" || def.Chain[0].Ref.Version != 1 || def.Chain[1].Ref.ID != "app" || def.Chain[1].Ref.Version != 0 {
		t.Fatalf("chain = %+v", def.Chain)
	}
	if _, err := util.ReadFile(def.Chain[0].Files, "Dockerfile"); err != nil {
		t.Fatalf("the parent's files must be loaded: %v", err)
	}
}

func TestDraftBuildRefusals(t *testing.T) {
	fb := &fakeDraftBuilder{}
	b, st, etag, _ := draftBuildsFor(t, fb)
	_, err := b.Start("app", "", "local")
	wantErr(t, err, "draft app: If-Match is required (send the etag from the draft status)", 428)
	_, err = b.Start("app", `"`+strings.Repeat("0", 64)+`"`, "local")
	wantErr(t, err, "draft app changed since you loaded it (reload)", 412)
	_, err = b.Start("a b\n", etag, "local")
	wantErr(t, err, unsafeKitNameText, 400)
	_, err = b.Start("nope", etag, "local")
	wantErr(t, err, "no such kit", 404)
	// A build arg named like a reserved secret is a problem: nothing builds.
	log := slog.New(slog.DiscardHandler)
	s, err := PutKitDraftFile(st, log, "app", "kit.yml", 0, []byte("kind: studio\nbuild-args:\n  AT_TASK_GIT_TOKEN: x\n"), etag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Start("app", s.ETag, "local")
	if err == nil || WriteStatus(err, 500) != 400 || !strings.HasPrefix(err.Error(), "draft app has problems: ") ||
		!strings.Contains(err.Error(), `build-arg "AT_TASK_GIT_TOKEN" is a reserved secret name`) {
		t.Fatalf("err = %v", err)
	}
	s, err = RemoveKitDraftFile(st, log, "app", "kit.yml", s.ETag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Start("app", s.ETag, "local")
	wantErr(t, err, "draft app has problems: kit app: kit.yml missing at the root", 400)
	if fb.builds() != 0 {
		t.Fatal("a refused build ran")
	}
	if v, err := b.Status("app", 0); err != nil || v.State != DraftBuildUnknown {
		t.Fatalf("status = %+v, %v", v, err)
	}
	_, err = b.Status("app", -1)
	wantErr(t, err, "since must be a log offset (0 or more)", 400)
	_, err = b.Status("nope", 0)
	wantErr(t, err, "no such kit", 404)
}

// Review Focus 3: two starts race; exactly one builds.
func TestDraftBuildOnePerKit(t *testing.T) {
	fb := &fakeDraftBuilder{started: make(chan string, 4), release: make(chan struct{})}
	b, _, etag, _ := draftBuildsFor(t, fb)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := b.Start("app", etag, "local")
			errs <- err
		}()
	}
	e1, e2 := <-errs, <-errs
	if (e1 == nil) == (e2 == nil) {
		t.Fatalf("errs = %v, %v (want one start and one refusal)", e1, e2)
	}
	refused := e1
	if refused == nil {
		refused = e2
	}
	wantErr(t, refused, "kit app already has a draft build running", 409)
	<-fb.started
	close(fb.release)
	if v := finished(t, b); v.State != DraftBuildSucceeded {
		t.Fatalf("state = %s", v.State)
	}
	if _, err := b.Start("app", etag, "local"); err != nil {
		t.Fatalf("a new build after the last ended: %v", err)
	}
	<-fb.started
	finished(t, b)
	if fb.builds() != 2 {
		t.Fatalf("builds = %d", fb.builds())
	}
}

func TestDraftBuildCancel(t *testing.T) {
	fb := &fakeDraftBuilder{started: make(chan string, 1), release: make(chan struct{})}
	b, _, etag, _ := draftBuildsFor(t, fb)
	if _, err := b.Start("app", etag, "local"); err != nil {
		t.Fatal(err)
	}
	<-fb.started
	v, err := b.Cancel(context.Background(), "app")
	if err != nil || v.State != DraftBuildFailed || v.Error != "cancelled" || !strings.HasSuffix(v.Log, "build failed: cancelled\n") {
		t.Fatalf("cancel = %+v, %v", v, err)
	}
	_, err = b.Cancel(context.Background(), "app")
	wantErr(t, err, "kit app has no draft build running", 404)
	_, err = b.Cancel(context.Background(), "a b")
	wantErr(t, err, unsafeKitNameText, 400)
}

// Review Focus 2: shutdown cancels a running build; a restart knows nothing of it.
func TestDraftBuildShutdown(t *testing.T) {
	fb := &fakeDraftBuilder{started: make(chan string, 1), release: make(chan struct{})}
	b, st, etag, stop := draftBuildsFor(t, fb)
	if _, err := b.Start("app", etag, "local"); err != nil {
		t.Fatal(err)
	}
	<-fb.started
	stop()
	if !b.Wait(5 * time.Second) {
		t.Fatal("builds still running after shutdown")
	}
	if v, _ := b.Status("app", 0); v.State != DraftBuildFailed || v.Error != "cancelled: Jam is shutting down" {
		t.Fatalf("status = %+v", v)
	}
	again := NewKitDraftBuilds(context.Background(), st, fb, slog.New(slog.DiscardHandler))
	if v, err := again.Status("app", 0); err != nil || v.State != DraftBuildUnknown || v.DraftETag != etag || v.ID != "" {
		t.Fatalf("after a restart = %+v, %v", v, err)
	}
}

// Review Focus 1: the draft is edited (and GC'd) while its build runs; the
// build finishes on the tree it started from and says it is not current.
func TestDraftBuildEditedWhileRunning(t *testing.T) {
	fb := &fakeDraftBuilder{started: make(chan string, 1), release: make(chan struct{}), readFile: "Dockerfile"}
	b, st, etag, _ := draftBuildsFor(t, fb)
	d1, _ := st.GetKitDraft("app")
	tree1, _ := st.KitTree(d1.Tree)
	old, _ := tree1.Lookup("Dockerfile")
	if _, err := b.Start("app", etag, "local"); err != nil {
		t.Fatal(err)
	}
	<-fb.started
	s, err := PutKitDraftFile(st, slog.New(slog.DiscardHandler), "app", "Dockerfile", 0, []byte("FROM scratch\n"), etag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.KitBlob(old.SHA256); ok {
		t.Fatal("setup: the first draft Dockerfile should have been collected")
	}
	if v, _ := b.Status("app", 0); v.State != DraftBuildRunning || v.Tree != d1.Tree || v.Current || v.DraftETag != s.ETag {
		t.Fatalf("status mid-build = %+v", v)
	}
	close(fb.release)
	v := finished(t, b)
	if v.State != DraftBuildSucceeded || v.Tree != d1.Tree || v.Current || !strings.Contains(v.Log, "file: ARG JAM_STUDIO_BASE_IMAGE\nFROM ${JAM_STUDIO_BASE_IMAGE}\nRUN echo one\n") {
		t.Fatalf("finished = %+v", v)
	}
}

// Review Focus 5: the log keeps its last KitDraftLogCap bytes, offsets stay
// absolute, and an incomplete UTF-8 tail waits while the build runs.
func TestBuildLogKeepsItsTail(t *testing.T) {
	l := &buildLog{cap: KitDraftLogCap}
	line := strings.Repeat("x", 1023) + "\n"
	for range 3 * 1024 { // 3 MiB
		_, _ = l.Write([]byte(line))
	}
	text, from, end := l.read(0, true)
	if end != 3<<20 || from != 2<<20 || len(text) != KitDraftLogCap || text[len(text)-1024:] != line {
		t.Fatalf("from %d end %d len %d", from, end, len(text))
	}
	if text, from, _ = l.read(end-10, true); from != end-10 || len(text) != 10 {
		t.Fatalf("read(end-10) = %d bytes from %d", len(text), from)
	}
	if text, from, _ = l.read(end+5, true); text != "" || from != end {
		t.Fatalf("read past the end = %q from %d", text, from)
	}
	u := &buildLog{cap: KitDraftLogCap}
	_, _ = u.Write([]byte("é")[:1])
	if text, _, end := u.read(0, false); text != "" || end != 0 {
		t.Fatalf("an incomplete rune was returned: %q %d", text, end)
	}
	_, _ = u.Write([]byte("é")[1:])
	if text, _, end := u.read(0, false); text != "é" || end != 2 {
		t.Fatalf("read = %q %d", text, end)
	}
}

func TestDraftBuildLogSkipped(t *testing.T) {
	fb := &fakeDraftBuilder{write: strings.Repeat("y", KitDraftLogCap+512)}
	b, _, etag, _ := draftBuildsFor(t, fb)
	if _, err := b.Start("app", etag, "local"); err != nil {
		t.Fatal(err)
	}
	v := finished(t, b)
	if !v.LogSkipped || v.LogFrom == 0 || len(v.Log) != KitDraftLogCap || !strings.HasSuffix(v.Log, "(sha256:img)\n") {
		t.Fatalf("skipped %v from %d len %d", v.LogSkipped, v.LogFrom, len(v.Log))
	}
	if tail, _ := b.Status("app", v.LogEnd); tail.Log != "" || tail.LogSkipped || tail.LogFrom != v.LogEnd {
		t.Fatalf("follow from the end = %+v", tail)
	}
}

// Ruling F6: once shutdown begins (the serve context cancelled, or Wait
// called) a start is refused, 503, and nothing builds.
func TestDraftBuildStartRefusedAfterShutdown(t *testing.T) {
	fb := &fakeDraftBuilder{}
	b, _, etag, stop := draftBuildsFor(t, fb)
	stop()
	_, err := b.Start("app", etag, "local")
	wantErr(t, err, "Jam is shutting down", 503)

	b2, _, etag2, _ := draftBuildsFor(t, fb)
	if !b2.Wait(5 * time.Second) {
		t.Fatal("an idle registry did not finish waiting")
	}
	_, err = b2.Start("app", etag2, "local")
	wantErr(t, err, "Jam is shutting down", 503)
	if fb.builds() != 0 {
		t.Fatal("a refused build ran")
	}
}

// Ruling F7: a start refused because a build is running copies no files: the
// refusal comes before the definition's blobs are read.
func TestDraftBuildRunningRefusedBeforeCopying(t *testing.T) {
	fb := &fakeDraftBuilder{started: make(chan string, 1), release: make(chan struct{})}
	b, st, etag, _ := draftBuildsFor(t, fb)
	if _, err := b.Start("app", etag, "local"); err != nil {
		t.Fatal(err)
	}
	<-fb.started
	d, _ := st.GetKitDraft("app")
	tree, _ := st.KitTree(d.Tree)
	df, _ := tree.Lookup("Dockerfile")
	reads := &countingBlobs{Store: st, sha: df.SHA256}
	b.store = reads
	_, err := b.Start("app", etag, "local")
	wantErr(t, err, "kit app already has a draft build running", 409)
	if n := reads.count(); n != 0 {
		t.Fatalf("the refused start read the Dockerfile blob %d times", n)
	}
	close(fb.release)
	finished(t, b)
}

// countingBlobs counts the reads of one blob.
type countingBlobs struct {
	Store
	sha string
	mu  sync.Mutex
	n   int
}

func (c *countingBlobs) KitBlob(sha string) ([]byte, bool) {
	if sha == c.sha {
		c.mu.Lock()
		c.n++
		c.mu.Unlock()
	}
	return c.Store.KitBlob(sha)
}

func (c *countingBlobs) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// The log is written by a command's stdout and stderr copiers at once:
// concurrent writes each land whole, and none is lost.
func TestBuildLogConcurrentWriters(t *testing.T) {
	l := &buildLog{cap: KitDraftLogCap}
	const writers, lines = 8, 500
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			line := strings.Repeat(string(rune('a'+w)), 63) + "\n"
			for range lines {
				_, _ = l.Write([]byte(line))
			}
		})
	}
	wg.Wait()
	text, from, end := l.read(0, true)
	if from != 0 || end != writers*lines*64 || len(text) != writers*lines*64 {
		t.Fatalf("from %d end %d len %d", from, end, len(text))
	}
	counts := map[string]int{}
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			continue
		}
		if len(line) != 64 || strings.Trim(line[:63], line[:1]) != "" {
			t.Fatalf("a torn write: %q", line)
		}
		counts[line[:1]]++
	}
	for w := range writers {
		if c := counts[string(rune('a'+w))]; c != lines {
			t.Fatalf("writer %d: %d lines, want %d", w, c, lines)
		}
	}
}

// A build copies each parent's version tree: a parent version that is gone
// or unreadable fails the copy; only a pre-tree (text) parent has no files.
func TestLoadDraftFilesParents(t *testing.T) {
	st := NewMemStore()
	doc := Kit{Name: "old", Current: 1, Versions: map[int]string{1: `{"kind":"studio","prompt":"p"}`}, Broken: map[int]string{2: "its base.context is not a readable tar.gz"}}
	if err := st.PutKitDocs([]Kit{doc}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	self := kittree.Tree{}
	def := func(parent KitRef) *KitDefinition {
		return &KitDefinition{Chain: []KitLink{{Ref: parent}, {Ref: KitRef{ID: "app"}}}}
	}
	d := def(KitRef{ID: "old", Version: 1})
	if _, err := loadDraftFilesLocked(st, d, self); err != nil || d.Chain[0].Files != nil {
		t.Fatalf("pre-tree parent: %v (files %v)", err, d.Chain[0].Files)
	}
	for _, p := range []KitRef{{ID: "old", Version: 2}, {ID: "old", Version: 3}, {ID: "gone", Version: 1}} {
		if _, err := loadDraftFilesLocked(st, def(p), self); err == nil {
			t.Fatalf("parent %s: no error", p)
		}
	}
}
