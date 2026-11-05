package launcher

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/studio"
)

// recProgress records every activity a launcher starts, and its steps.
type recProgress struct {
	mu    sync.Mutex
	log   []string // "start kind kit", "step text", "done"
	steps []string // "step|detail"
}

func (r *recProgress) track(kind string, kit jam.KitRef) Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, "start "+kind+" "+kit.String())
	return r
}

func (r *recProgress) Step(step, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, step+"|"+detail)
	r.log = append(r.log, "step "+step+"|"+detail)
}

func (r *recProgress) Done() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, "done")
}

func TestParseBuildStep(t *testing.T) {
	for _, c := range []struct {
		line  string
		stage string
		k, m  int
		ok    bool
	}{
		{"#7 [3/12] RUN apt-get update", "", 3, 12, true},
		{"#12 [harness 2/5] RUN npm install", "harness", 2, 5, true},
		{"#14 [stage-1 10/21] COPY image-files/ /", "stage-1", 10, 21, true},
		{"#1 [internal] load build definition from Dockerfile", "", 0, 0, false},
		{"#7 CACHED", "", 0, 0, false},
		{"#7 0.512 [3/12] not a header", "", 0, 0, false},
		{"Step 3/12 : RUN x", "", 0, 0, false},
		{"#7 [0/12] RUN x", "", 0, 0, false},
		{"#7 [13/12] RUN x", "", 0, 0, false},
	} {
		stage, k, m, ok := parseBuildStep(c.line)
		if stage != c.stage || k != c.k || m != c.m || ok != c.ok {
			t.Errorf("parseBuildStep(%q) = %q %d %d %v, want %q %d %d %v", c.line, stage, k, m, ok, c.stage, c.k, c.m, c.ok)
		}
	}
}

// The build-output parser reports "building" with the highest step of the
// stage building now, across split writes; a new stage starts over, and
// a repeated or lower step is not reported again.
func TestBuildStepsWriter(t *testing.T) {
	r := &recProgress{}
	w := newBuildSteps(r)
	out := "#5 [internal] load metadata\n#6 [harness 1/3] FROM base\n#7 [harness 2/3] RUN a\n#7 CACHED\n" +
		"#8 [harness 3/3] RUN b\n#8 0.1 output [9/9] noise\n#9 [stage-1 1/4] COPY x\n#10 [stage-1 3/4] RUN c\n#9 [stage-1 2/4] late\n#11 [stage-1 4/4] RU"
	for i := 0; i < len(out); i += 7 {
		_, _ = io.WriteString(w, out[i:min(i+7, len(out))])
	}
	_, _ = io.WriteString(w, "N d\n")
	want := []string{"building|step 1/3", "building|step 2/3", "building|step 3/3", "building|step 1/4", "building|step 3/4", "building|step 4/4"}
	if strings.Join(r.steps, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %q, want %q", r.steps, want)
	}
}

// A runaway line (no newline) is dropped, not buffered without bound.
func TestBuildStepsWriterBoundsALine(t *testing.T) {
	r := &recProgress{}
	w := newBuildSteps(r)
	_, _ = io.WriteString(w, strings.Repeat("x", maxBuildLine+10))
	_, _ = io.WriteString(w, "#7 [1/2] RUN a\n")
	if len(r.steps) != 0 {
		t.Fatalf("steps = %q: a line continuing a dropped one must not count", r.steps)
	}
	_, _ = io.WriteString(w, "#8 [2/2] RUN b\n")
	if strings.Join(r.steps, ",") != "building|step 2/2" {
		t.Fatalf("steps = %q", r.steps)
	}
}

// outputOps is a fakeOps that is a backend.TeedKitBuilder: its build writes
// docker-like progress to the output writer it was given.
type outputOps struct {
	fakeOps
	out    io.Writer
	output string
}

func (o *outputOps) WithBuildOutput(w io.Writer) backend.KitImageBuilder {
	o.out = w
	return o
}

func (o *outputOps) BuildKitImage(buildDir, tag, base string, buildArgs map[string]string, noCache bool) (string, error) {
	if o.out != nil {
		_, _ = io.WriteString(o.out, o.output)
	}
	return o.fakeOps.BuildKitImage(buildDir, tag, base, buildArgs, noCache)
}

