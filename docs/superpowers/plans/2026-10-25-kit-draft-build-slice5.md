# Kit drafts — slice 5 (test builds and draft studios) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An operator can test-build a kit's draft with the real chain, harness and hardening (`at-jam kit draft build`, `/admin/kits/{name}/draft/build`), follow its log, and raise a manual studio that runs the draft without publishing it (`at-jam studio raise --kit-draft <kit>`). The setup inspect of that studio heads its kit section `kit (draft @<tree[:7]>)`.

**Architecture:**
- **The build path is the launcher's own.** `Launcher.BuildKitDraft` runs `PrepareKit`'s steps: the same assemble, the same parent stages (cached and reused), the same gate and build args. Two things differ. The draft's own stage is tagged `cove-kit-draft:<kit>-<tree[:12]>-stage`, and the image is tagged `cove-kit-draft:<kit>-<tree[:12]>`. No kit version's `cove-kit:` image tag or `cove-kit-base:` stage tag is ever looked up or written for the draft.
- **Builds log and stop through the runner.** `runner.Scoped(ctx, r, log)` runs every command of a build under a context, with its output copied to the build's log. `backend.LoggedKitBuilder` (implemented by `Colima.WithBuildLog`) hands the launcher a builder that works this way, so the docker argv and the gate stay exactly as they are today.
- **Jobs live in Jam's memory.** `jam.KitDraftBuilds` holds at most one running build per kit, plus the latest build per kit. Each build runs in a goroutine under the serve's build context; a capped log (the last 1 MiB) and the state are never persisted. The admin routes, the CLI and a draft studio's raise all go through it.
- **A draft studio is a manual raise with a different kit.** `RaiseSpec.KitDraft` makes `Supervisor.Raise` resolve the draft tree in place of the role's kit, under the role's harness. The image comes from `KitDraftBuilds.Ensure`: the last build of exactly that definition while its image exists, else a build waited for. The launcher runs that image by id. The instance records `KitDraft{Kit, Tree}`. GC treats a live instance's draft tree as a root, and the image status is `draft`, never `stale`.

**Tech Stack:** Go 1.26; `os/exec` (`CommandContext`, `Cancel`, `WaitDelay`); `net/http` ServeMux; the existing `jam` store (MemStore and Postgres via pgx/v5), `storetest`, `kittree`, `studio`, `harnessinstall`, `launcher`, `backend/colima`, `runner`, `adminclient`, `textesc`; `github.com/go-git/go-billy/v5` (`util.ReadFile` in tests).

**Spec:** [`docs/superpowers/specs/2026-10-24-kit-trees-design.md`](../specs/2026-10-24-kit-trees-design.md). This plan covers §3 "Test build" and "Draft studio", and the §6 testing line "test build (`runner.Fake`), draft studio raise". Slice 1 (trees, #479) and slice 3 (drafts API/CLI, #484) are merged. Slice 4 (the Draft tab UI, PR #487) is not merged, so this plan has no UI (ruling 20).

## Spec deviations, interpretations and rulings

Rulings 1–9 are the binding design rulings handed to this plan, refined where the code demanded. Rulings 10 onward are this plan's own.

1. **Test build = the launcher's real build path.** It covers the chain (parents' stages exactly as today: `kitStageTag`, cached under `cove-kit-base:`, built on a miss), the harness, the hardening and the build args. The draft's tree is the top stage. The differences:
   - the top context comes from the draft tree;
   - the top stage is tagged `cove-kit-draft:<kit>-<tree[:12]>-stage`;
   - the image is tagged `cove-kit-draft:<kit>-<tree[:12]>`.

   No kit version's image tag (`cove-kit:<digest>-<asm>`) or stage tag (`kitStageTag`) is written, moved or looked up for the draft. A draft with problems is refused before building: 400 `draft <kit> has problems: …`.

   *Refinement:* the code has no `current` image tag. Image tags are content-addressed. "Never `current`" therefore means never a kit version's tag.
2. **Jobs.** At most one build per kit at a time; another start answers 409 `kit <kit> already has a draft build running`.
   - `POST /admin/kits/{name}/draft/build` needs `If-Match` (the draft ETag), builds that exact tree, and answers 202 with the build (`id`, `tree`, `state`, …).
   - `GET …/draft/build[?since=N]` returns the latest build: `state` (`running|succeeded|failed`, or `unknown` when none has run since Jam started), `tree`, `tag`, `digest`, `started_at`/`finished_at`, and the log from offset N.
   - `DELETE …/draft/build` cancels.
   - The log is in memory, capped at its last 1 MiB, never persisted, and escaped by consumers.
   - Builds run in a goroutine under a context the serve cancels on shutdown.
3. **CLI.** `at-jam kit draft build <kit> [--etag e] [--no-follow]` starts a build and follows its log (polling `?since=`) until it ends. It exits 0 on success, printing the tag and the digest, and 1 on failure.
4. **Draft studio.** `at-jam studio raise --kit-draft <kit>` uses the existing manual raise (`RaiseManual`, an ephemeral session, the `/admin` operator gate). It runs the draft tree as of the raise.
   - The instance records `KitDraft{Kit, Tree}`.
   - The image is the test-build image of that tree, built if absent, synchronously, inside the raise (the raise already builds synchronously through `PrepareKit`).
   - It never goes stale and is never restarted onto `current`. Its tree stays GC-live while the instance exists.
5. **Setup inspect.** The kit section of a draft studio is headed `kit (draft @<tree[:7]>)`. Role inspect is unchanged.
6. **Secrets.** A build takes no secrets. A build arg named like a reserved secret is already refused by kit validation, so it is a draft problem and nothing builds (test in Task 2). The launcher's own log lines never print build-arg values, and no build command gets an env (test in Task 3). The audit log lines carry no log content.
7. **Docs.** `docs/usage/jam/kits-drafts.md` gains "Test build" and "Draft studio". `coves.md` and `inspect.md` link to them.
8. **Tests are hermetic.** `runner.Fake` drives the real colima backend for the docker argv; fakes cover the launcher seam.
9. **Repo rules.** As in Global Constraints.
10. **Harness.** A test build (`kit draft build`) builds under `harnessinstall.Default()`, the harness `kit show` and the setup inspect use. A draft studio builds under its role's model-spec harness, through the same job. The two share the tag `cove-kit-draft:<kit>-<tree[:12]>`, which names the latest build of that tree. A draft studio runs its image by id (`RaiseSpec.DraftImage.ID`), so a later build that moves the tag cannot change a running studio.
11. **Stage builds keep `-q`.** The streamed stage build (`docker build -q … -`) keeps its argv, so it is the real path. Its log is therefore docker's stderr only: diagnostics and the failing step. The image build (`--progress=plain`) logs in full. The launcher adds `== …` header lines that never print build-arg values. A full progress log for the stage is a follow-up.
12. **Log storage.**
    - Offsets count from the build's first byte, so `?since=` keeps working after truncation.
    - The buffer trims to the last 1 MiB once it passes 2 MiB (amortised copying). A read returns at most the last 1 MiB, and `log_skipped` marks dropped bytes.
    - `since` past the end is clamped; a negative or non-numeric `since` is 400 `since must be a log offset (0 or more)`.
    - While the build runs, a trailing incomplete UTF-8 sequence is held back for the next read.
13. **Cancel is a failure.** A cancelled build ends `failed` with `error: "cancelled"`. A build stopped by shutdown ends `failed` with `"cancelled: Jam is shutting down"`. There is no fourth state.
    - `DELETE` waits for the build to stop and answers 200 with it, or 404 `kit <kit> has no draft build running`.
    - The CLI adds `--cancel`, and `--show`, which prints the latest build without starting one.
