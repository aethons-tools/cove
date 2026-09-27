# `at-cove install --no-cache` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `--no-cache` flag to `at-cove install` that makes the hardening-image `docker build` bypass the layer cache, so a rebuild re-runs the build-time `claude`/plugin install instead of reusing cached layers.

**Architecture:** A boolean threads from the `install` CLI flag → `doInstall` → `backend.InstallContext.NoCache` → `Colima.Install` → `Colima.dockerBuild`, which conditionally appends docker's `--no-cache` to its single `docker build` invocation. Default off; behavior is unchanged when the flag is absent.

**Tech Stack:** Go; hermetic tests via `internal/runner.Fake` (no docker/network).

## Global Constraints

- **Install-only.** Only `at-cove install` gets `--no-cache`; `create`/`recreate`/`chat`/`work`/`dispatch` never build and must not accept it.
- **Default off / no behavior change when unset.** With the flag absent, the `docker build` argv is byte-for-byte what it is today.
- **Single build site.** The only `docker build` touched is `Colima.dockerBuild` (`internal/backend/colima/colima.go`). Do NOT touch the base-image build in `internal/backend/colima/baseimage.go`.
- **No hardening/template change.** Nothing under `internal/assemble/`. No new Dockerfile `ARG`.
- **`--dry-run` stays side-effect-free** — no docker/keys/manifest; it only prints intent.
- **Preserve the plan/execution split; TDD** — write the failing test first.

---

### Task 1: `--no-cache` flag threaded install→dockerBuild

**Files:**
- Modify: `internal/backend/backend.go` (add `NoCache` to `InstallContext`, ~L88)
- Modify: `internal/backend/colima/colima.go` (`Install` ~L189, `dockerBuild` ~L150)
- Modify: `cmd/at-cove/main.go` (`install` command block ~L69–84; `doInstall` ~L455)
- Test: `internal/backend/colima/colima_test.go` (backend argv)
- Test: `cmd/at-cove/main_test.go` (CLI wiring + dry-run note)
- Modify: `docs/OVERVIEW.md` (install command-table row, L135)

**Interfaces:**
- Produces: `backend.InstallContext.NoCache bool`; `Colima.dockerBuild(buildDir, tag string, base backend.BaseSpec, noCache bool)`; `doInstall(kitDir string, r runner.Runner, allowUnverifiedBase, assembleOnly, dryRun, noCache bool, stdout io.Writer) error`.
- Consumes (existing, do not change): `runner.Fake` records each call as `runner.Call{Name, Args}`; colima test helpers `dockerCall(calls, "build")` and `contains(args, s)`; CLI test helpers `run(args, runner, lookupEnv, lookPath, stdout, stderr) int`, `writeKit(t, dir)`, `seedConfigDir(t)`, `dummyLookPath`.

---

#### Backend layer (add the field + thread to the build argv)

- [ ] **Step 1: Write the failing backend test**

Add to `internal/backend/colima/colima_test.go`:

```go
// --no-cache: Install threads InstallContext.NoCache into the build argv, and
// omits it by default (guards against an always-on flag).
func TestInstallNoCacheThreadsToBuildArgv(t *testing.T) {
	f := &runner.Fake{}
	if _, err := New(f).Install(backend.InstallContext{Kit: "box", BuildDir: "/b", NoCache: true}); err != nil {
		t.Fatal(err)
	}
	build := dockerCall(f.Calls, "build")
	if build == nil || !contains(build, "--no-cache") {
		t.Fatalf("NoCache:true must add --no-cache to the build; build=%+v", f.Calls)
	}

	f2 := &runner.Fake{}
	if _, err := New(f2).Install(backend.InstallContext{Kit: "box", BuildDir: "/b"}); err != nil {
		t.Fatal(err)
	}
	build2 := dockerCall(f2.Calls, "build")
	if build2 == nil || contains(build2, "--no-cache") {
		t.Fatalf("default install must NOT pass --no-cache; build=%+v", f2.Calls)
	}
}
```

- [ ] **Step 2: Run it — expect a compile failure**

Run: `go test ./internal/backend/colima/ -run TestInstallNoCacheThreadsToBuildArgv`
Expected: FAIL to compile — `unknown field 'NoCache' in struct literal of type backend.InstallContext`.

- [ ] **Step 3: Add the `InstallContext` field**

In `internal/backend/backend.go`, add `NoCache` to the struct (keep the existing fields/comments):

