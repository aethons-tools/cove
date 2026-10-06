# jam: `at-jam colima setup-docker` — write the Sysbox prerequisite into the colima config

**Status:** design approved (user chose "edit file, then prompt"), pre-plan
**Scope:** a host-side `at-jam colima` command group that turns the one-time, hand-edited Sysbox prerequisite for `docker: true` kits ([`docs/usage/docker-in-sandbox.md`](../../usage/docker-in-sandbox.md#prerequisite-install-sysbox-in-the-colima-vm-one-time)) into one idempotent command, plus a check subcommand.
**Builds on:** the colima backend's existing `requireSysboxRuntime` preflight (`internal/backend/colima/colima.go`) — `docker --context colima info -f '{{json .Runtimes}}'` → has `sysbox-runc`; `gopkg.in/yaml.v3` (already a dependency) for comment-preserving Node edits; `runner.Runner`/`runner.Fake` for the hermetic check.
**Does not change:** at-cove still **detects but never installs** Sysbox; the docker:true argv, cache volume, hardening and egress; the manual YAML recipe (kept as the fallback in the usage doc).

## Problem

`docker: true` needs two edits to the colima config, both of which must survive `colima stop/start`:

1. a **system-mode `provision:` hook** that installs Sysbox CE (≥ 0.7.1) on every boot, idempotent on the binary;
2. a **`docker.runtimes.sysbox-runc.path: /usr/bin/sysbox-runc`** entry — colima owns and regenerates `/etc/docker/daemon.json` each start, so the runtime must be registered through colima's own `docker:` passthrough.

Today the operator pastes both via `colima start --edit`. It's error-prone (the `.deb` URL naming, the version floor, the wiped `daemon.json` gotcha) and not re-runnable.

## Command surface

```
at-jam colima setup-docker [--sysbox-version 0.7.1] [--dry-run]
at-jam colima check-docker
```

- **`setup-docker`** edits the colima config in place, then **prints** the next steps (`colima restart`, then `at-jam colima check-docker`). It does **not** restart the VM — a restart bounces every running cove in that VM, so the operator chooses when.
- **`--dry-run`** prints a unified diff of the would-be change and writes nothing.
- **`--sysbox-version`** (default `0.7.1`, the proven floor) sets the version the hook installs. Values below 0.7.1 are rejected (0.6.x lacks time-namespace support). Format: `MAJOR.MINOR.PATCH`.
- **`check-docker`** runs the same runtime probe as the at-cove preflight and exits 0 when `sysbox-runc` is registered, non-zero with an actionable message (pointing at `setup-docker`) when it isn't or the daemon is unreachable.

Both are plain host-side utilities: no admin API, no Jam connection, no login. Output is plain stdout (intent/diff/next steps); errors go through the usual stderr diagnostic path.

## Which file

`$COLIMA_HOME/default/colima.yaml`, falling back to `~/.colima/default/colima.yaml` when `COLIMA_HOME` is unset. **No `--profile`**: at-cove pins every docker call to the `colima` docker context, which is the *default* profile, so a non-default profile would never be used by a cove (YAGNI).

A missing file is an error: `colima config not found at <path> — run 'colima start' once to create it`. An empty file is treated as an empty mapping.

## The edit — `internal/colimacfg` (pure)

```go
type Options struct{ SysboxVersion string }
type Change struct{ What string } // human-readable, e.g. "added docker.runtimes.sysbox-runc"
func Apply(in []byte, o Options) (out []byte, changes []Change, err error)
```

Pure bytes-in/bytes-out (no I/O), so every case is a fixture test. It decodes into a `yaml.Node`, mutates, and re-encodes (2-space indent).

1. **Runtime.** Ensure top-level `docker` is a mapping (create it if absent or `{}`/null), ensure `docker.runtimes` is a mapping, and set `docker.runtimes.sysbox-runc.path` to `/usr/bin/sysbox-runc`. Other `docker:` keys and other runtimes are untouched. If `sysbox-runc` already exists with a different `path`, it is overwritten and reported as a change. A `docker` / `runtimes` value that exists but is not a mapping is an error (don't guess).
2. **Provision hook.** Ensure top-level `provision` is a sequence (create if absent/null). Our entry is identified by a marker line inside its script: `# managed by at-jam colima setup-docker — re-run it to change; edits here are overwritten`. If an entry whose `script` contains the marker exists, replace its `mode`/`script` with the rendered one; otherwise append. Other hooks are untouched and keep their order. If more than one marked entry exists, replace the first and drop the rest (reported).
3. **Idempotence.** When the rendered result is semantically equal to the input (no changes collected), `Apply` returns the **input bytes unchanged** and no changes — so a no-op run never reformats the file.

The hook script is the doc's script, templated on the version, with the marker line after the shebang:

```bash
#!/usr/bin/env bash
# managed by at-jam colima setup-docker — re-run it to change; edits here are overwritten
set -euo pipefail
command -v sysbox-runc >/dev/null 2>&1 && exit 0
arch="$(dpkg --print-architecture)"
ver="<version>"
apt-get update && apt-get install -y jq
curl -fsSL -o /tmp/sysbox.deb \
  "https://github.com/nestybox/sysbox/releases/download/v${ver}/sysbox-ce_${ver}.linux_${arch}.deb"
apt-get install -y /tmp/sysbox.deb
```

**Known trade-offs (stated, accepted):**
- A *changing* run re-encodes the whole file: comments survive (yaml.v3 Node), but indentation/quoting is normalized.
- The hook is idempotent on the binary, so it **won't upgrade** a VM that already has an older `sysbox-runc`. `setup-docker` prints this caveat whenever it writes a hook, with the one-line manual upgrade (as in the usage doc).

## Writing

Non-dry-run with changes: copy the original to `colima.yaml.bak` (overwriting a previous backup), then write the new content atomically (temp file in the same dir + rename) preserving the original file mode. No changes → write nothing, print `colima config already set up for docker:true` and still print the `check-docker` hint.

## Check — reuse, don't duplicate

Export the runtime probe from the colima backend as a small function (e.g. `colima.HasSysboxRuntime(r runner.Runner) (bool, error)`) and have both `requireSysboxRuntime` and `check-docker` call it, so the two can't drift.

## Error message hand-off

at-cove's missing-runtime preflight error additionally names the shortcut: "…or run `at-jam colima setup-docker` on the host", keeping the doc pointer.

## Testing (hermetic)

- `colimacfg.Apply` fixtures: colima's fresh default config (`docker: {}`, `provision: []`); already-applied (input bytes returned unchanged, no changes); marked hook at an older version (replaced, other fields kept); user hooks + a custom `docker:` block with other keys/runtimes (preserved, order kept); comments preserved; empty file; `sysbox-runc` with a different path (overwritten, reported); non-mapping `docker` (error); duplicate marked hooks (deduped).
- Version validation: rejects `0.6.9`, `latest`, `1.2`; accepts `0.7.1`, `0.8.0`.
- Command tests against a temp `COLIMA_HOME`: missing file error; dry-run writes nothing and prints a diff; real run writes `.bak` + new file with original mode; second run is a no-op.
- `check-docker` + `HasSysboxRuntime` via `runner.Fake`: present → 0; absent → non-zero naming `setup-docker`; unreachable/garbage → error.

No real colima, VM, or network in any test.

## Docs

- `docs/usage/docker-in-sandbox.md` prerequisite section leads with `at-jam colima setup-docker` → `colima restart` → `at-jam colima check-docker`; the manual YAML stays as the fallback (and remains the explanation of *what* the command writes).
- The at-jam usage map (`docs/usage/jam/INDEX.md`) gains a row pointing at that section (the command's usage is owned by the docker-in-sandbox doc, not duplicated).
