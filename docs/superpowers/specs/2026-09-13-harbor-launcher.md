# harbor: real Colima Launcher + cove-master in the image (COV-158)

**Status:** design approved, pre-plan
**Issue:** COV-158 (slice 6 of the cove-management tree — the first fully working managed cove)
**Foundation:** COV-149 (supervisor + `Launcher` seam), COV-156 (agent wrapper), COV-157 (:443 Attach transport). **Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md`.

## Summary

Replace `placeholderLauncher` with a real Colima-backed `harbor.Launcher`, and get `cove-master` into the cove image. After this slice:

```
at-harbor cove raise --role worker --prompt-file task.md
```
starts a Colima cove from a configured pre-built image, injects the connector + prompt over SSH, starts `cove-master`, which runs `claude -p` on the prompt (with Anthropic + git reachable through harbor), reports `Running`→`Done` over the :443 Attach stream, and the supervisor tears the cove down. This is the manual precursor to COV-146's dispatcher — it exercises the whole stack (supervisor → launcher → cove-master → agent → Attach → teardown) end to end.

**One combined slice** (build-tree embedding + harbor-side launcher), per the approved plan.

## Package layout & boundaries

- **`internal/covemasterbin`** (new) — embeds the linux `cove-master` binaries (`//go:embed bin` + `Binary(goarch)` + `BinFS()`), mirroring `internal/atswitchboard` exactly.
- **`internal/assemble`** — new `writeCoveMaster` stages the binary into the build context; the hardening `Dockerfile` installs it.
- **`internal/install`** — `AtCoveIdentity` also hashes `covemasterbin.BinFS()` (a cove-master rebuild invalidates installs, like at-task/at-switchboard).
- **`internal/harbor`** (core, stays grpc/kit-free) — the `Launcher` seam gains a `creds` parameter and `RaiseSpec` gains `Prompt`. No backend/connect imports here.
- **`internal/connect`** — new exported `LaunchCoveMaster` (mirrors `LaunchTeammate`); stays go-oidc-free.
- **`internal/harbor/launcher`** (new) — the real `harbor.Launcher`. Imports `internal/backend`, `internal/connect`, `internal/harbor`, `internal/keys`, `internal/sshargs`, `internal/naming`, `internal/harbor/snippet`. **Not** imported by harbor core; wired from `cmd/at-harbor`.
- **`cmd/at-harbor`** — builds the real launcher from `runtime.launcher` config, replacing `placeholderLauncher`.

**Boundary gates unchanged:** `go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc'` empty; `internal/harbor` core imports no backend/connect/grpc; `internal/covemaster` untouched.

## 1. Embed `cove-master` in the image

Mirror `internal/atswitchboard` (COV-135) precisely:
- `internal/covemasterbin/covemasterbin.go`: package doc + `//go:embed bin` + `Binary(goarch) ([]byte, error)` + `BinFS() fs.FS` + the `lookup` helper (error text names `cove-master`).
- `internal/covemasterbin/bin/README` + `internal/covemasterbin/bin/.gitignore` (ignore `cove-master-linux-*`, keep README) — so `//go:embed bin` compiles on a fresh checkout.
- `scripts/stage-attask.sh`: add a third staging loop building `cove-master-linux-{amd64,arm64}` into `internal/covemasterbin/bin/` (same `CGO_ENABLED=0 GOOS=linux` + `LDFLAGS`/`VERSION` pattern). (`scripts/build.sh` already calls `stage-attask.sh`.)
- `internal/assemble/assemble.go`: add `writeCoveMaster(buildDir)` (mirrors `writeSwitchboard`, staging into `buildDir/covemaster/cove-master-linux-<arch>`; 0-byte placeholder when unstaged) and call it in `Assemble` after `writeSwitchboard`.
- `internal/assemble/hardening/Dockerfile`: add a `COPY covemaster/ /tmp/covemaster/` + guarded `install -m 0755 … /usr/local/bin/cove-master` step (mirrors the at-switchboard block: install only when the arch binary is non-empty; no base fallback).
- `internal/install/currency.go`: in `AtCoveIdentity`, hash `covemasterbin.BinFS()` with field label `"covemaster"` (after the switchboard field).

Result: a cove built by `at-cove install` carries `/usr/local/bin/cove-master`, version-locked to the at-cove that built it.

## 2. Supervisor seam change (`internal/harbor`)

