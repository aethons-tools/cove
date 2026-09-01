# Workspace Visibility (VS Code Remote-SSH + git) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator connect VS Code Remote-SSH and git to an isolated at-cove sandbox through a permanently-stable SSH alias, with no new component in the image and no change to the egress boundary.

**Architecture:** A new `internal/sshconfig` package holds two pure functions — an OpenSSH `Host`-block renderer and an idempotent `~/.ssh/config` managed-block upsert. Two new `at-cove` subcommands wire them: `ssh-proxy <collaborator>` is a `ProxyCommand` target that resolves the sandbox's rotating ephemeral port via the backend `Dial` and relays the byte stream; `view <collaborator>` prints (or `--write`s) a `Host` block whose `ProxyCommand` calls `ssh-proxy`, plus a git-over-SSH remote line. The alias stays valid across `recreate` because the port is resolved at connect time and the host-key pin lives in the per-sandbox `known_hosts.d/<container>` file that `recreate` already reaps.

**Tech Stack:** Go (stdlib only — `net`, `io`, `strings`, `os/exec` via the existing `runner.Runner`), the repo's `internal/cli` command registry, `internal/backend` (`Dial`/`Endpoint`), `internal/keys`, `internal/state`.

## Global Constraints

- Module `github.com/aethons-tools/cove`; binaries `at-cove`, `at-task`. Add subcommands only to `cmd/at-cove/main.go`'s `cli.App.Commands` slice — no new binary.
- Tests are **hermetic**: drive `internal/runner.Fake`; no Docker, VM, or external network. In-process `127.0.0.1` sockets are allowed (they are not external egress). Any real Remote-SSH round-trip goes behind the `//go:build integration` tag.
- **No egress change.** This feature must not add any domain to `image.allowed-domains` or the squid allow-list. Server transport is the client-side `remote.SSH.localServerDownload: "always"` setting, documented, not built.
- **No sealed-file / hardening edits.** Nothing under `internal/assemble/hardening/`.
- SSH constants copied verbatim from existing code: user `agent`; identity `keys.Ensure(r, configDir())` → `<configDir>/id_ed25519`; per-sandbox known-hosts `filepath.Join(configDir(), "known_hosts.d", <container>)`; `configDir()` = `$XDG_CONFIG_HOME/at-cove` or `~/.config/at-cove`.
- Follow TDD: failing test first, minimal impl, frequent commits. DRY, YAGNI.
- Per repo rule (AGENTS.md): no task is done until its docs are updated in the same change.

---

### Task 1: `internal/sshconfig` — pure Host-block renderer + git remote URL

**Files:**
- Create: `internal/sshconfig/sshconfig.go`
- Test: `internal/sshconfig/sshconfig_test.go`

**Interfaces:**
- Consumes: nothing (leaf package, stdlib only).
- Produces:
  - `type HostParams struct { Alias, ProxyCommand, User, IdentityFile, KnownHostsFile string }`
  - `func RenderHostBlock(p HostParams) string`
  - `func GitRemoteURL(alias string) string` (returns `alias + ":/home/agent/workspace"`)

- [ ] **Step 1: Write the failing test**

```go
package sshconfig

import "testing"

func TestRenderHostBlock(t *testing.T) {
	got := RenderHostBlock(HostParams{
		Alias:          "cove-demo-agent",
		ProxyCommand:   "/usr/local/bin/at-cove ssh-proxy --project-dir /p demo",
		User:           "agent",
		IdentityFile:   "/home/u/.config/at-cove/id_ed25519",
		KnownHostsFile: "/home/u/.config/at-cove/known_hosts.d/demo-agent",
	})
	want := `Host cove-demo-agent
    HostName cove-demo-agent
    User agent
    IdentityFile /home/u/.config/at-cove/id_ed25519
    IdentitiesOnly yes
    UserKnownHostsFile /home/u/.config/at-cove/known_hosts.d/demo-agent
    StrictHostKeyChecking accept-new
    ProxyCommand /usr/local/bin/at-cove ssh-proxy --project-dir /p demo
`
	if got != want {
		t.Fatalf("RenderHostBlock mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestGitRemoteURL(t *testing.T) {
	if got := GitRemoteURL("cove-demo-agent"); got != "cove-demo-agent:/home/agent/workspace" {
		t.Fatalf("GitRemoteURL = %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sshconfig/`
