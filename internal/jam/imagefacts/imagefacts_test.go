package imagefacts

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/backend/colima"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/runner"
)

const imageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const inspectJSON = `{"Id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Config":{"Env":["PATH=/usr/bin","DB_PASSWORD=hunter2"],"Labels":{"cove.kit":"my-app@v1"}}}`

type entry struct {
	name string
	body []byte
	dir  bool
	link string // symlink target (TypeSymlink)
	hard string // hardlink target (TypeLink)
}

func seedTar(t *testing.T, entries ...entry) string {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		switch {
		case e.dir:
			h = &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}
		case e.link != "":
			h = &tar.Header{Name: e.name, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: e.link}
		case e.hard != "":
			h = &tar.Header{Name: e.name, Mode: 0o644, Typeflag: tar.TypeLink, Linkname: e.hard}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	return b.String()
}

func newReader(f runner.Runner) *Reader { return New(f, colima.NewWithContext(f, "colima-jam")) }

// assertNeverStarts fails when any call runs a container.
func assertNeverStarts(t *testing.T, calls []runner.Call) {
	t.Helper()
	for i, c := range calls {
		for _, verb := range []string{"start", "run", "exec"} {
			if slices.Contains(c.Args, verb) {
				t.Fatalf("call %d runs %q: %v", i, verb, c.Args)
			}
		}
	}
}

func TestImageFactsArgvNeverStarts(t *testing.T) {
	tarball := seedTar(t, entry{name: ".init-agent-data/", dir: true}, entry{name: ".init-agent-data/CLAUDE.md", body: []byte("@SANDBOX.md\n")})
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: inspectJSON}, {Stdout: "0123456789abcdef\n"}, {Stdout: tarball}, {}}}
	got, err := newReader(f).ImageFacts(context.Background(), "cove-kit:abc-asm")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"--context", "colima-jam", "image", "inspect", "--format", "{{json .}}", "cove-kit:abc-asm"},
		{"--context", "colima-jam", "create", "--pull=never", imageID, "/bin/true"},
		{"--context", "colima-jam", "cp", "0123456789abcdef:" + SeedDir, "-"},
		{"--context", "colima-jam", "rm", "-f", "-v", "0123456789abcdef"},
	}
	if len(f.Calls) != len(want) {
		t.Fatalf("calls = %+v", f.Calls)
	}
	for i, c := range f.Calls {
		if c.Name != "docker" || !slices.Equal(c.Args, want[i]) {
			t.Errorf("call %d = %s %v, want docker %v", i, c.Name, c.Args, want[i])
		}
	}
	assertNeverStarts(t, f.Calls)
	if got.ID != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || len(got.Env) != 2 || got.Labels["cove.kit"] != "my-app@v1" {
		t.Fatalf("facts = %+v", got)
	}
	if len(got.Seed) != 1 || got.Seed[0].Path != "CLAUDE.md" || !got.Seed[0].HasText || got.SeedNote != "" || got.SeedErr {
		t.Fatalf("seed = %+v note %q err %v", got.Seed, got.SeedNote, got.SeedErr)
	}
}

func TestImageFactsCachedByID(t *testing.T) {
	tarball := seedTar(t, entry{name: ".init-agent-data/CLAUDE.md", body: []byte("x")})
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: inspectJSON}, {Stdout: "0123456789abcdef"}, {Stdout: tarball}, {}, {Stdout: inspectJSON}}}
	r := newReader(f)
	if _, err := r.ImageFacts(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	got, err := r.ImageFacts(context.Background(), "t2-same-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 5 {
		t.Fatalf("calls = %d, want 4 + one inspect (the seed is cached per image id)", len(f.Calls))
	}
	if len(got.Seed) != 1 || got.Seed[0].Path != "CLAUDE.md" {
		t.Fatalf("cached seed = %+v", got.Seed)
	}
}

