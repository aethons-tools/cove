package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/backend/colima"
	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/studio"
)

const draftTag = "jam-kit-draft:app-0123456789ab"

// colimaDraftLauncher is a launcher over the real colima backend driven by
// f, so a draft build's docker argv is asserted call by call.
func colimaDraftLauncher(t *testing.T, f *runner.Fake) *Launcher {
	t.Helper()
	return New(Config{
		Ops: colima.NewWithContext(f, "colima"), Runner: &runner.Fake{},
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		BuildRoot: t.TempDir(), Inventory: &fakeInv{}, assemble: noAsm, sleep: func(time.Duration) {},
	})
}

// dockerArgsAll is the args (after the pinned context) of every `docker <sub>` call.
func dockerArgsAll(calls []runner.Call, sub string) (out [][]string, stdin []string) {
	for _, c := range calls {
		if a := c.Args; c.Name == "docker" && len(a) > 2 && a[0] == "--context" && a[2] == sub {
			out, stdin = append(out, a[2:]), append(stdin, c.Stdin)
		}
	}
	return out, stdin
}

func after(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// gatePasses is the gate's layer reads of every compiled-in blessed ref,
// each a prefix of the stage's ["sha256:a","sha256:b","sha256:c"]. The
// committed watermark always blesses one ref, so an empty set is a broken
// build, not a reason to skip.
func gatePasses(t *testing.T) []runner.FakeResult {
	t.Helper()
	blessed := basedigest.BlessedRefs()
	if len(blessed) == 0 {
		t.Fatal("no blessed base refs compiled in (internal/basedigest/blessed/watermark.txt holds one)")
	}
	outs := make([]runner.FakeResult, len(blessed))
	for i := range blessed {
		outs[i] = runner.FakeResult{Stdout: `["sha256:a","sha256:b"]`}
	}
	return outs
}

// An unchained draft: the real BuildKitImage argv with the draft tag and
// the kit's build args, the build streamed to the log, no env, no
// jam-kit: tag anywhere, and no build-arg value in the launcher's lines.
func TestBuildKitDraftArgv(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{}, // docker info (preflight)
		{Stdout: "#5 [3/3] RUN install\n#5 DONE\n"}, // docker build --progress=plain …
		{Stdout: fakeImageID + "\n"},                // docker inspect --format {{.Id}} <tag>
	}}
	l := colimaDraftLauncher(t, f)
	link := treeLink(t, "app", 0, "kit.yml", "kind: studio\nbuild-args:\n  NPM_TOKEN: npmSECRET\n")
	def := KitDefinition{Ref: link.Ref, Kit: link.Kit, Chain: []jam.KitLink{link}, Harness: harnessinstall.Default()}
	var log bytes.Buffer
	id, err := l.BuildKitDraft(context.Background(), def, draftTag, &log)
	if err != nil || id != fakeImageID {
		t.Fatalf("BuildKitDraft = %q, %v", id, err)
	}
	builds, _ := dockerArgsAll(f.Calls, "build")
	if len(builds) != 1 || after(builds[0], "-t") != draftTag || !slices.Contains(builds[0], "NPM_TOKEN=npmSECRET") ||
		!slices.Contains(builds[0], "BASE="+basedigest.DefaultRef()) || !slices.Contains(builds[0], "--progress=plain") {
		t.Fatalf("builds = %v", builds)
	}
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if strings.HasPrefix(a, "jam-kit:") {
				t.Fatalf("a draft build named a kit version's image tag: %v", c.Args)
			}
		}
		if len(c.Env) != 0 {
			t.Fatalf("a draft build passed an env: %+v", c)
		}
	}
	if !strings.Contains(log.String(), "#5 [3/3] RUN install\n#5 DONE\n") || !strings.Contains(log.String(), "== image "+draftTag+" FROM ") {
		t.Fatalf("log = %q", log.String())
	}
	if strings.Contains(log.String(), "npmSECRET") {
		t.Fatal("the launcher's log lines must not print build-arg values")
	}
}