Expected: FAIL — `undefined: RenderHostBlock` / `undefined: HostParams` / `undefined: GitRemoteURL`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package sshconfig renders OpenSSH client config for reaching an at-cove
// sandbox — a ProxyCommand-based Host block whose alias stays valid across a
// `recreate` even though the container's ephemeral ssh port rotates.
package sshconfig

import (
	"fmt"
	"strings"
)

// HostParams are the inputs to a sandbox Host block.
type HostParams struct {
	Alias          string // Host alias, e.g. "cove-<container>"
	ProxyCommand   string // full ProxyCommand line (at-cove ssh-proxy ...)
	User           string // "agent"
	IdentityFile   string // <configDir>/id_ed25519
	KnownHostsFile string // <configDir>/known_hosts.d/<container>
}

// RenderHostBlock renders an OpenSSH client-config Host block. HostName is the
// alias itself: ProxyCommand supplies the transport, so HostName serves only as
// the known_hosts key — a stable alias keeps the host-key pin stable even though
// the real port rotates each boot. accept-new + the per-sandbox known_hosts file
// re-pin the regenerated key after a recreate without a MITM prompt.
func RenderHostBlock(p HostParams) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Host %s\n", p.Alias)
	fmt.Fprintf(&b, "    HostName %s\n", p.Alias)
	fmt.Fprintf(&b, "    User %s\n", p.User)
	fmt.Fprintf(&b, "    IdentityFile %s\n", p.IdentityFile)
	fmt.Fprintf(&b, "    IdentitiesOnly yes\n")
	fmt.Fprintf(&b, "    UserKnownHostsFile %s\n", p.KnownHostsFile)
	fmt.Fprintf(&b, "    StrictHostKeyChecking accept-new\n")
	fmt.Fprintf(&b, "    ProxyCommand %s\n", p.ProxyCommand)
	return b.String()
}