```go
type InstallContext struct {
	Kit      string   // identity for the built image tag (naming.Image → atcove-<Kit>)
	BuildDir string   // the assembled .build context to build
	Base     BaseSpec // base resolution + provenance gate inputs (owns AllowUnverified)
	NoCache  bool     // bypass docker's layer cache for this build (forces a fresh claude/plugin install)
}
```

- [ ] **Step 4: Thread it through `Install` and `dockerBuild`**

In `internal/backend/colima/colima.go`, change the `dockerBuild` signature and build the argv as a slice:

```go
func (c *Colima) dockerBuild(buildDir, tag string, base backend.BaseSpec, noCache bool) (resolvedBase, digest string, err error) {
	if err := c.preflight(); err != nil {
		return "", "", err
	}
	resolvedBase, err = c.resolveBase(base)
	if err != nil {
		return "", "", err
	}
	// --progress=plain: line-by-line build output (BuildKit's TTY renderer can
	// overflow the terminal by a column). --no-cache: rebuild every layer so the
	// build-time `claude`/plugin install re-runs instead of reusing cached layers.
	buildArgs := []string{"build", "--progress=plain"}
	if noCache {
		buildArgs = append(buildArgs, "--no-cache")
	}
	buildArgs = append(buildArgs, "--build-arg", "BASE="+resolvedBase, "-t", tag, buildDir)
	if err := c.r.Run("docker", dargs(buildArgs...)...); err != nil {
		return "", "", err
	}
	out, err := c.r.Output("docker", dargs("inspect", "--format", "{{.Id}}", tag)...)
	if err != nil {
		return "", "", err
	}
	return resolvedBase, strings.TrimSpace(out), nil
}
```

And update the caller in `Install`:

```go
func (c *Colima) Install(ctx backend.InstallContext) (backend.InstalledImage, error) {
	img := naming.Image(ctx.Kit)
	base, digest, err := c.dockerBuild(ctx.BuildDir, img, ctx.Base, ctx.NoCache)
	if err != nil {
		return backend.InstalledImage{}, err
	}
	return backend.InstalledImage{Ref: img, Digest: digest, BaseDigest: base}, nil
}
```

- [ ] **Step 5: Run the backend test — expect PASS**

Run: `go test ./internal/backend/colima/ -run TestInstallNoCacheThreadsToBuildArgv`
Expected: PASS.

- [ ] **Step 6: Confirm no other `dockerBuild` caller broke**

Run: `go build ./... && go test ./internal/backend/...`
Expected: builds; backend tests green (the existing `TestInstallBuildsGatesTags` still passes — the default argv is unchanged since `noCache` is false there).

#### CLI layer (flag + doInstall + dry-run note)

- [ ] **Step 7: Write the failing CLI tests**