// A draft with a Dockerfile on a parent: its context streams from the
// draft's files to `docker build -q … -` FROM the parent, is gated, and is
// tagged <tag>-stage, never the link's kitStageTag; the image builds FROM
// that stage. The stage's stderr reaches the log.
func TestBuildKitDraftStageArgv(t *testing.T) {
	outs := []runner.FakeResult{
		{Stdout: "sha256:child\n", Stderr: "#3 [2/2] RUN make\n"}, // docker build -q … - (the draft's stage)
		{}, // docker tag sha256:child jam-kit-base:child
		{Stdout: `["sha256:a","sha256:b","sha256:c"]`}, // the stage's layers
	}
	outs = append(outs, gatePasses(t)...)
	outs = append(outs,
		runner.FakeResult{},                           // docker tag jam-kit-base:child <tag>-stage
		runner.FakeResult{},                           // docker info
		runner.FakeResult{},                           // docker build --progress=plain …
		runner.FakeResult{Stdout: fakeImageID + "\n"}) // docker inspect
	f := &runner.Fake{Outputs: outs}
	l := colimaDraftLauncher(t, f)
	root := treeLink(t, "web", 2, "kit.yml", "kind: studio\n")
	app := treeLink(t, "app", 0, "kit.yml", "kind: studio\nbase: {kit: web}\n", "Dockerfile", df+"RUN make\n", "run.sh", "echo\n")
	var log bytes.Buffer
	if _, err := l.BuildKitDraft(context.Background(), chainDef(nil, root, app), draftTag, &log); err != nil {
		t.Fatal(err)
	}
	builds, stdin := dockerArgsAll(f.Calls, "build")
	if len(builds) != 2 || !slices.Contains(builds[0], "-") || !slices.Contains(builds[0], "JAM_STUDIO_BASE_IMAGE="+basedigest.DefaultRef()) {
		t.Fatalf("builds = %v", builds)
	}
	if got := tarNames(t, []byte(stdin[0])); got != "Dockerfile,run.sh" {
		t.Fatalf("the stage context = %s, want the draft's files minus kit.yml", got)
	}
	tags, _ := dockerArgsAll(f.Calls, "tag")
	if last := tags[len(tags)-1]; len(last) != 3 || last[1] != "jam-kit-base:child" || last[2] != jam.KitDraftStageTag(draftTag) {
		t.Fatalf("stage tag call = %v", last)
	}
	normal, err := kitStageTag(l.asm, basedigest.DefaultRef(), app.Kit)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if slices.Contains(c.Args, normal) {
			t.Fatalf("the draft's stage used its kit version's stage tag: %v", c.Args)
		}
	}
	if after(builds[1], "-t") != draftTag || !slices.Contains(builds[1], "BASE="+jam.KitDraftStageTag(draftTag)) {
		t.Fatalf("image build = %v", builds[1])
	}
	if !strings.Contains(log.String(), "#3 [2/2] RUN make\n") {
		t.Fatalf("the stage's stderr is not in the log: %q", log.String())
	}
}