// GitRemoteURL is the git-over-SSH remote for the sandbox workspace, addressed
// through the Host alias so it rides the same ProxyCommand.
func GitRemoteURL(alias string) string {
	return alias + ":/home/agent/workspace"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/sshconfig/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/sshconfig/sshconfig.go internal/sshconfig/sshconfig_test.go
git commit -m "feat(sshconfig): render ProxyCommand-based Host block + git remote URL"
```

---

### Task 2: `internal/sshconfig` — idempotent `~/.ssh/config` managed-block upsert

**Files:**
- Modify: `internal/sshconfig/sshconfig.go` (add upsert)
- Test: `internal/sshconfig/upsert_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `func UpsertManagedBlock(cfg, alias, block string) string` — returns `cfg` with the per-alias at-cove managed block inserted (appended) or replaced. Idempotent: same inputs → identical output.

- [ ] **Step 1: Write the failing test**

```go
package sshconfig

import (
	"strings"
	"testing"
)

func TestUpsertManagedBlock_AppendThenIdempotent(t *testing.T) {
	base := "Host existing\n    HostName 10.0.0.1\n"
	block := "Host cove-demo\n    User agent\n"
	once := UpsertManagedBlock(base, "cove-demo", block)
	if !strings.Contains(once, "# >>> at-cove managed block: cove-demo >>>") ||
		!strings.Contains(once, "# <<< at-cove managed block: cove-demo <<<") {
		t.Fatalf("markers missing:\n%s", once)
	}
	if !strings.HasPrefix(once, base) {
		t.Fatalf("existing content not preserved:\n%s", once)
	}
	twice := UpsertManagedBlock(once, "cove-demo", block)
	if once != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestUpsertManagedBlock_Replace(t *testing.T) {
	first := UpsertManagedBlock("", "cove-demo", "Host cove-demo\n    Port 111\n")
	second := UpsertManagedBlock(first, "cove-demo", "Host cove-demo\n    Port 222\n")
	if strings.Contains(second, "Port 111") {
		t.Fatalf("old block not replaced:\n%s", second)
	}
	if strings.Count(second, "at-cove managed block: cove-demo >>>") != 1 {
		t.Fatalf("duplicate block:\n%s", second)
	}
	if !strings.Contains(second, "Port 222") {
		t.Fatalf("new block missing:\n%s", second)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sshconfig/ -run TestUpsertManagedBlock`
Expected: FAIL — `undefined: UpsertManagedBlock`.

- [ ] **Step 3: Write minimal implementation** (append to `sshconfig.go`)

```go
const (
	beginFmt = "# >>> at-cove managed block: %s >>>"
	endFmt   = "# <<< at-cove managed block: %s <<<"
)

// UpsertManagedBlock inserts or replaces the at-cove managed block for `alias`
// in an ~/.ssh/config body. The block is delimited by per-alias markers so
// multiple sandboxes coexist and re-running is idempotent.
func UpsertManagedBlock(cfg, alias, block string) string {
	begin := fmt.Sprintf(beginFmt, alias)
	end := fmt.Sprintf(endFmt, alias)
	managed := begin + "\n" + strings.TrimRight(block, "\n") + "\n" + end + "\n"

	if bi := strings.Index(cfg, begin); bi >= 0 {
		if rel := strings.Index(cfg[bi:], end); rel >= 0 {
			ei := bi + rel + len(end)
			rest := strings.TrimPrefix(cfg[ei:], "\n")
			return cfg[:bi] + managed + rest
		}
	}

	if cfg == "" {
		return managed
	}
	if !strings.HasSuffix(cfg, "\n") {
		cfg += "\n"
	}
	return cfg + "\n" + managed
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/sshconfig/`
Expected: PASS (both new tests and Task 1's).

- [ ] **Step 5: Commit**

```bash
git add internal/sshconfig/sshconfig.go internal/sshconfig/upsert_test.go
git commit -m "feat(sshconfig): idempotent ~/.ssh/config managed-block upsert"
```

---

### Task 3: `relay` byte-copy helper + `at-cove ssh-proxy` subcommand

**Files:**
- Create: `cmd/at-cove/sshproxy.go` (`relay`, `doSSHProxy`, `loadInstanceState`)
- Modify: `cmd/at-cove/main.go` (register the `ssh-proxy` command in the `Commands` slice, after `chat` at `main.go:112`)
- Test: `cmd/at-cove/sshproxy_test.go`

**Interfaces:**
- Consumes: `getBackend(name, r) (backend.Backend, error)` (`main.go:340`); `backend.Endpoint{Host,Port,User}`; `b.Dial(container) (backend.Endpoint, func(), error)`; `loadCurrentInstall(kitDir)` (`main.go:510`); `instanceFor(cfg, collaborator) (class string, hasCollab bool, instKey state.Instance, name string, err error)` (`main.go:537`); `state.LoadFor(kitDir, instKey) (state.State, error)`.
- Produces: `func relay(conn net.Conn, in io.Reader, out io.Writer) error`; `func doSSHProxy(collaborator, kitDir string, r runner.Runner, stderr io.Writer) error`; `func loadInstanceState(kitDir, collaborator string) (state.State, error)`.

**Note on `state` field names:** this task reads `st.Backend` and `st.Container` from the value `state.LoadFor` returns — the exact fields `doChat` uses (`main.go:840`, `main.go:1010`). Use those names verbatim; do not invent new ones.

- [ ] **Step 1: Write the failing test** (relay is the pure, hermetic core)

```go
package main

import (
	"io"
	"net"
	"strings"
	"testing"
)

// A local echo server stands in for the sandbox sshd; relay must shuttle bytes
// both ways over an in-process socket (no Docker, no external network).
func TestRelay_EchoRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		io.Copy(c, c) // echo
		c.Close()
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	in := strings.NewReader("ping\n")
	var out strings.Builder
	if err := relay(conn, in, &out); err != nil && err != io.EOF {
		t.Fatalf("relay: %v", err)
	}
	if out.String() != "ping\n" {
		t.Fatalf("echo mismatch: got %q", out.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-cove/ -run TestRelay_EchoRoundTrip`
Expected: FAIL — `undefined: relay`.

- [ ] **Step 3: Write minimal implementation** (`cmd/at-cove/sshproxy.go`)

```go
package main

import (
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/state"
)

// relay shuttles bytes between the ssh client (in→conn) and the sandbox sshd
// (conn→out), returning when either direction closes. out carries the raw ssh
// byte stream, so NOTHING else may be written to it — diagnostics go to stderr.
func relay(conn net.Conn, in io.Reader, out io.Writer) error {
	errc := make(chan error, 2)
	go func() { _, err := io.Copy(conn, in); errc <- err }()
	go func() { _, err := io.Copy(out, conn); errc <- err }()
	return <-errc
}

// loadInstanceState resolves a collaborator to its instance state, reusing the
// exact chain doChat uses (main.go:722): install manifest → instanceFor → state.
func loadInstanceState(kitDir, collaborator string) (state.State, error) {
	m, err := loadCurrentInstall(kitDir)
	if err != nil {
		return state.State{}, err
	}
	_, _, instKey, _, err := instanceFor(m.RunConfig, collaborator)
	if err != nil {
		return state.State{}, err
	}
	return state.LoadFor(kitDir, instKey)
}

// doSSHProxy is the ProxyCommand target: resolve the sandbox, discover its
// current (rotating) ssh port via the backend Dial, and relay stdio to it.
func doSSHProxy(collaborator, kitDir string, r runner.Runner, stdin io.Reader, stdout, stderr io.Writer) error {
	st, err := loadInstanceState(kitDir, collaborator)
	if err != nil {
		return err
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}
	ep, cleanup, err := b.Dial(st.Container)
	if err != nil {
		return err
	}
	defer cleanup()
	conn, err := net.Dial("tcp", net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port)))
	if err != nil {
		return fmt.Errorf("ssh-proxy: dial sandbox: %w", err)
	}
	defer conn.Close()
	return relay(conn, stdin, stdout)
}
```

- [ ] **Step 4: Register the command** in `cmd/at-cove/main.go`, immediately after the `chat` entry (`main.go:112`):

```go
{Name: "ssh-proxy", Brief: "ProxyCommand transport to a sandbox (used by `at-cove view` configs)", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
	fs := flag.NewFlagSet("ssh-proxy", flag.ContinueOnError)
	pd := projectDirFlag(fs)
	pos, code, ok := cli.ParseFlags(fs, args, out, errw)
	if !ok {
		return code
	}
	collaborator, kitDir, code := resolveCollaborator(*pd, pos, "ssh-proxy", errw)
	if code != 0 {
		return code
	}
	// stdout is the raw ssh byte channel; keep all diagnostics on stderr.
	return exitCode("at-cove", doSSHProxy(collaborator, kitDir, r, os.Stdin, out, errw), errw)
}},
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/at-cove/ -run TestRelay_EchoRoundTrip` then `go build ./...`
Expected: PASS, then a clean build (imports `os`, `net`, `strconv` resolve; `state.State`/`st.Backend`/`st.Container` compile).

- [ ] **Step 6: Commit**

```bash
git add cmd/at-cove/sshproxy.go cmd/at-cove/sshproxy_test.go cmd/at-cove/main.go
git commit -m "feat(at-cove): ssh-proxy ProxyCommand transport + byte relay"
```

---

### Task 4: `at-cove view` subcommand (print + `--write`)

**Files:**
- Create: `cmd/at-cove/view.go` (`doView`, `sshConfigPath`, `writeManagedBlock`)
- Modify: `cmd/at-cove/main.go` (register `view` after `ssh-proxy`)
- Test: `cmd/at-cove/view_test.go`

**Interfaces:**
- Consumes: `sshconfig.RenderHostBlock`, `sshconfig.GitRemoteURL`, `sshconfig.UpsertManagedBlock` (Task 1–2); `loadInstanceState` (Task 3); `keys.Ensure(r, configDir()) (priv, pub string, err error)`; `configDir()`; `st.Container`.
- Produces: `func doView(collaborator, kitDir string, r runner.Runner, write bool, out io.Writer) error`.

- [ ] **Step 1: Write the failing test** (drives the real `run()` harness with a `runner.Fake`; assert the printed block)

```go
package main

import (
	"strings"
	"testing"
)

func TestView_PrintsHostBlockAndGitRemote(t *testing.T) {
	env := newViewTestEnv(t) // installs a kit + instance state so loadInstanceState resolves; see helper below
	var out, errbuf strings.Builder
	code := run([]string{"view", "--project-dir", env.projectDir, "agent"},
		env.fakeRunner, env.lookup, env.lookPath, &out, &errbuf)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errbuf.String())
	}
	s := out.String()
	for _, want := range []string{
		"Host cove-" + env.container,
		"ProxyCommand",
		"ssh-proxy",
		"StrictHostKeyChecking accept-new",
		"git remote add sandbox cove-" + env.container + ":/home/agent/workspace",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("output missing %q:\n%s", want, s)
		}
	}
}
```

> **Helper note for the implementer:** `newViewTestEnv` builds the minimal fixture other `cmd/at-cove` tests already construct — a temp kit dir with `.at-cove/config.yml`, an install manifest, and an instance state file whose `Backend` is a `runner.Fake`-backed test backend returning a fixed `Endpoint`. Reuse the existing test fixture helpers in `cmd/at-cove/*_test.go` (grep for `state.SaveFor`/`loadCurrentInstall` fixtures used by the `chat`/`status` tests) rather than writing a new one; wire `Dial` to return `backend.Endpoint{Host:"127.0.0.1", Port:49999, User:"agent"}`. `env.container` is the container name that fixture records.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-cove/ -run TestView_PrintsHostBlockAndGitRemote`
Expected: FAIL — unknown command `view` (exit 2) / `undefined: doView`.

- [ ] **Step 3: Write minimal implementation** (`cmd/at-cove/view.go`)

```go
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/keys"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshconfig"
)

// doView prints (or --writes) an OpenSSH Host block for reaching the sandbox
// with VS Code Remote-SSH, plus a git-over-SSH remote line. The block routes
// through `at-cove ssh-proxy`, so the alias survives recreate (rotating port).
func doView(collaborator, kitDir string, r runner.Runner, write bool, out io.Writer) error {
	st, err := loadInstanceState(kitDir, collaborator)
	if err != nil {
		return err
	}
	priv, _, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil || self == "" {
		self = "at-cove"
	}
	// projectDir is the parent of the kit's .at-cove dir; ssh-proxy re-resolves
	// the collaborator from it at connect time.
	proxy := fmt.Sprintf("%s ssh-proxy --project-dir %s %s", self, filepath.Dir(kitDir), collaborator)
	alias := "cove-" + st.Container
	block := sshconfig.RenderHostBlock(sshconfig.HostParams{
		Alias:          alias,
		ProxyCommand:   proxy,
		User:           "agent",
		IdentityFile:   priv,
		KnownHostsFile: filepath.Join(configDir(), "known_hosts.d", st.Container),
	})

	if write {
		if err := writeManagedBlock(alias, block); err != nil {
			return err
		}
		fmt.Fprintf(out, "wrote managed block for %s to %s\n", alias, sshConfigPath())
		fmt.Fprintf(out, "git remote add sandbox %s\n", sshconfig.GitRemoteURL(alias))
		return nil
	}

	fmt.Fprint(out, block)
	fmt.Fprintf(out, "\n# git-over-SSH:\n# git remote add sandbox %s\n", sshconfig.GitRemoteURL(alias))
	return nil
}

func sshConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "config")
}