14. **POST takes `application/json` only** (415 otherwise, before the body is read) and needs `If-Match` (428 without it, 412 when stale), as publish does. No browser no-cors request can start a build.
15. **A build's files are copied into memory under `kitMu`** (every link: the draft tree and each parent's version tree). An edit plus GC, or a version removal, landing mid-build cannot pull a blob out from under it. A tree is at most 64 MiB and a chain at most 4 links.
16. **Status says whether the build is current.** `current` is true while the build's tree is still the draft's tree; `draft_etag` is the draft's ETag now (`""` without a draft). A build keeps running when the draft is edited.
17. **Ensure (the draft studio's image).**
    - It reuses the latest build when that build has the same tree and the same `def.Ref.Digest` (harness included), succeeded, and its image id is still present (`HasKitDraftImage`).
    - It waits for a running build of the same definition.
    - A running build of another tree or harness is 409. A failed build is 400 `draft <kit>: the test build failed (see at-jam kit draft build <kit> --show)`.
    - A raise whose request goes away (ctx done) stops waiting; the build runs on for a later raise to reuse.
    - `studio raise --kit-draft` allows 30 minutes (the plain raise keeps its 10 s client timeout).
18. **The draft studio's instance.**
    - `Kit` is `{kit, Version 0, Digest: the chain digest}`.
    - `KitChain` records the parents at their versions, with the draft itself last as v0.
    - `ImageTag` is the draft tag.
    - `KitDraft` is `{Kit, Tree}`.

    Image status is `draft`. A draft raise never takes the `ErrKitNotReady`/`PrepareKit` path. The session context's kit fact reads `<kit> (draft)`. A context refresh recompiles from the draft tree and the recorded parents (`draftStudioKit`), not from the role's current kit.
19. **GC roots and the raise gap.**
    - `deadKitObjects` counts the `KitDraft.Tree` of every non-Gone instance as live.
    - A raise reads the draft, builds (minutes) and only then stores the instance. A discard or an edit plus GC in that gap could collect the tree. So after `PutInstance` the raise re-stores the draft tree and its blobs if they are gone (`PutKitDocs(nil, objs)`).
20. **No UI in this plan.** The Draft tab (#487) is not merged. A "Test build" button with a log pane on that tab is a follow-up. Its API is ready: the POST, the GET with `?since=`, and `current`/`draft_etag`.
21. **Serve shutdown.** `at-jam serve` today dies on SIGINT/SIGTERM unhandled. It now catches them, cancels the draft builds (each docker child gets an interrupt, then a kill after 10 s), waits up to 15 s for them, and exits `128+signal`, the code an unhandled signal gave.
22. **`KitDraftTag` refuses a name that cannot form a docker tag** (400 `kit name cannot form a draft image tag (at most 109 characters of [A-Za-z0-9_.-], not starting with . or -)`). The tag part is at most 128 characters, and the `-stage` suffix must fit.
23. **The setup heading is a field.** `SetupSection.Title` (JSON `title`) heads the section when set; `Name` stays the key for sections, diffs and the UI. The draft studio's kit body names the kit `<kit> (draft @<tree[:12]>)` and its last chain link `<kit> (draft)`. Its provenance row reads `draft, not published`.
24. **Follow-ups (not in this plan):**
    - the Draft-tab Test build button and log pane (after #487);
    - garbage collection of `cove-kit-draft` images (they accumulate per tree built);
    - a full progress log for context stages (ruling 11).

## Global Constraints

- **Test build (spec §3, verbatim):** "`kit draft build` / **Test build**: build the draft's image with the real chain, harness and hardening; stream the log; report the result and digest; tag `cove-kit-draft:<kit>-<tree[:12]>`, never `current`."
- **Draft studio (spec §3, verbatim):** "`at-jam studio raise --kit-draft <kit>` (admin only, ephemeral manual studio) runs the draft without publishing; its setup inspect shows `kit (draft @<tree[:7]>)`."
- **Testing (spec §6, verbatim):** "test build (`runner.Fake`), draft studio raise".
- **Tags:** the image is `cove-kit-draft:<kit>-<tree[:12]>` and its own stage `cove-kit-draft:<kit>-<tree[:12]>-stage`. The draft's build never names a `cove-kit:` tag, and never names the `kitStageTag` of its own link.
- **Fixed texts this plan adds (exact):**
  - `kit app already has a draft build running` (409)
  - `kit app has no draft build running` (404)
  - `since must be a log offset (0 or more)` (400)
  - `test builds need a launcher that builds images (runtime.launcher)` (503)
  - `kit name cannot form a draft image tag (at most 109 characters of [A-Za-z0-9_.-], not starting with . or -)` (400)
  - `draft app: the test build failed (see at-jam kit draft build app --show)` (400)
  - `cancelled` and `cancelled: Jam is shutting down` (a build's `error`)

  Reused: `draft app has problems: …`, `draft app changed since you loaded it (reload)`, `draft app: If-Match is required (send the etag from the draft status)`, `kit app has no draft`, `no such kit`, `kit name is not tag-safe (allowed: [A-Za-z0-9_.-])`.
- **States:** `running`, `succeeded`, `failed`, `unknown` (the constants `DraftBuildRunning|Succeeded|Failed|Unknown`).
- **Log:** `KitDraftLogCap = 1 << 20`. Kept in memory only; offsets are absolute; consumers escape it (`textesc.Escape(log, true)` in the CLI).
- **Lock order:** `configMu → kitMu → KitDraftBuilds.mu → store mu`. `kitMu` is held only while a build's definition and files are read, never across a build or a wait. `KitDraftBuilds.mu` is never held across a builder call or a wait.
- **Secrets:** builds take none. Never log build-arg values, log content, or an unsafe kit name. The kit name is checked tag-safe before any text names it.
- **Audit lines:**
  - `kit draft build requested` (operator, kit, tree prefix, build id)
  - `kit draft build started` (kit, tree, build, by)
  - `kit draft build finished` (kit, tree, build, state)
  - `kit draft build cancelled` (operator, kit, build)
- Tests are hermetic: MemStore, `httptest`, `t.TempDir`, `runner.Fake`. Postgres tests stay behind the `integration` tag and run in CI only.
- Use `GOPROXY=https://proxy.golang.org,direct` for every `go` command here.
- Every commit passes `just test`, `just lint` (check the exit code: `just lint; echo "lint exit=$?"`) and `go vet -tags integration ./...`. Run `gofmt -l` on the touched files: it must print nothing.
- In doc-edit steps, the form <link `target` as "text"> stands for the markdown link `[text](target)` to write. The plan spells it that way so its own link check stays clean.
- **Docs** change in the same task as the behaviour they describe, in the doc that owns the subject, with no copies.
  - `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1` must report no more than **743** errors: 741 before this plan, plus 2 for this plan file (no frontmatter and not in `INDEX.md`, the convention for plans).
  - `kits-drafts.md` must stay under the 200-line leaf budget.

## Review Focus

1. **The draft is edited while its build runs.** Build B starts at tree T1, then a save moves the draft to T2 and GC collects T1's unique blob. B still finishes on T1 (its files were copied), labelled `tree: T1`. Its status shows `current: false` and `draft_etag` equal to T2's ETag. *(Task 2: `TestDraftBuildEditedWhileRunning`.)*
2. **Serve shuts down mid-build.** SIGTERM cancels the build context. The build ends `failed` with `cancelled: Jam is shutting down`, serve waits for it and then exits 143. After a restart its state is `unknown`. *(Task 2: `TestDraftBuildShutdown`; Task 4: `TestShutdownCancelsDraftBuilds`.)*
3. **Two concurrent starts.** Two POSTs for the same kit race: exactly one answers 202, the other 409 `kit app already has a draft build running`. After the first ends, a new start works. *(Task 2: `TestDraftBuildOnePerKit`; Task 4: `TestDraftBuildTwoConcurrentPosts`.)*
4. **Discard or publish while a draft studio runs.** The studio keeps running, its image status stays `draft`, and its tree stays stored and readable (context, setup). This holds even when the draft is discarded during the raise's own build. Once the studio is torn down, GC collects the tree. *(Task 6: `TestDraftStudioKeepsItsTreeLive`, `TestDraftStudioTreeReStoredAfterARacingDiscard`; storetest "GC keeps a draft studio's tree…".)*
5. **A build log with terminal escapes, past the cap.** A Dockerfile prints `\x1b[31m` and the log grows past 1 MiB. The API returns the raw bytes; the CLI prints `\x1b[31m` escaped (never a raw ESC) and notes the dropped bytes. The log keeps exactly its last 1 MiB, and follow offsets stay valid. *(Task 2: `TestBuildLogKeepsItsTail`, `TestDraftBuildLogSkipped`; Task 5: `TestKitDraftBuildFollowsToSuccess`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/runner/runner.go` (modify) | `ContextRunner`, `OS.RunIOContext`, `Fake.RunIOContext` and `Fake.OnRun`, `Scoped` |
| `internal/backend/backend.go`, `internal/backend/colima/colima.go` (modify) | `LoggedKitBuilder`, `Colima.WithBuildLog` |
| `internal/jam/kit_draft_build.go` (create) | build types, `KitDraftTag`, `KitDraftStageTag`, `kitDraftDefinitionLocked`, `copyBlobs`, `buildLog`, `KitDraftBuilds` (`Start`, `Status`, `Cancel`, `Wait`, later `Ensure`), `DraftKitDefinition` |
| `internal/jam/launcher/prepare.go` (modify), `internal/jam/launcher/draft.go` (create), `internal/jam/launcher/launcher.go` (modify) | the base resolvers take a builder and a top-stage tag; `BuildKitDraft`, `HasKitDraftImage`; `Raise` runs `DraftImage` |
| `internal/jam/kit_draft_build_admin.go` (create), `internal/jam/admin.go` (modify) | the `/draft/build` routes, `WithKitDraftBuilds`; `CoveRaiseBody.KitDraft`, `CoveRaiseResult.KitDraftTree`, image status `draft` |
| `internal/jam/supervisor.go` (modify) | `RaiseSpec.DraftImage`/`KitDraft`, `SetKitDraftBuilds`, the draft raise, `keepDraftTree`, context for a draft studio |
| `internal/jam/instance.go`, `kit_objects.go`, `kitchain.go`, `setup_record.go` (modify) | `Instance.KitDraft`, GC roots, `recordedChainKits`, `draftStudioKit`, `instanceKit` |
| `internal/jam/setup.go`, `setup_parts.go`, `setup_image_bodies.go`, `setup_render.go` (modify), `testdata/setup/studio-draft.golden` (create) | `SetupSection.Title`, the draft kit body and heading |
| `internal/jam/storetest/kits_draft.go` (modify) | the draft-studio GC-root conformance |
| `internal/jam/adminclient/kitdraft.go`, `adminclient.go` (modify) | the build client; `CoveRaiseParams.KitDraft` with the long timeout |
| `cmd/at-jam/kitdraft.go`, `cmd/at-jam/main.go` (modify), `cmd/at-jam/serve_shutdown.go` (create) | `kit draft build`, `studio raise --kit-draft`, the build wiring and signal shutdown |
| `docs/usage/jam/kits-drafts.md`, `INDEX.md`, `coves.md`, `inspect.md` (modify) | docs |

---

### Task 1: Builds that log and stop (`runner.Scoped`, `Colima.WithBuildLog`)

**Files:**
- Modify: `internal/runner/runner.go` (add `ContextRunner`, `OS.RunIOContext`, `Fake.OnRun`, `Fake.RunIOContext`, `Scoped`, `tee`)
- Modify: `internal/backend/backend.go` (add `LoggedKitBuilder` after `KitImageBuilder`)
- Modify: `internal/backend/colima/colima.go` (add `WithBuildLog`)
- Test: `internal/runner/runner_test.go`, `internal/backend/colima/buildlog_test.go` (create)

**Interfaces:**
- Consumes: `runner.Runner`, `runner.Fake`, `runner.Call`, `runner.FakeResult`, `runner.ExitError`, `orReader`/`orWriter` (runner.go); `colima.Colima{r, context}`, `NewWithContext`; the colima test helpers `dockerCall`, `contains`, `allPinned`.
- Produces:
```go
package runner
type ContextRunner interface {
	RunIOContext(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
}
func (OS) RunIOContext(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
// Fake gains: OnRun func(ctx context.Context, c Call) error
func (f *Fake) RunIOContext(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
func Scoped(ctx context.Context, r Runner, log io.Writer) Runner

package backend
type LoggedKitBuilder interface {
	WithBuildLog(ctx context.Context, log io.Writer) KitImageBuilder
}

package colima
func (c *Colima) WithBuildLog(ctx context.Context, log io.Writer) backend.KitImageBuilder
```

- [ ] **Step 1: Write the failing runner tests**

Append to `internal/runner/runner_test.go`. Add `bytes`, `context`, `errors`, `io` and `time` to its imports if absent.

```go
// Scoped copies Run's output and Output's stderr to the log; Output's
// stdout is the caller's, Probe's output is dropped.
func TestScopedCopiesOutputToLog(t *testing.T) {
	f := &Fake{Outputs: []FakeResult{
		{Stdout: "built\n", Stderr: "warn\n"},
		{Stdout: "sha256:id\n", Stderr: "inspect-noise\n"},
		{Stdout: "probe-out", Stderr: "probe-err"},
	}}
	var log bytes.Buffer
	r := Scoped(context.Background(), f, &log)
	if err := r.Run("docker", "build"); err != nil {
		t.Fatal(err)
	}
	out, err := r.Output("docker", "inspect")
	if err != nil || out != "sha256:id\n" {
		t.Fatalf("Output = %q, %v", out, err)
	}
	if err := r.Probe("docker", "info"); err != nil {
		t.Fatal(err)
	}
	if got := log.String(); got != "built\nwarn\ninspect-noise\n" {
		t.Fatalf("log = %q", got)
	}
	if len(f.Calls) != 3 || f.Calls[0].Args[0] != "build" || f.Calls[2].Args[0] != "info" {
		t.Fatalf("calls = %+v", f.Calls)
	}
}

// RunIO keeps the caller's stdout (a streamed build's image id) and tees
// its stderr into the log.
func TestScopedRunIOKeepsStdoutTeesStderr(t *testing.T) {
	f := &Fake{Outputs: []FakeResult{{Stdout: "sha256:child\n", Stderr: "#3 RUN make\n"}}}
	var log, out, errb bytes.Buffer
	if err := Scoped(context.Background(), f, &log).RunIO(strings.NewReader("TAR"), &out, &errb, "docker", "build", "-"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "sha256:child\n" || errb.String() != "#3 RUN make\n" || log.String() != "#3 RUN make\n" || f.Calls[0].Stdin != "TAR" {
		t.Fatalf("out %q err %q log %q stdin %q", out.String(), errb.String(), log.String(), f.Calls[0].Stdin)
	}
}

// A done context refuses every command, the call still recorded.
func TestScopedDoneContextRefuses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &Fake{}
	r := Scoped(ctx, f, io.Discard)
	if err := r.Run("docker", "build"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if _, err := r.Output("docker", "inspect"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Output = %v", err)
	}
	if err := r.RunEnv(nil, "docker"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunEnv = %v", err)
	}
	if len(f.Calls) != 2 {
		t.Fatalf("calls = %+v (RunEnv checks ctx before delegating)", f.Calls)
	}
}

// OnRun lets a test hold a command until its context is cancelled.
func TestFakeOnRunBlocksUntilCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &Fake{OnRun: func(ctx context.Context, c Call) error {
		cancel() // the operator cancels while the command runs
		<-ctx.Done()
		return ctx.Err()
	}}
	if err := Scoped(ctx, f, io.Discard).Run("docker", "build"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
}

// A cancelled OS command is interrupted and the call returns ctx.Err().
func TestOSRunIOContextStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := OS{}.RunIOContext(ctx, nil, io.Discard, io.Discard, "sleep", "30")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
}

func TestOSRunIOContextPropagatesExitCode(t *testing.T) {
	err := OS{}.RunIOContext(context.Background(), nil, io.Discard, io.Discard, "sh", "-c", "exit 3")
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 {
		t.Fatalf("err = %v, want exit 3", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/runner/ -run 'Scoped|OnRun|RunIOContext' -v`
Expected: FAIL to compile: `undefined: Scoped`, `f.OnRun undefined`, `OS{}.RunIOContext undefined`.

- [ ] **Step 3: Implement the runner additions**

In `internal/runner/runner.go`, add `bytes`, `context` and `time` to the imports. Add the field to `Fake` (after `Outputs`):

```go
	// OnRun, when set, runs inside every RunIOContext call once it is
	// recorded: a test holds a command on its ctx, or fails one.
	OnRun func(ctx context.Context, c Call) error
```

Append:

```go
// ContextRunner is a Runner that can run a command under a context: a done
// ctx stops the child, and the call returns ctx.Err(). Scoped uses it.
type ContextRunner interface {
	RunIOContext(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
}

// osWaitDelay is how long a cancelled child has to exit after its
// interrupt before it is killed.
const osWaitDelay = 10 * time.Second

// RunIOContext is RunIO under ctx: when ctx is done the child gets an
// interrupt, and a kill osWaitDelay later; the call then returns ctx.Err().
func (OS) RunIOContext(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = orReader(stdin, os.Stdin)
	cmd.Stdout = orWriter(stdout, os.Stdout)
	cmd.Stderr = orWriter(stderr, os.Stderr)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = osWaitDelay
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Code: ee.ExitCode(), Err: ee}
	}
	return err
}

// RunIOContext records the call, fails with ctx.Err() when ctx is done,
// then runs OnRun. Unlike RunIO it consumes one queued result on every
// call, whatever its streams (Stdout to stdout and Stderr to stderr when
// given), so a scripted docker sequence is one result per command.
func (f *Fake) RunIOContext(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	var in string
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	c := Call{Name: name, Args: append([]string(nil), args...), Stdin: in, Stdout: stdout, Stderr: stderr}
	f.Calls = append(f.Calls, c)
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.OnRun != nil {
		if err := f.OnRun(ctx, c); err != nil {
			return err
		}
	}
	if f.out >= len(f.Outputs) {
		return f.Err
	}
	r := f.Outputs[f.out]
	f.out++
	if stdout != nil {
		_, _ = io.WriteString(stdout, r.Stdout)
	}
	if stderr != nil {
		_, _ = io.WriteString(stderr, r.Stderr)
	}
	if r.Err != nil {
		return r.Err
	}
	return f.Err
}

// Scoped is r with every command run under ctx and its output copied to
// log. That is Run's stdout and stderr, and Output's and RunIO's stderr;
// their stdout stays the caller's. Probe's output is dropped. A done ctx
// stops a running command (when r is a ContextRunner) or refuses the next
// one. RunEnv and OutputEnv only check ctx (no build uses them). It is how
// a kit draft test build streams and cancels its docker commands without
// changing the build code. log must be safe for concurrent writes (a
// command's stdout and stderr); nil drops the output.
func Scoped(ctx context.Context, r Runner, log io.Writer) Runner {
	if log == nil {
		log = io.Discard
	}
	return scoped{ctx: ctx, r: r, log: log}
}

type scoped struct {
	ctx context.Context
	r   Runner
	log io.Writer
}

func (s scoped) runIO(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	if cr, ok := s.r.(ContextRunner); ok {
		return cr.RunIOContext(s.ctx, stdin, stdout, stderr, name, args...)
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return s.r.RunIO(stdin, stdout, stderr, name, args...)
}

func (s scoped) Run(name string, args ...string) error {
	return s.runIO(nil, s.log, s.log, name, args...)
}

func (s scoped) RunStdin(stdin io.Reader, name string, args ...string) error {
	return s.runIO(stdin, s.log, s.log, name, args...)
}

func (s scoped) RunIO(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	return s.runIO(stdin, orWriter(stdout, s.log), tee(stderr, s.log), name, args...)
}

func (s scoped) Output(name string, args ...string) (string, error) {
	var out bytes.Buffer
	err := s.runIO(nil, &out, s.log, name, args...)
	return out.String(), err
}

func (s scoped) Probe(name string, args ...string) error {
	return s.runIO(nil, io.Discard, io.Discard, name, args...)
}

func (s scoped) RunEnv(extraEnv []string, name string, args ...string) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return s.r.RunEnv(extraEnv, name, args...)
}

func (s scoped) OutputEnv(extraEnv []string, name string, args ...string) (string, error) {
	if err := s.ctx.Err(); err != nil {
		return "", err
	}
	return s.r.OutputEnv(extraEnv, name, args...)
}

// tee is w and log both, or log alone when w is nil.
func tee(w, log io.Writer) io.Writer {
	if w == nil {
		return log
	}
	return io.MultiWriter(w, log)
}
```

- [ ] **Step 4: Run the runner tests**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/runner/ -v`
Expected: PASS, the existing tests included.

- [ ] **Step 5: Write the failing colima tests**

Create `internal/backend/colima/buildlog_test.go`:

```go
package colima

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

// A logged builder builds exactly as the plain one (same pinned argv, kit
// build args, --progress=plain) and copies the build's output to the log.
func TestWithBuildLogStreamsTheBuildAndKeepsItsArgv(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{}, // docker info (preflight)
		{Stdout: "#1 [1/2] FROM base\n#2 DONE\n"}, // docker build --progress=plain …
		{Stdout: "sha256:img\n"},                  // docker inspect --format {{.Id}} <tag>
	}}
	var log bytes.Buffer
	b := NewWithContext(f, "colima").WithBuildLog(context.Background(), &log)
	id, err := b.BuildKitImage(t.TempDir(), "cove-kit-draft:app-0123456789ab", "base@sha256:x", map[string]string{"A": "1"}, false)
	if err != nil || id != "sha256:img" {
		t.Fatalf("BuildKitImage = %q, %v", id, err)
	}
	if !strings.Contains(log.String(), "#1 [1/2] FROM base\n#2 DONE\n") {
		t.Fatalf("log = %q", log.String())
	}
	build := dockerCall(f.Calls, "build")
	if !contains(build, "--progress=plain") || !contains(build, "A=1") || !contains(build, "BASE=base@sha256:x") || !contains(build, "cove-kit-draft:app-0123456789ab") {
		t.Fatalf("build argv = %v", build)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
	}
}

// A cancelled context stops the build before docker build runs.
func TestWithBuildLogCancelledStopsBeforeBuilding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &runner.Fake{}
	if _, err := NewWithContext(f, "").WithBuildLog(ctx, io.Discard).BuildKitImage(t.TempDir(), "t", "b", nil, false); err == nil {
		t.Fatal("a cancelled build must fail")
	}
	if dockerCall(f.Calls, "build") != nil {
		t.Fatalf("a cancelled build ran docker build: %+v", f.Calls)
	}
}
```

- [ ] **Step 6: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/backend/colima/ -run WithBuildLog -v`
Expected: FAIL to compile: `NewWithContext(f, "colima").WithBuildLog undefined`.

- [ ] **Step 7: Implement `LoggedKitBuilder` and `WithBuildLog`**

In `internal/backend/backend.go`, add `context` to the imports. After the `KitImageBuilder` interface, add:

```go
// LoggedKitBuilder is optionally implemented by a KitImageBuilder that can
// run its builds under a context, their output copied to a log: the Jam
// launcher's kit draft test build (docs/usage/jam/kits-drafts.md#test-build).
// The returned builder builds exactly as the receiver does — same argv,
// same gate — only logged and cancellable.
type LoggedKitBuilder interface {
	WithBuildLog(ctx context.Context, log io.Writer) KitImageBuilder
}
```

In `internal/backend/colima/colima.go`, add `context` to the imports, and after `HasKitImage` add:

```go
// WithBuildLog is c with every docker command run under ctx (a done ctx
// stops the build) and its output copied to log (runner.Scoped), per
// backend.LoggedKitBuilder. The build steps, argv and gate are c's own.
func (c *Colima) WithBuildLog(ctx context.Context, log io.Writer) backend.KitImageBuilder {
	return &Colima{r: runner.Scoped(ctx, c.r, log), context: c.context}
}

var _ backend.LoggedKitBuilder = (*Colima)(nil)
```

- [ ] **Step 8: Run the backend tests**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/backend/... ./internal/runner/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
gofmt -l internal/runner internal/backend
git add internal/runner internal/backend
git commit -m "feat(runner,colima): context-scoped, logged runs for kit draft builds

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Draft build jobs (`jam.KitDraftBuilds`)

**Files:**
- Create: `internal/jam/kit_draft_build.go`
- Test: `internal/jam/kit_draft_build_test.go` (create)

**Interfaces:**
- Consumes: `kitMu`; `lockedDraftTree(store Store, name, ifMatch string) (KitDraft, kittree.Tree, error)`, `draftProblems(store Store, name string, tree kittree.Tree) (studio.StudioKit, []string)`, `validateKit(store KitReader, name string, sk studio.StudioKit) ([]KitLink, error)`, `LoadKitVersion`, `chainKits`, `DraftETag`, `shortTree`, `unsafeKitName()`, `unsafeKitNameText`, `writeErr` (all in package jam); `KitDefinition`, `KitRef`, `KitLink`, `KitObjects`; `studio.ChainDigest`, `studio.Merge`, `studio.TagSafeName`; `kittree.NewFS`, `kittree.MapBlobs`; the test helpers `pushApp`, `draftNow`, `wantErr` (`kit_draft_test.go`).
- Produces:
```go
package jam
const (
	DraftBuildRunning   = "running"
	DraftBuildSucceeded = "succeeded"
	DraftBuildFailed    = "failed"
	DraftBuildUnknown   = "unknown"
)
const KitDraftLogCap = 1 << 20
type KitDraftBuilder interface {
	BuildKitDraft(ctx context.Context, def KitDefinition, tag string, log io.Writer) (imageID string, err error)
	HasKitDraftImage(imageID string) (bool, error)
}
type KitDraftImage struct{ Tag, ID string }   // json tag, id
type KitDraftRef struct{ Kit, Tree string }   // json kit, tree
type KitDraftBuild struct { ID, Kit, Tree, State, Tag, Digest, Error, By string; StartedAt, FinishedAt time.Time;
	DraftETag string; Current bool; Log string; LogFrom, LogEnd int64; LogSkipped bool }
func KitDraftTag(kit, tree string) (string, error)
func KitDraftStageTag(tag string) string
func kitDraftDefinitionLocked(store Store, name string, tree kittree.Tree, h harnessinstall.Install) (KitDefinition, KitObjects, error)
func NewKitDraftBuilds(ctx context.Context, store Store, builder KitDraftBuilder, log *slog.Logger) *KitDraftBuilds
func (b *KitDraftBuilds) Start(name, ifMatch, by string) (KitDraftBuild, error)
func (b *KitDraftBuilds) Status(name string, since int64) (KitDraftBuild, error)
func (b *KitDraftBuilds) Cancel(ctx context.Context, name string) (KitDraftBuild, error)
func (b *KitDraftBuilds) Wait(timeout time.Duration) bool
// internal, used by Task 6: (b *KitDraftBuilds) startLocked(def KitDefinition, tree, tag, by string) *draftBuildJob;
// errDraftBuildRunning(name string) error; the draftBuildJob fields id, kit, tree, tag, buildDigest, state, imageID, done
// test helpers used by Tasks 4 and 6: fakeDraftBuilder, draftBuildsFor, finished
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/kit_draft_build_test.go`:

```go
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
	pushApp(t, st, "kit.yml", "kind: studio\n", "Dockerfile", "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n")
	d, err := OpenKitDraft(st, "app", 0, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	s, err := PutKitDraftFile(st, slog.New(slog.DiscardHandler), "app", "Dockerfile", 0,
		[]byte("ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\nRUN echo one\n"), DraftETag(d.Tree), "local", draftNow)
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
	if _, _, err := PushKitFiles(st, "web", kittreetest.Files("kit.yml", "kind: studio\n", "Dockerfile", "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n")); err != nil {
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
	if v.State != DraftBuildSucceeded || v.Tree != d1.Tree || v.Current || !strings.Contains(v.Log, "file: ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\nRUN echo one\n") {
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'KitDraftTag|DraftBuild|BuildLog' -v`
Expected: FAIL to compile: `undefined: KitDraftBuilds`, `undefined: KitDraftTag`, `undefined: buildLog`.

- [ ] **Step 3: Implement `internal/jam/kit_draft_build.go`**

```go
package jam

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// Draft build states (KitDraftBuild.State).
const (
	DraftBuildRunning   = "running"
	DraftBuildSucceeded = "succeeded"
	DraftBuildFailed    = "failed"
	DraftBuildUnknown   = "unknown" // no build of the kit since Jam started
)

// KitDraftLogCap is how much of a build's log is kept: its last 1 MiB.
const KitDraftLogCap = 1 << 20

// KitDraftBuilder is implemented by a Launcher that can test-build a kit
// draft (the colima launcher). It builds def's chain with the real harness
// and hardening, the top stage from the draft's tree, tagged tag (never a
// kit version's image or stage tag). Its output goes to log, and it stops
// when ctx is done. It returns the built image's id (sha256:…).
type KitDraftBuilder interface {
	BuildKitDraft(ctx context.Context, def KitDefinition, tag string, log io.Writer) (imageID string, err error)
	// HasKitDraftImage reports whether a built image id is still present.
	HasKitDraftImage(imageID string) (bool, error)
}

// KitDraftImage is a draft build's image: its tag and its id. A draft
// studio runs the id, so a later build moving the tag cannot change it.
type KitDraftImage struct {
	Tag string `json:"tag"`
	ID  string `json:"id"`
}

// KitDraftRef names what a draft studio runs: kit Kit's draft tree Tree as
// of its raise.
type KitDraftRef struct {
	Kit  string `json:"kit"`
	Tree string `json:"tree"`
}

// KitDraftBuild is a kit's latest draft build
// (GET /admin/kits/{name}/draft/build). Log is the log from the offset
// asked for: LogFrom is its first byte's offset (past since when bytes were
// dropped: LogSkipped), and LogEnd is the since of the next read. Current
// reports whether Tree is the draft's tree now; DraftETag is the draft's
// ETag now ("" without a draft). The log is docker's output, kept raw:
// consumers escape it.
type KitDraftBuild struct {
	ID         string    `json:"id,omitempty"`
	Kit        string    `json:"kit"`
	Tree       string    `json:"tree,omitempty"`
	State      string    `json:"state"`
	Tag        string    `json:"tag,omitempty"`
	Digest     string    `json:"digest,omitempty"` // the built image's id, on success
	Error      string    `json:"error,omitempty"`
	By         string    `json:"by,omitempty"` // the operator, or "studio raise <label>"
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	DraftETag  string    `json:"draft_etag,omitempty"`
	Current    bool      `json:"current"`
	Log        string    `json:"log"`
	LogFrom    int64     `json:"log_from"`
	LogEnd     int64     `json:"log_end"`
	LogSkipped bool      `json:"log_skipped,omitempty"`
}

const kitDraftStageSuffix = "-stage"

// KitDraftTag is the image tag a build of kit's draft tree gets:
// cove-kit-draft:<kit>-<tree[:12]>. It is never a kit version's tag. A
// name that cannot make a docker tag (the tag part is at most 128
// characters and must fit the -stage suffix; it cannot start with . or -)
// is refused (400), unnamed.
func KitDraftTag(kit, tree string) (string, error) {
	t := kit + "-" + shortTree(tree)
	if !studio.TagSafeName(kit) || len(t)+len(kitDraftStageSuffix) > 128 || kit[0] == '.' || kit[0] == '-' {
		return "", writeErr(http.StatusBadRequest, "kit name cannot form a draft image tag (at most 109 characters of [A-Za-z0-9_.-], not starting with . or -)")
	}
	return "cove-kit-draft:" + t, nil
}

// KitDraftStageTag is the tag of a draft build's own stage (its Dockerfile
// context built on its parent): never a kit version's stage tag.
func KitDraftStageTag(tag string) string { return tag + kitDraftStageSuffix }

// kitDraftDefinitionLocked is the definition a build of kit name's draft
// tree builds under harness h:
//   - the tree checked as a publish checks it (problems refuse, 400);
//   - its parents resolved as a push resolves them;
//   - every link's files copied into memory, so an edit, a GC or a version
//     removal landing while the build runs cannot pull a blob out from
//     under it.
//
// Ref is {name, v0, ChainDigest}; the self link has Version 0. objs is the
// draft tree and its blobs (a draft studio re-stores them should GC take
// them before its instance is recorded). Caller holds kitMu.
func kitDraftDefinitionLocked(store Store, name string, tree kittree.Tree, h harnessinstall.Install) (KitDefinition, KitObjects, error) {
	sk, problems := draftProblems(store, name, tree)
	if len(problems) > 0 {
		return KitDefinition{}, KitObjects{}, writeErr(http.StatusBadRequest, "draft %s has problems: %s", name, strings.Join(problems, "; "))
	}
	chain, err := validateKit(store, name, sk)
	if err != nil {
		return KitDefinition{}, KitObjects{}, err
	}
	var objs KitObjects
	for i := range chain {
		link := &chain[i]
		t := tree
		if i < len(chain)-1 {
			v, ok := LoadKitVersion(store, link.Ref.ID, link.Ref.Version)
			if !ok || v.TreeHash == "" {
				continue // a pre-tree parent has no files, and so no build context
			}
			t = v.Tree
		}
		blobs, err := copyBlobs(store, t)
		if err != nil {
			return KitDefinition{}, KitObjects{}, err
		}
		link.Files = kittree.NewFS(t, kittree.MapBlobs(blobs))
		if i == len(chain)-1 {
			objs = KitObjects{Trees: []kittree.Tree{t}, Blobs: blobs}
		}
	}
	kits := chainKits(chain)
	merged := sk // an unchained kit is exactly as parsed, not re-merged
	if len(chain) > 1 {
		merged = studio.Merge(kits)
	}
	return KitDefinition{Ref: KitRef{ID: name, Digest: studio.ChainDigest(kits, h)}, Kit: merged, Chain: chain, Harness: h}, objs, nil
}

// copyBlobs reads every blob tree t names. Caller holds kitMu.
func copyBlobs(store KitReader, t kittree.Tree) (map[string][]byte, error) {
	blobs := make(map[string][]byte, len(t.Entries))
	for _, e := range t.Entries {
		b, ok := store.KitBlob(e.SHA256)
		if !ok {
			return nil, fmt.Errorf("kit tree %.12s: a file has no stored content", t.Hash())
		}
		blobs[e.SHA256] = b
	}
	return blobs, nil
}

// buildLog is a build's log: its last cap bytes in memory, offsets counted
// from the build's first byte. Safe for concurrent writes (a command's
// stdout and stderr). The buffer trims to cap once it passes twice cap, so
// a long log costs amortised O(1) per byte; a read never returns more than
// cap.
type buildLog struct {
	mu      sync.Mutex
	buf     []byte
	dropped int64 // bytes trimmed from the head
	cap     int
}

func (l *buildLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	if len(l.buf) > 2*l.cap {
		over := len(l.buf) - l.cap
		l.buf = append(l.buf[:0:0], l.buf[over:]...)
		l.dropped += int64(over)
	}
	return len(p), nil
}

// read is the log from offset since: its text, the offset that text starts
// at (past since when bytes were dropped; clamped to the end), and the end
// offset (the next read's since). While final is false a trailing
// incomplete UTF-8 sequence is held back for the next read.
func (l *buildLog) read(since int64, final bool) (text string, from, end int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	end = l.dropped + int64(len(l.buf))
	head := max(l.dropped, end-int64(l.cap))
	from = min(max(since, head), end)
	chunk := l.buf[from-l.dropped:]
	if !final {
		chunk = completeRunes(chunk)
	}
	return string(chunk), from, from + int64(len(chunk))
}

// completeRunes is b without a trailing incomplete UTF-8 sequence.
func completeRunes(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

// draftBuildJob is one build. The fields after done are guarded by
// KitDraftBuilds.mu; the rest are set before the job is shared.
type draftBuildJob struct {
	id, kit, tree, tag, buildDigest, by string
	started                             time.Time
	log                                 *buildLog
	cancel                              context.CancelFunc
	done                                chan struct{}

	state, imageID, errText string
	finished                time.Time
}

// KitDraftBuilds runs kit draft test builds and draft studios' images
// (docs/usage/jam/kits-drafts.md#test-build). At most one build runs per
// kit at a time, each in a goroutine under a context derived from the
// serve's (cancelled on shutdown). Only the latest build per kit is kept,
// in memory: its state, image and log are lost on restart.
type KitDraftBuilds struct {
	ctx     context.Context
	store   Store
	builder KitDraftBuilder
	log     *slog.Logger
	now     func() time.Time
	logCap  int

	mu   sync.Mutex
	jobs map[string]*draftBuildJob // kit → its latest build
	wg   sync.WaitGroup
}

// NewKitDraftBuilds runs builds over builder under ctx (the serve's build
// context: cancelling it stops every build).
func NewKitDraftBuilds(ctx context.Context, store Store, builder KitDraftBuilder, log *slog.Logger) *KitDraftBuilds {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &KitDraftBuilds{ctx: ctx, store: store, builder: builder, log: log, now: time.Now, logCap: KitDraftLogCap, jobs: map[string]*draftBuildJob{}}
}

func errDraftBuildRunning(name string) error {
	return writeErr(http.StatusConflict, "kit %s already has a draft build running", name)
}

// Start starts a test build of kit name's draft at ETag ifMatch, under the
// default harness, for by. The draft is read and its files copied under
// kitMu. Refusals: 428/412 for If-Match, 400 for problems or a name that
// cannot form a tag, 404 for no kit or no draft, 409 when the kit already
// has a build running.
func (b *KitDraftBuilds) Start(name, ifMatch, by string) (KitDraftBuild, error) {
	def, d, err := b.draftForBuild(name, ifMatch)
	if err != nil {
		return KitDraftBuild{}, err
	}
	tag, err := KitDraftTag(name, d.Tree)
	if err != nil {
		return KitDraftBuild{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if j := b.jobs[name]; j != nil && j.state == DraftBuildRunning {
		return KitDraftBuild{}, errDraftBuildRunning(name)
	}
	return b.viewLocked(b.startLocked(def, d.Tree, tag, by), 0), nil
}

// draftForBuild is kit name's draft, its ETag checked, as the definition a
// test build builds, read under kitMu.
func (b *KitDraftBuilds) draftForBuild(name, ifMatch string) (KitDefinition, KitDraft, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	d, tree, err := lockedDraftTree(b.store, name, ifMatch)
	if err != nil {
		return KitDefinition{}, KitDraft{}, err
	}
	def, _, err := kitDraftDefinitionLocked(b.store, name, tree, harnessinstall.Default())
	return def, d, err
}

// startLocked starts a build of def (kit def.Ref.ID's draft tree tree) as
// the kit's latest. Caller holds b.mu and has checked none is running.
func (b *KitDraftBuilds) startLocked(def KitDefinition, tree, tag, by string) *draftBuildJob {
	ctx, cancel := context.WithCancel(b.ctx)
	j := &draftBuildJob{
		id: newDraftBuildID(), kit: def.Ref.ID, tree: tree, tag: tag, buildDigest: def.Ref.Digest, by: by,
		started: b.now().UTC(), log: &buildLog{cap: b.logCap}, cancel: cancel, done: make(chan struct{}),
		state: DraftBuildRunning,
	}
	b.jobs[j.kit] = j
	b.wg.Add(1)
	go b.run(ctx, j, def)
	b.log.Info("kit draft build started", "kit", j.kit, "tree", shortTree(tree), "build", j.id, "by", by)
	return j
}

// run is one build's goroutine: it builds, writes the outcome as the log's
// last line, and records the state.
func (b *KitDraftBuilds) run(ctx context.Context, j *draftBuildJob, def KitDefinition) {
	defer b.wg.Done()
	defer close(j.done)
	defer j.cancel()
	id, err := b.builder.BuildKitDraft(ctx, def, j.tag, j.log)
	state, errText := DraftBuildSucceeded, ""
	switch {
	case err == nil:
		fmt.Fprintf(j.log, "\nbuilt %s (%s)\n", j.tag, id)
	case b.ctx.Err() != nil:
		state, errText, id = DraftBuildFailed, "cancelled: Jam is shutting down", ""
	case ctx.Err() != nil:
		state, errText, id = DraftBuildFailed, "cancelled", ""
	default:
		state, errText, id = DraftBuildFailed, err.Error(), ""
	}
	if state == DraftBuildFailed {
		fmt.Fprintf(j.log, "\nbuild failed: %s\n", errText)
	}
	b.mu.Lock()
	j.state, j.imageID, j.errText, j.finished = state, id, errText, b.now().UTC()
	b.mu.Unlock()
	b.log.Info("kit draft build finished", "kit", j.kit, "tree", shortTree(j.tree), "build", j.id, "state", state)
}

// Status is kit name's latest build, its log from offset since; state
// unknown when none ran since Jam started. 400 for an unsafe name or a
// negative since, 404 for an unknown kit.
func (b *KitDraftBuilds) Status(name string, since int64) (KitDraftBuild, error) {
	if !studio.TagSafeName(name) {
		return KitDraftBuild{}, unsafeKitName()
	}
	if since < 0 {
		return KitDraftBuild{}, writeErr(http.StatusBadRequest, "since must be a log offset (0 or more)")
	}
	if _, ok := b.store.GetKit(name); !ok {
		return KitDraftBuild{}, writeErr(http.StatusNotFound, "no such kit")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	j := b.jobs[name]
	if j == nil {
		v := KitDraftBuild{Kit: name, State: DraftBuildUnknown}
		if d, ok := b.store.GetKitDraft(name); ok {
			v.DraftETag = DraftETag(d.Tree)
		}
		return v, nil
	}
	return b.viewLocked(j, since), nil
}

// viewLocked is j as the API shows it, its log from since. Caller holds b.mu.
func (b *KitDraftBuilds) viewLocked(j *draftBuildJob, since int64) KitDraftBuild {
	v := KitDraftBuild{
		ID: j.id, Kit: j.kit, Tree: j.tree, State: j.state, Tag: j.tag, Digest: j.imageID, Error: j.errText,
		By: j.by, StartedAt: j.started, FinishedAt: j.finished,
	}
	if d, ok := b.store.GetKitDraft(j.kit); ok {
		v.DraftETag, v.Current = DraftETag(d.Tree), d.Tree == j.tree
	}
	v.Log, v.LogFrom, v.LogEnd = j.log.read(since, j.state != DraftBuildRunning)
	v.LogSkipped = v.LogFrom > since
	return v
}

// Cancel stops kit name's running build and waits (until ctx is done) for
// it to end; it is then failed, "cancelled". 404 when none is running.
func (b *KitDraftBuilds) Cancel(ctx context.Context, name string) (KitDraftBuild, error) {
	if !studio.TagSafeName(name) {
		return KitDraftBuild{}, unsafeKitName()
	}
	b.mu.Lock()
	j := b.jobs[name]
	running := j != nil && j.state == DraftBuildRunning
	b.mu.Unlock()
	if !running {
		return KitDraftBuild{}, writeErr(http.StatusNotFound, "kit %s has no draft build running", name)
	}
	j.cancel()
	select {
	case <-j.done:
	case <-ctx.Done():
		return KitDraftBuild{}, ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.viewLocked(j, 0), nil
}

// Wait waits up to timeout for every build goroutine to end (after the
// serve context is cancelled); false when some still run.
func (b *KitDraftBuilds) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// newDraftBuildID is a build's id: kdb_ and 16 random hex digits.
func newDraftBuildID() string {
	var r [8]byte
	_, _ = rand.Read(r[:])
	return "kdb_" + hex.EncodeToString(r[:])
}
```

- [ ] **Step 4: Run the tests, under the race detector**

Run: `GOPROXY=https://proxy.golang.org,direct go test -race ./internal/jam/ -run 'KitDraftTag|DraftBuild|BuildLog' -v`
Expected: PASS. Then run all of jam: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/...`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l internal/jam
git add internal/jam/kit_draft_build.go internal/jam/kit_draft_build_test.go
git commit -m "feat(jam): kit draft test-build jobs (one per kit, capped in-memory log, cancel)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: The launcher's draft build and the draft image raise

**Files:**
- Modify: `internal/jam/launcher/prepare.go`. `resolveStudioBase`, `resolveOwnBase` and `resolveChainStage` take the builder; `resolveStudioBase` and `resolveChainStage` also take the top-stage tag. `PrepareKit` passes `l.cfg.Ops, …, ""`.
- Create: `internal/jam/launcher/draft.go`
- Modify: `internal/jam/launcher/launcher.go` (`Raise` runs `spec.DraftImage`)
- Modify: `internal/jam/supervisor.go` (`RaiseSpec.DraftImage`)
- Test: `internal/jam/launcher/draft_test.go` (create)

**Interfaces:**
- Consumes: `jam.KitDraftBuilder`, `jam.KitDraftImage`, `jam.KitDraftStageTag` (Task 2); `backend.KitImageBuilder`, `backend.LoggedKitBuilder` (Task 1); `colima.NewWithContext`; `runner.Fake` with `OnRun`; the launcher test helpers `treeLink`, `tarNames`, `df`, `ctxLink`, `chainLink`, `chainDef`, `mustStage`, `noAsm`, `fakeOps`, `fakeInv`, `newPrepareLauncher`.
- Produces:
```go
package launcher
func (l *Launcher) BuildKitDraft(ctx context.Context, def KitDefinition, tag string, log io.Writer) (string, error)
func (l *Launcher) HasKitDraftImage(id string) (bool, error)
func (l *Launcher) resolveStudioBase(ops backend.KitImageBuilder, def KitDefinition, top string) (string, error)

package jam
// RaiseSpec gains: DraftImage *KitDraftImage
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/launcher/draft_test.go`:

```go
package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
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

const draftTag = "cove-kit-draft:app-0123456789ab"

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

// An unchained draft: the real BuildKitImage argv with the draft tag and
// the kit's build args, the build streamed to the log, no env, no
// cove-kit: tag anywhere, and no build-arg value in the launcher's lines.
func TestBuildKitDraftArgv(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{}, // docker info (preflight)
		{Stdout: "#5 [3/3] RUN install\n#5 DONE\n"}, // docker build --progress=plain …
		{Stdout: "sha256:img\n"},                    // docker inspect --format {{.Id}} <tag>
	}}
	l := colimaDraftLauncher(t, f)
	link := treeLink(t, "app", 0, "kit.yml", "kind: studio\nbuild-args:\n  NPM_TOKEN: npmSECRET\n")
	def := KitDefinition{Ref: link.Ref, Kit: link.Kit, Chain: []jam.KitLink{link}, Harness: harnessinstall.Default()}
	var log bytes.Buffer
	id, err := l.BuildKitDraft(context.Background(), def, draftTag, &log)
	if err != nil || id != "sha256:img" {
		t.Fatalf("BuildKitDraft = %q, %v", id, err)
	}
	builds, _ := dockerArgsAll(f.Calls, "build")
	if len(builds) != 1 || after(builds[0], "-t") != draftTag || !slices.Contains(builds[0], "NPM_TOKEN=npmSECRET") ||
		!slices.Contains(builds[0], "BASE="+basedigest.DefaultRef()) || !slices.Contains(builds[0], "--progress=plain") {
		t.Fatalf("builds = %v", builds)
	}
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if strings.HasPrefix(a, "cove-kit:") {
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
	blessed := basedigest.BlessedRefs()
	if len(blessed) == 0 {
		t.Skip("no blessed base refs compiled in")
	}
	outs := []runner.FakeResult{
		{Stdout: "sha256:child\n", Stderr: "#3 [2/2] RUN make\n"}, // docker build -q … - (the draft's stage)
		{},                                   // docker tag sha256:child cove-kit-base:child
		{Stdout: `["sha256:a","sha256:b","sha256:c"]`}, // the stage's layers
	}
	for range blessed {
		outs = append(outs, runner.FakeResult{Stdout: `["sha256:a","sha256:b"]`}) // each blessed base: a prefix
	}
	outs = append(outs,
		runner.FakeResult{},                      // docker tag cove-kit-base:child <tag>-stage
		runner.FakeResult{},                      // docker info
		runner.FakeResult{},                      // docker build --progress=plain …
		runner.FakeResult{Stdout: "sha256:img\n"}) // docker inspect
	f := &runner.Fake{Outputs: outs}
	l := colimaDraftLauncher(t, f)
	root := treeLink(t, "web", 2, "kit.yml", "kind: studio\n")
	app := treeLink(t, "app", 0, "kit.yml", "kind: studio\nbase: {kit: web}\n", "Dockerfile", df+"RUN make\n", "run.sh", "echo\n")
	var log bytes.Buffer
	if _, err := l.BuildKitDraft(context.Background(), chainDef(nil, root, app), draftTag, &log); err != nil {
		t.Fatal(err)
	}
	builds, stdin := dockerArgsAll(f.Calls, "build")
	if len(builds) != 2 || !slices.Contains(builds[0], "-") || !slices.Contains(builds[0], "COVE_BASE_IMAGE="+basedigest.DefaultRef()) {
		t.Fatalf("builds = %v", builds)
	}
	if got := tarNames(t, []byte(stdin[0])); got != "Dockerfile,run.sh" {
		t.Fatalf("the stage context = %s, want the draft's files minus kit.yml", got)
	}
	tags, _ := dockerArgsAll(f.Calls, "tag")
	if last := tags[len(tags)-1]; len(last) != 3 || last[1] != "cove-kit-base:child" || last[2] != jam.KitDraftStageTag(draftTag) {
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
	const tag = "cove-kit-draft:my-app-0123456789ab"
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
	if _, err := l.BuildKitDraft(context.Background(), def, "cove-kit:abc-def", io.Discard); err == nil {
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
	spec := jam.RaiseSpec{ActorID: "d1", Kit: jam.KitRef{ID: "app", Digest: "d1gest"}, DraftImage: &jam.KitDraftImage{Tag: draftTag, ID: "sha256:img"}}
	if _, err := l.Raise(context.Background(), spec, jam.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if ops.runImage != draftTag || ops.runDigest != "sha256:img" {
		t.Fatalf("ran %q @ %q", ops.runImage, ops.runDigest)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/launcher/ -run 'KitDraft|DraftStudio' -v`
Expected: FAIL to compile: `l.BuildKitDraft undefined`, `unknown field DraftImage in struct literal of type jam.RaiseSpec`.

- [ ] **Step 3: Add `RaiseSpec.DraftImage`**

In `internal/jam/supervisor.go`, in `RaiseSpec`, after `Context *sessionctx.Bundle`, add:

```go
	// DraftImage, set by Supervisor.Raise for a draft studio
	// (docs/usage/jam/kits-drafts.md#draft-studio), is the test build's image
	// the launcher runs (by id) instead of the kit inventory's. nil for every
	// other raise.
	DraftImage *KitDraftImage
```

- [ ] **Step 4: Thread the builder and the top-stage tag through the base resolvers**

In `internal/jam/launcher/prepare.go`, change `PrepareKit`'s call `base, err := l.resolveStudioBase(def)` to `base, err := l.resolveStudioBase(l.cfg.Ops, def, "")`. Replace `resolveStudioBase`, `resolveOwnBase` and `resolveChainStage` with:

```go
// resolveStudioBase resolves the gated FROM-base for a studio kit on ops. An
// unchained kit (len(def.Chain) <= 1) resolves its own base by resolveOwnBase. A
// chain resolves its root that way, then builds each later link's own context as a
// stage FROM the previous ref (resolveChainStage); a link with no own context
// (studio.BaseKit) leaves the ref unchanged. The gate is ON for every stage
// built. The returned ref is the one BuildKitImage builds FROM, so the harness
// and hardening layers are applied exactly once, on top of the whole chain.
// top, when set, is a kit draft build's own stage tag: the chain's last link
// builds under it instead of its kit version's stage tag (BuildKitDraft).
func (l *Launcher) resolveStudioBase(ops backend.KitImageBuilder, def KitDefinition, top string) (string, error) {
	if len(def.Chain) <= 1 {
		// Guard on the chain's own link, not def.Kit.Base: Merge clears
		// Base.Kit, so the merged kit cannot show a named parent.
		own := jam.KitLink{Ref: def.Ref, Kit: def.Kit}
		if len(def.Chain) == 1 {
			own = def.Chain[0]
		}
		return l.resolveOwnBase(ops, own)
	}
	root := def.Chain[0]
	ref, err := l.resolveOwnBase(ops, root)
	if err != nil {
		return "", fmt.Errorf("kit %s: %w", root.Ref, err)
	}
	links := def.Chain[1:]
	for i, link := range links {
		stage := ""
		if i == len(links)-1 {
			stage = top
		}
		if ref, err = l.resolveChainStage(ops, link, ref, stage); err != nil {
			return "", fmt.Errorf("kit %s: %w", link.Ref, err)
		}
	}
	return ref, nil
}

// resolveOwnBase resolves a parentless link's base on ops. A kit whose tree holds a
// Dockerfile streams its directory (minus kit.yml) to the backend's `docker
// build -` FROM the blessed base, gated — no host dir, no extraction.
// Otherwise the declared Base.Image is resolved+gated ("" → blessed default).
// A base naming a parent kit is refused (fail closed): only a resolved chain
// may build it, on its parent's stage, never on the blessed base alone.
func (l *Launcher) resolveOwnBase(ops backend.KitImageBuilder, link jam.KitLink) (string, error) {
	b := link.Kit.Base
	if b.HasParent() {
		return "", fmt.Errorf("base names parent kit %q, which the kit chain does not resolve", b.Kit)
	}
	if link.Kit.BuildContext != "" {
		r, err := contextReader(link)
		if err != nil {
			return "", err
		}
		return ops.ResolveKitBaseTar(r)
	}
	return ops.ResolveKitBase(b.Image)
}

// resolveChainStage resolves one non-root chain link on parentRef (the previous
// stage), on ops. A link with no build context is the parent's stage as-is. Otherwise
// its stage is cached under kitStageTag(l.asm, parentRef, link.Kit): a present
// stage tag was applied only after that stage's gate passed, so it is reused;
// a missing one is built by streaming the link's context (its tree minus
// kit.yml) FROM parentRef, gated against the blessed base, and tagged. The
// stage lock keeps two concurrent prepares sharing a parent from building the
// same stage twice. A draft's own stage (top set) is never looked up or
// cached under a kit version's stage tag: it is built, gated, and tagged top.
func (l *Launcher) resolveChainStage(ops backend.KitImageBuilder, link jam.KitLink, parentRef, top string) (string, error) {
	if !link.Kit.Base.HasParent() {
		return "", fmt.Errorf("a non-root chain link must name its parent kit")
	}
	if link.Kit.BuildContext == "" {
		return parentRef, nil
	}
	if top != "" {
		r, err := contextReader(link)
		if err != nil {
			return "", err
		}
		return ops.ResolveKitBaseTarOn(r, parentRef, top)
	}
	stage, err := kitStageTag(l.asm, parentRef, link.Kit)
	if err != nil {
		return "", err
	}
	unlock := l.lockKey(stage)
	defer unlock()
	if ok, err := ops.HasKitBase(stage); err != nil {
		return "", fmt.Errorf("stage %s: %w", stage, err)
	} else if ok {
		return stage, nil
	}
	r, err := contextReader(link)
	if err != nil {
		return "", err
	}
	return ops.ResolveKitBaseTarOn(r, parentRef, stage)
}
```

Add `"github.com/aethons-tools/cove/internal/backend"` to `prepare.go`'s imports.

- [ ] **Step 5: Implement `internal/jam/launcher/draft.go`**

```go
package launcher

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/jam"
)

const draftTagPrefix = "cove-kit-draft:"

// BuildKitDraft is jam.KitDraftBuilder: a kit draft's test build
// (docs/usage/jam/kits-drafts.md#test-build). It builds def exactly as
// PrepareKit does: the same assemble (harness, hardening, egress, MCP
// servers, key), the chain's parent stages cached and reused, and the same
// gate and build args. Only the tags differ: the draft's own stage is
// tagged jam.KitDraftStageTag(tag) and the image tag. Neither is ever a kit
// version's stage or image tag. With a backend.LoggedKitBuilder, the docker
// output goes to log and a done ctx stops the build. It returns the image
// id. The launcher's own log lines never print build-arg values.
func (l *Launcher) BuildKitDraft(ctx context.Context, def KitDefinition, tag string, log io.Writer) (string, error) {
	if !strings.HasPrefix(tag, draftTagPrefix) {
		return "", fmt.Errorf("draft build %s: %q is not a draft image tag", def.Ref.ID, tag)
	}
	var ops backend.KitImageBuilder = l.cfg.Ops
	if lb, ok := l.cfg.Ops.(backend.LoggedKitBuilder); ok {
		ops = lb.WithBuildLog(ctx, log)
	}
	buildDir := filepath.Join(l.cfg.BuildRoot, "draft", strings.TrimPrefix(tag, draftTagPrefix))
	defer os.RemoveAll(buildDir)
	fmt.Fprintf(log, "== kit draft %s: %s (harness %s %s)\n", def.Ref.ID, chainText(def.Chain), def.Harness.Type, def.Harness.Version)
	if err := l.cfg.assemble(def, buildDir); err != nil {
		return "", fmt.Errorf("draft build %s: assemble: %w", def.Ref.ID, err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	base, err := l.resolveStudioBase(ops, def, jam.KitDraftStageTag(tag))
	if err != nil {
		return "", fmt.Errorf("draft build %s: resolve base: %w", def.Ref.ID, err)
	}
	fmt.Fprintf(log, "== image %s FROM %s\n", tag, base)
	id, err := ops.BuildKitImage(buildDir, tag, base, def.Kit.BuildArgs, false)
	if err != nil {
		return "", fmt.Errorf("draft build %s: build: %w", def.Ref.ID, err)
	}
	l.cfg.Log.Info("built kit draft", "kit", def.Ref.ID, "tag", tag, "asm", shortAsm(l.asm))
	return id, nil
}

// chainText is a draft's chain root first, the draft last: "web@v2 → app (draft)".
func chainText(chain []jam.KitLink) string {
	names := make([]string, len(chain))
	for i, c := range chain {
		names[i] = c.Ref.String()
	}
	if n := len(names); n > 0 {
		names[n-1] = chain[n-1].Ref.ID + " (draft)"
	}
	return strings.Join(names, " → ")
}

// HasKitDraftImage is jam.KitDraftBuilder's presence check: the image id on
// the substrate (the daemon BuildKitDraft and Raise use).
func (l *Launcher) HasKitDraftImage(id string) (bool, error) { return l.cfg.Ops.HasKitImage(id) }

var _ jam.KitDraftBuilder = (*Launcher)(nil)
```

- [ ] **Step 6: Run the draft image in `Raise`**

In `internal/jam/launcher/launcher.go` `Raise`, replace the block from `ok, err := l.inv.Has(spec.Kit)` through `image, digest := l.imageTag(spec.Kit), ""` with:

```go
	var image, digest string
	if d := spec.DraftImage; d != nil {
		// A draft studio runs its test build's image by id: the tag names
		// the latest build of that tree and may move. The supervisor built
		// (or checked) it just before; the kit inventory does not apply.
		image, digest = d.Tag, d.ID
	} else {
		ok, err := l.inv.Has(spec.Kit)
		if err != nil {
			return "", fmt.Errorf("raise %s: inventory: %w", name, err)
		}
		if !ok {
			return "", fmt.Errorf("raise %s: %w", spec.Kit, ErrKitNotReady)
		}
		// Run the immutable cove-kit:<build-digest>-<asm> tag. No digest pin: the tag already
		// names the exact built image by its build-input digest.
		image = l.imageTag(spec.Kit)
	}
```

- [ ] **Step 7: Run the launcher tests**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/launcher/ ./internal/jam/ -v -run 'KitDraft|DraftStudio|Prepare|Chain|Stage|Raise'`
Expected: PASS. The existing `PrepareKit`/chain/stage tests show the refactor kept the normal path. Then run `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/...`. Expected: PASS.

- [ ] **Step 8: Commit**

```bash
gofmt -l internal/jam
git add internal/jam/launcher internal/jam/supervisor.go
git commit -m "feat(launcher): kit draft test build on the real build path; raise a draft image by id

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: The build API, the client and the serve wiring

**Files:**
- Create: `internal/jam/kit_draft_build_admin.go`
- Modify: `internal/jam/admin.go` (`adminMux.builds`, `WithKitDraftBuilds`, the `draftBuilds` option variable, `registerKitDraftBuilds` call)
- Modify: `internal/jam/supervisor.go` (`draftBuilds` field, `SetKitDraftBuilds`)
- Modify: `internal/jam/adminclient/kitdraft.go` (the build client)
- Create: `cmd/at-jam/serve_shutdown.go`; Modify: `cmd/at-jam/main.go` (wiring)
- Modify: `docs/usage/jam/kits-drafts.md` ("## Test build" and the API rows)
- Test: `internal/jam/kit_draft_build_admin_test.go` (create), `internal/jam/adminclient/kitdraft_build_test.go` (create), `cmd/at-jam/serve_shutdown_test.go` (create)

**Interfaces:**
- Consumes: `KitDraftBuilds` and its `Start`/`Status`/`Cancel`/`Wait`, `KitDraftBuild`, `NewKitDraftBuilds`, `fakeDraftBuilder`, `draftBuildsFor` (Task 2); `requireContentType`, `decodeOptional`, `draftFail`, `writeJSON`, `OperatorID`, `shortTree`, `draftDo`, `body` (package jam); `adminclient.draftClient`, `draftPath`, `ifMatch`, `sendHeaders`.
- Produces:
```go
package jam
const kitDraftBuildsOff = "test builds need a launcher that builds images (runtime.launcher)"
func WithKitDraftBuilds(b *KitDraftBuilds) AdminOption
func registerKitDraftBuilds(mux *http.ServeMux, builds func() *KitDraftBuilds, log *slog.Logger)
func (s *Supervisor) SetKitDraftBuilds(b *KitDraftBuilds)

package adminclient
func (c *Client) StartKitDraftBuild(name, etag string) (jam.KitDraftBuild, error)
func (c *Client) KitDraftBuildStatus(name string, since int64) (jam.KitDraftBuild, error)
func (c *Client) CancelKitDraftBuild(name string) (jam.KitDraftBuild, error)

package main
func stopBuildsOnSignal(sigs <-chan os.Signal, stop context.CancelFunc, builds *jam.KitDraftBuilds, log *slog.Logger, exit func(int))
func shutdownSignals() <-chan os.Signal
```

- [ ] **Step 1: Write the failing route tests**

Create `internal/jam/kit_draft_build_admin_test.go`:

```go
package jam

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// buildsHandler is the admin handler serving draftBuildsFor's builds, its
// audit log, and the draft's ETag.
func buildsHandler(t *testing.T, fb *fakeDraftBuilder) (http.Handler, *KitDraftBuilds, string, *bytes.Buffer) {
	t.Helper()
	b, st, etag, _ := draftBuildsFor(t, fb)
	var logs bytes.Buffer
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil, WithKitDraftBuilds(b))
	return h, b, etag, &logs
}

func TestDraftBuildRoutes(t *testing.T) {
	fb := &fakeDraftBuilder{started: make(chan string, 1), release: make(chan struct{}), write: "#1 \x1b[31mred\x1b[0m\n"}
	h, _, etag, logs := buildsHandler(t, fb)
	const path = "/admin/kits/app/draft/build"
	if w := draftDo(t, h, "POST", path, "text/plain", etag, nil); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain = %d", w.Code)
	}
	if w := draftDo(t, h, "POST", path, "application/json", "", nil); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match = %d", w.Code)
	}
	if w := draftDo(t, h, "POST", path, "application/json", `"`+strings.Repeat("0", 64)+`"`, nil); w.Code != http.StatusPreconditionFailed || body(w) != "draft app changed since you loaded it (reload)" {
		t.Fatalf("stale = %d %s", w.Code, w.Body)
	}
	w := draftDo(t, h, "POST", path, "application/json", etag, nil)
	var v KitDraftBuild
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.State != DraftBuildRunning || DraftETag(v.Tree) != etag || v.ID == "" {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	if w := draftDo(t, h, "POST", path, "application/json", etag, nil); w.Code != http.StatusConflict || body(w) != "kit app already has a draft build running" {
		t.Fatalf("second start = %d %s", w.Code, w.Body)
	}
	<-fb.started // the fake wrote its log line before signalling
	g := draftDo(t, h, "GET", path, "", "", nil)
	var gv KitDraftBuild
	if g.Code != http.StatusOK || json.Unmarshal(g.Body.Bytes(), &gv) != nil || gv.ID != v.ID || !strings.Contains(gv.Log, "\x1b[31mred") || g.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status = %d %s", g.Code, g.Body)
	}
	f := draftDo(t, h, "GET", path+"?since="+jsonInt(gv.LogEnd), "", "", nil)
	var fv KitDraftBuild
	if f.Code != http.StatusOK || json.Unmarshal(f.Body.Bytes(), &fv) != nil || fv.Log != "" || fv.LogFrom != gv.LogEnd {
		t.Fatalf("follow = %d %s", f.Code, f.Body)
	}
	for _, q := range []string{"-1", "x"} {
		if w := draftDo(t, h, "GET", path+"?since="+q, "", "", nil); w.Code != http.StatusBadRequest || body(w) != "since must be a log offset (0 or more)" {
			t.Fatalf("since=%s = %d %s", q, w.Code, w.Body)
		}
	}
	d := draftDo(t, h, "DELETE", path, "", "", nil)
	var dv KitDraftBuild
	if d.Code != http.StatusOK || json.Unmarshal(d.Body.Bytes(), &dv) != nil || dv.State != DraftBuildFailed || dv.Error != "cancelled" {
		t.Fatalf("cancel = %d %s", d.Code, d.Body)
	}
	if w := draftDo(t, h, "DELETE", path, "", "", nil); w.Code != http.StatusNotFound || body(w) != "kit app has no draft build running" {
		t.Fatalf("second cancel = %d %s", w.Code, w.Body)
	}
	for _, want := range []string{"kit draft build requested", "kit draft build cancelled"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("audit lacks %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs.String(), "red") {
		t.Fatal("an audit line carried log content")
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestDraftBuildRoutesNeedALauncher(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.DiscardHandler), nil)
	for _, m := range []string{"GET", "DELETE"} {
		if w := draftDo(t, h, m, "/admin/kits/app/draft/build", "", "", nil); w.Code != http.StatusServiceUnavailable || body(w) != kitDraftBuildsOff {
			t.Fatalf("%s = %d %s", m, w.Code, w.Body)
		}
	}
	if w := draftDo(t, h, "POST", "/admin/kits/app/draft/build", "application/json", `"x"`, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST = %d", w.Code)
	}
}

// Review Focus 3, over HTTP: two concurrent POSTs, one 202 and one 409.
func TestDraftBuildTwoConcurrentPosts(t *testing.T) {
	fb := &fakeDraftBuilder{release: make(chan struct{})}
	h, _, etag, _ := buildsHandler(t, fb)
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- draftDo(t, h, "POST", "/admin/kits/app/draft/build", "application/json", etag, nil).Code
		}()
	}
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for c := range codes {
		got[c]++
	}
	if got[http.StatusAccepted] != 1 || got[http.StatusConflict] != 1 {
		t.Fatalf("codes = %v, want one 202 and one 409", got)
	}
	close(fb.release)
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'DraftBuildRoutes|DraftBuildTwo' -v`
Expected: FAIL to compile: `undefined: WithKitDraftBuilds`, `undefined: kitDraftBuildsOff`.

- [ ] **Step 3: Implement the routes**

Create `internal/jam/kit_draft_build_admin.go`:

```go
package jam

import (
	"log/slog"
	"net/http"
	"strconv"
)

// kitDraftBuildsOff answers a build route (and a draft studio's raise) on a
// Jam whose launcher cannot build images (no runtime.launcher).
const kitDraftBuildsOff = "test builds need a launcher that builds images (runtime.launcher)"

// WithKitDraftBuilds serves the draft test-build routes from b — the
// supervisor's (SetKitDraftBuilds), so a draft studio's raise builds
// through the same jobs. Without it the routes answer 503.
func WithKitDraftBuilds(b *KitDraftBuilds) AdminOption {
	return func(m *adminMux) { m.builds = b }
}

// registerKitDraftBuilds mounts the draft test-build routes
// (docs/usage/jam/kits-drafts.md#test-build) behind the operator gate:
//
//	POST   /admin/kits/{name}/draft/build            start (application/json, If-Match; 202)
//	GET    /admin/kits/{name}/draft/build[?since=N]  the latest build, its log from N
//	DELETE /admin/kits/{name}/draft/build            cancel the running build (200)
//
// builds is read per request: it is set from the options, before serving.
func registerKitDraftBuilds(mux *http.ServeMux, builds func() *KitDraftBuilds, log *slog.Logger) {
	wired := func(w http.ResponseWriter) (*KitDraftBuilds, bool) {
		b := builds()
		if b == nil {
			http.Error(w, kitDraftBuildsOff, http.StatusServiceUnavailable)
		}
		return b, b != nil
	}
	mux.HandleFunc("POST /admin/kits/{name}/draft/build", func(w http.ResponseWriter, r *http.Request) {
		if !requireContentType(w, r, "application/json") {
			return
		}
		var none struct{}
		if !decodeOptional(w, r, &none) {
			return
		}
		b, ok := wired(w)
		if !ok {
			return
		}
		name := r.PathValue("name")
		v, err := b.Start(name, r.Header.Get("If-Match"), OperatorID(r))
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		log.Info("kit draft build requested", "operator", OperatorID(r), "kit", name, "tree", shortTree(v.Tree), "build", v.ID)
		writeJSON(w, http.StatusAccepted, v)
	})
	mux.HandleFunc("GET /admin/kits/{name}/draft/build", func(w http.ResponseWriter, r *http.Request) {
		b, ok := wired(w)
		if !ok {
			return
		}
		var since int64
		if q := r.URL.Query().Get("since"); q != "" {
			n, err := strconv.ParseInt(q, 10, 64)
			if err != nil || n < 0 {
				http.Error(w, "since must be a log offset (0 or more)", http.StatusBadRequest)
				return
			}
			since = n
		}
		v, err := b.Status(r.PathValue("name"), since)
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, v)
	})
	mux.HandleFunc("DELETE /admin/kits/{name}/draft/build", func(w http.ResponseWriter, r *http.Request) {
		b, ok := wired(w)
		if !ok {
			return
		}
		name := r.PathValue("name")
		v, err := b.Cancel(r.Context(), name)
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		log.Info("kit draft build cancelled", "operator", OperatorID(r), "kit", name, "build", v.ID)
		writeJSON(w, http.StatusOK, v)
	})
}
```

In `internal/jam/admin.go`:
- In `adminMux`, add the field `builds *KitDraftBuilds // WithKitDraftBuilds; nil → the build routes answer 503`.
- In `NewAdminHandler`, right after `var importConds *condition.Tracker …`, add `var draftBuilds *KitDraftBuilds // set from the options below (WithKitDraftBuilds), before any request`.
- After `registerKitDrafts(mux, store, log)`, add `registerKitDraftBuilds(mux, func() *KitDraftBuilds { return draftBuilds }, log)`.
- After `importConds = am.conds`, add `draftBuilds = am.builds`.

In `internal/jam/supervisor.go`, add to `Supervisor` after `previews bool`:

```go
	// draftBuilds runs kit draft test builds, and the images draft studios
	// run (SetKitDraftBuilds); nil → draft studios are unavailable (503).
	draftBuilds *KitDraftBuilds
```

and after `SetTunnelCloser`:

```go
// SetKitDraftBuilds wires the kit draft test builds a draft studio's raise
// builds through (the admin API's, WithKitDraftBuilds). Set once at wiring.
func (s *Supervisor) SetKitDraftBuilds(b *KitDraftBuilds) { s.draftBuilds = b }
```

- [ ] **Step 4: Run the route tests**

Run: `GOPROXY=https://proxy.golang.org,direct go test -race ./internal/jam/ -run 'DraftBuild' -v`
Expected: PASS.

- [ ] **Step 5: Write the failing client test**

Create `internal/jam/adminclient/kitdraft_build_test.go`:

```go
package adminclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKitDraftBuildClient(t *testing.T) {
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.RequestURI()+" ct="+r.Header.Get("Content-Type")+" if="+r.Header.Get("If-Match"))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "POST":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":"kdb_1","kit":"app","tree":"t","state":"running"}`)
		case "GET":
			_, _ = io.WriteString(w, `{"id":"kdb_1","kit":"app","state":"running","log":"x","log_from":3,"log_end":4}`)
		case "DELETE":
			_, _ = io.WriteString(w, `{"id":"kdb_1","kit":"app","state":"failed","error":"cancelled"}`)
		}
	}))
	defer ts.Close()
	c := New(ts.URL, "")
	if b, err := c.StartKitDraftBuild("app", `"t"`); err != nil || b.ID != "kdb_1" || b.State != "running" {
		t.Fatalf("start = %+v %v", b, err)
	}
	if b, err := c.KitDraftBuildStatus("app", 3); err != nil || b.Log != "x" || b.LogEnd != 4 {
		t.Fatalf("status = %+v %v", b, err)
	}
	if b, err := c.CancelKitDraftBuild("app"); err != nil || b.Error != "cancelled" {
		t.Fatalf("cancel = %+v %v", b, err)
	}
	want := []string{
		`POST /admin/kits/app/draft/build ct=application/json if="t"`,
		`GET /admin/kits/app/draft/build?since=3 ct= if=`,
		`DELETE /admin/kits/app/draft/build ct= if=`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(got, "\n"))
	}
}
```

- [ ] **Step 6: Run it to verify it fails**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminclient/ -run KitDraftBuildClient -v`
Expected: FAIL to compile: `c.StartKitDraftBuild undefined`.

- [ ] **Step 7: Implement the client**

Append to `internal/jam/adminclient/kitdraft.go`, adding `strconv` to its imports:

```go
func draftBuildPath(name string) string { return draftPath(name) + "/build" }

// StartKitDraftBuild starts a test build of kit name's draft at etag (202).
func (c *Client) StartKitDraftBuild(name, etag string) (jam.KitDraftBuild, error) {
	var b jam.KitDraftBuild
	_, err := c.sendHeaders(c.draftClient(0), "POST", draftBuildPath(name), "application/json", []byte("{}"), ifMatch(etag), &b)
	return b, err
}

// KitDraftBuildStatus is kit name's latest draft build, its log from offset since.
func (c *Client) KitDraftBuildStatus(name string, since int64) (jam.KitDraftBuild, error) {
	var b jam.KitDraftBuild
	_, err := c.sendHeaders(c.draftClient(0), "GET", draftBuildPath(name)+"?since="+strconv.FormatInt(since, 10), "", nil, nil, &b)
	return b, err
}

// CancelKitDraftBuild stops kit name's running draft build and answers it
// once stopped (a docker build may take seconds to stop).
func (c *Client) CancelKitDraftBuild(name string) (jam.KitDraftBuild, error) {
	var b jam.KitDraftBuild
	_, err := c.sendHeaders(c.draftClient(time.Minute), "DELETE", draftBuildPath(name), "", nil, nil, &b)
	return b, err
}
```

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminclient/ -v`
Expected: PASS.

- [ ] **Step 8: Write the failing shutdown test**

Create `cmd/at-jam/serve_shutdown_test.go`:

```go
package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// blockingDraftBuilder's build waits for its context.
type blockingDraftBuilder struct{ started chan struct{} }

func (b blockingDraftBuilder) BuildKitDraft(ctx context.Context, _ jam.KitDefinition, _ string, _ io.Writer) (string, error) {
	close(b.started)
	<-ctx.Done()
	return "", ctx.Err()
}
func (blockingDraftBuilder) HasKitDraftImage(string) (bool, error) { return false, nil }

// Review Focus 2: SIGTERM cancels a running build, serve waits for it, then
// exits 143.
func TestShutdownCancelsDraftBuilds(t *testing.T) {
	store := jam.NewMemStore()
	if _, err := store.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	d, err := jam.OpenKitDraft(store, "app", 0, "op", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	bb := blockingDraftBuilder{started: make(chan struct{})}
	log := slog.New(slog.DiscardHandler)
	builds := jam.NewKitDraftBuilds(ctx, store, bb, log)
	if _, err := builds.Start("app", jam.DraftETag(d.Tree), "op"); err != nil {
		t.Fatal(err)
	}
	<-bb.started
	sigs := make(chan os.Signal, 1)
	exited := make(chan int, 1)
	go stopBuildsOnSignal(sigs, stop, builds, log, func(code int) { exited <- code })
	sigs <- syscall.SIGTERM
	select {
	case code := <-exited:
		if code != 143 {
			t.Fatalf("exit %d, want 143", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit")
	}
	if v, err := builds.Status("app", 0); err != nil || v.State != jam.DraftBuildFailed || v.Error != "cancelled: Jam is shutting down" {
		t.Fatalf("status = %+v %v", v, err)
	}
}

func TestShutdownWithoutBuildsExits(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	stopped := make(chan struct{})
	exited := make(chan int, 1)
	go stopBuildsOnSignal(sigs, func() { close(stopped) }, nil, slog.New(slog.DiscardHandler), func(code int) { exited <- code })
	sigs <- os.Interrupt
	if code := <-exited; code != 130 {
		t.Fatalf("exit %d, want 130", code)
	}
	<-stopped
}
```

Run: `GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/ -run Shutdown -v`
Expected: FAIL to compile: `undefined: stopBuildsOnSignal`.

- [ ] **Step 9: Implement the shutdown and the wiring**

Create `cmd/at-jam/serve_shutdown.go`:

```go
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// buildShutdownWait bounds how long serve waits for cancelled kit draft
// builds to stop before it exits.
const buildShutdownWait = 15 * time.Second

// shutdownSignals delivers SIGINT and SIGTERM, which serve now handles.
func shutdownSignals() <-chan os.Signal {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	return c
}

// stopBuildsOnSignal waits for a shutdown signal, then cancels the kit draft
// builds (each docker child is interrupted). It waits up to
// buildShutdownWait for them to stop and exits 128+signal, the code an
// unhandled SIGINT/SIGTERM gave before serve handled them. builds may be
// nil (no launcher).
func stopBuildsOnSignal(sigs <-chan os.Signal, stop context.CancelFunc, builds *jam.KitDraftBuilds, log *slog.Logger, exit func(int)) {
	sig := <-sigs
	stop()
	if builds != nil && !builds.Wait(buildShutdownWait) {
		log.Warn("kit draft builds still running at shutdown", "waited", buildShutdownWait.String())
	}
	code := 1
	if s, ok := sig.(syscall.Signal); ok {
		code = 128 + int(s)
	}
	exit(code)
}
```

In `cmd/at-jam/main.go` `cmdServe`, right after `sup.SetPreviewsEnabled(previewURL != nil)`, add:

```go
	// Kit draft test builds, and the images draft studios run, build under
	// buildCtx; SIGINT/SIGTERM cancels them before serve exits.
	buildCtx, stopBuilds := context.WithCancel(context.Background())
	var draftBuilds *jam.KitDraftBuilds
	if b, ok := lch.(jam.KitDraftBuilder); ok {
		draftBuilds = jam.NewKitDraftBuilds(buildCtx, st, b, log)
		sup.SetKitDraftBuilds(draftBuilds)
	}
	go stopBuildsOnSignal(shutdownSignals(), stopBuilds, draftBuilds, log, os.Exit)
```

In the `jam.NewAdminHandler(…)` call, add `jam.WithKitDraftBuilds(draftBuilds),` after `jam.WithSetupInspect(setupDeps, log)` (turn that line's `)` into `,` and close the call after the new option).

- [ ] **Step 10: Document the test build**

In `docs/usage/jam/kits-drafts.md`, insert this section between "## Publish" and "## The `kit draft` verbs". Write each <link> as a real link.

```markdown
## Test build

A **test build** builds the draft's image the way a raise does: the parent
chain (its stages reused as usual), the harness, the sealed hardening layer
and the build args, with the draft's files as the kit's own stage. Nothing
is published. The image is tagged `cove-kit-draft:<kit>-<tree[:12]>` (its own
stage `…-stage`), never a version's image or stage tag.

- It builds the draft at the ETag you send (`If-Match`), under the default
  harness. A draft with problems is refused (400
  `draft <kit> has problems: …`) before anything builds.
- One build per kit at a time. Another start answers 409
  `kit <kit> already has a draft build running`.
- The log is docker's output plus `== …` lines. Jam keeps its last 1 MiB in
  memory only; a restart loses it, and the state is then `unknown`. It holds
  whatever a Dockerfile prints, so clients show it escaped.
- Editing the draft does not stop a build: it finishes on the tree it
  started from. `current` says whether that tree is still the draft's, and
  `draft_etag` is the draft's ETag now.
- Stopping Jam (SIGINT or SIGTERM) cancels running builds: `failed`,
  `cancelled: Jam is shutting down`. Draft images are not collected: remove
  old `cove-kit-draft` images with `docker image rm`.
```

Append these rows to the API table, after the publish row:

```markdown
| `POST /admin/kits/{name}/draft/build` | Start a test build (`If-Match`; `application/json`, else 415). 202 with the build: `id`, `tree`, `state`, `tag`. 409 when one is running; 400 for problems. |
| `GET /admin/kits/{name}/draft/build[?since=N]` | The latest build: `state` (`running`, `succeeded`, `failed`, `unknown`), `tree`, `tag`, `digest` (the image id), `error`, `started_at`, `finished_at`, `current`, `draft_etag`, and the `log` from offset N (`log_from`, `log_end` for the next `since`, `log_skipped` when bytes were dropped). |
| `DELETE /admin/kits/{name}/draft/build` | Cancel the running build; 200 with it once stopped (`failed`, `cancelled`), 404 `kit <name> has no draft build running`. |
```

In the audit paragraph at the end, add this sentence after the publish sentence: "A build logs `kit draft build requested` and `kit draft build cancelled` (the kit, the operator, the build id), and `kit draft build started` and `kit draft build finished` (the kit, the tree prefix, the build id, its state), never log content."

- [ ] **Step 11: Run everything touched**

Run: `GOPROXY=https://proxy.golang.org,direct go test -race ./internal/jam/... ./cmd/at-jam/ && go vet ./cmd/at-jam/ ./internal/jam/...`
Expected: PASS.

- [ ] **Step 12: Commit**

```bash
gofmt -l internal/jam cmd/at-jam
git add internal/jam cmd/at-jam docs/usage/jam/kits-drafts.md
git commit -m "feat(jam): draft test-build API, client, serve wiring and shutdown

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `at-jam kit draft build`

**Files:**
- Modify: `cmd/at-jam/kitdraft.go` (the `build` verb, `kitDraftBuild`, `printBuildLog`, `buildResult`, `draftBuildPoll`)
- Modify: `docs/usage/jam/kits-drafts.md` (the verbs block and its bullets)
- Test: `cmd/at-jam/kitdraft_build_test.go` (create)

**Interfaces:**
- Consumes: `adminclient.Client.StartKitDraftBuild`, `KitDraftBuildStatus`, `CancelKitDraftBuild` (Task 4); `jam.KitDraftBuild`, `jam.DraftBuild*`, `jam.NewKitDraftBuilds`, `jam.WithKitDraftBuilds`; `cmdKitDraft`'s `etagFor`, `treePrefix`, `textesc.Escape`.
- Produces:
```go
package main
var draftBuildPoll = time.Second
func kitDraftBuild(c *adminclient.Client, kit, name string, etagFor func() (string, error), noFollow, show, cancel bool, stdout, stderr io.Writer) int
// test helpers used by Task 7: cliDraftBuilder
```

- [ ] **Step 1: Write the failing tests**

Create `cmd/at-jam/kitdraft_build_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// cliDraftBuilder's build writes log, waits for release (or its ctx), and
// returns err or sha256:img.
type cliDraftBuilder struct {
	log     string
	err     error
	release chan struct{}
}

func (f *cliDraftBuilder) BuildKitDraft(ctx context.Context, _ jam.KitDefinition, _ string, log io.Writer) (string, error) {
	_, _ = io.WriteString(log, f.log)
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if f.err != nil {
		return "", f.err
	}
	return "sha256:img", nil
}
func (f *cliDraftBuilder) HasKitDraftImage(string) (bool, error) { return true, nil }

// draftBuildCLI is a Jam holding kit app v1 with an open draft, test builds
// over fb, and a runner for `at-jam kit draft build …` against it.
func draftBuildCLI(t *testing.T, fb *cliDraftBuilder) (func(args ...string) (int, string, string), jam.KitDraft) {
	t.Helper()
	old := draftBuildPoll
	draftBuildPoll = time.Millisecond
	t.Cleanup(func() { draftBuildPoll = old })
	store := jam.NewMemStore()
	if _, err := store.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	d, err := jam.OpenKitDraft(store, "app", 0, "op", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.DiscardHandler)
	builds := jam.NewKitDraftBuilds(ctx, store, fb, log)
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, jam.WithKitDraftBuilds(builds)))
	t.Cleanup(ts.Close)
	return func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		argv := append([]string{"kit", "draft", "build", "--admin-url", ts.URL}, args...)
		code := run(argv, func(string) string { return "" }, &out, &errb)
		return code, out.String(), errb.String()
	}, d
}

// Review Focus 5: the log is followed to the end and printed escaped.
func TestKitDraftBuildFollowsToSuccess(t *testing.T) {
	kit, d := draftBuildCLI(t, &cliDraftBuilder{log: "#1 step\n#2 \x1b[31mred\x1b[0m\n"})
	code, out, errs := kit("app")
	tree := d.Tree[:12]
	for _, want := range []string{
		"building draft app @" + tree + " (build kdb_",
		"#1 step\n",
		`#2 \x1b[31mred\x1b[0m` + "\n",
		"built draft app @" + tree + ": cove-kit-draft:app-" + tree + " (digest sha256:img)\n",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("exit %d lacks %q:\n%s\nstderr: %s", code, want, out, errs)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Fatal("a raw escape reached the terminal")
	}
}

func TestKitDraftBuildFailureExits1(t *testing.T) {
	kit, d := draftBuildCLI(t, &cliDraftBuilder{err: errors.New("RUN make: exit 2")})
	code, _, errs := kit("app")
	if code != 1 || !strings.Contains(errs, "at-jam: draft build of app @"+d.Tree[:12]+" failed: RUN make: exit 2\n") {
		t.Fatalf("exit %d stderr %q", code, errs)
	}
}

func TestKitDraftBuildNoFollowShowCancel(t *testing.T) {
	fb := &cliDraftBuilder{release: make(chan struct{})}
	kit, _ := draftBuildCLI(t, fb)
	if code, out, _ := kit("app", "--no-follow"); code != 0 || !strings.HasPrefix(out, "building draft app @") {
		t.Fatalf("--no-follow: %d %q", code, out)
	}
	if code, _, errs := kit("app", "--no-follow"); code != 1 || !strings.Contains(errs, "kit app already has a draft build running") {
		t.Fatalf("second start: %d %q", code, errs)
	}
	if code, out, _ := kit("app", "--show"); code != 0 || !strings.Contains(out, "running") {
		t.Fatalf("--show while running: %d %q", code, out)
	}
	if code, out, _ := kit("app", "--cancel"); code != 0 || !strings.HasPrefix(out, "cancelled draft build kdb_") {
		t.Fatalf("--cancel: %d %q", code, out)
	}
	if code, _, errs := kit("app", "--show"); code != 1 || !strings.Contains(errs, "failed: cancelled") {
		t.Fatalf("--show after cancel: %d %q", code, errs)
	}
	if code, _, errs := kit("app", "--etag", strings.Repeat("0", 64)); code != 1 || !strings.Contains(errs, "draft app changed since you loaded it (reload)") {
		t.Fatalf("stale --etag: %d %q", code, errs)
	}
	if code, _, errs := kit("app", "--show", "--cancel"); code != 2 || !strings.Contains(errs, "--show and --cancel cannot be combined") {
		t.Fatalf("--show --cancel: %d %q", code, errs)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/ -run KitDraftBuild -v`
Expected: FAIL to compile: `undefined: draftBuildPoll`.

- [ ] **Step 3: Implement the verb**

In `cmd/at-jam/kitdraft.go`:
- Add to `kitDraftVerbs`: `"build": {"<kit> [--etag e] [--no-follow] [--show] [--cancel]", 1},`.
- Change `kitDraftUsage` to `"at-jam kit draft: expected open|status|get|put|rm|discard|publish|sync|build"`.
- Change the flag declaration line to also declare `noFollow, show, cancelBuild := new(bool), new(bool), new(bool)`, and add to the verb switch:

```go
	case "build":
		noFollow = fs.Bool("no-follow", false, "start the build and return without following its log")
		show = fs.Bool("show", false, "print the latest build and its log; start nothing")
		cancelBuild = fs.Bool("cancel", false, "stop the running build")
```

- Add `|| verb == "build"` to the condition that declares `--etag`.
- Add to the `switch verb` that runs the verbs:

```go
	case "build":
		return kitDraftBuild(c, kit, name, etagFor, *noFollow, *show, *cancelBuild, stdout, stderr)
```

Append:

```go
// draftBuildPoll is how often `kit draft build` polls its build's log.
var draftBuildPoll = time.Second

// kitDraftBuild is `kit draft build`. It starts a test build of the draft
// (at its etag) and follows its log until the build ends: exit 0 on
// success (with the tag and digest), 1 on failure. --no-follow returns
// once the build started; --show prints the latest build without starting
// one; --cancel stops it. The log and every name are printed escaped.
func kitDraftBuild(c *adminclient.Client, kit, name string, etagFor func() (string, error), noFollow, show, cancel bool, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, "at-jam:", textesc.Escape(err.Error(), false))
		return 1
	}
	switch {
	case show && cancel:
		fmt.Fprintln(stderr, "at-jam kit draft build: --show and --cancel cannot be combined")
		return 2
	case cancel:
		b, err := c.CancelKitDraftBuild(kit)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "cancelled draft build %s of %s @%s\n", textesc.Escape(b.ID, false), name, textesc.Escape(treePrefix(b.Tree), false))
		return 0
	case show:
		b, err := c.KitDraftBuildStatus(kit, 0)
		if err != nil {
			return fail(err)
		}
		printBuildLog(stdout, b, 0)
		return buildResult(stdout, stderr, name, b)
	}
	tag, err := etagFor()
	if err != nil {
		return fail(err)
	}
	b, err := c.StartKitDraftBuild(kit, tag)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "building draft %s @%s (build %s)\n", name, textesc.Escape(treePrefix(b.Tree), false), textesc.Escape(b.ID, false))
	if noFollow {
		return 0
	}
	id, since := b.ID, int64(0)
	for {
		if b, err = c.KitDraftBuildStatus(kit, since); err != nil {
			return fail(err)
		}
		if b.ID != id {
			fmt.Fprintf(stderr, "at-jam: build %s of draft %s was replaced by build %s\n", textesc.Escape(id, false), name, textesc.Escape(b.ID, false))
			return 1
		}
		printBuildLog(stdout, b, since)
		since = b.LogEnd
		if b.State != jam.DraftBuildRunning {
			return buildResult(stdout, stderr, name, b)
		}
		time.Sleep(draftBuildPoll)
	}
}

// printBuildLog prints a build's log chunk escaped (newlines and tabs
// kept), noting the bytes before it that the capped log dropped.
func printBuildLog(w io.Writer, b jam.KitDraftBuild, since int64) {
	if b.LogSkipped {
		fmt.Fprintf(w, "[… %d bytes of log dropped: Jam keeps the last 1 MiB]\n", b.LogFrom-since)
	}
	_, _ = io.WriteString(w, textesc.Escape(b.Log, true))
}

// buildResult prints a build's outcome: 0 unless it failed.
func buildResult(stdout, stderr io.Writer, name string, b jam.KitDraftBuild) int {
	tree := textesc.Escape(treePrefix(b.Tree), false)
	switch b.State {
	case jam.DraftBuildSucceeded:
		fmt.Fprintf(stdout, "built draft %s @%s: %s (digest %s)\n", name, tree, textesc.Escape(b.Tag, false), textesc.Escape(b.Digest, false))
		if !b.Current {
			fmt.Fprintf(stdout, "note: the draft changed since this build (etag now %s)\n", textesc.Escape(b.DraftETag, false))
		}
		return 0
	case jam.DraftBuildFailed:
		fmt.Fprintf(stderr, "at-jam: draft build of %s @%s failed: %s\n", name, tree, textesc.Escape(b.Error, false))
		return 1
	case jam.DraftBuildRunning:
		fmt.Fprintf(stdout, "draft %s @%s: build %s running\n", name, tree, textesc.Escape(b.ID, false))
		return 0
	}
	fmt.Fprintf(stdout, "no draft build of %s since Jam started\n", name)
	return 0
}
```

- [ ] **Step 4: Run the CLI tests**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/ -run 'KitDraft' -v`
Expected: PASS, the slice-3 `TestKitDraftArgs` included (its usage check still finds `…|sync`).

- [ ] **Step 5: Document the verb**

In `docs/usage/jam/kits-drafts.md`, "## The `kit draft` verbs": add this line to the code block, after `publish`:

```
at-jam kit draft build app [--no-follow]               # test-build the draft, following its log → built draft app @3f2a… (digest sha256:…)
```

Change "Each write first fetches the status" to "Each write (and `build`) first fetches the status". After the `publish --pin` bullet, add:

```markdown
- `build` prints the build's log as it comes, escaped, and exits 0 on
  success (with the tag and the digest) or 1 on failure. `--no-follow`
  returns once the build started; `--show` prints the latest build and its
  log without starting one; `--cancel` stops it. See
  <link `#test-build` as "Test build">.
```

- [ ] **Step 6: Commit**

```bash
gofmt -l cmd/at-jam
git add cmd/at-jam docs/usage/jam/kits-drafts.md
git commit -m "feat(at-jam): kit draft build — start, follow, show and cancel a test build

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: The draft studio raise, its GC root and its context

**Files:**
- Modify: `internal/jam/supervisor.go` (`RaiseSpec.KitDraft`; `Raise`; `raiseDraft`, `draftRaise`, `keepDraftTree`; `compileContext`'s kit fact; `ContextForActor`)
- Modify: `internal/jam/kit_draft_build.go` (`Ensure`, `DraftKitDefinition`)
- Modify: `internal/jam/instance.go` (`Instance.KitDraft`)
- Modify: `internal/jam/kit_objects.go` (`deadKitObjects` roots)
- Modify: `internal/jam/kitchain.go` (`recordedChainKits`, `draftStudioKit`)
- Modify: `internal/jam/setup_record.go` (`instanceKit`)
- Modify: `internal/jam/admin.go` (`imageStatus` returns `draft`)
- Modify: `internal/jam/supervisor_test.go` (`fakeLauncher.Raise` skips not-ready for a draft image)
- Modify: `internal/jam/storetest/kits_draft.go` (conformance)
- Test: `internal/jam/kit_draft_studio_test.go` (create)

**Interfaces:**
- Consumes: `KitDraftBuilds`, `startLocked`, `errDraftBuildRunning`, `kitDraftDefinitionLocked`, `KitDraftTag`, `KitDraftImage`, `KitDraftRef`, `fakeDraftBuilder` (Task 2); `RaiseSpec.DraftImage`, the `draftBuilds` field and `SetKitDraftBuilds` (Tasks 3–4); `kitDraftBuildsOff` (Task 4); `draftOf`, `storedDraftTree`, `PutKitDocs`, `chainRaiseKit`, `mustEnsure`, `CoveSummaries`, `DiscardKitDraft`, `PublishKitDraft`.
- Produces:
```go
package jam
// RaiseSpec gains: KitDraft string
// Instance gains:  KitDraft *KitDraftRef `json:"kit_draft,omitempty"`
func (b *KitDraftBuilds) Ensure(ctx context.Context, def KitDefinition, ref KitDraftRef, by string) (KitDraftImage, error)
func DraftKitDefinition(store Store, name string, h harnessinstall.Install) (KitDefinition, KitDraftRef, KitObjects, error)
func draftStudioKit(store KitReader, inst Instance) (studio.StudioKit, error)
func recordedChainKits(store KitReader, chain []KitRef) ([]studio.StudioKit, error)
// test helpers used by Tasks 7 and 8: draftStudioSetup, raiseDraftStudio
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/kit_draft_studio_test.go`:

```go
package jam

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/studio"
)

// draftStudioSetup is chainRaiseKit's my-app → web-dev → default, with
// my-app's draft open and its kit.yml edited (prompt "draft prompt"), and
// the supervisor's test builds over fb. It returns the draft's save.
func draftStudioSetup(t *testing.T, fb *fakeDraftBuilder) (*Supervisor, Store, *fakeLauncher, KitDraftSave) {
	t.Helper()
	sup, store, fl := chainRaiseKit(t)
	d, err := OpenKitDraft(store, "my-app", 0, "op", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	yml := "kind: studio\nbase: {kit: web-dev}\nprompt: draft prompt\n"
	s, err := PutKitDraftFile(store, slog.New(slog.DiscardHandler), "my-app", "kit.yml", 0, []byte(yml), DraftETag(d.Tree), "op", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sup.SetKitDraftBuilds(NewKitDraftBuilds(ctx, store, fb, slog.New(slog.DiscardHandler)))
	return sup, store, fl, s
}

func raiseDraftStudio(t *testing.T, sup *Supervisor, id string) (Instance, error) {
	t.Helper()
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Project: "default", Role: "dev", Name: id, KitDraft: "my-app"})
	return inst, err
}

func TestDraftStudioRaise(t *testing.T) {
	fb := &fakeDraftBuilder{}
	sup, store, fl, s := draftStudioSetup(t, fb)
	inst, err := raiseDraftStudio(t, sup, "d1")
	if err != nil {
		t.Fatal(err)
	}
	tag, _ := KitDraftTag("my-app", s.Tree)
	if inst.KitDraft == nil || *inst.KitDraft != (KitDraftRef{Kit: "my-app", Tree: s.Tree}) || inst.ImageTag != tag || inst.Kit.ID != "my-app" || inst.Kit.Version != 0 {
		t.Fatalf("instance = %+v (draft %+v)", inst, inst.KitDraft)
	}
	if len(inst.KitChain) != 3 || inst.KitChain[0] != (KitRef{ID: "default", Version: 1}) || inst.KitChain[2] != (KitRef{ID: "my-app"}) {
		t.Fatalf("chain = %+v", inst.KitChain)
	}
	if fl.gotSpec.DraftImage == nil || *fl.gotSpec.DraftImage != (KitDraftImage{Tag: tag, ID: "sha256:img"}) || fl.prepareCalls != 0 {
		t.Fatalf("launcher got %+v, prepared %d", fl.gotSpec.DraftImage, fl.prepareCalls)
	}
	def := fb.defs[0]
	if def.Chain[2].Kit.Prompt != "draft prompt" || !reflect.DeepEqual(def.Harness, harnessFor(fl.gotSpec)) || def.Ref != inst.Kit {
		t.Fatalf("built %+v under %+v", def.Ref, def.Harness)
	}
	c := fl.gotSpec.Context
	if !strings.Contains(c.Core, "root prompt\n\ndraft prompt") {
		t.Fatalf("the raise context must carry the draft's prompt:\n%s", c.Core)
	}
	again, err := sup.ContextFor("d1")
	if err != nil || again.Fingerprint != c.Fingerprint || !strings.Contains(again.Core, "draft prompt") {
		t.Fatalf("refresh = %v (fingerprint %s, want %s)", err, again.Fingerprint, c.Fingerprint)
	}
	if got := CoveSummaries(store, sup); len(got) != 1 || got[0].Image != "draft" {
		t.Fatalf("summaries = %+v", got)
	}
}

// A push or publish of the kit never stales or moves a draft studio.
func TestDraftStudioNeverStale(t *testing.T) {
	sup, store, _, _ := draftStudioSetup(t, &fakeDraftBuilder{})
	inst, err := raiseDraftStudio(t, sup, "d1")
	if err != nil {
		t.Fatal(err)
	}
	pushed := studio.StudioKit{Kind: studio.Kind, Prompt: "v3"}
	pushed.Base.Kit = "web-dev"
	mustEnsure(t, store, "my-app", pushed)
	if got := CoveSummaries(store, sup)[0].Image; got != "draft" {
		t.Fatalf("image = %s after a push", got)
	}
	cur, _ := store.GetInstance("d1")
	if !reflect.DeepEqual(cur.KitDraft, inst.KitDraft) || cur.ImageTag != inst.ImageTag || cur.Kit != inst.Kit {
		t.Fatalf("the instance moved: %+v", cur)
	}
	if b, err := sup.ContextFor("d1"); err != nil || strings.Contains(b.Core, "v3") || !strings.Contains(b.Core, "draft prompt") {
		t.Fatalf("a refresh must keep the draft's prompt: %v\n%s", err, b.Core)
	}
}

func TestDraftStudioReusesItsBuild(t *testing.T) {
	fb := &fakeDraftBuilder{}
	fb.setPresent("sha256:img", true)
	sup, _, _, _ := draftStudioSetup(t, fb)
	for _, id := range []string{"d1", "d2"} {
		if _, err := raiseDraftStudio(t, sup, id); err != nil {
			t.Fatal(err)
		}
	}
	if fb.builds() != 1 {
		t.Fatalf("builds = %d, want the second raise to reuse the first build", fb.builds())
	}
	fb.setPresent("sha256:img", false) // the image was removed
	if _, err := raiseDraftStudio(t, sup, "d3"); err != nil {
		t.Fatal(err)
	}
	if fb.builds() != 2 {
		t.Fatalf("builds = %d, want a rebuild once the image is gone", fb.builds())
	}
}

func TestDraftStudioRefusals(t *testing.T) {
	fb := &fakeDraftBuilder{err: errors.New("RUN make: exit 2")}
	sup, store, _, s := draftStudioSetup(t, fb)
	_, err := raiseDraftStudio(t, sup, "d1")
	if err == nil || !strings.Contains(err.Error(), "draft my-app: the test build failed (see at-jam kit draft build my-app --show)") || WriteStatus(err, 500) != 400 {
		t.Fatalf("failed build: %v", err)
	}
	if _, ok := store.GetInstance("d1"); ok {
		t.Fatal("a failed raise recorded an instance")
	}
	for _, a := range store.ListActors() {
		if a.ID == "d1" {
			t.Fatal("a failed raise left its identity")
		}
	}
	ctx := context.Background()
	_, _, _, err = sup.Raise(ctx, RaiseSpec{ActorID: "d2", Project: "default", Role: "dev", Name: "d2", KitDraft: "web-dev"})
	if err == nil || !strings.Contains(err.Error(), "kit web-dev has no draft") || WriteStatus(err, 500) != 404 {
		t.Fatalf("no draft: %v", err)
	}
	_, _, _, err = sup.Raise(ctx, RaiseSpec{ActorID: "d3", Project: "default", Role: "dev", Name: "d3", KitDraft: "a\x1b[31m"})
	if err == nil || !strings.Contains(err.Error(), unsafeKitNameText) || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("unsafe name: %q", err)
	}
	if _, err := PutKitDraftFile(store, slog.New(slog.DiscardHandler), "my-app", "kit.yml", 0, []byte("kind: nope\n"), s.ETag, "op", draftNow); err != nil {
		t.Fatal(err)
	}
	_, err = raiseDraftStudio(t, sup, "d4")
	if err == nil || !strings.Contains(err.Error(), "draft my-app has problems: ") {
		t.Fatalf("problems: %v", err)
	}
	bare, bareStore, _ := chainRaiseKit(t)
	if _, err := OpenKitDraft(bareStore, "my-app", 0, "op", draftNow); err != nil {
		t.Fatal(err)
	}
	_, err = raiseDraftStudio(t, bare, "d5")
	if err == nil || !strings.Contains(err.Error(), kitDraftBuildsOff) || WriteStatus(err, 500) != 503 {
		t.Fatalf("no builds wired: %v", err)
	}
}

// A test build of an older tree is running: a raise of the current tree
// does not wait on it, it is refused.
func TestDraftStudioRefusesWhileAnotherTreeBuilds(t *testing.T) {
	fb := &fakeDraftBuilder{release: make(chan struct{})}
	sup, store, _, s := draftStudioSetup(t, fb)
	if _, err := sup.draftBuilds.Start("my-app", s.ETag, "op"); err != nil {
		t.Fatal(err)
	}
	if _, err := PutKitDraftFile(store, slog.New(slog.DiscardHandler), "my-app", "notes.txt", 0, []byte("x"), s.ETag, "op", draftNow); err != nil {
		t.Fatal(err)
	}
	_, err := raiseDraftStudio(t, sup, "d1")
	if err == nil || !strings.Contains(err.Error(), "kit my-app already has a draft build running") || WriteStatus(err, 500) != 409 {
		t.Fatalf("err = %v", err)
	}
	close(fb.release)
}

// Review Focus 4: discarding or publishing the draft leaves a running draft
// studio, its image status, and its tree as they were.
func TestDraftStudioKeepsItsTreeLive(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	t.Run("discard", func(t *testing.T) {
		sup, store, _, s := draftStudioSetup(t, &fakeDraftBuilder{})
		inst, err := raiseDraftStudio(t, sup, "d1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DiscardKitDraft(store, log, "my-app", s.ETag); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.KitTree(s.Tree); !ok {
			t.Fatal("GC took a running draft studio's tree")
		}
		if sk, ok := instanceKit(store, inst); !ok || !strings.Contains(sk.Prompt, "draft prompt") {
			t.Fatalf("instanceKit = %+v %v", sk, ok)
		}
		if b, err := sup.ContextFor("d1"); err != nil || !strings.Contains(b.Core, "draft prompt") {
			t.Fatalf("context = %v", err)
		}
		if got := CoveSummaries(store, sup)[0].Image; got != "draft" {
			t.Fatalf("image = %s", got)
		}
		if err := sup.Teardown(context.Background(), "d1"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GCKitObjects(); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.KitTree(s.Tree); ok {
			t.Fatal("a torn-down draft studio's tree outlived it")
		}
	})
	t.Run("publish", func(t *testing.T) {
		sup, store, _, s := draftStudioSetup(t, &fakeDraftBuilder{})
		inst, err := raiseDraftStudio(t, sup, "d1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PublishKitDraft(store, log, "my-app", false, s.ETag); err != nil {
			t.Fatal(err)
		}
		cur, _ := store.GetInstance("d1")
		if !reflect.DeepEqual(cur.KitDraft, inst.KitDraft) || CoveSummaries(store, sup)[0].Image != "draft" {
			t.Fatalf("a publish moved the draft studio: %+v", cur)
		}
	})
}

// Review Focus 4: the draft is discarded (and GC'd) while the raise's own
// build runs; the raise still records the studio and re-stores its tree.
func TestDraftStudioTreeReStoredAfterARacingDiscard(t *testing.T) {
	fb := &fakeDraftBuilder{}
	sup, store, _, s := draftStudioSetup(t, fb)
	fb.during = func() {
		if _, err := DiscardKitDraft(store, slog.New(slog.DiscardHandler), "my-app", s.ETag); err != nil {
			t.Error(err)
		}
	}
	inst, err := raiseDraftStudio(t, sup, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.KitTree(s.Tree); !ok {
		t.Fatal("the studio's tree was not re-stored")
	}
	if sk, ok := instanceKit(store, inst); !ok || !strings.Contains(sk.Prompt, "draft prompt") {
		t.Fatalf("instanceKit = %+v %v", sk, ok)
	}
	if gc, err := store.GCKitObjects(); err != nil || gc.Trees != 0 {
		t.Fatalf("gc = %+v %v (the instance keeps its tree)", gc, err)
	}
}
```

In `internal/jam/supervisor_test.go` `fakeLauncher.Raise`, change `if f.notReadyOnce && f.prepareCalls == 0 {` to `if f.notReadyOnce && f.prepareCalls == 0 && spec.DraftImage == nil {`, matching the real launcher, which never asks its inventory about a draft image.

Append to `runKitDraftConformance` in `internal/jam/storetest/kits_draft.go`, before its closing brace:

```go
	t.Run("GC keeps a draft studio's tree until the studio is gone", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t, "kit.yml", "kind: studio\nprompt: a studio\n")
		if err := st.PutKitDraft(draftOn(tr.Hash()), objs); err != nil {
			t.Fatal(err)
		}
		inst := jam.Instance{ActorID: "cove-d1", Phase: jam.PhaseLive, KitDraft: &jam.KitDraftRef{Kit: "app", Tree: tr.Hash()}}
		if err := st.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
		if err := st.RemoveKitDraft("app"); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{}) {
			t.Fatalf("gc = %+v %v (a live draft studio references its tree)", gc, err)
		}
		if got, ok := st.GetInstance("cove-d1"); !ok || got.KitDraft == nil || *got.KitDraft != *inst.KitDraft {
			t.Fatalf("instance = %+v %v (kit_draft must round-trip)", got, ok)
		}
		inst.Phase = jam.PhaseGone
		if err := st.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{Trees: 1, Blobs: 1}) {
			t.Fatalf("gc once the studio is gone = %+v %v", gc, err)
		}
	})
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'DraftStudio|Conformance' -v`
Expected: FAIL to compile: `unknown field KitDraft in struct literal of type RaiseSpec`, `unknown field KitDraft in struct literal of type Instance`.

- [ ] **Step 3: Add the fields, the GC root and `imageStatus`**

- `internal/jam/supervisor.go`, in `RaiseSpec` after `DraftImage`:

```go
	// KitDraft names a kit whose draft the studio runs instead of the role's
	// kit: an admin manual raise only (docs/usage/jam/kits-drafts.md#draft-studio).
	// No other caller sets it.
	KitDraft string
```

- `internal/jam/instance.go`, in `Instance` after `ImageTag`:

```go
	KitDraft           *KitDraftRef  `json:"kit_draft,omitempty"`           // a draft studio: the kit and draft tree it runs (Kit is then that kit at v0); its tree stays GC-live while the instance exists
```

- `internal/jam/kit_objects.go` `deadKitObjects`, after the `m.kitDrafts` loop:

```go
	for _, i := range m.instances {
		if i.KitDraft != nil && i.Phase != PhaseGone {
			live[i.KitDraft.Tree] = true // a draft studio runs its tree as of its raise
		}
	}
```

and update its doc comment to "the trees no kit version, draft or draft studio references".

- `internal/jam/admin.go` `imageStatus`, right after the phase `switch`:

```go
	if i.KitDraft != nil {
		return "draft" // a draft studio runs its draft's build: never stale on the kit's versions
	}
```

- [ ] **Step 4: Add `Ensure` and `DraftKitDefinition`**

Append to `internal/jam/kit_draft_build.go`:

```go
// DraftKitDefinition is what a draft studio of kit name runs. It is the
// draft's tree as of now, read and copied under kitMu, built as a test
// build would be (problems refuse, 400), under the raising role's harness
// h. It returns the draft tree's objects too.
func DraftKitDefinition(store Store, name string, h harnessinstall.Install) (KitDefinition, KitDraftRef, KitObjects, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	d, err := draftOf(store, name)
	if err != nil {
		return KitDefinition{}, KitDraftRef{}, KitObjects{}, err
	}
	tree, err := storedDraftTree(store, d)
	if err != nil {
		return KitDefinition{}, KitDraftRef{}, KitObjects{}, err
	}
	def, objs, err := kitDraftDefinitionLocked(store, name, tree, h)
	return def, KitDraftRef{Kit: name, Tree: d.Tree}, objs, err
}

// Ensure is a draft studio's image: def (kit ref.Kit's draft tree ref.Tree
// under the raising role's harness), built through the same jobs as a test
// build, for by.
//   - The latest build of exactly that definition is reused while its image
//     is still present.
//   - A build of it already running is waited for.
//   - Otherwise a new build starts and is waited for, until ctx is done. The
//     build runs on regardless, for a later raise to reuse.
//
// A running build of another tree or harness is 409; a failed build is 400.
func (b *KitDraftBuilds) Ensure(ctx context.Context, def KitDefinition, ref KitDraftRef, by string) (KitDraftImage, error) {
	tag, err := KitDraftTag(ref.Kit, ref.Tree)
	if err != nil {
		return KitDraftImage{}, err
	}
	same := func(j *draftBuildJob) bool { return j != nil && j.tree == ref.Tree && j.buildDigest == def.Ref.Digest }
	b.mu.Lock()
	j := b.jobs[ref.Kit]
	reuse, id := same(j) && j.state == DraftBuildSucceeded, ""
	if reuse {
		id = j.imageID
	}
	b.mu.Unlock()
	if reuse {
		if ok, err := b.builder.HasKitDraftImage(id); err == nil && ok {
			return KitDraftImage{Tag: tag, ID: id}, nil
		}
	}
	b.mu.Lock()
	j = b.jobs[ref.Kit]
	switch {
	case j != nil && j.state == DraftBuildRunning && !same(j):
		b.mu.Unlock()
		return KitDraftImage{}, errDraftBuildRunning(ref.Kit)
	case j == nil || j.state != DraftBuildRunning:
		j = b.startLocked(def, ref.Tree, tag, by)
	}
	b.mu.Unlock()
	select {
	case <-j.done:
	case <-ctx.Done():
		return KitDraftImage{}, ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if j.state != DraftBuildSucceeded {
		return KitDraftImage{}, writeErr(http.StatusBadRequest, "draft %s: the test build failed (see at-jam kit draft build %s --show)", ref.Kit, ref.Kit)
	}
	return KitDraftImage{Tag: j.tag, ID: j.imageID}, nil
}
```

- [ ] **Step 5: Read a draft studio's kit**

In `internal/jam/kitchain.go`, replace `recordedChainKit` with:

```go
// recordedChainKit is the merged kit of a recorded image chain, each link read
// at its exact (immutable) version. A link no longer in the registry fails.
func recordedChainKit(store KitReader, chain []KitRef) (studio.StudioKit, error) {
	kits, err := recordedChainKits(store, chain)
	if err != nil {
		return studio.StudioKit{}, err
	}
	return studio.Merge(kits), nil
}

// recordedChainKits is each link of a recorded image chain, read at its
// exact version, root first.
func recordedChainKits(store KitReader, chain []KitRef) ([]studio.StudioKit, error) {
	kits := make([]studio.StudioKit, 0, len(chain))
	for _, r := range chain {
		v, ok := LoadKitVersion(store, r.ID, r.Version)
		if !ok {
			return nil, fmt.Errorf("kit %s (in the image's chain) is missing", r)
		}
		if v.Err != nil {
			return nil, fmt.Errorf("kit %s (in the image's chain) is invalid: %w", r, v.Err)
		}
		kits = append(kits, v.Kit)
	}
	return kits, nil
}

// draftStudioKit is the merged kit a draft studio was raised from: its
// draft tree as of the raise (stored while the studio exists), on top of
// its recorded parents at their exact versions.
func draftStudioKit(store KitReader, inst Instance) (studio.StudioKit, error) {
	d := inst.KitDraft
	t, ok := store.KitTree(d.Tree)
	if !ok {
		return studio.StudioKit{}, fmt.Errorf("kit %s draft tree %.12s is not stored", d.Kit, d.Tree)
	}
	self, err := studio.ParseTree(t, store.KitBlob)
	if err != nil {
		return studio.StudioKit{}, fmt.Errorf("kit %s draft @%.12s: %w", d.Kit, d.Tree, err)
	}
	if len(inst.KitChain) < 2 {
		return self, nil
	}
	parents, err := recordedChainKits(store, inst.KitChain[:len(inst.KitChain)-1])
	if err != nil {
		return studio.StudioKit{}, err
	}
	return studio.Merge(append(parents, self)), nil
}
```

In `internal/jam/setup_record.go` `instanceKit`, add at the top:

```go
	if inst.KitDraft != nil {
		sk, err := draftStudioKit(store, inst)
		return sk, err == nil
	}
```

- [ ] **Step 6: Raise a draft studio**

In `internal/jam/supervisor.go` `Raise`:

1. Replace the block `var def *KitDefinition` … `if roleOK { … }` (the role-kit resolution) with:

```go
	var def *KitDefinition
	var draft *draftRaise
	switch {
	case spec.KitDraft != "":
		dr, derr := s.raiseDraft(ctx, spec, role)
		if derr != nil {
			if rmErr := s.store.RemoveActor(spec.ActorID); rmErr != nil && s.log != nil {
				s.log.Warn("raise rollback: failed to revoke identity after draft resolve failure", "id", spec.ActorID, "error", rmErr)
			}
			return Instance{}, "", "", fmt.Errorf("raise: draft studio: %w", derr)
		}
		draft = &dr
		spec.Kit, spec.DraftImage, def = dr.def.Ref, &dr.image, &dr.def
	case roleOK:
		d, have, kerr := s.raiseKit(spec.Project, role)
		if kerr != nil {
			if rmErr := s.store.RemoveActor(spec.ActorID); rmErr != nil && s.log != nil {
				s.log.Warn("raise rollback: failed to revoke identity after kit resolve failure", "id", spec.ActorID, "error", rmErr)
			}
			return Instance{}, "", "", fmt.Errorf("raise: resolve kit for role %s/%s: %w", orDefaultProject(spec.Project), spec.Role, kerr)
		}
		if have {
			spec.Kit, def = d.Ref, &d
		}
	}
```

2. Change `if errors.Is(err, ErrKitNotReady) {` to `if errors.Is(err, ErrKitNotReady) && draft == nil {`.
3. After `if def != nil && def.Ref == spec.Kit { inst.KitChain = chainRefs(def.Chain) }`, add:

```go
	if draft != nil {
		inst.KitDraft, inst.ImageTag = &draft.ref, draft.image.Tag
	}
```

4. Right after the `PutInstance` error block (before `s.recordRaiseSetup`), add:

```go
	if draft != nil {
		s.keepDraftTree(draft.objs)
	}
```

Add, after `raiseKit`:

```go
// draftRaise is what a draft studio's raise resolved: its definition, the
// draft tree it runs, that tree's objects, and its built image.
type draftRaise struct {
	def   KitDefinition
	ref   KitDraftRef
	objs  KitObjects
	image KitDraftImage
}

// raiseDraft resolves a draft studio of kit spec.KitDraft for role: the
// draft tree as of now under the role's harness (DraftKitDefinition), and
// its image through the test-build jobs (Ensure), waited for.
func (s *Supervisor) raiseDraft(ctx context.Context, spec RaiseSpec, role Role) (draftRaise, error) {
	if s.draftBuilds == nil {
		return draftRaise{}, writeErr(http.StatusServiceUnavailable, kitDraftBuildsOff)
	}
	h, err := s.raiseHarness(spec.Project, role)
	if err != nil {
		return draftRaise{}, err
	}
	def, ref, objs, err := DraftKitDefinition(s.store, spec.KitDraft, h)
	if err != nil {
		return draftRaise{}, err
	}
	img, err := s.draftBuilds.Ensure(ctx, def, ref, "studio raise "+spec.Name)
	if err != nil {
		return draftRaise{}, err
	}
	return draftRaise{def: def, ref: ref, objs: objs, image: img}, nil
}

// keepDraftTree re-stores a draft studio's tree should GC have collected
// it between the raise reading the draft and recording the instance (a
// discard or an edit meanwhile). From the recorded instance on, it is a GC
// root, so this check cannot race a later collection.
func (s *Supervisor) keepDraftTree(objs KitObjects) {
	tree := objs.Trees[0].Hash()
	if _, ok := s.store.KitTree(tree); ok {
		return
	}
	if err := s.store.PutKitDocs(nil, objs); err != nil && s.log != nil {
		s.log.Warn("draft studio: could not re-store its tree", "tree", shortTree(tree), "err", err.Error())
	}
}
```

Add `"net/http"` to `supervisor.go`'s imports.

5. In `compileContext`, after `in.Session.Kit = spec.Kit.String()`, add:

```go
		if spec.KitDraft != "" {
			in.Session.Kit = spec.KitDraft + " (draft)"
		}
```

6. In `ContextForActor`, add `KitDraft` to the spec literal: after building `spec`, add `if inst.KitDraft != nil { spec.KitDraft = inst.KitDraft.Kit }`. Wrap the role-kit lookup `if d, have, err := s.kitFor(role, harnessinstall.Default()); err == nil && have { … }` in `if inst.KitDraft == nil { … }`, so a draft studio keeps its draft's prompt. Before the final `return`, add:

```go
	if inst.KitDraft != nil {
		sk, err := draftStudioKit(s.store, inst)
		if err != nil {
			spec.Kit = KitRef{}
			b := s.compileContext(spec, actor, KitRef{}, nil, nil)
			b.Warnings = append(b.Warnings, fmt.Sprintf("kit layer omitted: %v", err))
			return b, nil
		}
		def := KitDefinition{Ref: inst.Kit, Kit: sk}
		return s.compileContext(spec, actor, inst.Kit, &def, nil), nil
	}
```

- [ ] **Step 7: Run the tests, under the race detector**

Run: `GOPROXY=https://proxy.golang.org,direct go test -race ./internal/jam/ -run 'DraftStudio|Conformance|Raise|Context|Setup' -v`
Expected: PASS. Then run `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/... && go vet -tags integration ./internal/jam/...`. Expected: PASS.

- [ ] **Step 8: Commit**

```bash
gofmt -l internal/jam
git add internal/jam
git commit -m "feat(jam): draft studios — raise a kit's draft, its tree kept live, never stale

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Raising a draft studio over the API and the CLI

**Files:**
- Modify: `internal/jam/admin.go` (`CoveRaiseBody.KitDraft`, `CoveRaiseResult.KitDraftTree`, the `POST /admin/coves` handler)
- Modify: `internal/jam/adminclient/adminclient.go` (`CoveRaiseParams.KitDraft`, `draftRaiseTimeout`)
- Modify: `cmd/at-jam/main.go` (`studio raise --kit-draft`)
- Modify: `docs/usage/jam/kits-drafts.md` ("## Draft studio", frontmatter), `docs/usage/jam/INDEX.md`, `docs/usage/jam/coves.md`
- Test: `internal/jam/kit_draft_studio_admin_test.go` (create), `cmd/at-jam/kitdraft_build_test.go` (append)

**Interfaces:**
- Consumes: `RaiseSpec.KitDraft`, `Instance.KitDraft`, `draftStudioSetup`, `fakeDraftBuilder` (Task 6); `RaiseManual`; `doJSON` (admin_test.go); `cliDraftBuilder` (Task 5); `aliveLauncher` (cmd/at-jam main_test.go).
- Produces:
```go
package jam
// CoveRaiseBody gains:   KitDraft string `json:"kit_draft,omitempty"`
// CoveRaiseResult gains: KitDraftTree string `json:"kit_draft_tree,omitempty"`
package adminclient
// CoveRaiseParams gains: KitDraft string; const draftRaiseTimeout = 30 * time.Minute
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/kit_draft_studio_admin_test.go`:

```go
package jam

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestAdminRaisesADraftStudio(t *testing.T) {
	sup, store, _, s := draftStudioSetup(t, &fakeDraftBuilder{})
	var logs bytes.Buffer
	h := NewAdminHandler(store, sup, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	rec := doJSON(t, h, "POST", "/admin/coves", CoveRaiseBody{ID: "try", Role: "dev", KitDraft: "my-app"})
	var res CoveRaiseResult
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &res) != nil || res.KitDraftTree != s.Tree || res.Label != "try" {
		t.Fatalf("raise = %d %s", rec.Code, rec.Body)
	}
	if inst, ok := store.GetInstance(res.ID); !ok || inst.KitDraft == nil || inst.SessionKind != "" || inst.Name != "try" {
		t.Fatalf("instance = %+v %v (a manual, ephemeral session)", inst, ok)
	}
	if !strings.Contains(logs.String(), "kit_draft=my-app") {
		t.Fatalf("audit:\n%s", logs.String())
	}
	rec = doJSON(t, h, "POST", "/admin/coves", CoveRaiseBody{ID: "try2", Role: "dev", KitDraft: "web-dev"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "kit web-dev has no draft") {
		t.Fatalf("no draft = %d %s", rec.Code, rec.Body)
	}
}
```

Append to `cmd/at-jam/kitdraft_build_test.go` (add `regexp` to its imports):

```go
func TestStudioRaiseKitDraft(t *testing.T) {
	store := jam.NewMemStore()
	if err := store.PutRole("default", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	d, err := jam.OpenKitDraft(store, "app", 0, "op", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, log)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sup.SetKitDraftBuilds(jam.NewKitDraftBuilds(ctx, store, &cliDraftBuilder{}, log))
	ts := httptest.NewServer(jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil))
	t.Cleanup(ts.Close)
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	if code := run([]string{"studio", "raise", "--admin-url", ts.URL, "--id", "try", "--role", "guest", "--kit-draft", "app"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("raise: %d %s", code, errb.String())
	}
	want := regexp.MustCompile(`^raised try as session ses_[0-9a-z]{26} \(phase=live\) from the draft of app @` + d.Tree[:12] + `\n$`)
	if !want.MatchString(out.String()) {
		t.Fatalf("raise output = %q", out.String())
	}
	out.Reset()
	if code := run([]string{"studio", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 || !strings.Contains(out.String(), "image=draft") {
		t.Fatalf("list: %d %q", code, out.String())
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ ./cmd/at-jam/ -run 'RaisesADraftStudio|StudioRaiseKitDraft' -v`
Expected: FAIL to compile: `unknown field KitDraft in struct literal of type CoveRaiseBody`.

- [ ] **Step 3: Implement the API and the client**

In `internal/jam/admin.go`:
- `CoveRaiseBody`: add `KitDraft string `json:"kit_draft,omitempty"` // run this kit's draft instead of the role's kit (a draft studio)`.
- `CoveRaiseResult`: add `KitDraftTree string `json:"kit_draft_tree,omitempty"` // a draft studio: the draft tree it runs`.
- In `POST /admin/coves`, pass `KitDraft: b.KitDraft` in the `RaiseSpec`, and replace the success lines with:

```go
		log.Info("admin cove raised", "operator", OperatorID(r), "id", id, "label", b.ID, "project", inst.Project, "role", b.Role, "kit_draft", b.KitDraft)
		res := CoveRaiseResult{ID: id, Label: b.ID, Token: tok, LaunchSecret: secret, Phase: string(inst.Phase), Location: inst.Location}
		if inst.KitDraft != nil {
			res.KitDraftTree = inst.KitDraft.Tree
		}
		writeJSON(w, http.StatusCreated, res)
```

(The kit name is only logged after a successful raise, so it is tag-safe by then.)

In `internal/jam/adminclient/adminclient.go`, add `KitDraft string // run this kit's draft (a draft studio); the raise may build it first` to `CoveRaiseParams`, and replace `RaiseCove` with:

```go
// draftRaiseTimeout bounds a draft studio's raise, which may run the
// draft's test build first.
const draftRaiseTimeout = 30 * time.Minute

// RaiseCove raises a managed cove for a role and returns its runtime result
// (including the identity token, once). A draft studio's raise (KitDraft)
// may build its image first, so it waits up to draftRaiseTimeout.
func (c *Client) RaiseCove(p CoveRaiseParams) (jam.CoveRaiseResult, error) {
	var res jam.CoveRaiseResult
	body := jam.CoveRaiseBody{ID: p.ID, Project: p.Project, Role: p.Role, Unit: p.Unit, Prompt: p.Prompt, KitDraft: p.KitDraft}
	httpc := c.httpc
	if p.KitDraft != "" {
		hc := *c.httpc // keeps the Transport
		hc.Timeout = draftRaiseTimeout
		httpc = &hc
	}
	err := c.doWith(httpc, "POST", "/admin/coves", body, &res)
	return res, err
}
```

- [ ] **Step 4: Implement the CLI flag**

In `cmd/at-jam/main.go` `cmdStudio`, after the `promptFile` flag, add:

```go
	kitDraft := fs.String("kit-draft", "", "raise only: run this kit's draft instead of the role's kit (its test build is built first when needed)")
```

In `case "raise":`, pass `KitDraft: *kitDraft` in `adminclient.CoveRaiseParams`, and replace the success print with:

```go
		if res.KitDraftTree != "" {
			fmt.Fprintf(stdout, "raised %s as session %s (phase=%s) from the draft of %s @%s\n", res.Label, res.ID, res.Phase, textesc.Escape(*kitDraft, false), textesc.Escape(treePrefix(res.KitDraftTree), false))
		} else {
			fmt.Fprintf(stdout, "raised %s as session %s (phase=%s)\n", res.Label, res.ID, res.Phase)
		}
```

Add the `textesc` import to `main.go` if it is absent.

- [ ] **Step 5: Run the tests**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/... ./cmd/at-jam/`
Expected: PASS (`TestStudioCommandsRoundTrip` still prints its old line).

- [ ] **Step 6: Document the draft studio**

In `docs/usage/jam/kits-drafts.md`, insert this section after "## Test build". Write each <link> as a real link.

```markdown
## Draft studio

`at-jam studio raise --id try --role dev --kit-draft app` raises a manual
studio (<link `coves.md#the-studio-verbs` as "the studio verbs">) that runs the
draft's tree as of the raise, without publishing it. It is admin only, like
every raise, and the role supplies everything but the kit.

- The raise reads the draft (problems refuse it) and resolves its parents as
  a push would. It runs the test build of that tree under the role's
  model-spec harness. The last build of exactly that tree and harness is
  reused while its image exists; otherwise it is built now and the raise
  waits (`studio raise` allows 30 minutes). A build of another tree running
  answers 409. A failed build answers
  `draft <kit>: the test build failed (see at-jam kit draft build <kit> --show)`.
- The studio runs that image by id and records `kit_draft` (`kit`, `tree`).
  Its image status is `draft`: pushing or publishing the kit never makes it
  stale, and it is never restarted onto *current*.
- Its tree stays stored while the studio exists, even after the draft is
  discarded or published. Its session context reads it, and its setup
  inspect marks it (<link `inspect.md#sections` as "inspect.md">).
```

Update the frontmatter:
- `summary:` "A kit's draft — one mutable tree per kit, opened from a version, with its problems listed; the ETag/If-Match rule, the `kit draft open|status|get|put|rm|sync|publish|discard|build` verbs, test builds, draft studios (`studio raise --kit-draft`), and the draft, file, publish and build API."
- `read_when:` "You want to open, inspect, edit, test-build, try in a studio, discard or publish a kit's draft with `at-jam kit draft`, `studio raise --kit-draft` or over the admin API (`/admin/kits/{name}/draft`), or a draft call answered 409, 412, 413, 415 or 428."
- `owns:` append ", draft test builds (one per kit, the in-memory log, the `cove-kit-draft` tags, `/draft/build`), draft studios (the raise, its image, the `draft` image status, its tree kept stored)".

In `docs/usage/jam/INDEX.md`, set the `kits-drafts.md` row's text to the new `read_when`.

In `docs/usage/jam/coves.md`, "## The `studio` verbs":
- Add `[--kit-draft app]` to the end of the `at-jam studio raise` line in the code block.
- After the `studio raise` bullet, add: "- `--kit-draft <kit>` runs that kit's draft instead of the role's kit: a <link `kits-drafts.md#draft-studio` as "draft studio">."
- In the `image` bullet, after "Terminating, lost and gone studios show `-`.", add: "A draft studio shows `draft`."

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1` (expected: no more than 743 errors) and `wc -l docs/usage/jam/kits-drafts.md` (expected: under 200).

- [ ] **Step 7: Commit**

```bash
gofmt -l internal/jam cmd/at-jam
git add internal/jam cmd/at-jam docs/usage/jam
git commit -m "feat(at-jam): studio raise --kit-draft (draft studios over the admin API)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: The draft studio's setup inspect, and final verification

**Files:**
- Modify: `internal/jam/setup.go` (`SetupSection.Title`, `RenderSetupText`)
- Modify: `internal/jam/setup_parts.go` (`instanceKitRefsBody`)
- Modify: `internal/jam/setup_image_bodies.go` (`kitDetailBody` takes the draft)
- Modify: `internal/jam/setup_render.go` (both `kitDetailBody` calls, `instanceKitSection`)
- Modify: `internal/jam/setup_record.go` (`recordRaiseSetup`'s kit body)
- Create: `internal/jam/testdata/setup/studio-draft.golden`
- Modify: `docs/usage/jam/inspect.md`
- Test: `internal/jam/setup_draft_test.go` (create)

**Interfaces:**
- Consumes: `draftStudioSetup`, `raiseDraftStudio`, `fakeDraftBuilder` (Task 6); `fakeImages`, `checkGolden` (`setup_render_test.go`); `InstanceSetup`, `RoleSetup`, `SetupDeps`, `SectionKit`, `kitRefsBody`, `chainOrSelf`, `shortDigest`, `flat`.
- Produces:
```go
package jam
// SetupSection gains: Title string `json:"title,omitempty"`
func instanceKitRefsBody(inst Instance) string
func kitDetailBody(store Store, chain []KitRef, buildArgs map[string]string, draft *KitDraftRef) string
```

- [ ] **Step 1: Write the failing test**

Create `internal/jam/setup_draft_test.go`:

```go
package jam

import (
	"context"
	"strings"
	"testing"
)

func TestDraftStudioSetupGolden(t *testing.T) {
	sup, store, _, s := draftStudioSetup(t, &fakeDraftBuilder{})
	if _, err := raiseDraftStudio(t, sup, "d1"); err != nil {
		t.Fatal(err)
	}
	d := SetupDeps{Store: store, Sup: sup, Images: &fakeImages{}}
	got, err := InstanceSetup(context.Background(), d, "d1", []string{SectionKit}, false)
	if err != nil {
		t.Fatal(err)
	}
	heading := "kit (draft @" + s.Tree[:7] + ")"
	if got.Sections[0].Name != SectionKit || got.Sections[0].Title != heading || got.Sections[0].Stale {
		t.Fatalf("section = %+v", got.Sections[0])
	}
	var b strings.Builder
	RenderSetupText(&b, got)
	text := b.String()
	for _, want := range []string{
		"\n== " + heading + " (snapshot, ",
		"  kit: my-app (draft @" + s.Tree[:12] + ") digest ",
		"  chain (root first): default@v1 → web-dev@v1 → my-app (draft)\n",
		"  image tag: cove-kit-draft:my-app-" + s.Tree[:12] + "\n",
		"my-app (draft)  draft, not published",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "my-app@v0") {
		t.Fatalf("a draft studio must not show its kit as v0:\n%s", text)
	}
	checkGolden(t, "studio-draft", got)
	role, err := RoleSetup(context.Background(), d, "default", "dev", []string{SectionKit})
	if err != nil || role.Sections[0].Title != "" {
		t.Fatalf("the role render must be unchanged: %+v %v", role.Sections[0], err)
	}
}

func TestRenderSetupTextUsesTheTitle(t *testing.T) {
	var b strings.Builder
	RenderSetupText(&b, StudioSetup{Subject: "studio s1", Sections: []SetupSection{{Name: SectionKit, Title: "kit (draft @abcdef0)", Source: SourceConfig, Body: "k\n"}}})
	if want := "# studio s1\n\n== kit (draft @abcdef0) (config)\n  k\n"; b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'DraftStudioSetup|UsesTheTitle' -v`
Expected: FAIL to compile: `unknown field Title in struct literal of type SetupSection`.

- [ ] **Step 3: Implement the heading and the bodies**

`internal/jam/setup.go`, add to `SetupSection` after `Name`:

```go
	// Title, when set, heads the section in place of Name. A draft studio's
	// kit section is "kit (draft @<tree[:7]>)". Name stays the section's key.
	Title string `json:"title,omitempty"`
```

and in `RenderSetupText` replace `fmt.Fprintf(w, "\n== %s (%s)\n", flat(sec.Name), flat(head))` with:

```go
		name := sec.Name
		if sec.Title != "" {
			name = sec.Title
		}
		fmt.Fprintf(w, "\n== %s (%s)\n", flat(name), flat(head))
```

Update the doc comment to "headed "== <name or title> (<source>[, stale][, <at>])"".

`internal/jam/setup_parts.go`, after `kitRefsBody`:

```go
// instanceKitRefsBody is an instance's kit refs: kitRefsBody, or for a
// draft studio its kit as "<kit> (draft @<tree[:12]>)" and its own chain
// link as "<kit> (draft)", never "<kit>@v0".
func instanceKitRefsBody(inst Instance) string {
	if inst.KitDraft == nil {
		return kitRefsBody(inst.Kit, inst.KitChain, inst.ImageTag)
	}
	refs := chainOrSelf(inst.KitChain, inst.Kit)
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = flat(r.String())
	}
	names[len(names)-1] = flat(inst.KitDraft.Kit) + " (draft)"
	tag := inst.ImageTag
	if tag == "" {
		tag = "unknown"
	}
	return fmt.Sprintf("kit: %s (draft @%s) digest %s\nchain (root first): %s\nimage tag: %s\n",
		flat(inst.KitDraft.Kit), flat(shortDigest(inst.KitDraft.Tree)), flat(shortDigest(inst.Kit.Digest)), strings.Join(names, " → "), flat(tag))
}
```

`internal/jam/setup_image_bodies.go`: change the signature to `func kitDetailBody(store Store, chain []KitRef, buildArgs map[string]string, draft *KitDraftRef) string`, update its doc comment ("draft, when set, is a draft studio's: its own link (that kit at v0) reads from the draft tree"), and make the first statement inside the `for _, r := range chain {` loop:

```go
		if draft != nil && r.ID == draft.Kit && r.Version == 0 {
			files := "not loadable"
			if t, ok := store.KitTree(draft.Tree); ok {
				files = fmt.Sprintf("tree %s, %d file", shortDigest(draft.Tree), len(t.Entries))
				if len(t.Entries) != 1 {
					files += "s"
				}
			}
			rows = append(rows, []string{"  " + flat(r.ID) + " (draft)", "draft, not published", files})
			continue
		}
```

`internal/jam/setup_render.go`:
- In the role render, change the `kitDetailBody(d.Store, chain, wb.def.Kit.BuildArgs)` call to `kitDetailBody(d.Store, chain, wb.def.Kit.BuildArgs, nil)`.
- In `instanceKitSection`, change the initial `Body: kitRefsBody(inst.Kit, inst.KitChain, inst.ImageTag)` to `Body: instanceKitRefsBody(inst)`.
- Right after that line, add:

```go
	if inst.KitDraft != nil {
		sec.Title = SectionKit + " (draft @" + inst.KitDraft.Tree[:min(7, len(inst.KitDraft.Tree))] + ")"
	}
```

- Change its `kitDetailBody(…, sk.BuildArgs)` call to pass `inst.KitDraft` as the last argument.

`internal/jam/setup_record.go` `recordRaiseSetup`: change the kit part's body func to `return instanceKitRefsBody(inst)`.

- [ ] **Step 4: Generate and review the golden**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run TestDraftStudioSetupGolden -update-setup`, then read `internal/jam/testdata/setup/studio-draft.golden`. It must hold, in order:
- `# studio d1`;
- `== kit (draft @<7 hex>) (snapshot, 1970-01-01T00:16:40Z)`;
- `kit: my-app (draft @<hex>) digest <hex>`;
- `chain (root first): default@v1 → web-dev@v1 → my-app (draft)`;
- `image tag: cove-kit-draft:my-app-<hex>`;
- the provenance table, with `default@v1` and `web-dev@v1` as `local file, pushed <time>` and `my-app (draft)` as `draft, not published` with `tree <hex>, 1 file`;
- the build args (`GO_VERSION=1.27.1`, `NODE_VERSION=20`, no `my-app` args);
- the image facts' "not present" reason.

The heading's 7-hex tree prefix is fixed by the fixture's content; a change to the fixture rewrites it. Then run the test without the flag. Expected: PASS.

- [ ] **Step 5: Run every setup test**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'Setup|Render|Golden' -v`
Expected: PASS. `role.golden` and `studio.golden` must not change: run `git status internal/jam/testdata`, and only `studio-draft.golden` may be new.

- [ ] **Step 6: Document the heading**

In `docs/usage/jam/inspect.md`, "## Sections", append this to the end of the `kit` row's "Shows" cell (write the <link> as a real link): "; a <link `kits-drafts.md#draft-studio` as "draft studio">'s section is headed `kit (draft @<tree[:7]>)`, names its kit `<kit> (draft @<tree[:12]>)` and its own link `<kit> (draft)`, provenance `draft, not published`". After the line "Each section is headed `== <name> (<source>[, stale][, <at>])`.", add the sentence "A section with a `title` (JSON) is headed by it instead of its name."

- [ ] **Step 7: Full verification**

Run each of these and check its result:

```bash
export GOPROXY=https://proxy.golang.org,direct
just test
just lint; echo "lint exit=$?"
go vet -tags integration ./...
go test -race ./internal/jam/... ./internal/runner/... ./cmd/at-jam/
gofmt -l internal cmd
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1
wc -l docs/usage/jam/kits-drafts.md
```

Expected:
- every test passes;
- `lint exit=0`;
- `go vet` and `gofmt -l` print nothing;
- the audit reports no more than 743 errors;
- `kits-drafts.md` is under 200 lines.

- [ ] **Step 8: Commit**

```bash
git add internal/jam docs/usage/jam/inspect.md
git commit -m "feat(jam): setup inspect heads a draft studio's kit section kit (draft @tree)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Self-review

**1. Spec coverage.**
- **Test build** (§3) is Tasks 2–5: the real chain, harness and hardening via `BuildKitDraft` (Task 3); the log streamed and followed (Tasks 1, 2, 5); the result and digest reported (Tasks 2, 5); the `cove-kit-draft:` tags, never a version's (Tasks 2, 3).
- **Draft studio** (§3) is Tasks 6–7: admin only, through the `/admin` gate; ephemeral manual via `RaiseManual`; no publish. The setup heading is Task 8.
- **Testing** (§6): "test build (`runner.Fake`)" is `TestBuildKitDraftArgv`, `TestBuildKitDraftStageArgv` and `TestBuildKitDraftCancelStopsTheBuild`; "draft studio raise" is Task 6's tests.
- Each binding ruling has a task: jobs, 409, If-Match, the log cap, shutdown and DELETE (Tasks 2, 4); the CLI (Task 5); `KitDraft` and the GC roots (Task 6); inspect (Task 8); the docs (Tasks 4, 5, 7, 8); secrets (Tasks 2, 3). The UI is ruled out (ruling 20).

**2. Placeholder scan.** No TBD or "similar to". Every code step shows its code. The golden (Task 8 Step 4) is generated and then reviewed against an explicit checklist, because its hex runs are normalised by `checkGolden`. The test also asserts every load-bearing line literally.

**3. Type consistency.**
- `KitDraftBuild` and its fields, `KitDraftImage{Tag, ID}` and `KitDraftRef{Kit, Tree}` are defined in Task 2 and used unchanged in Tasks 3–8.
- `KitDraftBuilds.Start(name, ifMatch, by)`, `Status(name, since)`, `Cancel(ctx, name)` and `Wait(timeout)` are defined in Task 2, as is the `startLocked`/`errDraftBuildRunning` pair used by `Ensure`. Task 6 adds `Ensure(ctx, def, ref, by)`.
- `kitDraftDefinitionLocked` returns `(KitDefinition, KitObjects, error)` in Tasks 2 and 6.
- `KitDraftStageTag(tag)` is Task 2 and is used in Task 3.
- `RaiseSpec.DraftImage` is Task 3; `RaiseSpec.KitDraft` and `Instance.KitDraft` are Task 6.
- `kitDetailBody` gains `draft *KitDraftRef` in Task 8, at both of its call sites.
- `fakeDraftBuilder` (Task 2) carries every field the later tasks use (`during`, `present` through `setPresent`, `started`, `release`, `readFile`).

**4. Review Focus.** Each line has its test in its owning task: (1) Task 2; (2) Tasks 2 and 4; (3) Tasks 2 and 4; (4) Task 6, plus the storetest case; (5) Tasks 2 and 5.