func TestImageFactsCacheIsBoundedLRU(t *testing.T) {
	r := New(&runner.Fake{}, colima.NewWithContext(&runner.Fake{}, "x"))
	for i := range cacheSize + 1 {
		r.put(jam.ImageFacts{ID: string(rune('a' + i))})
		if i == 0 {
			continue
		}
		r.get("a") // keep "a" most recently used
	}
	if _, ok := r.get("a"); !ok {
		t.Fatal("the most recently used id was evicted")
	}
	if _, ok := r.get("b"); ok {
		t.Fatal("the least recently used id was kept")
	}
	if len(r.cache) != cacheSize || len(r.order) != cacheSize {
		t.Fatalf("cache %d order %d, want %d", len(r.cache), len(r.order), cacheSize)
	}
}

func TestImageFactsNotPresent(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Err: errors.New("exit status 1")}}}
	if _, err := newReader(f).ImageFacts(context.Background(), "cove-kit:gone"); !errors.Is(err, jam.ErrImageNotPresent) {
		t.Fatalf("err = %v, want ErrImageNotPresent", err)
	}
	if len(f.Calls) != 1 {
		t.Fatalf("a missing image must stop after inspect: %+v", f.Calls)
	}
	if _, err := newReader(&runner.Fake{}).ImageFacts(context.Background(), "--format"); !errors.Is(err, jam.ErrImageNotPresent) {
		t.Fatal("an image name that reads as a flag must be refused before docker runs")
	}
}

func TestImageFactsInspectGarbageUnreadable(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "not json"}}}
	if _, err := newReader(f).ImageFacts(context.Background(), "t"); !errors.Is(err, jam.ErrImageUnreadable) {
		t.Fatalf("err = %v, want ErrImageUnreadable", err)
	}
}

func TestImageFactsSeedDockerErrorNotCached(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: inspectJSON}, {Err: errors.New("daemon gone")}, {Stdout: inspectJSON}, {Err: errors.New("daemon gone")}}}
	r := newReader(f)
	got, err := r.ImageFacts(context.Background(), "t")
	if err != nil || got.Seed != nil || got.SeedNote != seedUnreadable || !got.SeedErr || len(got.Env) != 2 {
		t.Fatalf("facts = %+v, %v (want env and labels with a seed note and SeedErr)", got, err)
	}
	if _, err := r.ImageFacts(context.Background(), "t"); err != nil {
		t.Fatal(err)
	}
	if f.Calls[3].Args[2] != "create" {
		t.Fatalf("a docker error must not be cached: calls = %+v", f.Calls)
	}
}

func TestImageFactsCopyErrorStillRemovesContainer(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: inspectJSON}, {Stdout: "0123456789abcdef"}, {Stdout: "partial", Err: errors.New("cp failed")}, {}}}
	got, err := newReader(f).ImageFacts(context.Background(), "t")
	if err != nil || !got.SeedErr || got.SeedNote != seedUnreadable || got.Seed != nil {
		t.Fatalf("facts = %+v, %v", got, err)
	}
	last := f.Calls[len(f.Calls)-1]
	if len(f.Calls) != 4 || !slices.Equal(last.Args, []string{"--context", "colima-jam", "rm", "-f", "-v", "0123456789abcdef"}) {
		t.Fatalf("the created container must be removed after a failed cp: %+v", f.Calls)
	}
	assertNeverStarts(t, f.Calls)
}

func TestImageFactsCreateOddIDNeverCopies(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: inspectJSON}, {Stdout: "-rf /\n"}}}
	got, _ := newReader(f).ImageFacts(context.Background(), "t")
	if !got.SeedErr || len(f.Calls) != 2 {
		t.Fatalf("a create output that is not a container id must stop the read: %+v %+v", got, f.Calls)
	}
}

