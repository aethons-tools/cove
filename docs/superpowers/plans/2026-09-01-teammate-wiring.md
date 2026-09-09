# Teammate Wiring (Component A2, part 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the existing `at-switchboard` conductor into at-cove so an operator can run `at-cove teammate <class>` to launch a standing Discord teammate in an isolated sandbox — a `Teammate` config class, `discord.com` egress, the binary embedded in the image, and a detached launch that injects the bot token and reuses the saved Claude login.

**Architecture:** Six wiring changes. (1) A `Teammate` config class in `internal/kit` mirroring `Collaborator` plus a `*DiscordConfig` block. (2) A sibling `internal/atswitchboard` embed package (mirror `internal/attask`) folded into the install-currency hash. (3) Build/stage/assemble/Dockerfile plumbing to compile the binary into `/usr/local/bin/at-switchboard`. (4) The binary wired for `ErrorChannel` + a stderr `Log` so fail-soft isn't silent. (5) An `at-cove teammate` command that reuses `connect`'s setup helpers but launches the conductor **detached** (`setsid nohup … &`, non-tty ssh) with the bot token staged in tmpfs and Discord egress applied **persistently** (no clear-on-exit). (6) Docs. Builds on the merged-into-this-branch A1 core + hardening.

**Tech Stack:** Go stdlib + the repo's `internal/{kit,assemble,install,connect,backend,secret,sshargs,runner}` and `internal/cli`.

## Global Constraints

- Module `github.com/aethons-tools/cove`. Auth is the **saved `/agent-data` login** (reuse `ensureAuthenticated`) — the teammate bucket has **no** `ANTHROPIC_*` bearer secret; a one-time `at-cove chat` login is a documented prerequisite.
- **Lifetime is fail-loud, no auto-restart:** the launch is `setsid nohup at-switchboard &`; if it dies (or boot-Seed fails), it stays down until the operator re-runs `at-cove teammate`. Do NOT add a keepalive/systemd supervisor.
- **The bot token never hits disk, argv, or logs** — resolved host-side, staged into a `/dev/shm` env file over ssh stdin (umask 077), sourced-then-removed, exactly like `ensureWorkspace`/`cloneCmd` (`internal/connect/connect.go:297-336`).
- **Discord egress must PERSIST** after the command returns: apply `discord.com` via `ApplySessionEgress`, and do **not** replicate `doChat`'s `defer …ApplySessionEgress(container, nil)` clear-on-exit (`cmd/at-cove/main.go:998`).
- **The hardening `Dockerfile` is a sealed security-boundary file.** Adding the `at-switchboard` install mirrors the existing `at-task` install (`internal/assemble/hardening/Dockerfile:37-42`) — same shape, no weakening. Flag it in the task for security review.
- Tests hermetic (config parsing, embed/currency, argv/command builders via `runner.Fake`); real build/ssh behind `just build`/`integration`. TDD, plan/execute split, DRY, YAGNI, docs in the same change.

---

### Task W1: `Teammate` config class

**Files:**
- Modify: `internal/kit/config.go` (add `Teammate`, `DiscordConfig`, `Teammates` field, resolvers, validation)
- Test: `internal/kit/config_test.go` (add cases)

**Interfaces:**
- Produces: `type DiscordConfig struct { Channels []string; ErrorChannel string; BotTokenSecret string }`; `type Teammate struct { Prompt string; Default bool; Secrets map[string]SecretConfig; AllowedDomains []string; Discord *DiscordConfig }`; `Config.Teammates map[string]Teammate`; `func (c Config) ResolvedTeammate(class string) (Teammate, error)`; `func (c Config) ResolvedTeammateDomains(class string) ([]string, error)`.