// writeManagedBlock idempotently upserts the block into ~/.ssh/config, creating
// ~/.ssh (0700) and the file (0600) if absent.
func writeManagedBlock(alias, block string) error {
	path := sshConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	updated := sshconfig.UpsertManagedBlock(string(existing), alias, block)
	return os.WriteFile(path, []byte(updated), 0o600)
}
```

- [ ] **Step 4: Register the command** in `cmd/at-cove/main.go`, after the `ssh-proxy` entry:

```go
{Name: "view", Brief: "print an ssh/VS Code Remote-SSH config (and git remote) for the sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
	fs := flag.NewFlagSet("view", flag.ContinueOnError)
	pd := projectDirFlag(fs)
	write := fs.Bool("write", false, "upsert the block into ~/.ssh/config instead of printing")
	pos, code, ok := cli.ParseFlags(fs, args, out, errw)
	if !ok {
		return code
	}
	collaborator, kitDir, code := resolveCollaborator(*pd, pos, "view", errw)
	if code != 0 {
		return code
	}
	return exitCode("at-cove", doView(collaborator, kitDir, r, *write, out), errw)
}},
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/at-cove/ -run TestView` then `just test`
Expected: PASS; full hermetic suite green.

- [ ] **Step 6: Commit**

```bash
git add cmd/at-cove/view.go cmd/at-cove/view_test.go cmd/at-cove/main.go
git commit -m "feat(at-cove): view command prints/writes Remote-SSH config + git remote"
```

---

### Task 5: Documentation — usage guide, command surface, INDEX

**Files:**
- Create: `docs/usage/workspace-visibility.md`
- Modify: `docs/usage/INDEX.md` (add one row)
- Modify: `docs/OVERVIEW.md` (add `view` + `ssh-proxy` to the command surface list)

**Interfaces:** none (docs only). Follow the repo's progressive-disclosure rules (docs-author skill): one row in INDEX, front-loaded frontmatter, single source of truth, every fact in one place.

- [ ] **Step 1: Write `docs/usage/workspace-visibility.md`**

```markdown
---
kind: usage
subject: see into and edit an isolated sandbox workspace with VS Code Remote-SSH + git
read-when: you want to browse/watch/edit a collaborator sandbox's workspace from your machine
---