- `RaiseSpec` gains `Prompt string` (a request input like Role/Unit; **not** persisted on the Instance — it's workload config the launcher consumes). Update the type doc.
- `LaunchCreds struct { IdentityToken, LaunchSecret string }` (new).
- `Launcher.Raise(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (location string, err error)` — the supervisor already mints the identity token (`Enroll`) and launch secret (`MintToken`) *before* calling `Raise` (supervisor.go:91–99); it now passes them in `creds`. `Teardown`/`Probe` signatures unchanged.
- Update `Supervisor.Raise` to pass `LaunchCreds{tok, secret}` to `s.launcher.Raise`. Update the fake launcher in `supervisor_test.go`/`admin_test.go` to the new signature (record the creds it received).

`RuntimeAddr` is **not** in `creds` — it's static launcher config (see §6), not per-raise.

## 3. `connect.LaunchCoveMaster`

A new exported helper mirroring `connect.LaunchTeammate` (reusing the private `writeVM` + `detachedLaunchCmd` mechanism — secrets to `/dev/shm` over ssh stdin, never argv):

```go
type CoveMasterOptions struct {
    Target       sshargs.Target // dialed cove endpoint (from backend.Dial + keys/known-hosts)
    HarborHost   string         // harbor.host — for the connector base URL (https://harbor.host)
    RuntimeAddr  string         // AT_HARBOR_RUNTIME_ADDR (harbor.host:443)
    IdentityToken string        // shared: AT_HARBOR_IDENTITY_TOKEN + agent connector token
    LaunchSecret string         // AT_HARBOR_LAUNCH_SECRET
    WorkDir      string         // AT_COVE_WORKDIR (default /home/agent/workspace)
    Prompt       string         // written to a tmpfs prompt file; AT_COVE_AGENT_PROMPT_FILE points at it
}

func LaunchCoveMaster(r runner.Runner, o CoveMasterOptions) error
```

It (a) writes the prompt to a tmpfs file (`/dev/shm/cove-agent-prompt`) via `writeVM`; (b) builds an env script = `snippet.Render("https://"+HarborHost, IdentityToken)` (the agent connector: `snippet.Env` + `snippet.GitConfig`) **plus** the cove-master env exports (`AT_HARBOR_RUNTIME_ADDR`, `AT_HARBOR_LAUNCH_SECRET`, `AT_COVE_WORKDIR`, `AT_COVE_AGENT_PROMPT_FILE`); (c) writes it to `/dev/shm/cove-master-env` via `writeVM`; (d) runs the detached launch: `set -a; . /dev/shm/cove-master-env; set +a; rm -f /dev/shm/cove-master-env; setsid nohup cove-master </dev/null >>/agent-data/cove-master.log 2>&1 &` over a non-tty ssh. `AT_HARBOR_IDENTITY_TOKEN` is shared — cove-master reads it *and* it backs the agent's `ANTHROPIC_API_KEY`/git helper.

Hermetic test: drive `runner.Fake`, assert the prompt + env are written to the tmpfs paths (not argv), that the env includes all required vars + the connector, and that the detached `cove-master` command is issued.

## 4. `internal/harbor/launcher` pkg

Implements `harbor.Launcher` against `backend.DispatchOps` + `connect`:

```go
type Config struct {
    Ops          backend.DispatchOps
    Backend      backend.Backend // for Dial
    Runner       runner.Runner
    Image        string          // pre-built image tag (from install.json)
    ImageDigest  string          // built-image digest (from install.json)
    HarborHost   string          // harbor.host (added to addHosts so squid resolves it)
    RuntimeAddr  string          // harbor.host:443
    IdentityFile string          // ssh private key matching the image's baked authorized_keys
    KnownHostsDir string
    DNS          []string
    Docker       bool
    now          func() time.Time
    log          *slog.Logger
}
const Label = "harbor.cove"

func New(cfg Config) *Launcher
func (l *Launcher) Raise(ctx, spec harbor.RaiseSpec, creds harbor.LaunchCreds) (string, error)
func (l *Launcher) Teardown(ctx, inst harbor.Instance) error
func (l *Launcher) Probe(ctx, inst harbor.Instance) (harbor.Liveness, error)
```

- **Raise:** `name := naming.CoveContainer(spec.ActorID)`; `RunEphemeral(Image, ImageDigest, name, Label, DNS, []string{HarborHost}, Docker)` → `Backend.Dial(name)` → wait for sshd (a small poll like dispatch's `waitForSSH`, injected sleep) → build `sshargs.Target{Host, Port, User:"agent", IdentityFile, KnownHostsFile: filepath.Join(KnownHostsDir, name)}` → `connect.LaunchCoveMaster(Runner, CoveMasterOptions{Target, HarborHost, RuntimeAddr, IdentityToken: creds.IdentityToken, LaunchSecret: creds.LaunchSecret, WorkDir: "/home/agent/workspace", Prompt: spec.Prompt})`. Return `name` as the location. On any post-`RunEphemeral` failure, `RemoveContainer(name)` before returning the error (no leaked container).
- **Teardown:** `Ops.RemoveContainer(inst.Location)`.
- **Probe:** `Backend.GetStatus(inst.Location)` → `StateRunning`→`LivenessAlive`, `StateStopped`/`StateAbsent`→`LivenessDead`; a **non-nil error → `LivenessUnknown`** (transient docker hiccup shouldn't reap a live cove — COV-151).

Hermetic tests: a fake `DispatchOps` (records RunEphemeral/RemoveContainer/GetStatus calls, returns a canned Instance/Endpoint) + `runner.Fake` for the SSH steps. Assert: Raise runs+dials+launches with the right image/name/label/addHosts and returns the container name; a launch failure removes the container; Teardown removes by location; Probe maps states incl. error→Unknown.

## 5. Admin API + CLI

- `CoveRaiseBody` gains `Prompt string`; the POST `/admin/coves` handler passes it into `RaiseSpec.Prompt`.
- `at-harbor cove raise` gains `--prompt-file <path>` (read the file host-side, send its contents as `Prompt`). Required when a real launcher is configured; the prompt is workload input.
- Keep the existing `--role`/`--project`/`--unit`/`--id` flags.

## 6. Serve wiring + config

- `serveConfig.Runtime` gains a `Launcher` block:
  ```yaml
  runtime:
    launcher:
      install-manifest: /etc/harbor/install.json  # → Image + ImageDigest via internal/install.Load
      runtime-addr: harbor.example.com:443        # what the cove dials (AT_HARBOR_RUNTIME_ADDR)
      identity-file: /var/lib/harbor/at-cove/id_ed25519  # ssh key matching the image's authorized_keys
      known-hosts-dir: /var/lib/harbor/known_hosts.d
      harbor-host: harbor.example.com             # added to addHosts; connector base host
      dns: [...]
      docker: false
  ```
- `cmdServe`: when `runtime.launcher.install-manifest` is set, load the manifest (`internal/install.Load` → `Image`, `ImageDigest`), build the Colima backend + `launcher.New(...)`, and pass it to `NewSupervisor` instead of `placeholderLauncher`. When unset, keep `placeholderLauncher` (dev/tests unaffected).
- Validation: if a launcher block is present, `install-manifest`, `runtime-addr`, and `harbor-host` are required; error clearly otherwise.

## What gets injected into the cove (summary)

| Var | Source | Consumer |
|-----|--------|----------|
| `AT_HARBOR_RUNTIME_ADDR` = `harbor.host:443` | launcher config | cove-master (Attach dial) |
| `AT_HARBOR_LAUNCH_SECRET` | supervisor mint (creds) | cove-master (Attach auth) |
| `AT_COVE_WORKDIR`, `AT_COVE_AGENT_PROMPT_FILE` | launcher | cove-master → agent wrapper |
| `AT_HARBOR_IDENTITY_TOKEN` | supervisor mint (creds) | cove-master **and** the agent connector (shared) |
| `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY` | `snippet.Env` | the agent (Anthropic via harbor) |
| git `insteadOf` + credential helper | `snippet.GitConfig` | the agent (git via harbor) |

The prompt is written to `/dev/shm/cove-agent-prompt`; every secret travels via tmpfs over ssh stdin, never on argv or docker `-e`.

## SSH bootstrap + deployment constraint

Coves authenticate via the single shared host key (`keys.Ensure` → `id_ed25519`) whose public key is baked into the image at `at-cove install`; the host SSHes as user `agent` via `sshargs.Base` (`-i <key>`, `StrictHostKeyChecking=accept-new`, per-container known_hosts). The launcher reuses this exactly. **Deployment constraint:** harbor must be given the *same* private key that `at-cove install` baked its `authorized_keys` from — hence `runtime.launcher.identity-file`. Post-create SSH injection is required because `backend.CreateContext`/`RunEphemeral` expose no env field, and docker `-e` would leak secrets on argv.

## Tests (hermetic)

- `covemasterbin`: `Binary`/`lookup` error path (unstaged → actionable error) — mirrors atswitchboard's test.
- `assemble`: `writeCoveMaster` stages both arches (or placeholder); an assemble test asserts the build context contains `covemaster/cove-master-linux-*` and the Dockerfile installs it. (Follow the existing assemble/embed test pattern.)
- `install`: `AtCoveIdentity` changes when the covemaster embed changes (extend the currency test).
- `connect.LaunchCoveMaster`: `runner.Fake` — prompt + env to tmpfs, connector + cove-master vars present, detached launch issued, nothing secret on argv.
- `internal/harbor/launcher`: fake `DispatchOps` + `runner.Fake` — Raise/Teardown/Probe behavior incl. launch-failure cleanup and Probe error→Unknown.
- `internal/harbor` seam: supervisor passes creds to `Raise` (fake launcher records them); `RaiseSpec.Prompt` flows through.
- `cmd/at-harbor`: `runtime.launcher` config parse/validation.
- Real Colima end-to-end is operator/integration-run (not hermetic).

## Docs

- `docs/usage/harbor/coves.md`: the `raise --prompt-file` flow and what a raised managed cove now does end to end (image → inject → cove-master → agent → report → teardown).
- `docs/usage/harbor/serve.md`: the `runtime.launcher` config block + the identity-file/key-dir deployment constraint.

## Deferred (unchanged)

- Role→kit→image resolution (single configured image this slice; COV-144 slice 2).
- The `at-task` prepare/complete bracket — git **output-handling** (commit/push/PR) after the agent (the dispatcher's concern); this slice gives the agent git *access* but doesn't automate results.
- cove-master as the image **entrypoint** / non-root PID-1 (SSH bootstrap remains this slice).
- Fly backend; `:443` already done (COV-157); token rotation; private-CA trust distribution.
- COV-146 dispatcher (trigger → `raise`).