Add to `cmd/at-cove/main_test.go` (uses `slices` — add `"slices"` to the file's imports if not present):

```go
// findBuild returns the recorded `docker build` call, or nil.
func findBuild(calls []runner.Call) *runner.Call {
	for i := range calls {
		if calls[i].Name == "docker" && slices.Contains(calls[i].Args, "build") {
			return &calls[i]
		}
	}
	return nil
}

// TestInstallNoCacheFlag: `install --no-cache` reaches the docker build with
// --no-cache; plain `install` does not.
func TestInstallNoCacheFlag(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	seedConfigDir(t)

	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"install", "--no-cache", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("install --no-cache should be accepted; code=%d stderr=%s", code, errOut.String())
	}
	if b := findBuild(f.Calls); b == nil || !slices.Contains(b.Args, "--no-cache") {
		t.Fatalf("install --no-cache must pass --no-cache to docker build; calls=%+v", f.Calls)
	}

	f2 := &runner.Fake{}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"install", "--project-dir", dir}, f2, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("plain install should be accepted; code=%d stderr=%s", code, errOut.String())
	}
	if b := findBuild(f2.Calls); b == nil || slices.Contains(b.Args, "--no-cache") {
		t.Fatalf("plain install must NOT pass --no-cache; calls=%+v", f2.Calls)
	}
}

// TestDryRunInstallNoCacheNote: `--dry-run install --no-cache` notes no-cache in
// the intent line and records no docker calls.
func TestDryRunInstallNoCacheNote(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"--dry-run", "install", "--no-cache", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("--dry-run install --no-cache should be accepted; code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "build (no cache)") {
		t.Fatalf("dry-run --no-cache must note the cache bypass; out=%q", out.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("--dry-run must record no docker calls; calls=%+v", f.Calls)
	}
}
```

- [ ] **Step 8: Run them — expect failure**

Run: `go test ./cmd/at-cove/ -run 'TestInstallNoCacheFlag|TestDryRunInstallNoCacheNote'`
Expected: FAIL — the flag isn't registered (either a flag-parse error on `--no-cache`, or the build lacks it / the dry-run note is missing).

- [ ] **Step 9: Register the flag and thread it into `doInstall`**

In `cmd/at-cove/main.go`, in the `install` command's `Run` (alongside the existing `fs.Bool` flags), add:

```go
noCache := fs.Bool("no-cache", false, "rebuild every layer, bypassing docker's build cache (forces a fresh claude/plugin install)")
```

and pass `*noCache` in the `doInstall(...)` call (add it as the argument matching the new parameter, before `out`):

```go
return exitCode("at-cove", doInstall(kitDir, r, *allowUnverified, *assembleOnly, g.DryRun, *noCache, out), errw)
```

- [ ] **Step 10: Add the `noCache` parameter + dry-run note to `doInstall`**

In `cmd/at-cove/main.go`, change the signature and the dry-run line (leave the rest of the body unchanged except passing `NoCache` into the `InstallContext`):

```go
func doInstall(kitDir string, r runner.Runner, allowUnverifiedBase, assembleOnly, dryRun, noCache bool, stdout io.Writer) error {
	cfg, err := kit.Load(kitDir)
	if err != nil {
		return err
	}
	buildDir := filepath.Join(kitDir, ".build")
	img := naming.Image(cfg.Name)
	if dryRun {
		cacheNote := ""
		if noCache {
			cacheNote = " (no cache)"
		}
		fmt.Fprintf(stdout, "would assemble %s, then build%s + gate + tag %s and write %s\n", buildDir, cacheNote, img, install.Path(kitDir))
		return nil
	}
	// ... assemble / assembleOnly unchanged ...
```

and in the `b.Install(backend.InstallContext{...})` call, set the field:

```go
	installed, err := b.Install(backend.InstallContext{
		Kit: cfg.Name, BuildDir: buildDir, NoCache: noCache,
		Base: backend.BaseSpec{KitDir: kitDir, Base: cfg.Image.Base, AllowUnverified: allowUnverifiedBase},
	})
```

- [ ] **Step 11: Run the CLI tests — expect PASS**

Run: `go test ./cmd/at-cove/ -run 'TestInstallNoCacheFlag|TestDryRunInstallNoCacheNote'`
Expected: PASS.

#### Docs + full green

- [ ] **Step 12: Update the `install` row in `docs/OVERVIEW.md`**

Change the L135 row signature and add a clause. Replace:

`| `+"`at-cove install [--project-dir DIR] [--allow-unverified-base] [--assemble-only]`"+` |`

with `[--no-cache]` added to the signature, and append this sentence to the row's description (after the `--dry-run` sentence):

> `--no-cache` bypasses docker's layer cache for the build, forcing the build-time `claude`/plugin install to re-run (use it to pick up a newer Claude Code without other kit changes).

- [ ] **Step 13: Full hermetic suite + lint**

Run: `just test && just lint`
Expected: all green; no lint findings on the touched files.

- [ ] **Step 14: Commit**

```bash
git add internal/backend/backend.go internal/backend/colima/colima.go internal/backend/colima/colima_test.go cmd/at-cove/main.go cmd/at-cove/main_test.go docs/OVERVIEW.md
git commit -m "feat(install): --no-cache to bypass docker's layer cache

Thread NoCache from the install flag through InstallContext to the single
dockerBuild site; --no-cache forces the build-time claude/plugin install to
re-run. Install-only; default off. Docs + hermetic argv/wiring/dry-run tests."
```

---

## Self-Review

**Spec coverage:** flag (Steps 9–10) ✓; `InstallContext.NoCache` (Step 3) ✓; `Install`→`dockerBuild` thread + argv (Step 4) ✓; `doInstall` param + dry-run note (Step 10) ✓; three tests — backend argv incl. negative (Step 1), CLI wiring incl. negative (Step 7), dry-run note + no-docker (Step 7) ✓; docs row (Step 12) ✓; base-image build untouched (Global Constraints, no step touches `baseimage.go`) ✓.

**Placeholder scan:** no TBD/TODO; every code block is complete. The `slices` import is called out in Step 7.

**Type consistency:** `dockerBuild(buildDir, tag string, base backend.BaseSpec, noCache bool)` used identically in Steps 4 (def) and 4 (call in `Install`); `doInstall(..., dryRun, noCache bool, stdout io.Writer)` used identically in Steps 9 (call) and 10 (def); `InstallContext.NoCache` set in Step 10 matches the field added in Step 3; dry-run string `"build (no cache)"` asserted in Step 7 matches the `" (no cache)"` note produced in Step 10.