**Design notes:** mirror `Collaborator` (config.go:273-280), `ResolvedCollaborator` (298-317), `ResolvedCollaboratorDomains` (319-332), and the collaborator validation block (615-646). `DiscordConfig` is a pointer whose presence is required for a real teammate class (mirror `VertexProvider`'s validate-when-non-nil shape). Validation: a non-`<common>` teammate must set `Discord` with ≥1 `channels` entry and a non-empty `bot-token-secret` that is declared in the class's (merged) `secrets`; `<common>` must not set prompt/default/discord. Reuse `validateSecretNames(..., false)` (no bearer needed — auth is saved-login), `rejectReservedSecretNames`, `unionDomains`, `validateClassTree`. Add a `teammateKeys(map[string]Teammate) []string` extractor (parallel to `collaboratorKeys`, config.go:824).

- [ ] **Step 1: Write the failing test**

```go
func TestParseConfig_TeammateClass(t *testing.T) {
	y := `
name: k
image: {}
workers: {}
teammates:
  <common>:
    allowed-domains: [example.com]
  helper:
    prompt: be helpful
    secrets:
      DISCORD_BOT_TOKEN: {description: bot}
    allowed-domains: [discord.com]
    discord:
      channels: ["111", "222"]
      error-channel: "999"
      bot-token-secret: DISCORD_BOT_TOKEN
`
	cfg, err := ParseConfig([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	tm, err := cfg.ResolvedTeammate("helper")
	if err != nil {
		t.Fatal(err)
	}
	if tm.Discord == nil || len(tm.Discord.Channels) != 2 || tm.Discord.BotTokenSecret != "DISCORD_BOT_TOKEN" {
		t.Fatalf("discord block wrong: %+v", tm.Discord)
	}
	doms, err := cfg.ResolvedTeammateDomains("helper")
	if err != nil {
		t.Fatal(err)
	}
	// <common> ∪ own, deduped+sorted
	if !contains(doms, "discord.com") || !contains(doms, "example.com") {
		t.Fatalf("domains not unioned: %v", doms)
	}
}

func TestParseConfig_TeammateRejectsMissingDiscord(t *testing.T) {
	y := `
name: k
image: {}
workers: {}
teammates:
  helper:
    secrets: {DISCORD_BOT_TOKEN: {description: x}}
`
	if _, err := ParseConfig([]byte(y)); err == nil {
		t.Fatal("expected error: teammate class needs a discord block")
	}
}

func TestParseConfig_TeammateRejectsUndeclaredTokenSecret(t *testing.T) {
	y := `
name: k
image: {}
workers: {}
teammates:
  helper:
    discord: {channels: ["1"], bot-token-secret: NOPE}
`
	if _, err := ParseConfig([]byte(y)); err == nil {
		t.Fatal("expected error: bot-token-secret must be a declared secret")
	}
}
```

> Reuse the test file's existing `contains` helper (or `strings`-based). If `config_test.go` already asserts an exhaustive "unknown field rejected" list, add `teammates` there too.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/kit/ -run TestParseConfig_Teammate`
Expected: FAIL — `Teammates`/`ResolvedTeammate` undefined; unknown field `teammates` rejected by the strict decoder.

- [ ] **Step 3: Implement**

Add the types near `Collaborator` (config.go:~281):
```go
// DiscordConfig configures a teammate class's Discord presence. Its presence on
// a teammate class is required; validated when non-nil.
type DiscordConfig struct {
	Channels       []string `yaml:"channels"`
	ErrorChannel   string   `yaml:"error-channel,omitempty"` // defaults to Channels[0] at launch
	BotTokenSecret string   `yaml:"bot-token-secret"`
}

// Teammate is a standing Discord conductor class — like a Collaborator, plus a
// Discord block. Auth is the saved /agent-data login (no bearer secret).
type Teammate struct {
	Prompt         string                  `yaml:"prompt,omitempty"`
	Default        bool                    `yaml:"default,omitempty"`
	Secrets        map[string]SecretConfig `yaml:"secrets,omitempty"`
	AllowedDomains []string                `yaml:"allowed-domains,omitempty"`
	Discord        *DiscordConfig          `yaml:"discord,omitempty"`
}
```
Add to `Config` (after `Collaborators`, config.go:~399): `Teammates map[string]Teammate `yaml:"teammates,omitempty"``.

Add resolvers (mirror the collaborator ones, config.go:~333):
```go
func (c Config) ResolvedTeammate(class string) (Teammate, error) {
	if class == "" || class == commonKey {
		return Teammate{}, fmt.Errorf("kit %q: %q is not a teammate class", c.Name, class)
	}
	own, ok := c.Teammates[class]
	if !ok {
		return Teammate{}, fmt.Errorf("kit %q declares no teammate class %q", c.Name, class)
	}
	merged := map[string]SecretConfig{}
	for k, v := range c.Teammates[commonKey].Secrets {
		merged[k] = v
	}
	for k, v := range own.Secrets {
		merged[k] = v
	}
	own.Secrets = merged
	return own, nil
}

func (c Config) ResolvedTeammateDomains(class string) ([]string, error) {
	if class == "" || class == commonKey {
		return nil, fmt.Errorf("kit %q: %q is not a teammate class", c.Name, class)
	}
	own, ok := c.Teammates[class]
	if !ok {
		return nil, fmt.Errorf("kit %q declares no teammate class %q", c.Name, class)
	}
	return unionDomains(c.Teammates[commonKey].AllowedDomains, own.AllowedDomains), nil
}
```
Add `teammateKeys` (parallel to `collaboratorKeys`, config.go:824):
```go
func teammateKeys(m map[string]Teammate) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
```
Add a validation block in `ParseConfig` right after the collaborators block (config.go:~646):
```go
if err := validateClassTree("teammates", teammateKeys(cfg.Teammates)); err != nil {
	return Config{}, err
}
for name, tm := range cfg.Teammates {
	bucket := fmt.Sprintf("teammates[%q].secrets", name)
	if err := validateSecretNames(bucket, tm.Secrets, false); err != nil {
		return Config{}, err
	}
	if err := rejectReservedSecretNames(bucket, tm.Secrets); err != nil {
		return Config{}, err
	}
	for i, d := range tm.AllowedDomains {
		if strings.TrimSpace(d) == "" {
			return Config{}, fmt.Errorf("teammates[%q].allowed-domains[%d] is empty", name, i)
		}
	}
	if name == commonKey {
		if tm.Prompt != "" || tm.Default || tm.Discord != nil {
			return Config{}, fmt.Errorf("teammates[%q]: the base must not set a prompt, default, or discord block", commonKey)
		}
		continue
	}
	if tm.Discord == nil {
		return Config{}, fmt.Errorf("teammates[%q]: a discord block is required", name)
	}
	if len(tm.Discord.Channels) == 0 {
		return Config{}, fmt.Errorf("teammates[%q].discord: at least one channel is required", name)
	}
	if tm.Discord.BotTokenSecret == "" {
		return Config{}, fmt.Errorf("teammates[%q].discord: bot-token-secret is required", name)
	}
	// the token secret must be declared in the class's own or <common> secrets
	if _, ok := cfg.Teammates[commonKey].Secrets[tm.Discord.BotTokenSecret]; !ok {
		if _, ok := tm.Secrets[tm.Discord.BotTokenSecret]; !ok {
			return Config{}, fmt.Errorf("teammates[%q].discord.bot-token-secret %q is not a declared secret", name, tm.Discord.BotTokenSecret)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/kit/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(kit): teammate config class with discord block + resolvers"
```

---

### Task W2: `internal/atswitchboard` embed package + install-currency

**Files:**
- Create: `internal/atswitchboard/atswitchboard.go` (mirror `internal/attask/attask.go`)
- Create: `internal/atswitchboard/bin/.gitkeep` (so `//go:embed bin` compiles before staging — mirror how `internal/attask/bin` is handled)
- Modify: `internal/install/currency.go` (`AtCoveIdentity` — hash the switchboard bytes)
- Test: `internal/atswitchboard/atswitchboard_test.go`

**Interfaces:**
- Produces: `atswitchboard.Binary(goarch string) ([]byte, error)`; `atswitchboard.BinFS() fs.FS`.

**Design notes:** copy `internal/attask/attask.go` verbatim, renaming `attask`→`atswitchboard`, the embed path to `bin`, and the binary basename to `at-switchboard-linux-`. **First read `internal/attask/attask.go` and check whether `internal/attask/bin` holds a committed placeholder or a `.gitignore`** — replicate exactly so `go build ./...` works without running the stage script (a bare `//go:embed bin` over an empty/absent dir fails to compile). Then add a third length-prefixed field to `AtCoveIdentity` (currency.go:113-131) hashing `atswitchboard.BinFS()`, so a switchboard rebuild invalidates installs.

- [ ] **Step 1: Write the failing test**

```go
package atswitchboard

import "testing"

func TestBinaryUnknownArchErrors(t *testing.T) {
	if _, err := Binary("sparc"); err == nil {
		t.Fatal("expected error for unstaged/unknown arch")
	}
}

func TestBinFSNonNil(t *testing.T) {
	if BinFS() == nil {
		t.Fatal("BinFS() is nil")
	}
}
```
Plus, in `internal/install/currency_test.go` (if a currency test exists, extend it; else add):
```go
func TestAtCoveIdentityIncludesSwitchboard(t *testing.T) {
	// AtCoveIdentity must be stable and non-empty; a smoke check that it computes.
	id, err := AtCoveIdentity()
	if err != nil || id == "" {
		t.Fatalf("AtCoveIdentity: %q err=%v", id, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/atswitchboard/`
Expected: FAIL — package/functions don't exist.

- [ ] **Step 3: Implement** — mirror `internal/attask/attask.go`:

```go
// Package atswitchboard embeds the cross-compiled at-switchboard binaries so
// at-cove can stage them into the sandbox image (mirrors internal/attask).
package atswitchboard

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed bin
var binFS embed.FS

// Binary returns the linux/<goarch> at-switchboard binary, erroring if unstaged.
func Binary(goarch string) ([]byte, error) {
	return lookup(goarch)
}

// BinFS exposes the embedded tree for install-currency hashing.
func BinFS() fs.FS { return binFS }

func lookup(goarch string) ([]byte, error) {
	name := "bin/at-switchboard-linux-" + goarch
	b, err := binFS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("atswitchboard: %s not staged: %w", name, err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("atswitchboard: %s is an empty placeholder", name)
	}
	return b, nil
}
```
> Adjust to match the exact `internal/attask` signatures/error style you find when you read it (this is a faithful mirror, not a redesign). Create `internal/atswitchboard/bin/.gitkeep` (or whatever placeholder `internal/attask/bin` uses).

In `internal/install/currency.go` `AtCoveIdentity`, after the `attask` field:
```go
	sb, err := HashTree(atswitchboard.BinFS())
	if err != nil {
		return "", err
	}
	writeField(h, []byte("switchboard"))
	writeField(h, []byte(sb))
```
Add the import `"github.com/aethons-tools/cove/internal/atswitchboard"` (currency.go:~15).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/atswitchboard/ ./internal/install/` then `go build ./...`
Expected: PASS, clean build (embed compiles via the placeholder).

- [ ] **Step 5: Commit**

```bash
git add internal/atswitchboard/ internal/install/currency.go internal/install/currency_test.go
git commit -m "feat(atswitchboard): embed package + fold into install-currency hash"
```

---

### Task W3: build/stage scripts + assemble + Dockerfile install

**Files:**
- Modify: `scripts/stage-attask.sh` (also stage at-switchboard) OR create `scripts/stage-switchboard.sh`
- Modify: `scripts/build.sh` (add `at-switchboard` to `BINARIES`; call the switchboard staging)
- Modify: `internal/assemble/assemble.go` (`writeSwitchboard` + call it in `Assemble`)
- Modify: `internal/assemble/hardening/Dockerfile` (COPY + install to `/usr/local/bin/at-switchboard`)
- Test: `internal/assemble/assemble_test.go` (assert `writeSwitchboard` writes the arch files)

**Interfaces:**
- Consumes: `atswitchboard.Binary` (W2).
- Produces: `writeSwitchboard(buildDir string) error` writing `buildDir/switchboard/at-switchboard-linux-<arch>`.

**Design notes:** mirror `writeAtTask` (assemble.go:66-83) and its call site (assemble.go:39). The scripts + Dockerfile are build infra (not unit-testable) — mirror the at-task lines exactly (§2 of the plan's recon). **The Dockerfile edit touches the sealed hardening layer** — same shape as the existing at-task install, no capability/policy change; call it out for security review. Unlike at-task there's no base-image fallback binary, so the install guard should still tolerate an unstaged placeholder (a local `go build` without staging), but `at-cove teammate` will error clearly if the binary is absent at launch (handled in W5 by checking the binary exists, or letting the ssh command fail loudly).

- [ ] **Step 1: Write the failing test** (assemble — the hermetic part)

```go
func TestWriteSwitchboard_WritesArchFiles(t *testing.T) {
	dir := t.TempDir()
	if err := writeSwitchboard(dir); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		p := filepath.Join(dir, "switchboard", "at-switchboard-linux-"+arch)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/assemble/ -run TestWriteSwitchboard`
Expected: FAIL — `writeSwitchboard` undefined.

- [ ] **Step 3: Implement**

In `assemble.go`, mirror `writeAtTask`:
```go
func writeSwitchboard(buildDir string) error {
	dir := filepath.Join(buildDir, "switchboard")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, arch := range []string{"amd64", "arm64"} {
		b, err := atswitchboard.Binary(arch)
		if err != nil {
			b = nil // placeholder; a missing binary is caught at launch, not build
		}
		if err := os.WriteFile(filepath.Join(dir, "at-switchboard-linux-"+arch), b, 0o755); err != nil {
			return err
		}
	}
	return nil
}
```
Call it in `Assemble` right after `writeAtTask(buildDir)` (assemble.go:~39); import `atswitchboard`.

In `internal/assemble/hardening/Dockerfile`, after the at-task block (Dockerfile:37-42):
```dockerfile
COPY switchboard/ /tmp/switchboard/
RUN arch="$(dpkg --print-architecture)" \
 && if [ -s "/tmp/switchboard/at-switchboard-linux-${arch}" ]; then \
      install -m 0755 "/tmp/switchboard/at-switchboard-linux-${arch}" /usr/local/bin/at-switchboard; \
    fi \
 && rm -rf /tmp/switchboard
```

In `scripts/stage-attask.sh` extend the arch loop to also build `./cmd/at-switchboard` into `internal/atswitchboard/bin/at-switchboard-linux-${a}` (or add `scripts/stage-switchboard.sh` and call it from `build.sh`). In `scripts/build.sh`, add `at-switchboard` to `BINARIES` (build.sh:31) and add the staging call next to the at-task one (build.sh:35-36). Mirror the goreleaser before-hook if it invokes staging.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/assemble/` then `just build` (exercises the scripts + a real image-context assembly if wired; at minimum `go build ./...` clean)
Expected: assemble test PASS; build green.

- [ ] **Step 5: Commit**

```bash
git add scripts/stage-attask.sh scripts/build.sh internal/assemble/assemble.go internal/assemble/assemble_test.go internal/assemble/hardening/Dockerfile
git commit -m "build(switchboard): stage + embed at-switchboard into the sandbox image"
```

---

### Task W4: binary `ErrorChannel` + `Log` wiring

**Files:**
- Modify: `cmd/at-switchboard/main.go` (read `SWITCHBOARD_ERROR_CHANNEL`; set `Config.ErrorChannel` + `Config.Log`)
- Test: `cmd/at-switchboard/main_test.go` (assert error-channel env parsed; a nil-safe default)

**Interfaces:**
- Consumes: `switchboard.Config{ErrorChannel, Log}` (from hardening).

**Design notes:** the binary currently builds `Config{PollInterval: interval}` — fail-soft is silent. Wire: `ErrorChannel` from `SWITCHBOARD_ERROR_CHANNEL` (empty → defaults to the first channel), and `Log` to a stderr line-logger. Keep `run(argv, getenv, stdout, stderr)` testable.

- [ ] **Step 1: Write the failing test**

```go
func TestRun_WiresErrorChannelDefaultToFirstChannel(t *testing.T) {
	// With channels set and no explicit error channel, Config.ErrorChannel should
	// default to the first channel. Assert via a seam: run() should build a Config
	// whose ErrorChannel == "111" when SWITCHBOARD_CHANNELS="111,222" and no
	// SWITCHBOARD_ERROR_CHANNEL. (Extract config-building into buildConfig for test.)
	env := map[string]string{"DISCORD_BOT_TOKEN": "t", "SWITCHBOARD_CHANNELS": "111,222"}
	cfg, channels, err := buildConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ErrorChannel != "111" {
		t.Fatalf("error channel default = %q, want 111", cfg.ErrorChannel)
	}
	if len(channels) != 2 {
		t.Fatalf("channels = %v", channels)
	}
	if cfg.Log == nil {
		t.Fatal("Log should be wired (fail-soft must not be silent)")
	}
}
```

> Refactor the env-reading + Config-building out of `run` into a `buildConfig(getenv) (switchboard.Config, []string, error)` (plus token) so it's unit-testable without launching the loop. `run` calls it. Adjust the existing `TestRun_MissingToken_IsUsageError` if needed.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-switchboard/ -run TestRun_WiresError`
Expected: FAIL — `buildConfig` undefined / ErrorChannel not wired.

- [ ] **Step 3: Implement**

Extract `buildConfig` and wire `ErrorChannel` + `Log`:
```go
func buildConfig(getenv func(string) string) (switchboard.Config, []string, string, error) {
	token := getenv("DISCORD_BOT_TOKEN")
	channels := splitNonEmpty(getenv("SWITCHBOARD_CHANNELS"))
	// ... existing required-var checks (return usage errors) ...
	interval := 3 * time.Second
	// ... existing SWITCHBOARD_POLL_INTERVAL parse ...
	errCh := getenv("SWITCHBOARD_ERROR_CHANNEL")
	if errCh == "" && len(channels) > 0 {
		errCh = channels[0]
	}
	cfg := switchboard.Config{
		PollInterval: interval,
		ErrorChannel: errCh,
		Log:          func(s string) { fmt.Fprintln(os.Stderr, "at-switchboard:", s) },
	}
	return cfg, channels, token, nil
}
```
> Note `Log` writing to `os.Stderr` directly is fine for the binary; if you prefer testability, inject the stderr writer. Keep the required-var usage errors (exit 2, no token echo). Update `run` to call `buildConfig` and pass `cfg`/`channels`/`token` into `NewRESTClient`/`NewClaudeAgent`/`Run`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/at-switchboard/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-switchboard/main.go cmd/at-switchboard/main_test.go
git commit -m "feat(at-switchboard): wire error-channel + stderr log (fail-soft no longer silent)"
```

---

### Task W5: `at-cove teammate` detached launch

**Files:**
- Create: `internal/connect/teammate.go` (`LaunchTeammate` + the detached remote-command builder)
- Test: `internal/connect/teammate_test.go`
- Modify: `cmd/at-cove/main.go` (register `teammate`; add `doTeammate`)
- Test: `cmd/at-cove/teammate_test.go`

**Interfaces:**
- Consumes: `secret.Resolve`, `sshargs`, `ensureAuthenticated`, `writeVM`, `backend.Backend.Dial`, `backend.SessionEgress`.
- Produces: `func detachedLaunchCmd(envVMPath string, channels []string, errorChannel string) string`; `func LaunchTeammate(r runner.Runner, b backend.Backend, o TeammateOptions) error` with `TeammateOptions{Container, BotTokenSpec secret.Spec, Channels []string, ErrorChannel, IdentityFile, KnownHostsFile, CredentialsFile string}`; `func doTeammate(collaborator, kitDir string, r runner.Runner, dryRun bool, out, errw io.Writer) error`.

**Design notes:** do NOT reuse `connect.Connect` (its `aw.Inhibit`/`saveCredentials` assume a blocking session — connect.go:189-203). `LaunchTeammate` reuses only the *setup* pieces:
1. `Dial` the container (endpoint).
2. Build the `sshargs.Target` (identity, known-hosts, accept-new — same as `doChat`).
3. `ensureAuthenticated(r, tgt, credsFile, errw)` — seeds the saved `/agent-data` login (one-time human login already done; this reuses it). **Reuse the existing unexported helper** — since `LaunchTeammate` is in package `connect`, it can call `ensureAuthenticated`/`writeVM` directly.
4. Resolve ONLY the bot token: `env, err := secret.Resolve(r, nil, []secret.Spec{o.BotTokenSpec})`; stage `export DISCORD_BOT_TOKEN=<quoted>\nexport SWITCHBOARD_CHANNELS=<quoted>\n…` into a `/dev/shm/cove-teammate-env` file via `writeVM` (stdin, umask 077 — token never on argv). Mirror `ensureWorkspace`/`cloneCmd` (connect.go:297-336).
5. Run the **detached** command over **non-tty** ssh so it returns immediately: `set -a; . <env>; rm -f <env>; setsid nohup at-switchboard </dev/null >>/agent-data/switchboard.log 2>&1 &`. Use `sshargs.Base(tgt)` (not `Interactive`/`-tt`).

`doTeammate` mirrors `doChat`'s spine (main.go:831-1018) but: resolve the **teammate** class (`ResolvedTeammate`), plan/resolve the bot-token secret, `getBackend`, apply `ResolvedTeammateDomains` egress via `ApplySessionEgress` **without** the clear-on-exit defer, then call `LaunchTeammate`. Channels/error-channel come from the resolved `Discord` block (error-channel defaults to `Channels[0]`).

- [ ] **Step 1: Write the failing test** (the pure command builder + the launch wiring)

```go
// internal/connect/teammate_test.go
func TestDetachedLaunchCmd(t *testing.T) {
	got := detachedLaunchCmd("/dev/shm/cove-teammate-env", []string{"111", "222"}, "999")
	for _, want := range []string{
		". /dev/shm/cove-teammate-env",
		"rm -f /dev/shm/cove-teammate-env",
		"setsid",
		"at-switchboard",
		"</dev/null",              // detached from the closing ssh channel
		">>/agent-data/switchboard.log",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("cmd missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "-tt") {
		t.Fatal("detached launch must not use a PTY")
	}
}
```
```go
// cmd/at-cove/teammate_test.go — drive run() with a runner.Fake, assert:
//  (1) ApplySessionEgress called with discord.com, and NOT cleared (no nil call),
//  (2) at-switchboard launched detached (setsid) with the channels,
//  (3) the bot token is staged via stdin (writeVM), never on an ssh argv.
func TestTeammate_AppliesPersistentEgressAndLaunchesDetached(t *testing.T) {
	env := newTeammateTestEnv(t) // kit with a teammate class + instance state (reuse chat/status fixtures)
	var out, errb strings.Builder
	code := run([]string{"teammate", "--project-dir", env.projectDir, "helper"},
		env.fakeRunner, env.lookup, env.lookPath, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	env.assertEgressAppliedWith(t, "discord.com")
	env.assertEgressNotCleared(t) // no ApplySessionEgress(container, nil)
	env.assertSSHRan(t, "setsid", "at-switchboard")
	env.assertTokenNeverOnArgv(t, "DISCORD_BOT_TOKEN")
}
```

> The `cmd/at-cove` test needs a fixture with a teammate class + instance state + a fake backend implementing `SessionEgress`. Reuse the existing `chat`/`status` fixture helpers (grep `writeStateFor`/`writeInstall`/`seedConfigDir`, and the fake backend used in egress tests). The fake backend must record `ApplySessionEgress` calls (domains + a nil-clear) and the ssh commands the `runner.Fake` received. If a `SessionEgress`-recording fake backend doesn't exist, extend the nearest one. If the full fixture is disproportionate, it is acceptable to test `doTeammate` directly with a hand-built teammate config + state + `runner.Fake` (say so in the report), keeping the three assertions.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/connect/ -run TestDetachedLaunchCmd` and `go test ./cmd/at-cove/ -run TestTeammate`
Expected: FAIL — `detachedLaunchCmd`/`teammate` command undefined.

- [ ] **Step 3: Implement**

`internal/connect/teammate.go`:
```go
package connect

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/sshargs"
)

const teammateEnvVMPath = "/dev/shm/cove-teammate-env"
const teammateLogVMPath = "/agent-data/switchboard.log"

// TeammateOptions carries what LaunchTeammate needs (all host-side inputs).
type TeammateOptions struct {
	Container       string
	BotTokenSpec    secret.Spec
	Channels        []string
	ErrorChannel    string
	IdentityFile    string
	KnownHostsFile  string
	CredentialsFile string
}

// detachedLaunchCmd builds the remote shell command: source the tmpfs env,
// remove it, then start at-switchboard detached (setsid, stdin from /dev/null,
// output to a log) so it survives the ssh channel closing.
func detachedLaunchCmd(envVMPath string, channels []string, errorChannel string) string {
	// channels/errorChannel are already exported into the env file by the caller;
	// they're accepted here only so the builder is self-describing/testable.
	_ = channels
	_ = errorChannel
	return "set -a; . " + envVMPath + "; set +a; rm -f " + envVMPath + "; " +
		"setsid nohup at-switchboard </dev/null >>" + teammateLogVMPath + " 2>&1 &"
}

// LaunchTeammate dials the container, ensures the saved login, stages the bot
// token + channels into tmpfs, and starts at-switchboard detached. It returns as
// soon as the conductor is backgrounded (fire-and-forget).
func LaunchTeammate(r runner.Runner, b backend.Backend, o TeammateOptions) error {
	ep, cleanup, err := b.Dial(o.Container)
	if err != nil {
		return err
	}
	defer cleanup()
	tgt := sshargs.Target{
		Host: ep.Host, Port: ep.Port, User: ep.User,
		IdentityFile: o.IdentityFile, KnownHostsFile: o.KnownHostsFile,
	}
	if err := ensureAuthenticated(r, tgt, o.CredentialsFile, nil); err != nil {
		return fmt.Errorf("teammate auth: %w", err)
	}
	env, err := secret.Resolve(r, nil, []secret.Spec{o.BotTokenSpec})
	if err != nil {
		return err
	}
	var script strings.Builder
	fmt.Fprintf(&script, "export DISCORD_BOT_TOKEN=%s\n", shellQuote(env[o.BotTokenSpec.Name]))
	fmt.Fprintf(&script, "export SWITCHBOARD_CHANNELS=%s\n", shellQuote(strings.Join(o.Channels, ",")))
	if o.ErrorChannel != "" {
		fmt.Fprintf(&script, "export SWITCHBOARD_ERROR_CHANNEL=%s\n", shellQuote(o.ErrorChannel))
	}
	if err := writeVM(r, tgt, script.String(), teammateEnvVMPath); err != nil {
		return err
	}
	cmd := detachedLaunchCmd(teammateEnvVMPath, o.Channels, o.ErrorChannel)
	if err := r.Run("ssh", append(sshargs.Base(tgt), cmd)...); err != nil {
		return fmt.Errorf("teammate launch: %w", err)
	}
	return nil
}
```
> Confirm the real signatures when you implement: `ensureAuthenticated(r, tgt, credsFile, stderr)` (connect.go:214) — pass a real stderr writer, not nil, if the signature requires it; `writeVM(r, tgt, data, vmPath)` (connect.go:340) — match its exact param order/types; `secret.Spec`/`secret.Resolve` shapes; `sshargs.Target` fields (connect.go:150-156). `shellQuote` exists in `transport.go:165` (same package). Adjust to compile.

In `cmd/at-cove/main.go`, register `teammate` (mirror the `chat` entry, main.go:99) and add `doTeammate` mirroring `doChat`'s spine with the teammate deltas: `cfg.ResolvedTeammate(class)`; the bot-token secret plan; `getBackend`; `eg.ApplySessionEgress(st.Container, teammateDomains)` **without** the clear defer; `errorChannel := tm.Discord.ErrorChannel; if errorChannel == "" { errorChannel = tm.Discord.Channels[0] }`; then `connect.LaunchTeammate(...)`. Print a "teammate launched; tail /agent-data/switchboard.log" line.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/connect/ ./cmd/at-cove/` then `just test`/`just lint`
Expected: PASS; whole suite green.

- [ ] **Step 5: Commit**

```bash
git add internal/connect/teammate.go internal/connect/teammate_test.go cmd/at-cove/main.go cmd/at-cove/teammate_test.go
git commit -m "feat(at-cove): teammate command launches at-switchboard detached with persistent egress"
```

---

### Task W6: docs

**Files:**
- Create: `docs/usage/discord-teammate.md`
- Modify: `docs/usage/INDEX.md` (one row), `docs/OVERVIEW.md` (command surface: `at-cove teammate`), the config reference doc that lists classes (add the `teammates:` block)

**Design notes:** progressive-disclosure rules (docs-author). Document: configuring a `teammates.<class>` with a `discord` block (channels, optional error-channel, bot-token-secret), the `DISCORD_BOT_TOKEN` secret, **the one-time `at-cove chat` login prerequisite** (auth reuses the saved `/agent-data` login), running `at-cove teammate <class>`, the fail-loud/no-restart behavior (re-run to restart; tail `/agent-data/switchboard.log`), and that Discord egress is added to that class only. Link the spec §A. Do not duplicate the spec's design.

- [ ] **Step 1: Write `docs/usage/discord-teammate.md`** (frontmatter matched to sibling usage docs — check the real schema, e.g. `summary`/`read_when`/`owns`).

- [ ] **Step 2: Add the INDEX row** (match the real column format).

- [ ] **Step 3: Add `at-cove teammate` to the OVERVIEW command surface** (match the existing style; link the usage doc). Add the `teammates:` block to the config reference doc.

- [ ] **Step 4: Verify docs health** — run the docs-audit checker; confirm no orphans/dangling links.

- [ ] **Step 5: Commit**

```bash
git add docs/usage/discord-teammate.md docs/usage/INDEX.md docs/OVERVIEW.md docs/usage/*config*.md
git commit -m "docs(usage): document the at-cove teammate (Discord conductor) workflow"
```

---

## Self-Review

**Spec / decision coverage:**
- Teammate config class + discord block (channels list + error-channel, no bearer secret) → W1. ✅
- `at-switchboard` embedded in the image + currency → W2, W3. ✅
- `discord.com` egress, PERSISTENT (no clear-on-exit) → W5 (`doTeammate`). ✅
- Detached launch, token-in-tmpfs, non-tty ssh, fail-loud/no-restart → W5. ✅
- Auth via saved `/agent-data` login (reuse `ensureAuthenticated`; one-time login prereq) → W5, W6. ✅
- Fail-soft observability wired (ErrorChannel + stderr Log) — closes A2-part-1 carryover #4 → W4. ✅
- Docs → W6. ✅
- **Left as accepted:** boot-Seed fatal (no resilience/restart, per the fail-loud decision); the `//go:build integration` real-`claude` continuity test and `shellQuote` consolidation remain follow-ups (note in the PR).

**Placeholder scan:** concrete code for W1/W2/W4/W5's builder; the at-task-pipeline mirrors (W2/W3) cite exact source lines to replicate and instruct reading `internal/attask` first (the plan can't see its exact bin/ placeholder handling); the `doTeammate`/fixture specifics (W5) cite `doChat` + name the exact deltas and a sanctioned direct-`doTeammate` test fallback.

**Type consistency:** `Teammate`/`DiscordConfig`/`ResolvedTeammate`/`ResolvedTeammateDomains` (W1) are consumed by `doTeammate` (W5); `atswitchboard.Binary`/`BinFS` (W2) by `writeSwitchboard` (W3) + currency (W2); `switchboard.Config{ErrorChannel,Log}` (W4) matches the hardening additions; `LaunchTeammate`/`TeammateOptions`/`detachedLaunchCmd` (W5) are internal to the launch.

**Security review flags:** the sealed hardening `Dockerfile` gains an `at-switchboard` install (W3) — same shape as at-task, no policy change; Discord egress persists by design (W5) — a teammate container stays live with widened egress, breaking the "idle reverts to root-only" assumption intentionally; the bot token stays off disk/argv/logs (W5, verified by a test asserting it's never on an ssh argv).