func TestImageFactsOversizeStreamIsCapped(t *testing.T) {
	// Many entries of 64 KiB push the docker cp stream past maxTarBytes.
	var es []entry
	for i := range 40 {
		es = append(es, entry{name: ".init-agent-data/f" + string(rune('a'+i%26)) + string(rune('a'+i/26)), body: bytes.Repeat([]byte("a"), 64<<10)})
	}
	tarball := seedTar(t, es...)
	if len(tarball) <= maxTarBytes {
		t.Fatalf("fixture %d bytes is within the cap", len(tarball))
	}
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: inspectJSON}, {Stdout: "0123456789abcdef"}, {Stdout: tarball}, {}}}
	got, err := newReader(f).ImageFacts(context.Background(), "t")
	if err != nil || got.SeedErr || !strings.Contains(got.SeedNote, "truncated") {
		t.Fatalf("facts note %q err %v seedErr %v", got.SeedNote, err, got.SeedErr)
	}
	var total int64
	for _, s := range got.Seed {
		total += s.Size
	}
	if total > MaxSeedBytes {
		t.Fatalf("listed %d bytes, cap %d", total, MaxSeedBytes)
	}
	if w, ok := f.Calls[2].Stdout.(*capWriter); !ok || w.buf.Len() > maxTarBytes {
		t.Fatalf("cp stdout must be a capped writer: %T", f.Calls[2].Stdout)
	}
}

// waitersForTest is how many callers wait on id's in-flight read.
func (x *Reader) waitersForTest(id string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	if c, ok := x.inflight[id]; ok {
		return c.waiters
	}
	return 0
}

// scriptRunner answers docker calls by verb, safe for concurrent callers.
// onCreate runs (outside the lock) on each create, numbered from 1.
type scriptRunner struct {
	mu       sync.Mutex
	calls    []runner.Call
	creates  int
	onCreate func(n int)
	tarball  string
}

func (g *scriptRunner) RunIO(_ io.Reader, stdout, _ io.Writer, name string, args ...string) error {
	g.mu.Lock()
	g.calls = append(g.calls, runner.Call{Name: name, Args: append([]string(nil), args...)})
	verb := args[2]
	if verb == "create" {
		g.creates++
	}
	n := g.creates
	g.mu.Unlock()
	switch verb {
	case "image":
		_, _ = io.WriteString(stdout, inspectJSON)
	case "create":
		if g.onCreate != nil {
			g.onCreate(n)
		}
		_, _ = io.WriteString(stdout, "0123456789abcdef")
	case "cp":
		_, _ = io.WriteString(stdout, g.tarball)
	}
	return nil
}
func (g *scriptRunner) Run(string, ...string) error                           { panic("unused") }
func (g *scriptRunner) RunEnv([]string, string, ...string) error              { panic("unused") }
func (g *scriptRunner) RunStdin(io.Reader, string, ...string) error           { panic("unused") }
func (g *scriptRunner) Probe(string, ...string) error                         { panic("unused") }
func (g *scriptRunner) Output(string, ...string) (string, error)              { panic("unused") }
func (g *scriptRunner) OutputEnv([]string, string, ...string) (string, error) { panic("unused") }

func (g *scriptRunner) createCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.creates
}

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Errorf("timed out waiting for %s", what)
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func TestImageFactsSeedReadIsSingleflight(t *testing.T) {
	g := &scriptRunner{tarball: seedTar(t, entry{name: ".init-agent-data/CLAUDE.md", body: []byte("x")})}
	r := New(g, colima.NewWithContext(g, "colima-jam"))
	// Hold the first create until the second caller is waiting on it.
	g.onCreate = func(n int) {
		if n == 1 {
			waitFor(t, "a second caller joining the in-flight read", func() bool { return r.waitersForTest(imageID) == 1 })
		}
	}
	var wg sync.WaitGroup
	results := make([]jam.ImageFacts, 2)
	for i := range 2 {
		wg.Go(func() {
			f, err := r.ImageFacts(context.Background(), "t")
			if err != nil {
				t.Error(err)
			}
			results[i] = f
		})
	}
	wg.Wait()
	if g.creates != 1 {
		t.Fatalf("creates = %d, want 1 (concurrent uncached reads of one image share one seed read)", g.creates)
	}
	for i, f := range results {
		if len(f.Seed) != 1 || f.Seed[0].Path != "CLAUDE.md" {
			t.Fatalf("result %d = %+v", i, f)
		}
	}
	assertNeverStarts(t, g.calls)
}