// A parent's stage is reused as for any build; the draft's own stage is
// rebuilt each time under the draft stage tag.
func TestBuildKitDraftReusesParentStages(t *testing.T) {
	ops := &fakeOps{}
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
	root := chainLink("base-dev", 1, studio.Base{})
	mid := ctxLink(t, "web-dev", 2, "base-dev", "mid")
	draft := ctxLink(t, "my-app", 0, "web-dev", "draft")
	def := chainDef(map[string]string{"A": "1"}, root, mid, draft)
	const tag = "jam-kit-draft:my-app-0123456789ab"
	if _, err := l.BuildKitDraft(context.Background(), def, tag, io.Discard); err != nil {
		t.Fatal(err)
	}
	midStage := mustStage(t, "blessed-default", mid.Kit)
	if len(ops.stages) != 2 || ops.stages[0].stage != midStage || ops.stages[1].parent != midStage || ops.stages[1].stage != jam.KitDraftStageTag(tag) {
		t.Fatalf("stages = %+v", ops.stages)
	}
	if ops.builtTag != tag || ops.builtBase != jam.KitDraftStageTag(tag) || ops.builtArgs["A"] != "1" {
		t.Fatalf("image: tag %q base %q args %v", ops.builtTag, ops.builtBase, ops.builtArgs)
	}
	if slices.Contains(ops.hasBaseAsks, mustStage(t, midStage, draft.Kit)) {
		t.Fatal("the draft's stage must never look up its kit version's stage tag")
	}
	if _, err := l.BuildKitDraft(context.Background(), def, tag, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(ops.stages) != 3 || ops.stages[2].stage != jam.KitDraftStageTag(tag) {
		t.Fatalf("a second build must reuse the parent stage and rebuild only the draft's: %+v", ops.stages)
	}
}

// Cancelling stops docker build; nothing runs after it.
func TestBuildKitDraftCancelStopsTheBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &runner.Fake{OnRun: func(ctx context.Context, c runner.Call) error {
		if len(c.Args) > 2 && c.Args[2] == "build" {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}}
	l := colimaDraftLauncher(t, f)
	link := treeLink(t, "app", 0, "kit.yml", "kind: studio\n")
	def := KitDefinition{Ref: link.Ref, Kit: link.Kit, Chain: []jam.KitLink{link}, Harness: harnessinstall.Default()}
	if _, err := l.BuildKitDraft(ctx, def, draftTag, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if inspect, _ := dockerArgsAll(f.Calls, "inspect"); len(inspect) != 0 {
		t.Fatalf("the build went on after cancel: %+v", f.Calls)
	}
}

func TestBuildKitDraftRefusesANonDraftTag(t *testing.T) {
	l := newPrepareLauncher(&fakeOps{}, &fakeInv{}, noAsm)
	link := treeLink(t, "app", 0, "kit.yml", "kind: studio\n")
	def := KitDefinition{Ref: link.Ref, Kit: link.Kit, Chain: []jam.KitLink{link}}
	if _, err := l.BuildKitDraft(context.Background(), def, "jam-kit:abc-def", io.Discard); err == nil {
		t.Fatal("a draft build must refuse a kit version's tag")
	}
}

// A draft studio runs its test build's image by id, without the inventory.
func TestRaiseDraftStudioRunsItsBuiltImage(t *testing.T) {
	ops := &fakeOps{}
	l := New(Config{
		Ops: ops, Runner: &runner.Fake{}, JamHost: "jam.example.com", RuntimeAddr: "jam.example.com:443",
		IdentityFile: "k", KnownHostsDir: "/kh", Inventory: &fakeInv{}, sleep: func(time.Duration) {},
	})
	inv := &fakeInv{}
	l.inv = inv
	spec := jam.RaiseSpec{ActorID: "d1", Kit: jam.KitRef{ID: "app", Digest: "d1gest"}, DraftImage: &jam.KitDraftImage{Tag: draftTag, ID: fakeImageID}}
	if _, err := l.Raise(context.Background(), spec, jam.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if ops.runImage != draftTag || ops.runDigest != fakeImageID {
		t.Fatalf("ran %q @ %q", ops.runImage, ops.runDigest)
	}
	if inv.asks != 0 {
		t.Fatalf("a draft raise asked the kit inventory %d times", inv.asks)
	}
}

// An unchained draft with a Dockerfile (ruling F2): its context streams FROM
// the blessed base, is gated, and its stage is tagged <tag>-stage — the ref
// the image builds FROM — never the content-id jam-kit-base:<id> ref a kit
// version's own base resolves to, nor any kit version's stage tag.
func TestBuildKitDraftUnchainedStageArgv(t *testing.T) {
	outs := []runner.FakeResult{
		{Stdout: "sha256:own\n"}, // docker build -q … - (the draft's stage)
		{},                       // docker tag sha256:own jam-kit-base:own
		{Stdout: `["sha256:a","sha256:b","sha256:c"]`}, // the stage's layers
	}
	outs = append(outs, gatePasses(t)...)
	outs = append(outs,
		runner.FakeResult{},                           // docker tag jam-kit-base:own <tag>-stage
		runner.FakeResult{},                           // docker info
		runner.FakeResult{},                           // docker build --progress=plain …
		runner.FakeResult{Stdout: fakeImageID + "\n"}) // docker inspect
	f := &runner.Fake{Outputs: outs}
	l := colimaDraftLauncher(t, f)
	link := treeLink(t, "app", 0, "kit.yml", "kind: studio\n", "Dockerfile", df+"RUN make\n")
	def := KitDefinition{Ref: link.Ref, Kit: link.Kit, Chain: []jam.KitLink{link}, Harness: harnessinstall.Default()}
	if _, err := l.BuildKitDraft(context.Background(), def, draftTag, io.Discard); err != nil {
		t.Fatal(err)
	}
	stage := jam.KitDraftStageTag(draftTag)
	builds, stdin := dockerArgsAll(f.Calls, "build")
	if len(builds) != 2 || !slices.Contains(builds[0], "JAM_STUDIO_BASE_IMAGE="+basedigest.DefaultRef()) {
		t.Fatalf("builds = %v", builds)
	}
	if got := tarNames(t, []byte(stdin[0])); got != "Dockerfile" {
		t.Fatalf("the stage context = %s, want the draft's files minus kit.yml", got)
	}
	tags, _ := dockerArgsAll(f.Calls, "tag")
	if last := tags[len(tags)-1]; len(last) != 3 || last[1] != "jam-kit-base:own" || last[2] != stage {
		t.Fatalf("stage tag call = %v", last)
	}
	if after(builds[1], "-t") != draftTag || after(builds[1], "--build-arg") != "BASE="+stage {
		t.Fatalf("the image must build FROM the draft's stage tag: %v", builds[1])
	}
	normal, err := kitStageTag(l.asm, basedigest.DefaultRef(), link.Kit)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if slices.Contains(c.Args, normal) {
			t.Fatalf("the draft's stage used a kit version's stage tag: %v", c.Args)
		}
	}
}

// The same on the launcher seam: the unchained draft's own base is one gated
// stage FROM the blessed default under the draft stage tag — not the kit
// version's ResolveKitBaseTar.
func TestBuildKitDraftUnchainedStageTag(t *testing.T) {
	ops := &fakeOps{}
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
	link := treeLink(t, "app", 0, "kit.yml", "kind: studio\n", "Dockerfile", df)
	def := KitDefinition{Ref: link.Ref, Kit: link.Kit, Chain: []jam.KitLink{link}, Harness: harnessinstall.Default()}
	if _, err := l.BuildKitDraft(context.Background(), def, draftTag, io.Discard); err != nil {
		t.Fatal(err)
	}
	stage := jam.KitDraftStageTag(draftTag)
	if ops.resolvedTar != nil || len(ops.stages) != 1 || ops.stages[0].parent != basedigest.DefaultRef() || ops.stages[0].stage != stage {
		t.Fatalf("resolvedTar %d bytes, stages = %+v", len(ops.resolvedTar), ops.stages)
	}
	if got := tarNames(t, ops.stages[0].tar); got != "Dockerfile" {
		t.Fatalf("the stage context = %s", got)
	}
	if ops.builtTag != draftTag || ops.builtBase != stage {
		t.Fatalf("image: tag %q base %q", ops.builtTag, ops.builtBase)
	}
}

// A draft build assembles exactly what PrepareKit assembles for the same
// definition (harness, hardening, egress, MCP servers, key) and builds the
// image FROM the same base with the same build args; only the tags differ.
func TestBuildKitDraftMatchesPrepareKit(t *testing.T) {
	root := chainLink("base-dev", 1, studio.Base{})
	draft := ctxLink(t, "my-app", 0, "base-dev", "draft")
	def := chainDef(map[string]string{"A": "1"}, root, draft)
	def.Harness.Plugins = []string{"p@m"}

	var prepAsm, draftAsm []KitDefinition
	prepOps := &fakeOps{}
	if _, err := newPrepareLauncher(prepOps, &fakeInv{}, func(d KitDefinition, _ string) error {
		prepAsm = append(prepAsm, d)
		return nil
	}).PrepareKit(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	draftOps := &fakeOps{}
	if _, err := newPrepareLauncher(draftOps, &fakeInv{}, func(d KitDefinition, _ string) error {
		draftAsm = append(draftAsm, d)
		return nil
	}).BuildKitDraft(context.Background(), def, draftTag, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(prepAsm) != 1 || len(draftAsm) != 1 || !reflect.DeepEqual(prepAsm[0], draftAsm[0]) {
		t.Fatalf("assembled defs differ:\nprepare %+v\ndraft   %+v", prepAsm, draftAsm)
	}
	if !reflect.DeepEqual(prepOps.builtArgs, draftOps.builtArgs) || prepOps.stages[0].parent != draftOps.stages[0].parent ||
		!bytes.Equal(prepOps.stages[0].tar, draftOps.stages[0].tar) {
		t.Fatalf("prepare built %+v args %v, draft %+v args %v", prepOps.stages, prepOps.builtArgs, draftOps.stages, draftOps.builtArgs)
	}
	if strings.HasPrefix(draftOps.builtTag, "jam-kit:") || draftOps.stages[0].stage == prepOps.stages[0].stage {
		t.Fatalf("the draft wrote a kit version's tag: image %q stage %q", draftOps.builtTag, draftOps.stages[0].stage)
	}
}

// badDraftImageIDs are ids a draft image must never be run or inspected by:
// empty (colima would fall back to the mutable tag), a flag, a tag.
var badDraftImageIDs = []string{"", "-x", "jam-kit-draft:app-0123456789ab", "sha256:" + strings.Repeat("A", 64), fakeImageID + "0"}

// A draft studio runs only a well-formed image id; anything else is refused
// before any container runs.
func TestRaiseDraftStudioRefusesABadImageID(t *testing.T) {
	for _, id := range badDraftImageIDs {
		ops := &fakeOps{}
		l := New(Config{
			Ops: ops, Runner: &runner.Fake{}, JamHost: "jam.example.com", RuntimeAddr: "jam.example.com:443",
			IdentityFile: "k", KnownHostsDir: "/kh", Inventory: &fakeInv{}, sleep: func(time.Duration) {},
		})
		spec := jam.RaiseSpec{ActorID: "d1", Kit: jam.KitRef{ID: "app", Digest: "d1gest"}, DraftImage: &jam.KitDraftImage{Tag: draftTag, ID: id}}
		_, err := l.Raise(context.Background(), spec, jam.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"})
		if err == nil || !strings.Contains(err.Error(), errDraftImageID.Error()) {
			t.Fatalf("id %q: err = %v, want %v", id, err, errDraftImageID)
		}
		if ops.ran || len(ops.created) != 0 {
			t.Fatalf("id %q: a container ran (or a volume was made): %+v", id, ops)
		}
	}
}

func TestHasKitDraftImageRefusesABadImageID(t *testing.T) {
	for _, id := range badDraftImageIDs {
		ops := &fakeOps{}
		l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
		if ok, err := l.HasKitDraftImage(id); ok || !errors.Is(err, errDraftImageID) {
			t.Fatalf("id %q: HasKitDraftImage = %v, %v", id, ok, err)
		}
		if len(ops.hasImageAsks) != 0 {
			t.Fatalf("id %q: inspected %v", id, ops.hasImageAsks)
		}
	}
	ops := &fakeOps{hasImage: map[string]bool{fakeImageID: true}}
	if ok, err := newPrepareLauncher(ops, &fakeInv{}, noAsm).HasKitDraftImage(fakeImageID); !ok || err != nil {
		t.Fatalf("HasKitDraftImage(valid) = %v, %v", ok, err)
	}
}

// The id docker reports for the built image is checked too: a draft build
// never hands the supervisor an id Raise would refuse.
func TestBuildKitDraftRefusesABadBuiltImageID(t *testing.T) {
	for _, id := range badDraftImageIDs {
		f := &runner.Fake{Outputs: []runner.FakeResult{{}, {}, {Stdout: id + "\n"}}} // info, build, inspect
		l := colimaDraftLauncher(t, f)
		link := treeLink(t, "app", 0, "kit.yml", "kind: studio\n")
		def := KitDefinition{Ref: link.Ref, Kit: link.Kit, Chain: []jam.KitLink{link}, Harness: harnessinstall.Default()}
		if got, err := l.BuildKitDraft(context.Background(), def, draftTag, io.Discard); got != "" || !errors.Is(err, errDraftImageID) {
			t.Fatalf("id %q: BuildKitDraft = %q, %v", id, got, err)
		}
	}
}