# Workspace visibility (VS Code Remote-SSH + git)

An isolated sandbox's workspace is a private Docker volume — invisible on the host.
`at-cove view` emits an SSH config that connects your editor and git to it over the
existing sandbox SSH endpoint. No new service runs in the sandbox and no egress domain
is added.

## One-time client setup

1. Install the **Remote - SSH** extension in VS Code.
2. Set, in VS Code settings, `"remote.SSH.localServerDownload": "always"`. This makes VS
   Code download its server on *your* machine and push it into the sandbox over SFTP, so
   the sandbox needs **no internet access** for Remote-SSH to work — the egress wall is
   untouched.

## Connect

```
at-cove view --write <collaborator>     # upsert a Host block into ~/.ssh/config
```

Then in VS Code: **Remote-SSH: Connect to Host…** → `cove-<container>`. Open
`/home/agent/workspace`. Browse, read, watch the built-in Source Control diff, and edit
in place when you need to (read-only is a discipline here, not enforced).

`at-cove view <collaborator>` (without `--write`) prints the block to stdout instead, and
in both modes prints the git-over-SSH remote:

```
git remote add sandbox cove-<container>:/home/agent/workspace
git fetch sandbox
```

## Why the alias keeps working after `recreate`

The sandbox's ssh port is ephemeral and its host key regenerates each boot. The generated
Host block routes through `at-cove ssh-proxy`, which resolves the current port at connect
time, and pins the host key in the per-sandbox `known_hosts.d/<container>` file that
`recreate` reaps — so the same alias reconnects cleanly across rebuilds. Re-run
`at-cove view --write` only if you change collaborators or move the kit.