// PrepareKit reports a prepare-kit activity for its kit: assembling,
// resolving the base, then building with the steps docker printed; Done
// once it returns.
func TestPrepareKitReportsProgress(t *testing.T) {
	ops := &outputOps{output: "#6 [1/2] FROM x\n#7 [2/2] RUN y\n"}
	r := &recProgress{}
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
	l.cfg.Track = r.track
	def := studioKitDef("web", 2, studio.StudioKit{Kind: studio.Kind})
	if _, err := l.PrepareKit(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	want := []string{"start prepare-kit web@v2", "step assembling|", "step resolving base|", "step building|",
		"step building|step 1/2", "step building|step 2/2", "done"}
	if strings.Join(r.log, ",") != strings.Join(want, ",") {
		t.Fatalf("progress = %q\nwant %q", r.log, want)
	}
}

// A chain reports each stage it resolves, root first.
func TestPrepareKitReportsChainStages(t *testing.T) {
	ops := &fakeOps{}
	r := &recProgress{}
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
	l.cfg.Track = r.track
	root := chainLink("base-dev", 1, studio.Base{})
	mid := ctxLink(t, "web-dev", 2, "base-dev", "mid")
	leaf := ctxLink(t, "my-app", 3, "web-dev", "leaf")
	if _, err := l.PrepareKit(context.Background(), chainDef(nil, root, mid, leaf)); err != nil {
		t.Fatal(err)
	}
	want := []string{"assembling|", "resolving base|", "building stage 1/3|base-dev@v1", "building stage 2/3|web-dev@v2",
		"building stage 3/3|my-app@v3", "building|"}
	if strings.Join(r.steps, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %q\nwant %q", r.steps, want)
	}
}

// An already-prepared kit reports nothing; nor does a launcher with no hook,
// and a failed prepare still ends its activity.
func TestPrepareKitProgressEdges(t *testing.T) {
	def := studioKitDef("web", 2, studio.StudioKit{Kind: studio.Kind})
	inv := &fakeInv{}
	inv.set(def.Ref, true)
	r := &recProgress{}
	l := newPrepareLauncher(&fakeOps{}, inv, noAsm)
	l.cfg.Track = r.track
	if _, err := l.PrepareKit(context.Background(), def); err != nil || len(r.log) != 0 {
		t.Fatalf("ready kit: %v, progress %q", err, r.log)
	}

	if _, err := newPrepareLauncher(&fakeOps{}, &fakeInv{}, noAsm).PrepareKit(context.Background(), def); err != nil {
		t.Fatalf("no hook: %v", err)
	}

	l = newPrepareLauncher(&fakeOps{buildErr: fmt.Errorf("boom")}, &fakeInv{}, noAsm)
	r = &recProgress{}
	l.cfg.Track = r.track
	if _, err := l.PrepareKit(context.Background(), def); err == nil {
		t.Fatal("want the build error")
	}
	if n := len(r.log); n == 0 || r.log[n-1] != "done" {
		t.Fatalf("failed prepare: progress %q, want it done", r.log)
	}
}

// On the colima backend, PrepareKit's step parser reads the docker build's
// --progress=plain output, and a draft build's reads its log's copy.
func TestColimaBuildsReportDockerSteps(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "#5 [1/3] FROM base\n", Stderr: "#7 [3/3] RUN install\n#7 DONE\n"}, // docker build --progress=plain …
		{Stdout: fakeImageID + "\n"}, // docker inspect --format {{.Id}} <tag>
	}}
	l := colimaDraftLauncher(t, f)
	r := &recProgress{}
	l.cfg.Track = r.track
	def := studioKitDef("web", 2, studio.StudioKit{Kind: studio.Kind})
	if _, err := l.PrepareKit(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.steps, ","); got != "assembling|,resolving base|,building|,building|step 1/3,building|step 3/3" {
		t.Fatalf("prepare steps = %q", got)
	}

	f = &runner.Fake{Outputs: []runner.FakeResult{
		{}, // docker info (preflight)
		{Stdout: "#5 [2/4] RUN install\n#5 DONE\n"}, // docker build --progress=plain …
		{Stdout: fakeImageID + "\n"},                // docker inspect --format {{.Id}} <tag>
	}}
	l = colimaDraftLauncher(t, f)
	r = &recProgress{}
	l.cfg.Track = r.track
	link := treeLink(t, "app", 0, "kit.yml", "kind: studio\n")
	ddef := KitDefinition{Ref: link.Ref, Kit: link.Kit, Chain: []jam.KitLink{link}, Harness: harnessinstall.Default()}
	var log strings.Builder
	if _, err := l.BuildKitDraft(context.Background(), ddef, draftTag, &log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "#5 [2/4] RUN install") {
		t.Fatalf("log lost the build output: %q", log.String())
	}
	want := "start build-kit-draft app@v0,step assembling|,step resolving base|,step building|,step building|step 2/4,done"
	if got := strings.Join(r.log, ","); got != want {
		t.Fatalf("draft progress = %q\nwant %q", got, want)
	}
}
