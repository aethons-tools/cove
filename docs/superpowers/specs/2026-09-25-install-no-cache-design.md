# `at-cove install --no-cache`: force a cache-bypassing hardening-image build

**Status:** design approved (flag shape + install-only scope confirmed), pre-plan
**Motivation:** The hardening image installs Claude Code and seeds plugins at build time (`curl … https://claude.ai/install.sh | bash`, then `seed-plugins.sh`). Those steps land in cached docker layers, so a later `at-cove install` reuses the cache and keeps the *old* `claude`/plugins even though the source of truth (the installer / marketplace) has moved on. Operators need a way to force a fresh build without hand-running docker.
**Touches:** `cmd/at-cove/main.go` (install flag + `doInstall`), `internal/backend/backend.go` (`InstallContext`), `internal/backend/colima/colima.go` (`Install` + `dockerBuild`), `docs/OVERVIEW.md` (command-table row). No hardening-layer change, no template change.
**Explicitly out of scope:** the separate base-image build in `internal/backend/colima/baseimage.go` (the `just adopt-base` path); any change to `create`/`recreate`/`chat`/`work`/`dispatch` (they never build); any new cache-busting `ARG` in the Dockerfile (docker's own `--no-cache` is the mechanism).

## Problem

`at-cove install` is the single build+gate+tag path. Its build runs at exactly one site — `Colima.dockerBuild` (`internal/backend/colima/colima.go:150`):

```
docker build --progress=plain --build-arg BASE=<resolved> -t <tag> <buildDir>
```

`buildDir` is the assembled `.build/` context whose Dockerfile (the hardening layer) does the build-time `claude` install and plugin seed. Because docker caches layers, a rebuild on an unchanged Dockerfile reuses those layers and reproduces the previously-installed `claude`/plugin versions — the installer script's output is not part of the cache key. There is no way, short of manually running `docker build --no-cache` (which bypasses the provenance gate and manifest write), to force a fresh install through `at-cove`.

`doInstall` (`cmd/at-cove/main.go:455`) always builds when it reaches the backend — there is no "already current, skip" short-circuit (currency is computed *after* the build, only to freeze `install.json`). So the fix is a straight flag thread-through; no interaction with currency.

## Design

Add a boolean `--no-cache` flag to `at-cove install`. When set, `dockerBuild` adds docker's `--no-cache` to the build invocation, so every layer rebuilds and the `curl … install.sh` / `seed-plugins.sh` steps re-run against whatever is current. Default off — behavior is byte-for-byte unchanged when the flag is absent. `install` is the only command that builds, so it is the only command that gets the flag.

The flag only changes docker's layer reuse. Base resolution, the provenance gate, image-ID capture, and the `install.json` write are all downstream of the build line and unaffected.

### Thread-through (five touch points)

**1. CLI flag** — `cmd/at-cove/main.go`, the `install` command block (~L68–84). Register alongside the existing `--allow-unverified-base` / `--assemble-only` flags:

```go
noCache := fs.Bool("no-cache", false, "rebuild every layer, bypassing docker's build cache (forces a fresh claude/plugin install)")
```

Pass `*noCache` into `doInstall`.

**2. `doInstall`** — add a `noCache bool` parameter:

```go
func doInstall(kitDir string, r runner.Runner, allowUnverifiedBase, assembleOnly, dryRun, noCache bool, stdout io.Writer) error
```

- Thread it into the `backend.InstallContext` (below).
- `--dry-run` stays a pure preview (no assemble, no docker). When `noCache` is set, append a note so the intent line reflects it, e.g.:
  `would assemble <buildDir>, then build (no cache) + gate + tag <img> and write <path>`.
  Compute the parenthetical from `noCache` (empty string when false) so the existing line is unchanged by default.

**3. `backend.InstallContext`** — `internal/backend/backend.go:88`, add a field:

```go
type InstallContext struct {
	Kit      string
	BuildDir string
	Base     BaseSpec
	NoCache  bool // bypass docker's layer cache for this build
}
```

**4. `Colima.Install`** — `internal/backend/colima/colima.go:189`, pass it down:

```go
base, digest, err := c.dockerBuild(ctx.BuildDir, img, ctx.Base, ctx.NoCache)
```

**5. `dockerBuild`** — `internal/backend/colima/colima.go:150`, add `noCache bool` and build the argv as a slice so the flag is inserted only when set:

```go
func (c *Colima) dockerBuild(buildDir, tag string, base backend.BaseSpec, noCache bool) (resolvedBase, digest string, err error) {
	// … preflight + resolveBase unchanged …
	buildArgs := []string{"build", "--progress=plain"}
	if noCache {
		buildArgs = append(buildArgs, "--no-cache")
	}
	buildArgs = append(buildArgs, "--build-arg", "BASE="+resolvedBase, "-t", tag, buildDir)
	if err := c.r.Run("docker", dargs(buildArgs...)...); err != nil {
		return "", "", err
	}
	// … inspect {{.Id}} unchanged …
}
```

`--no-cache` is placed immediately after `build`; ordering relative to `--progress`/`--build-arg` is irrelevant to docker.

## Testing

Hermetic, driving `internal/runner.Fake` (no docker/network), preserving the plan/execution split.

1. **`dockerBuild` / `Install` argv (backend unit test):** with `InstallContext{NoCache: true}`, the recorded `docker build` argv contains `--no-cache`; with `NoCache: false` (or unset), it does **not** — the negative case guards against an always-on flag.
2. **CLI wiring (`cmd/at-cove`):** `at-cove install --no-cache` reaches the backend with `NoCache == true`, and plain `at-cove install` reaches it with `false`. Mirror the existing `--allow-unverified-base` wiring test.
3. **`--dry-run` interaction:** `at-cove install --dry-run --no-cache` prints the intent line carrying the no-cache note and records **zero** docker calls; the default `--dry-run` line is unchanged.

## Docs

Per `AGENTS.md`, update docs in the same change. The `at-cove install` row in `docs/OVERVIEW.md`'s command table owns the command surface — add `[--no-cache]` to its signature and one clause on when to use it (force a fresh build-time `claude`/plugin install by bypassing docker's layer cache). No other doc documents this flag; no duplication.

## Definition of done

- `at-cove install --no-cache` adds `--no-cache` to the hardening-image `docker build`; plain `install` is byte-for-byte unchanged.
- The three hermetic tests above pass (`just test`); the negative argv case is present.
- `docs/OVERVIEW.md` install row updated in the same change.
- No change under `internal/assemble/` (hardening/templates), `baseimage.go`, or any run command; one PR against `main`.
