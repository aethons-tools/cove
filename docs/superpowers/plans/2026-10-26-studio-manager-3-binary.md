# Studio Manager, Plan 3: The Manager Binary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `at-studio-manager`, a bare-metal binary that runs next to Colima on the Mac, dials Jam over TLS, and hosts studios through the existing Colima launcher. It is the remote counterpart of the in-process `local` manager.

**Architecture:**
- **Two fixes left over from plan 2 come first:**
  - Jam sends `Welcome` before the new connection becomes routable.
  - A raise that races a forced revoke is pinned to converge.
- **Small backend and launcher changes make one Colima daemon shareable:**
  - studio containers carry actor and manager labels;
  - a remote manager scopes its state volumes by its own name;
  - the launcher lists its own studios from those labels;
  - removing a container that is already gone counts as success;
  - the cove-master log tail now travels in the Teardown result.
- **A new package, `internal/studiomanager`, holds the manager:**
  - its config file;
  - the launcher stack (Colima backend, `launcher.Launcher`, imagefacts, a capacity gate);
  - a reconnect loop around plan 2's `client.Run`.
- **The binary:** `cmd/at-studio-manager` is a thin CLI over that package. The `colima setup-docker|check-docker` verbs move into a shared package, so `at-jam` and `at-studio-manager` both serve them.

**Tech Stack:** Go; gRPC with TLS (`google.golang.org/grpc/credentials`); buf v1.45.0 (`just buf-gen`) for the one proto field; `gopkg.in/yaml.v3`; `google.golang.org/grpc/test/bufconn` for hermetic tests.

**Spec:** `docs/superpowers/specs/2026-10-26-studio-manager-design.md`. This plan implements:
- Goal 1, the binary;
- "What moves where", the manager config and the verbs;
- from "Disconnects and reconciliation": the labels, the inventory from docker labels, and that a manager restart leaves studios running;
- from "Connection, protocol, and auth": the TLS CA config and the teardown log tail;
- capacity declines, from "Placement".