func TestImageFactsSeedPanicDoesNotWedge(t *testing.T) {
	g := &scriptRunner{onCreate: func(int) { panic("boom") }}
	r := New(g, colima.NewWithContext(g, "colima-jam"))
	for i := range 2 {
		done := make(chan jam.ImageFacts, 1)
		go func() {
			f, _ := r.ImageFacts(context.Background(), "t")
			done <- f
		}()
		select {
		case f := <-done:
			if !f.SeedErr || f.SeedNote != seedUnreadable || len(f.Env) != 2 {
				t.Fatalf("call %d after a panic = %+v, want SeedErr", i, f)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("call %d blocked: a panicked seed read wedged the image", i)
		}
	}
	if r.waitersForTest(imageID) != 0 || len(r.inflight) != 0 {
		t.Fatal("the in-flight entry outlived the panic")
	}
	if g.createCount() != 2 {
		t.Fatalf("creates = %d, want 2 (a panicked read is not cached)", g.createCount())
	}
}

func TestImageFactsWaiterHonoursContext(t *testing.T) {
	release := make(chan struct{})
	g := &scriptRunner{tarball: seedTar(t), onCreate: func(int) { <-release }}
	r := New(g, colima.NewWithContext(g, "colima-jam"))
	first := make(chan struct{})
	go func() {
		_, _ = r.ImageFacts(context.Background(), "t")
		close(first)
	}()
	waitFor(t, "the first read reaching create", func() bool { return g.createCount() == 1 })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan jam.ImageFacts, 1)
	go func() {
		f, _ := r.ImageFacts(ctx, "t")
		done <- f
	}()
	waitFor(t, "the second caller waiting", func() bool { return r.waitersForTest(imageID) == 1 })
	cancel()
	select {
	case f := <-done:
		if !f.SeedErr || len(f.Env) != 2 {
			t.Fatalf("a cancelled waiter = %+v, want SeedErr with env", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled waiter kept blocking on the in-flight read")
	}
	close(release)
	<-first
}

func TestImageFactsNoSeedDirIsCached(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: inspectJSON},
		{Stdout: "0123456789abcdef"},
		{Stderr: "Error response from daemon: Could not find the file /home/agent/.init-agent-data in container 0123456789abcdef\n", Err: errors.New("exit status 1")},
		{},
		{Stdout: inspectJSON},
	}}
	r := newReader(f)
	got, err := r.ImageFacts(context.Background(), "t")
	if err != nil || got.SeedErr || got.Seed != nil || got.SeedNote != seedNone {
		t.Fatalf("facts = %+v, %v (want a cacheable %q note)", got, err, seedNone)
	}
	if !slices.Equal(f.Calls[3].Args, []string{"--context", "colima-jam", "rm", "-f", "-v", "0123456789abcdef"}) {
		t.Fatalf("container not removed: %+v", f.Calls)
	}
	again, _ := r.ImageFacts(context.Background(), "t")
	if len(f.Calls) != 5 || again.SeedNote != seedNone {
		t.Fatalf("an image that seeds nothing must be cached by id: calls %d, %+v", len(f.Calls), again)
	}
	for _, stderr := range []string{"Error: No such container:path: 0123456789abcdef:/home/agent/.init-agent-data\n"} {
		f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: inspectJSON}, {Stdout: "0123456789abcdef"}, {Stderr: stderr, Err: errors.New("exit status 1")}, {}}}
		if got, _ := newReader(f).ImageFacts(context.Background(), "t"); got.SeedErr || got.SeedNote != seedNone {
			t.Errorf("stderr %q: %+v", stderr, got)
		}
	}
}