## Break-glass shell

`at-cove chat --raw <collaborator>` opens a plain shell in the same sandbox.
```

- [ ] **Step 2: Add the INDEX row** to `docs/usage/INDEX.md` (match the existing table's columns; place near other connection/session rows):

```markdown
| [workspace-visibility](workspace-visibility.md) | See into / edit an isolated sandbox workspace via VS Code Remote-SSH + git |
```

- [ ] **Step 3: Add `view` + `ssh-proxy` to the command surface** in `docs/OVERVIEW.md` (find the list that enumerates `create`/`chat`/`recreate`/… and add):

```markdown
- `at-cove view <collaborator>` — print (or `--write` to `~/.ssh/config`) a VS Code
  Remote-SSH config + git-over-SSH remote for the sandbox workspace; see
  [workspace visibility](usage/workspace-visibility.md).
- `at-cove ssh-proxy <collaborator>` — internal ProxyCommand transport used by the
  `view` config; not run directly.
```

- [ ] **Step 4: Verify docs health**

Run: the docs-audit skill's checker (per repo doctrine) — confirm no orphans, no dangling links, INDEX row resolves.
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add docs/usage/workspace-visibility.md docs/usage/INDEX.md docs/OVERVIEW.md
git commit -m "docs(usage): document workspace visibility via Remote-SSH + git"
```

---

## Self-Review

**Spec coverage** (§B of `2026-08-26-remote-teammate-design.md`):
- "No egress needed / localServerDownload" → Task 5 client setup + Global Constraints. ✅
- "Stable alias via ProxyCommand" → Task 1 (renderer), Task 3 (ssh-proxy). ✅
- "known_hosts pin reaped by recreate / accept-new" → Task 1 renderer emits `accept-new` + the per-sandbox known_hosts path; Task 5 explains it. ✅
- "`at-cove view` prints/`--write`s block + git remote" → Task 4. ✅
- "Break-glass = same connection / chat --raw" → Task 5. ✅
- "Hermetic tests: renderer, upsert, relay; run() harness for resolution; integration-tagged real round-trip" → Tasks 1–4 tests. ✅

**Placeholder scan:** no TBD/TODO; every code step shows complete code. The one fixture indirection (`newViewTestEnv` in Task 4) explicitly points at the existing `cmd/at-cove` test fixtures to reuse rather than inventing an unspecified helper — resolve by grepping the neighboring `chat`/`status` tests during implementation.

**Type consistency:** `HostParams`/`RenderHostBlock`/`GitRemoteURL`/`UpsertManagedBlock` names match across Tasks 1–4; `relay`/`doSSHProxy`/`loadInstanceState`/`doView` signatures match their call sites; `st.Backend`/`st.Container` are used verbatim as in `doChat` (`main.go:840`, `:1010`) rather than renamed. `b.Dial` returns `(backend.Endpoint, func(), error)` as in `colima.go:241`.

**Open risk to verify during Task 3–4:** the exact `state` value type and field names returned by `state.LoadFor` — confirmed used as `st.Backend`/`st.Container` by existing code; if the struct is named other than `state.State`, adjust the two signatures accordingly (behavior unchanged).