Plan 2 (`docs/superpowers/plans/2026-10-26-studio-manager-2-protocol-registry.md`, #520) built the Jam side and `client.Run`.

**Later plans (not this one):**
- **Plan 4**, per-manager kit operations:
  - draft build records keyed by (kit, manager);
  - an optional manager for admin draft builds and image facts;
  - per-manager staleness;
  - `PrepareImage` on the pinned manager.
- **Plan 5:** the Jam container image, the compose file, and the overview docs.
- **Parked:** `regMu` serializes connects behind admin operations. That is acceptable at one Jam with a handful of managers, so it is not changed here.

## Global Constraints

- **The binary:**
  - It is `cmd/at-studio-manager`, a binary named `at-studio-manager`.
  - It is stamped with `-X main.version=<V>` exactly as at-jam is, so Jam's exact-version check passes for a matched release.
- **Labels on a studio container:**
  - `harbor.cove` (unchanged, no value);
  - `harbor.cove.actor=<actorID>`, always;
  - `harbor.cove.manager=<name>`, only when the launcher has a manager name.
- **Labels on a state volume:**
  - the existing `harbor.cove.state=<actorID>` and `harbor.cove.jam=<JamID>`;
  - plus `harbor.cove.manager=<name>` when the launcher has a manager name.
- **JamID default:**
  - `<runtime-addr>` with no manager name (local: unchanged);
  - `<runtime-addr>#<name>` with one. That way `local`'s volume filter (`harbor.cove.jam=<runtime-addr>`) never matches a remote manager's volumes on a shared daemon.
- **The `local` manager is unchanged:** it has no manager name, so it adds no manager label and keeps the same JamID and the same StateOwners. Existing launcher and Colima tests keep passing. The only edits are the `RunEphemeral` signature in fakes and the argv lines that gain the actor label.
- **Reconnect backoff:**
  - It starts at `1s`, doubles each time, and is capped at `30s`.
  - It resets to `1s` after a session that reached `Welcome`.
  - A refusal (gRPC `Unauthenticated`, `PermissionDenied` or `FailedPrecondition`) goes straight to `30s` and logs at Error.
- **TLS only:**
  - The manager uses system roots plus an optional `ca-file` (PEM) and an optional `server-name`.
  - There is no plaintext option, because the bearer token must never cross in clear.
- **Secrets:**
  - The token is read from `token-file` with surrounding whitespace trimmed, and never logged.
  - `LaunchCreds` and frame payloads are never logged (plan 2's rule).
  - The cove-master log tail is the one payload Jam logs, exactly as `local` logs it today.
- **Capacity:**
  - `capacity: 0` means unlimited.
  - Otherwise a raise is declined with `at capacity (<n>/<cap> studios)`, where n is the labelled studios plus raises in flight.
- **Hermetic tests:** fakes, `runner.Fake`, `MemStore` and `bufconn`. No Docker and no network. Use TDD.
- **Go commands** in this sandbox: prefix them with `GOPROXY=https://proxy.golang.org GOFLAGS=`.

## Review Focus

1. **Jam restarts or is unreachable when the manager starts.** The manager keeps retrying at most every 30s, never touches a running studio, and reconnects on its own once Jam is back. Task 7 adds `TestServeReconnectsAfterJamRestart`.
2. **The manager restarts while studios run.** Its Hello inventory, read from labels, reports them, so Jam keeps them Live and does not mark them Lost. Task 4 adds `TestStudiosListsOnlyThisManager`, and Task 7 adds `TestServeReportsRunningStudiosInHello`.
3. **A studio's container is already gone at teardown** (a Mac reboot removed the `--rm` containers). Teardown succeeds, so Jam drops the record instead of retrying forever. Task 3 adds `TestRemoveContainerAbsentIsSuccess`.
4. **`local` and a remote manager share one Colima daemon.** Neither one's inventory or StateOwners includes the other's studios or volumes. Task 4 adds `TestRemoteManagerStateScopedAwayFromLocal`.
5. **A wrong or rotated token, or a version mismatch.** The manager logs one clear Error line with Jam's reason and retries at 30s, never hot-looping. Task 7 adds `TestLoopRefusalBacksOffToMax`.

---

### Task 1: Welcome before attach, the SSH user CA in Welcome, and the forced-revoke race

**Files:**
- Modify: `internal/jam/managers/proto/manager.proto` (`Welcome`), then regenerate `internal/jam/managers/managerpb/` with `just buf-gen`
- Modify: `internal/jam/managers/hub.go` (`Config.SSHUserCA`, `admit`, `serve`)
- Modify: `internal/jam/managers/client/client.go` (`Config.OnWelcome`)
- Modify: `cmd/at-jam/main.go` (pass `sshUserCA` to `managers.NewHub`)
- Modify: `docs/usage/jam/studio-managers.md` (the "Enable it" warning)
- Test: `internal/jam/managers/hub_test.go`, `internal/jam/managers/client/client_test.go`, `internal/jam/fleet/supervisor_integration_test.go`

**Interfaces:**
- Produces:
  - proto `message Welcome { bytes ssh_user_ca = 1; }`, which gives Go `(*managerpb.Welcome).GetSshUserCa() []byte`;
  - `managers.Config.SSHUserCA []byte`;
  - `client.Config.OnWelcome func(w *managerpb.Welcome)`, which may be nil. It is called synchronously once Welcome arrives and before any frame is served.

- [ ] **Step 1: Write the failing tests**
  - `TestHubWelcomeBeforeRoutable` (hub_test). A raw test stream sends Hello. The test then reads the first message, while a goroutine calls `remote.Probe` as soon as `env.available("m1")` turns true. The first `ManagerDown` the stream sees must be a `Welcome`, never a `Frame`. Repeat 50 times; the old order fails within a few.
  - `TestHubWelcomeCarriesSSHUserCA` (hub_test). With `Config.SSHUserCA = []byte("ssh-ed25519 AAAA ca")`, the Welcome's `GetSshUserCa()` equals that value.
  - `TestRunCallsOnWelcomeBeforeServing` (client_test). `OnWelcome` records the CA. The first request served sees the CA already recorded.
  - `TestRaiseLandingAfterForcedRevokeConverges` (fleet `supervisor_integration_test.go`):
    - Store an actor and a `PhaseLive` instance with `Manager: "gone"`, a manager the router does not have. This is the state a raise leaves when its success lands after `ForgetManager` ran.
    - Run `sup.Reconcile(ctx)` at most twice.
    - Assert `store.GetInstance("w1")` is not found, or is `PhaseGone`, and the actor is removed.
    - If this already passes, keep it as the pin and note that in the commit.

- [ ] **Step 2: Run them and see the first three fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/managers/... ./internal/jam/fleet/ -run 'Welcome|OnWelcome|ForcedRevokeConverges'`
Expected: a FAIL for the Welcome and OnWelcome tests (the field does not exist; Welcome is sent after attach).

- [ ] **Step 3: Implement**
  - **The proto field:** add it, then run `just buf-gen`.
  - **Where `admit` sends Welcome:**
    - after the token re-check and the reconcile, still under `regMu` but before taking `h.mu`;
    - so before `remote.attach`;
    - using the same `send` func, which `admit` now takes as a parameter.

    A send error returns that error, and nothing is attached.
  - **`serve`** no longer sends Welcome. It keeps the `Router.Kick()` and the "manager connected" log.
  - **The client:** in `client.Run`, call `cfg.OnWelcome(m.GetWelcome())` when it is set, before setting `welcomed = true`.
  - **`main.go`:** pass `SSHUserCA: sshUserCA` to `managers.NewHub`.
  - **The doc:** replace the "Enable it" warning's "That is the same as a forced revoke (below)." with:

    > Jam then probes their studios as dead: each is torn down (a no-op, since no manager is registered to call), its identity revoked and its record dropped; the containers stay on the host.

- [ ] **Step 4: Run the tests and the packages**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/... ./cmd/at-jam/`
Expected: PASS

- [ ] **Step 5: Commit**: `fix(jam): send Welcome before a manager stream is routable; carry the SSH user CA`

---

### Task 2: The teardown log tail travels in the Teardown result

**Files:**
- Modify: `internal/jam/launcher/launcher.go` (`TeardownCapture`; `Teardown` calls it)
- Modify: `internal/jam/managers/wire/ops.go` (`TeardownResult`, `TeardownCapturer`)
- Modify: `internal/jam/managers/client/client.go` (`OpTeardown`)
- Modify: `internal/jam/managers/remote.go` (`Teardown` logs the tail)
- Test: `internal/jam/launcher/launcher_test.go`, `internal/jam/managers/client/client_test.go`, `internal/jam/managers/remote_test.go`

**Interfaces:**
- Produces:
  - `func (l *Launcher) TeardownCapture(ctx context.Context, inst jam.Instance) (agentLog string, err error)`. It does what `Teardown` does today, but returns the tail instead of logging it.
  - `Teardown` becomes `TeardownCapture` plus the existing Warn log line when the tail is not empty. Its message is unchanged: `cove agent log (tail, captured on teardown)`.
  - `wire.TeardownResult struct{ AgentLog string }`.
  - `wire.TeardownCapturer interface{ TeardownCapture(ctx context.Context, inst jam.Instance) (string, error) }`.

- [ ] **Step 1: Write the failing tests**
  - `TestTeardownCaptureReturnsTail` (launcher_test). The fake runner's ssh `tail` returns `"boom\n"`. `TeardownCapture` returns `"boom"` and removes the container. `Teardown`'s existing log test still passes.
  - `TestServeTeardownReturnsAgentLog` (client_test). The launcher implements `TeardownCapture`, returning `"why it died"`. The decoded `wire.TeardownResult.AgentLog` equals `"why it died"`.
  - `TestServeTeardownPlainLauncher` (client_test). A launcher without `TeardownCapture` still tears down, and `AgentLog` is `""`.
  - `TestRemoteTeardownLogsAgentLog` (remote_test). The fake manager replies with `TeardownResult{AgentLog: "why"}`. Jam's log contains `cove agent log (tail, captured on teardown)`, `manager=m1`, `id=<actor>` and `why`.

- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/launcher/ ./internal/jam/managers/... -run 'TeardownCapture|TeardownReturnsAgentLog|TeardownPlainLauncher|RemoteTeardownLogs'`
Expected: FAIL (undefined: TeardownCapture, TeardownResult)

- [ ] **Step 3: Implement**
  - **The client:** for `OpTeardown`, use `wire.TeardownCapturer` when `cfg.Launcher` has it, otherwise `Teardown`. Reply with `wire.TeardownResult{AgentLog: tail}`.
  - **`Remote.Teardown`:** decode into `TeardownResult` and Warn-log a non-empty tail with `"id", inst.ActorID, "manager", r.name, "log", tail`.

    An older reply body (`{}`) decodes to an empty tail.

- [ ] **Step 4: Run the packages**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/...`
Expected: PASS

- [ ] **Step 5: Commit**: `feat(jam): return the cove-master log tail in a remote teardown`

---

### Task 3: Colima backend: labels, listing containers, idempotent removal

**Files:**
- Modify: `internal/backend/backend.go` (the `DispatchOps.RunEphemeral` signature; the new `ContainerLister`)
- Modify: `internal/backend/colima/dispatch.go` (`RunEphemeral`, `RemoveContainer`, `ListContainers`)
- Modify: `internal/jam/launcher/launcher.go` (the `RunEphemeral` call site passes `[]string{Label}` for now; Task 4 adds the rest)
- Modify: `internal/jam/launcher/launcher_test.go` (the fake's signature)
- Test: `internal/backend/colima/dispatch_test.go`

**Interfaces:**
- Produces:
  - `RunEphemeral(image, digest, name string, labels []string, dns, addHosts []string, docker bool, mounts ...backend.Mount) (backend.Instance, error)`. It adds one `--label <l>` per entry, in order, where the single `--label` is today.
  - `backend.ContainerLister interface{ ListContainers(key string, match ...string) (map[string]string, error) }`, which maps container name to the value of `key`. It filters like `ListVolumes`, but over `docker ps`, which includes paused containers.
  - `(*Colima).ListContainers`, which runs `docker [--context c] ps --filter label=<key> [--filter label=<m>]... --format {{.Names}}\t{{.Label "<key>"}}`.
  - `(*Colima).RemoveContainer`, where a daemon `No such container` is success. Use `dockerIdempotent("No such container", ...)`.

- [ ] **Step 1: Write the failing tests**
  - `TestRunEphemeralLabels`. Labels `["harbor.cove", "harbor.cove.actor=w1"]` give argv containing `--label harbor.cove --label harbor.cove.actor=w1`, in that order.
  - `TestListContainersArgvAndParse`. The argv is exactly as above for key `harbor.cove.actor` and match `harbor.cove.manager=m1`. The output `"cove-w1\tw1\ncove-w2\tw2\n"` parses to `{"cove-w1":"w1","cove-w2":"w2"}`, and empty output gives an empty map.
  - `TestRemoveContainerAbsentIsSuccess`. A `runner.Fake` whose `rm -f` exits 1 with `Error response from daemon: No such container: cove-w1` makes `RemoveContainer` return nil. Any other failure is still returned.

- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/backend/... -run 'RunEphemeralLabels|ListContainers|RemoveContainerAbsent'`
Expected: FAIL (compile errors on the new signature and on ListContainers)

- [ ] **Step 3: Implement** the signature change, `ListContainers` and the idempotent remove. Fix every caller and fake: `grep -rn RunEphemeral --include=*.go`.

- [ ] **Step 4: Run the packages**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/backend/... ./internal/jam/launcher/ && GOPROXY=https://proxy.golang.org GOFLAGS= go build ./...`
Expected: PASS, and the build succeeds

- [ ] **Step 5: Commit**: `feat(colima): multi-label runs, label-filtered container listing, idempotent remove`

---

### Task 4: Launcher: manager scoping, studio inventory, settable SSH CA

**Files:**
- Modify: `internal/jam/launcher/launcher.go`
- Create: `internal/jam/launcher/studios.go` (`Studios`)
- Test: `internal/jam/launcher/launcher_test.go`, `internal/jam/launcher/studios_test.go`

**Interfaces:**
- Consumes: Task 3's `labels []string` and `backend.ContainerLister`.
- Produces:
  - `const ActorLabel = "harbor.cove.actor"` and `const ManagerLabel = "harbor.cove.manager"`.
  - `Config.Manager string`. Empty means `local`, which keeps today's behaviour. When it is set, the JamID default becomes `RuntimeAddr + "#" + Manager`.
  - The labels a raise passes: `[Label, ActorLabel+"="+spec.ActorID]`, plus `ManagerLabel+"="+Manager` when it is set.
  - The labels a state volume is created with: the same, plus `ManagerLabel` when it is set.
  - `StateOwners` additionally matches `ManagerLabel+"="+Manager` when it is set.
  - `func (l *Launcher) Studios(ctx context.Context) ([]jam.ManagedStudio, error)`:
    - it lists by `ActorLabel`, matching `ManagerLabel=<Manager>`;
    - `Location` is the container name and `ActorID` is the label value;
    - results are sorted by ActorID;
    - with no manager name, it returns `nil, nil` (local has no inventory);
    - an `Ops` without `ContainerLister` is an error.
  - `func (l *Launcher) SetSSHUserCA(pub []byte)`, which is safe concurrently with Raise. `installSSHUserCA` reads it under a mutex.

- [ ] **Step 1: Write the failing tests**
  - `TestRaiseLabelsLocal`. With no manager name, the fake records the labels `["harbor.cove","harbor.cove.actor=w1"]`.
  - `TestRaiseLabelsManager`. With `Manager: "m1"`, the labels gain `harbor.cove.manager=m1`, and the state volumes of a standing raise carry `harbor.cove.manager=m1` and `harbor.cove.jam=<addr>#m1`.
  - `TestRemoteManagerStateScopedAwayFromLocal`:
    - One fake `Ops` holds the volumes of a local-created actor (`jam=<addr>`) and an m1-created actor (`jam=<addr>#m1`, `manager=m1`).
    - The local launcher's `StateOwners` returns only the local actor, and m1's returns only the m1 actor.
    - The fake's `ListVolumes` must honour match filters, so extend it if needed.
  - `TestStudiosListsOnlyThisManager`. The fake's containers are `cove-a` (manager m1), `cove-b` (manager m2) and `cove-c` (no manager label). m1's `Studios` returns `[{ActorID:"a", Location:"cove-a"}]`.
  - `TestStudiosLocalIsEmpty`.
  - `TestSetSSHUserCAUsedByNextRaise`. After `SetSSHUserCA([]byte("ca"))`, a raise installs `"ca"`. After `SetSSHUserCA(nil)`, nothing is installed. Run it under `-race`, with a Raise concurrent with the Set.

- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test -race ./internal/jam/launcher/ -run 'RaiseLabels|StateScoped|Studios|SetSSHUserCA'`
Expected: FAIL

- [ ] **Step 3: Implement** the items listed under Produces.

- [ ] **Step 4: Run the packages**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test -race ./internal/jam/launcher/ ./internal/backend/... ./cmd/at-jam/`
Expected: PASS

- [ ] **Step 5: Commit**: `feat(launcher): manager-scoped labels and state, studio inventory, settable SSH CA`

---

### Task 5: Shared `colima` verbs

**Files:**
- Create: `internal/colimacmd/colimacmd.go`, moved from `cmd/at-jam/colima.go`
- Create: `internal/colimacmd/colimacmd_test.go`, moved from `cmd/at-jam/colima_test.go`
- Modify: `cmd/at-jam/main.go` (the `colima` command now calls `colimacmd.Command("at-jam", ...)`)
- Delete: `cmd/at-jam/colima.go`, `cmd/at-jam/colima_test.go`
- Modify: `internal/colimacfg/` (add `ValidDockerContext`); `cmd/at-jam/config.go` uses it instead of `dockerContextRe`

**Interfaces:**
- Produces:
  - `func Command(prog string, getenv func(string) string, r runner.Runner) func([]string, cli.Globals, io.Writer, io.Writer) int`. It behaves exactly like today's `cmdColima`, with every message prefix `"at-jam colima"` replaced by `prog + " colima"`.
  - `func colimacfg.ValidDockerContext(name string) bool`, using docker's rule `^[a-zA-Z0-9][a-zA-Z0-9_.+-]*$`.

- [ ] **Step 1: Move the tests**
  - Move the tests, with `prog = "at-jam"`, so their expected strings are unchanged.
  - Add `TestCommandUsesProgName`: with `prog = "at-studio-manager"`, no arguments prints `at-studio-manager colima: expected setup-docker|check-docker` and returns 2.
  - Add `TestValidDockerContext`: `colima` and `colima-work` are valid; `""`, `-x` and `a b` are not.

- [ ] **Step 2: Run them and see the new ones fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/colimacmd/ ./internal/colimacfg/`
Expected: FAIL (the package does not exist)

- [ ] **Step 3: Move the code and parameterize the prefix.** Behaviour is otherwise unchanged.

- [ ] **Step 4: Run the packages**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/colimacmd/ ./internal/colimacfg/ ./cmd/at-jam/`
Expected: PASS

- [ ] **Step 5: Commit**: `refactor: move the colima verbs into internal/colimacmd for both binaries`

---

### Task 6: Manager config

**Files:**
- Create: `internal/studiomanager/config.go`
- Test: `internal/studiomanager/config_test.go`

**Interfaces:**
- Produces:

```go
type Config struct {
	Name       string       `yaml:"name"`        // required; jam.ValidManagerName, not "local"
	JamAddr    string       `yaml:"jam-addr"`    // required host:port of Jam's cove-facing TLS listener
	ServerName string       `yaml:"server-name"` // optional TLS name override; default the jam-addr host
	CAFile     string       `yaml:"ca-file"`     // optional PEM added to the system roots
	TokenFile  string       `yaml:"token-file"`  // default <dir>/token
	Capacity   int          `yaml:"capacity"`    // max studios; 0 = unlimited; negative is an error
	LogLevel   string       `yaml:"log-level"`   // debug|info|warn|error; default info
	Colima     ColimaConfig `yaml:"colima"`
}
type ColimaConfig struct {
	RuntimeAddr   string   `yaml:"runtime-addr"`    // required: the Jam address studios dial
	JamHost       string   `yaml:"jam-host"`        // required: the Jam host name studios resolve
	DockerContext string   `yaml:"docker-context"`  // colimacfg.ValidDockerContext; "" = colima
	Docker        bool     `yaml:"docker"`
	DNS           []string `yaml:"dns"`
	IdentityFile  string   `yaml:"identity-file"`   // default <dir>/id_ed25519
	KnownHostsDir string   `yaml:"known-hosts-dir"` // default <dir>/known_hosts.d
}
func DefaultDir(getenv func(string) string) string // $XDG_CONFIG_HOME/at-studio-manager, else $HOME/.config/at-studio-manager
func Load(path string, getenv func(string) string) (Config, error) // strict YAML (KnownFields), then defaults, then validation
func (c Config) Token() (string, error) // reads TokenFile, trims whitespace; empty is an error naming the file
```

- [ ] **Step 1: Write the failing tests**
  - `TestLoadMinimal`. A file with only `name`, `jam-addr`, `colima.runtime-addr` and `colima.jam-host` loads with these defaults, where `<dir>` is `DefaultDir`:
    - `TokenFile` is `<dir>/token`;
    - `IdentityFile` is `<dir>/id_ed25519`;
    - `KnownHostsDir` is `<dir>/known_hosts.d`;
    - `LogLevel` is `info`.
  - `TestLoadRejects`, table-driven. Each row is refused with an error naming the field:
    - a missing `name`;
    - `name: local`;
    - `name: Bad_Name`;
    - a missing `jam-addr`;
    - `jam-addr: jam.example.com` (no port);
    - `capacity: -1`;
    - a missing `colima.runtime-addr`;
    - a missing `colima.jam-host`;
    - `docker-context: "a b"`;
    - `log-level: loud`;
    - an unknown key `colour: red`.
  - `TestTokenTrimsAndRejectsEmpty`. A file `"tok123\n"` gives `tok123`. A file holding only whitespace is an error containing the path. A missing file is an error.
  - `TestDefaultDir`. `XDG_CONFIG_HOME=/x` gives `/x/at-studio-manager`. Otherwise, `HOME=/h` gives `/h/.config/at-studio-manager`.

- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/studiomanager/`
Expected: FAIL (the package does not exist)

- [ ] **Step 3: Implement `Config`, `ColimaConfig`, `DefaultDir`, `Load` and `Token`.** Validate `jam-addr` with `net.SplitHostPort`, requiring a non-empty host and port.

- [ ] **Step 4: Run the tests**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/studiomanager/`
Expected: PASS

- [ ] **Step 5: Commit**: `feat(studiomanager): config file`

---

### Task 7: Manager runtime: launcher stack, capacity gate, reconnect loop

**Files:**
- Create: `internal/studiomanager/capacity.go`
- Create: `internal/studiomanager/loop.go`
- Create: `internal/studiomanager/serve.go`
- Test: `internal/studiomanager/capacity_test.go`, `internal/studiomanager/loop_test.go`, `internal/studiomanager/serve_test.go`

**Interfaces:**
- Consumes:
  - Task 1's `client.Config.OnWelcome` and `GetSshUserCa`;
  - Task 2's `wire.TeardownCapturer`;
  - Task 4's `launcher.Config.Manager`, `(*Launcher).Studios` and `(*Launcher).SetSSHUserCA`;
  - Task 6's `Config`.
- Produces:

```go
// gated is what a manager serves: the Colima launcher (embedded concretely so
// its optional interfaces stay visible to client.Run) gated by capacity.
type gated struct {
	*launcher.Launcher
	capacity int // 0 = unlimited
	mu       sync.Mutex
	inflight int
}
func (g *gated) Raise(ctx context.Context, spec jam.RaiseSpec, creds jam.LaunchCreds) (string, error)

type Loop struct {
	Run   func(ctx context.Context, welcomed func()) error // one session; calls welcomed once Welcome arrives
	Sleep func(ctx context.Context, d time.Duration)       // nil: a timer that returns early when ctx ends
	Log   *slog.Logger
}
func (l Loop) Serve(ctx context.Context) error // until ctx ends; then nil

// Deps are Serve's seams; zero values are the real ones.
type Deps struct {
	Runner runner.Runner                                       // nil: runner.OS{}
	Dial   func(cfg Config) (grpc.ClientConnInterface, error)  // nil: TLS grpc.NewClient
	Ops    launcher.Backend                                    // nil: colima.NewWithContext(Runner, cfg.Colima.DockerContext)
	Images jam.ImageFactsReader                                // nil: imagefacts.New(Runner, the colima backend)
	Sleep  func(ctx context.Context, d time.Duration)          // nil: Loop's default; tests shorten the backoff
}
func Serve(ctx context.Context, cfg Config, version string, log *slog.Logger, deps Deps) error
```

- **The gate:**
  - It counts `len(Studios())` plus the raises in flight, under `mu`.
  - At or over capacity, it returns `fmt.Errorf("at capacity (%d/%d studios)", n, cap)` without calling the launcher.
  - A `Studios` error is returned as `capacity check: <err>`.
  - The in-flight count is decremented whatever Raise returns.
- **`Loop.Serve`:**
  - The backoff starts at 1s, doubles, caps at 30s, and resets after `welcomed`.
  - A refusal (`status.Code` of `Unauthenticated`, `PermissionDenied` or `FailedPrecondition`) goes straight to 30s and logs Error `manager: Jam refused the connection` with `err`.
  - Other ends log Warn `manager: disconnected from Jam`, with `err` and `retry`.
  - Run returning after ctx ends leaves `Serve` returning nil.
- **`Serve`:**
  - It reads the token, then ensures the identity key and the known-hosts dir, reusing `keys.EnsureFile` and `os.MkdirAll(dir, 0o700)` as at-jam does.
  - It builds:
    - `launcher.New`, with `Manager: cfg.Name`, `JamHost`, `RuntimeAddr`, `IdentityFile`, `KnownHostsDir`, `DNS`, `Docker`, `PublicKey`, and `Subscription: false`;
    - the gate;
    - the dialer.
  - It runs `Loop` with `client.Run`, where `Inventory` is `Launcher.Studios`, `Images` is the image reader, `Capacity` is `cfg.Capacity`, and `OnWelcome` calls `SetSSHUserCA(w.GetSshUserCa())`, logs Info `manager: connected to Jam` and calls `welcomed`.
  - The real dial is `grpc.NewClient(cfg.JamAddr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: system roots plus CAFile, ServerName: cfg.ServerName, MinVersion: tls.VersionTLS12})))`.

- [ ] **Step 1: Write the failing tests**
  - `TestGatedDeclinesAtCapacity`. With capacity 1 and a fake `Ops` listing one m1 container, Raise fails with `at capacity (1/1 studios)` and the backend's `RunEphemeral` is not called.
  - `TestGatedCountsInflight`. Capacity is 1, with no containers. A first Raise blocks inside the fake's `RunEphemeral`, and a concurrent second Raise is declined with `at capacity (1/1 studios)`. After the first returns, a third is admitted. Run it with `-race`.
  - `TestGatedUnlimited`. Capacity 0 never calls `Studios`.
  - `TestLoopBackoffDoublesAndCaps`. `Run` fails 7 times with a plain error. The recorded sleeps are `1s, 2s, 4s, 8s, 16s, 30s, 30s`.
  - `TestLoopResetsAfterWelcome`. Run fails twice, then calls `welcomed` and fails, then fails again. The sleeps are `1s, 2s, 1s, 2s`.
  - `TestLoopRefusalBacksOffToMax`. A first Run returning `status.Error(codes.FailedPrecondition, "version mismatch: jam A, manager B")` sleeps 30s. The log has exactly one Error line, containing `refused` and `version mismatch`.
  - `TestLoopStopsOnCtx`. Cancelling ctx during Sleep returns nil promptly.
  - `TestServeReportsRunningStudiosInHello`, an e2e test:
    - Set up a real `managers.NewHub` with `jam.NewMemStore()`, `fleet.New(log)` and `Version: "v1"`, registered on a `bufconn` gRPC server, with `hub.Add("m1")` writing the token file.
    - Run `Serve` with `Deps.Dial` returning a bufconn client and `Deps.Ops` as a fake `launcher.Backend` with `ListContainers`, listing `cove-w1` with actor `w1` and manager `m1`.
    - Wait until `hub.Statuses()` shows m1 connected.
    - With no Reconciler set, it is enough that `Hello` reached Jam. Use a `Reconciler` stub recording the inventory, and assert it got `{ActorID:"w1", Location:"cove-w1"}`.
  - `TestServeReconnectsAfterJamRestart`, an e2e test:
    - Start as above, then stop the gRPC server.
    - Start a new server with the same hub on a new bufconn listener that `Dial` now returns.
    - m1 shows connected again.
    - The fake `Ops` saw no `RemoveContainer`, `Pause` or `Unpause`.
    - Use a fast `Deps.Sleep`, so the test runs in well under 5s.
  - `TestServeSetsSSHUserCAFromWelcome`, an e2e test. The hub has `SSHUserCA` `"ca-line"`. A raise through `router.RaiseOn` on m1 makes the fake backend's `InstallSSHUserCA` receive `"ca-line"`.

    The fake must also satisfy `backend.SSHCA`, and the raise needs the fake runner's ssh calls to succeed, so use `runner.Fake` set to succeed.

- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test -race ./internal/studiomanager/`
Expected: FAIL (undefined: gated, Loop, Serve)

- [ ] **Step 3: Implement** `gated`, `Loop` and `Serve` as specified. `Serve` logs no secrets: neither the token nor any payload.

- [ ] **Step 4: Run the package**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test -race ./internal/studiomanager/ ./internal/jam/managers/...`
Expected: PASS

- [ ] **Step 5: Commit**: `feat(studiomanager): capacity gate, reconnect loop and serve`

---

### Task 8: The `at-studio-manager` binary and release packaging

**Files:**
- Create: `cmd/at-studio-manager/main.go`, `cmd/at-studio-manager/main_test.go`
- Modify: `scripts/build.sh` (`BINARIES`)
- Modify: `.goreleaser.yaml` (a build `id: at-studio-manager`, added to the archive `ids`)
- Modify: `scripts/release/check-archive.sh` (expects `at-cove at-jam at-mint at-studio-manager`)
- Modify: `install.sh` (installs `at-studio-manager`, skipping it when the archive lacks it, as for at-jam)
- Modify: `justfile` (`install` loop)

**Interfaces:**
- Consumes: Task 5's `colimacmd.Command`; Task 6's `Load` and `DefaultDir`; Task 7's `Serve`.
- Produces:
  - `var version = "dev"`;
  - `func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int`;
  - the `cli.App{Name: "at-studio-manager", Version: version}` commands below.

  | Command | Behaviour |
  |---|---|
  | `run [--config <path>]` | The default path is `DefaultDir(getenv)/config.yml`. It loads the config, builds a text `slog` logger to stderr at `log-level`, runs `Serve` until SIGINT or SIGTERM, and exits 0. A config error prints `at-studio-manager run: <err>` and exits 2. |
  | `colima` | `colimacmd.Command("at-studio-manager", getenv, runner.OS{})` |

- [ ] **Step 1: Write the failing tests**
  - `TestRunBadConfigExits2`. `run --config <missing>` returns 2, and stderr starts with `at-studio-manager run:`.
  - `TestVersion`. `run([]string{"version"}, ...)` prints `at-studio-manager dev`.
  - `TestColimaWired`. `colima` with no arguments prints `at-studio-manager colima: expected setup-docker|check-docker`.

- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./cmd/at-studio-manager/`
Expected: FAIL

- [ ] **Step 3: Implement** `main.go` and the packaging edits.
  - `install.sh`'s loop becomes `for b in at-mint at-jam at-studio-manager`, and skips any missing binary other than at-mint.
  - The success line in `check-archive.sh` names all four.

- [ ] **Step 4: Verify**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./cmd/at-studio-manager/ && GOPROXY=https://proxy.golang.org GOFLAGS= go build ./... && bash -n install.sh scripts/build.sh scripts/release/check-archive.sh`
Expected: PASS, a clean build, and no syntax errors

- [ ] **Step 5: Commit**: `feat: at-studio-manager binary, shipped in the release archive`

---

### Task 9: Docs

**Files:**
- Create: `docs/usage/studio-manager.md`, a leaf (the binary: install, config, run, colima verbs, labels, capacity, reconnect)
- Modify: `docs/usage/jam/studio-managers.md` (drop "arrives in a later plan"; link the new leaf; one line on the teardown log tail)
- Modify: `docs/usage/docker-in-sandbox.md` (the verbs also run as `at-studio-manager colima …` on a manager host; no copy)
- Modify: `docs/usage/INDEX.md` and `docs/INDEX.md` (a row for the new leaf)

**Interfaces:** none (docs).

- [ ] **Step 1: Write `docs/usage/studio-manager.md`** with the docs-author skill. It needs the frontmatter `summary`, `read_when`, `owns`, `prereqs`, `tier: leaf` and `updated: 2026-10-26`. It covers:
  - the full config example from Task 6, with each field's default;
  - the token file;
  - TLS (system roots, `ca-file`, `server-name`);
  - `at-studio-manager run`;
  - the backoff;
  - capacity and the decline text;
  - the labels and why `local` and a manager can share a daemon;
  - that a different `identity-file` from `local`'s means separate image builds.

  Facts owned by `studio-managers.md` (registration, auth, lifecycle) are linked, not repeated.
- [ ] **Step 2: Make the edits to the other three docs.**
- [ ] **Step 3: Run the docs-audit skill's checker** and fix every finding.
- [ ] **Step 4: Commit**: `docs: the at-studio-manager binary`

---

## Coverage (spec to task)

| Spec item | Task |
|---|---|
| The manager binary, wrapping launcher, Colima and imagefacts | 7, 8 |
| Manager config (`runtime.launcher` fields plus name, token, Jam URL, CA, capacity) | 6 |
| The colima verbs move to the manager | 5, 8 |
| Manager-name labels; `local` and remote sharing one daemon | 3, 4 |
| A manager restart rebuilds its inventory from labels | 4, 7 |
| The teardown log tail in the Teardown result | 2 |
| Declining at capacity | 7 |
| TLS: the manager verifies Jam with a CA bundle | 7 |
| Welcome-before-attach and the forced-revoke race (left over from plan 2) | 1 |