func TestImageFactsRejectsOddImageID(t *testing.T) {
	for _, id := range []string{"sha256:abc", "-x", "sha256:" + strings.Repeat("A", 64), "md5:" + strings.Repeat("a", 64)} {
		f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"Id":"` + id + `"}`}}}
		if _, err := newReader(f).ImageFacts(context.Background(), "t"); !errors.Is(err, jam.ErrImageUnreadable) || len(f.Calls) != 1 {
			t.Errorf("id %q: err %v calls %d", id, err, len(f.Calls))
		}
	}
}

func TestParseSeedTarCaps(t *testing.T) {
	var many []entry
	for i := range MaxSeedEntries + 1 {
		many = append(many, entry{name: ".init-agent-data/f" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + ".md", body: []byte("x")})
	}
	if _, note := parseSeedTar(strings.NewReader(seedTar(t, many...))); !strings.Contains(note, "200 entries") {
		t.Errorf("entries note = %q", note)
	}
	big := bytes.Repeat([]byte("a"), MaxSeedBytes/2+1)
	files, note := parseSeedTar(strings.NewReader(seedTar(t, entry{name: ".init-agent-data/a", body: big}, entry{name: ".init-agent-data/b", body: big})))
	if len(files) != 1 || !strings.Contains(note, "1048576 bytes") || files[0].HasText {
		t.Errorf("size cap: files %+v note %q (a 512 KiB file is listed, not shown)", files, note)
	}
	files, _ = parseSeedTar(strings.NewReader(seedTar(t,
		entry{name: ".init-agent-data/bin", body: []byte{0xff, 0xfe}},
		entry{name: ".init-agent-data/../../etc/passwd", body: []byte("root")},
		entry{name: ".init-agent-data/skills/s/SKILL.md", body: []byte("---\nname: s\n---\n")})))
	if len(files) != 3 || files[0].Path != "bin" || files[0].HasText || files[1].Path != "passwd" || files[2].Path != "skills/s/SKILL.md" || !files[2].HasText {
		t.Errorf("files = %+v (binary unshown, .. cleaned under the seed, sorted)", files)
	}
	if _, note := parseSeedTar(strings.NewReader(seedTar(t, entry{name: ".init-agent-data/a", body: []byte("x")})[:700])); !strings.Contains(note, "incomplete") {
		t.Errorf("a cut archive = %q", note)
	}
}

func TestParseSeedTarCapsBeforeSkipping(t *testing.T) {
	// An entry outside the seed paths still counts against the byte cap, so
	// its body is never drained past it.
	_, note := parseSeedTar(strings.NewReader(seedTar(t, entry{name: ".init-agent-data", body: bytes.Repeat([]byte("a"), MaxSeedBytes+1)})))
	if !strings.Contains(note, "1048576 bytes") {
		t.Fatalf("note = %q", note)
	}
}

func TestParseSeedTarOnlyRegularFiles(t *testing.T) {
	files, note := parseSeedTar(strings.NewReader(seedTar(t,
		entry{name: ".init-agent-data/", dir: true},
		entry{name: ".init-agent-data/evil", link: "/etc/shadow"},
		entry{name: ".init-agent-data/hard", hard: "/etc/shadow"},
		entry{name: ".init-agent-data", body: []byte("top-level file, no seed path")},
		entry{name: ".init-agent-data/ok.md", body: []byte("fine")})))
	if note != "" || len(files) != 1 || files[0].Path != "ok.md" || files[0].Text != "fine" {
		t.Fatalf("files = %+v note %q (symlinks, hardlinks, dirs and the root itself are skipped)", files, note)
	}
}

func TestParseSeedTarIncompleteArchives(t *testing.T) {
	whole := seedTar(t, entry{name: ".init-agent-data/a", body: bytes.Repeat([]byte("z"), 1000)})
	for name, cut := range map[string]int{
		"mid-body":       600,
		"block boundary": 512 + 1024, // header + padded body, no end marker
		"one zero block": 512 + 1024 + 512,
		"mid end marker": 512 + 1024 + 700,
	} {
		if _, note := parseSeedTar(strings.NewReader(whole[:cut])); note != noteIncomplete {
			t.Errorf("%s: note = %q, want incomplete", name, note)
		}
	}
	if files, note := parseSeedTar(strings.NewReader(whole)); note != "" || len(files) != 1 || files[0].Size != 1000 {
		t.Errorf("whole archive: %+v note %q", files, note)
	}
	if files, note := parseSeedTar(strings.NewReader(seedTar(t))); note != "" || files != nil {
		t.Errorf("empty archive: %+v note %q", files, note)
	}
	if _, note := parseSeedTar(strings.NewReader("")); note != noteIncomplete {
		t.Errorf("no archive at all: note %q", note)
	}
}
