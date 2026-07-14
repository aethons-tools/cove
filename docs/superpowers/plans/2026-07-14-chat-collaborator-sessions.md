# `chat` collaborator sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make an interactive collaborator session first-class — rename `connect`→`chat`, standardize the kit directory as a `--kit-dir` flag, give a `collaborators:` class a role `prompt:` injected as `CLAUDE.md` context, and select it by an optional positional with a `default`.

**Architecture:** The kit dir moves from a positional to a shared `--kit-dir` flag across all commands, freeing `chat`'s leading positional for the collaborator key. `Collaborator` gains `Prompt`/`Default`; a pure selection function picks the class; `chat` (the renamed `doConnect`) loads the kit config, resolves the class + its secrets, and writes its prompt to a VM file that the sealed `CLAUDE.md` `@`-includes. GitHub/Linear ride the human's connectors — no collaborator credential minting.

**Tech Stack:** Go 1.22, `gopkg.in/yaml.v3`, the in-repo `internal/{kit,connect,secret,usersecret,mint,assemble}`. Hermetic tests via `runner.Fake`.

## Global Constraints

- **No new dependencies.** Standard library + `gopkg.in/yaml.v3`. `go.mod`/`go.sum` unchanged.
- **`connect` → `chat` is a hard rename** — no alias. A kit with no collaborators yields today's exact plain session under `chat`.
- **`--kit-dir` replaces the positional kit-dir on every command** (default: `.` / today's `resolveKit` cwd resolution). The only command with a positional afterward is `chat` (the optional collaborator).
- **A malformed/absent kit config on `chat` is a hard error** (abort before connecting).
- **The role prompt is injected as `CLAUDE.md` context, never `-p`.** `chat` writes the resolved prompt to `/agent-data/COLLABORATOR.md`; the seeded `CLAUDE.md` `@`-includes it; a default empty `COLLABORATOR.md` ships in the **hardening (sealed) layer** so the include never dangles.
- **Collaborators use connectors; no collaborator token minting.** The prompt is source-controlled kit content, not a secret.
- **At most one collaborator may set `default: true`; `<common>` may set neither `prompt` nor `default`.**

---

## File Structure

- `cmd/at-cove/main.go` — `--kit-dir` flag + shared resolver (`resolveKitDir`); rename `connect`→`chat`; `doChat` wraps the old `doConnect` with kit-config load + collaborator selection + prompt injection.
- `internal/kit/config.go` — `Collaborator{Prompt, Default, Secrets}`; validation; `ResolvedCollaborator` returns the prompt; a new `SelectCollaborator` pure function.
- `internal/connect/connect.go` — `Options.CollaboratorPrompt`; write the role file over ssh before launch.
- `internal/assemble/hardening/image-files/home/agent/.init-agent-data/` — `CLAUDE.md` gains `@COLLABORATOR.md`; add a default `COLLABORATOR.md`.
- Tests alongside each; docs under `docs/usage/` + `docs/OVERVIEW.md` + `docs/orchestration/`.

---

### Task 1: `--kit-dir` flag standard (positional → flag)

**Files:**
- Modify: `cmd/at-cove/main.go` (`kitDirArg` → `resolveKitDir`; every command's FlagSet; `doWork`/`doDispatch`)
- Modify: `cmd/at-cove/main_test.go` (every invocation that passed a kit-dir positional)

**Interfaces:**
- Produces: `func kitDirFlag(fs *flag.FlagSet) *string` and `func resolveKitDir(flagVal string, pos []string, cmd string, stderr io.Writer) (string, int)`. Commands register the flag, parse, then resolve. Non-`chat` commands reject any leftover positional.

- [ ] **Step 1: Write the failing test**

Add to `cmd/at-cove/main_test.go`:

```go
func TestKitDirFlagResolves(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("name: k\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	got, code := resolveKitDir(dir, nil, "build", &errb)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	if got == "" {
		t.Fatalf("resolveKitDir returned empty for %q", dir)
	}
}

func TestKitDirFlagRejectsPositional(t *testing.T) {
	var errb bytes.Buffer
	if _, code := resolveKitDir(".", []string{"stray"}, "build", &errb); code != 2 {
		t.Fatalf("code=%d, want 2 for a stray positional", code)
	}
	if !strings.Contains(errb.String(), "--kit-dir") {
		t.Fatalf("error should mention --kit-dir; got %q", errb.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-cove/ -run TestKitDirFlag -v`
Expected: FAIL — `undefined: resolveKitDir`.

- [ ] **Step 3: Write minimal implementation**

In `cmd/at-cove/main.go`, add the two helpers (near `kitDirArg`) and delete `kitDirArg`:

```go
// kitDirFlag registers the standard --kit-dir flag on fs (default ".", i.e. the
// current directory / single-kit resolution). Every command that targets a kit
// registers it.
func kitDirFlag(fs *flag.FlagSet) *string {
	return fs.String("kit-dir", ".", "kit directory (default: current dir / the single kit)")
}

// resolveKitDir resolves the --kit-dir flag value to a kit directory, rejecting
// any leftover positional (commands other than `chat` take none).
func resolveKitDir(flagVal string, pos []string, cmd string, stderr io.Writer) (string, int) {
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "at-cove: %s takes no positional arguments (use --kit-dir)\n", cmd)
		return "", 2
	}
	kitDir, err := resolveKit(flagVal)
	if err != nil {
		fmt.Fprintln(stderr, "at-cove:", err)
		return "", 1
	}
	return kitDir, 0
}
```

Then, in **every** command handler in `run()` — `build`, `create`, `connect`, `recreate` — and in `instanceCmd`, register the flag and swap the resolver. (`connect` keeps its name and its `--raw`/`--no-auth`/`--fresh` flags here; Task 4 renames it to `chat`. Migrating it now is what lets `kitDirArg` be deleted while the repo stays green.) The pattern per command:

```go
			{Name: "build", Brief: "assemble the kit's build context", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("build", flag.ContinueOnError)
				fs.SetOutput(errw)
				kd := kitDirFlag(fs)
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := resolveKitDir(*kd, pos, "build", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doBuild(kitDir, r, g.DryRun, out), errw)
			}},
```

Apply the same shape to `create` and `recreate` (keep their existing `--workspace`/`--ws` flags). Update `instanceCmd`:

```go
func instanceCmd(cmd string, args []string, r runner.Runner, g cli.Globals, out, errw io.Writer, do func(kitDir string, inst state.Instance) error) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errw)
	kd := kitDirFlag(fs)
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	kitDir, code := resolveKitDir(*kd, pos, cmd, errw)
	if code != 0 {
		return code
	}
	return exitCode("at-cove", do(kitDir, state.Interactive), errw)
}
```

In `doWork` and `doDispatch`, register `kd := kitDirFlag(fs)` before `cli.ParseInterspersed`, and replace `kitDirArg(pos, "work"/"dispatch", stderr)` with `resolveKitDir(*kd, pos, "work"/"dispatch", stderr)`. Remove the now-stale `if len(pos) < 1 { ... expected <kit-dir> }` guard in `doWork` (the flag has a default; a stray positional is now the error).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/at-cove/ -run TestKitDirFlag -v`
Expected: PASS.

- [ ] **Step 5: Migrate existing command tests**

Every test in `cmd/at-cove/main_test.go` that invokes a command with a positional kit-dir must switch to `--kit-dir`. Find them: `grep -n 'run(\[\]string{' cmd/at-cove/main_test.go`. For each, a call like `run([]string{"work", kitDir, "--in", in, ...}, ...)` becomes `run([]string{"work", "--kit-dir", kitDir, "--in", in, ...}, ...)`; `run([]string{"create", kitDir}, ...)` becomes `run([]string{"create", "--kit-dir", kitDir}, ...)`; etc. (A bare `run([]string{"status"}, ...)` relying on cwd is unchanged.)

Run: `go test ./cmd/at-cove/ -v`
Expected: PASS (all command tests green under the flag).

- [ ] **Step 6: Commit**

```bash
git add cmd/at-cove/
git commit -m "feat(cli): --kit-dir flag replaces the positional kit-dir on all commands"
```

---

### Task 2: `Collaborator` schema — `prompt` + `default`

**Files:**
- Modify: `internal/kit/config.go` (`Collaborator`, `ResolvedCollaborator`, `ParseConfig` validation)
- Modify: `internal/kit/config_test.go`

**Interfaces:**
- Produces:
  - `type Collaborator struct { Prompt string; Default bool; Secrets map[string]SecretConfig }`
  - `ResolvedCollaborator(class) (Collaborator, error)` unchanged signature, now also returns the resolved `Prompt`.
  - Validation: at most one `Default: true` across collaborators; `<common>` may set neither `Prompt` nor `Default`.

- [ ] **Step 1: Write the failing test**

Add to `internal/kit/config_test.go`:

```go
func TestCollaboratorPromptAndDefault(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
collaborators:
  <common>:
    secrets: {}
  triager:
    default: true
    prompt: "you are the steward"
    secrets:
      LINEAR_TOKEN: { description: "d" }
`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := cfg.ResolvedCollaborator("triager")
	if err != nil {
		t.Fatal(err)
	}
	if c.Prompt != "you are the steward" || !c.Default {
		t.Fatalf("resolved = %+v", c)
	}
}

func TestCollaboratorAtMostOneDefault(t *testing.T) {
	_, err := ParseConfig([]byte(`
name: k
collaborators:
  a: { default: true, prompt: "x" }
  b: { default: true, prompt: "y" }
`))
	if err == nil {
		t.Fatal("want error: two collaborators marked default")
	}
}

func TestCollaboratorCommonNoRole(t *testing.T) {
	_, err := ParseConfig([]byte(`
name: k
collaborators:
  <common>: { prompt: "nope" }
`))
	if err == nil {
		t.Fatal("want error: <common> must not set a prompt")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/kit/ -run TestCollaborator -v`
Expected: FAIL — `unknown field "prompt"` / `unknown field "default"` (KnownFields), and the validators don't exist.

- [ ] **Step 3: Write minimal implementation**

In `internal/kit/config.go`, extend the type:

```go
// Collaborator declares an interactive (chat) handler class: an optional role
// prompt injected into the session, an optional default marker, and a secrets
// bucket (inherited from the collaborators <common> base). GitHub/Linear ride
// the human's connectors, so secrets are often empty.
type Collaborator struct {
	Prompt  string                  `yaml:"prompt,omitempty"`
	Default bool                    `yaml:"default,omitempty"`
	Secrets map[string]SecretConfig `yaml:"secrets,omitempty"`
}
```

`ResolvedCollaborator` already returns the `own` value with merged secrets; since `own` now carries `Prompt`/`Default`, no change is needed there beyond the type. Confirm its body still reads `own, ok := c.Collaborators[class]` and returns `own` (with merged secrets) — the prompt rides along automatically.

Add validation in `ParseConfig`, in the existing collaborators loop (where `rejectReservedSecretNames` is called):

```go
	defaults := 0
	for name, col := range cfg.Collaborators {
		if err := rejectReservedSecretNames(fmt.Sprintf("collaborators[%q].secrets", name), col.Secrets); err != nil {
			return Config{}, err
		}
		if name == commonKey {
			if col.Prompt != "" || col.Default {
				return Config{}, fmt.Errorf("config.yml: collaborators[%q]: the base must not set a prompt or default", commonKey)
			}
			continue
		}
		if col.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return Config{}, fmt.Errorf("config.yml: collaborators: at most one may set default: true (got %d)", defaults)
	}
```

(Replace the existing simpler `for name, col := range cfg.Collaborators { rejectReservedSecretNames(...) }` loop with this expanded one.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/kit/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(kit): collaborators carry prompt + default (at-most-one; <common> role-free)"
```

---

### Task 3: `SelectCollaborator` — the selection rule

**Files:**
- Modify: `internal/kit/config.go` (add `SelectCollaborator`)
- Modify: `internal/kit/config_test.go`

**Interfaces:**
- Consumes: `Config.Collaborators`, `commonKey`.
- Produces: `func (c Config) SelectCollaborator(explicit string) (class string, ok bool, err error)` — `explicit` is the CLI positional (may be `""`). Rules: explicit non-empty → must be a defined non-`<common>` class (else error); explicit empty → the sole non-`<common>` class; if several, the `default: true` one; if several and none default, an error listing them; if none defined, `("", false, nil)` (plain session). `<common>` is never selectable.

- [ ] **Step 1: Write the failing test**

```go
func selCfg(t *testing.T, body string) Config {
	t.Helper()
	cfg, err := ParseConfig([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestSelectCollaborator(t *testing.T) {
	none := selCfg(t, "name: k\n")
	sole := selCfg(t, "name: k\ncollaborators:\n  triager: { prompt: x }\n")
	multi := selCfg(t, "name: k\ncollaborators:\n  a: { prompt: x }\n  b: { prompt: y, default: true }\n")
	multiNoDef := selCfg(t, "name: k\ncollaborators:\n  a: { prompt: x }\n  b: { prompt: y }\n")

	// none defined -> plain session
	if class, ok, err := none.SelectCollaborator(""); err != nil || ok || class != "" {
		t.Fatalf("none: %q,%v,%v", class, ok, err)
	}
	// sole, no explicit -> that one
	if class, ok, err := sole.SelectCollaborator(""); err != nil || !ok || class != "triager" {
		t.Fatalf("sole: %q,%v,%v", class, ok, err)
	}
	// multi, no explicit -> the default
	if class, ok, err := multi.SelectCollaborator(""); err != nil || !ok || class != "b" {
		t.Fatalf("multi-default: %q,%v,%v", class, ok, err)
	}
	// multi, no default, no explicit -> error
	if _, _, err := multiNoDef.SelectCollaborator(""); err == nil {
		t.Fatal("multi-no-default: want error")
	}
	// explicit match
	if class, ok, err := multi.SelectCollaborator("a"); err != nil || !ok || class != "a" {
		t.Fatalf("explicit: %q,%v,%v", class, ok, err)
	}
	// explicit miss -> error
	if _, _, err := sole.SelectCollaborator("nope"); err == nil {
		t.Fatal("explicit-miss: want error")
	}
	// <common> is not selectable
	if _, _, err := sole.SelectCollaborator(commonKey); err == nil {
		t.Fatal("<common> must not be selectable")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/kit/ -run TestSelectCollaborator -v`
Expected: FAIL — `undefined: SelectCollaborator`.

- [ ] **Step 3: Write minimal implementation**

```go
// SelectCollaborator resolves the CLI's optional collaborator positional to a
// class. explicit=="" means "choose the default": the sole class, or the one
// marked default:true, or an error if ambiguous. No collaborators defined ->
// ("", false, nil): a plain session. <common> is never selectable.
func (c Config) SelectCollaborator(explicit string) (string, bool, error) {
	names := make([]string, 0, len(c.Collaborators))
	for name := range c.Collaborators {
		if name != commonKey {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	if explicit != "" {
		if explicit == commonKey {
			return "", false, fmt.Errorf("%q is not a selectable collaborator", commonKey)
		}
		if _, ok := c.Collaborators[explicit]; !ok || explicit == commonKey {
			return "", false, fmt.Errorf("kit %q declares no collaborator %q (have: %s)", c.Name, explicit, strings.Join(names, ", "))
		}
		return explicit, true, nil
	}
	switch len(names) {
	case 0:
		return "", false, nil
	case 1:
		return names[0], true, nil
	default:
		for _, name := range names {
			if c.Collaborators[name].Default {
				return name, true, nil
			}
		}
		return "", false, fmt.Errorf("kit %q has multiple collaborators; specify one of: %s", c.Name, strings.Join(names, ", "))
	}
}
```

Ensure `sort` is imported in `config.go` (add `"sort"` to the import block if absent).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/kit/ -run TestSelectCollaborator -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(kit): SelectCollaborator — explicit/sole/default/ambiguous/none rule"
```

---

### Task 4: `chat` command + role injection

**Files:**
- Modify: `internal/connect/connect.go` (`Options.CollaboratorPrompt`; write the role file)
- Modify: `internal/connect/connect_test.go` (role-file write)
- Modify: `cmd/at-cove/main.go` (rename `connect`→`chat`; `doChat` wraps `doConnect` with kit-config load + selection + prompt)

**Interfaces:**
- Consumes: `kit.Load`, `Config.SelectCollaborator`, `Config.ResolvedCollaborator`, `usersecret.Store.Plan`, `mint.Expander`, `connect.Connect`.
- Produces: `Options.CollaboratorPrompt string`; a VM path constant `collaboratorVMPath = "/agent-data/COLLABORATOR.md"`; `doChat(collaborator, kitDir string, r runner.Runner, dryRun, raw, noAuth, fresh bool, stdout, stderr io.Writer) error`.

- [ ] **Step 1: Write the failing test (connect writes the role file)**

`writeCollaboratorRole` is a small unit — test it directly against a `runner.Fake` (a zero `sshargs.Target` is fine; the Fake just records the call). Add to `internal/connect/connect_test.go` (ensure the file imports `"strings"`, `"github.com/aethons-tools/cove/internal/runner"`, and `"github.com/aethons-tools/cove/internal/sshargs"` — some may already be present):

```go
func TestWriteCollaboratorRole(t *testing.T) {
	f := &runner.Fake{}
	if err := writeCollaboratorRole(f, sshargs.Target{}, "you are the steward"); err != nil {
		t.Fatal(err)
	}
	var wrote bool
	for _, c := range f.Calls {
		if strings.Contains(strings.Join(c.Args, " "), collaboratorVMPath) && c.Stdin == "you are the steward" {
			wrote = true
		}
	}
	if !wrote {
		t.Fatalf("no ssh write of %s with the prompt; calls=%+v", collaboratorVMPath, f.Calls)
	}
}

func TestWriteCollaboratorRoleEmptyWritesPlaceholder(t *testing.T) {
	f := &runner.Fake{}
	if err := writeCollaboratorRole(f, sshargs.Target{}, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) == 0 || f.Calls[0].Stdin == "" {
		t.Fatalf("empty prompt must still write a placeholder; calls=%+v", f.Calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/connect/ -run TestConnectWritesRoleFile -v`
Expected: FAIL — `undefined: writeCollaboratorRole` / `collaboratorVMPath`.

- [ ] **Step 3: Write minimal implementation (connect side)**

In `internal/connect/connect.go`, add the constant, the `Options` field, and a writer that mirrors `seedCredentials`' `umask 077; cat > path` pattern:

```go
// collaboratorVMPath is where chat writes the selected collaborator's role
// prompt; the seeded CLAUDE.md @-includes it. Not a secret (source-controlled
// kit content), but written the same in-memory-over-ssh way.
const collaboratorVMPath = "/agent-data/COLLABORATOR.md"
```

Add to `Options`:

```go
	// CollaboratorPrompt is the selected collaborator's role prompt, written to
	// collaboratorVMPath before launch so the session's CLAUDE.md includes it.
	// Empty writes a benign placeholder (clears any prior role).
	CollaboratorPrompt string
```

Add the writer (near `seedCredentials`):

```go
// writeCollaboratorRole writes the role prompt (or a placeholder when empty) to
// the VM's COLLABORATOR.md over ssh, so the session's CLAUDE.md include resolves
// to the active role.
func writeCollaboratorRole(r runner.Runner, tgt sshargs.Target, prompt string) error {
	body := prompt
	if body == "" {
		body = "# (no collaborator role active)\n"
	}
	args := append(sshargs.Base(tgt), "umask 077; cat > "+collaboratorVMPath)
	if err := r.RunStdin(bytes.NewReader([]byte(body)), "ssh", args...); err != nil {
		return fmt.Errorf("writing collaborator role: %w", err)
	}
	return nil
}
```

Call it in `Connect`, after the VM is confirmed running / dialed and before `t.Launch` (i.e. alongside where credentials are seeded — see the existing `seedCredentials` call site):

```go
	if err := writeCollaboratorRole(r, tgt, o.CollaboratorPrompt); err != nil {
		return err
	}
```

(Place it after `tgt` is constructed and the auth/seed steps, before `launchErr := t.Launch(tgt, env)`.)

- [ ] **Step 4: Run test to verify it passes (connect side)**

Run: `go test ./internal/connect/ -v`
Expected: PASS.

- [ ] **Step 5: Rename `connect`→`chat` and wire `doChat`**

In `cmd/at-cove/main.go`, replace the `connect` command with `chat`. It registers `--kit-dir` + the interactive flags, takes the optional collaborator positional, and calls `doChat`:

```go
			{Name: "chat", Brief: "open an interactive collaborator session in the sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("chat", flag.ContinueOnError)
				fs.SetOutput(errw)
				kd := kitDirFlag(fs)
				raw := fs.Bool("raw", false, "open a raw shell instead of the agent")
				noAuth := fs.Bool("no-auth", false, "skip the interactive login step")
				fresh := fs.Bool("fresh", false, "start a fresh agent session")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				collaborator := ""
				if len(pos) == 1 {
					collaborator = pos[0]
				} else if len(pos) > 1 {
					fmt.Fprintln(errw, "at-cove: chat takes at most one collaborator")
					return 2
				}
				kitDir, err := resolveKit(*kd)
				if err != nil {
					fmt.Fprintln(errw, "at-cove:", err)
					return 1
				}
				return exitCode("at-cove", doChat(collaborator, kitDir, r, g.DryRun, *raw, *noAuth, *fresh, out, errw), errw)
			}},
```

Rename `doConnect` → `doChat` and add the `collaborator` parameter + the kit-config/selection/prompt logic. The secret-resolution + launch body is unchanged except for adding the collaborator's secrets and prompt:

```go
func doChat(collaborator, kitDir string, r runner.Runner, dryRun, raw, noAuth, fresh bool, stdout, stderr io.Writer) error {
	st, err := state.Load(kitDir)
	if err != nil {
		return err
	}
	// chat is kit-aware: a malformed/absent kit config is a hard error.
	cfg, err := kit.Load(kitDir)
	if err != nil {
		return fmt.Errorf("chat requires a valid kit config: %w", err)
	}
	class, hasCollab, err := cfg.SelectCollaborator(collaborator)
	if err != nil {
		return err
	}
	var role kit.Collaborator
	if hasCollab {
		if role, err = cfg.ResolvedCollaborator(class); err != nil {
			return err
		}
	}

	// Demanded secrets: the state/root demands (as before) plus the collaborator's.
	demandSet := map[string]struct{}{}
	demanded := make([]string, 0, len(st.Secrets)+len(role.Secrets))
	for _, s := range st.Secrets {
		if _, dup := demandSet[s.Name]; !dup {
			demandSet[s.Name] = struct{}{}
			demanded = append(demanded, s.Name)
		}
	}
	for name := range role.Secrets {
		if _, dup := demandSet[name]; !dup {
			demandSet[name] = struct{}{}
			demanded = append(demanded, name)
		}
	}
	secretsPath := filepath.Join(configDir(), "secrets.yml")
	localPath := filepath.Join(configDir(), "secrets.local.yml")
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		return err
	}
	expand := mint.Expander(r, store.Global, "") // chat mints no github token (connectors)
	specs, unresolved, err := store.Plan(st.Name, canonicalKitPath(kitDir), demanded, expand)
	if err != nil {
		return err
	}
	for _, name := range unresolved {
		fmt.Fprintf(stderr, "at-cove: warning: secret %q is demanded but has no supply for kit %q in %s (or secrets.local.yml); it will not be set\n", name, st.Name, secretsPath)
	}

	launch := "claude"
	if raw {
		launch = "bash"
	}
	resume := !raw && !fresh
	if dryRun {
		who := "no collaborator"
		if hasCollab {
			who = "collaborator " + class
		}
		fmt.Fprintf(stdout, "would resolve %d secrets and connect to %s as %s, launching %s\n",
			len(specs), st.Container, who, launch)
		return nil
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}
	lock, err := state.AcquireShared(kitDir)
	if err != nil {
		if errors.Is(err, state.ErrLocked) {
			return fmt.Errorf("sandbox %q is being destroyed; try again shortly", st.Container)
		}
		return err
	}
	defer lock.Release()
	priv, _, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	cmd := ""
	if raw {
		cmd = "bash"
	}
	return connect.Connect(b, r, connect.StdinScript{R: r, Cmd: cmd, Resume: resume, Name: st.Name}, awake.New(), connect.Options{
		Container:          st.Container,
		Secrets:            specs,
		IdentityFile:       priv,
		KnownHostsDir:      filepath.Join(configDir(), "known_hosts.d"),
		SkipAuth:           noAuth,
		Stderr:             stderr,
		CredentialsFile:    filepath.Join(configDir(), "credentials.json"),
		CollaboratorPrompt: role.Prompt,
	})
}
```

Add the `kit` import to `main.go` if not already present (it is used by `work`/`dispatch`, so likely present).

- [ ] **Step 6: Run tests + full build**

Run: `go test ./cmd/at-cove/ ./internal/connect/ ./internal/kit/ -v` then `go build ./...` and `go test ./...`.
Expected: PASS across the repo. Migrate any `connect`-named command test to `chat` (e.g. `run([]string{"connect", ...})` → `run([]string{"chat", "--kit-dir", dir})`); a `chat` dry-run test asserting the resolved collaborator line is a good add.

- [ ] **Step 7: Commit**

```bash
git add cmd/at-cove/ internal/connect/
git commit -m "feat(chat): rename connect->chat; select collaborator + inject role prompt"
```

---

### Task 5: image payload — the `COLLABORATOR.md` include (hardening layer)

**Files:**
- Modify: `internal/assemble/hardening/image-files/home/agent/.init-agent-data/CLAUDE.md`
- Create: `internal/assemble/hardening/image-files/home/agent/.init-agent-data/COLLABORATOR.md`
- Modify: an `internal/assemble` test asserting the built image carries both.

**Interfaces:** none (payload).

- [ ] **Step 1: Write the failing test**

The hardening tree is copied wholesale into every image, so its source *is* what ships. Assert the two invariants against the payload (a test in package `assemble`, so paths are relative to `internal/assemble/`). Add to `internal/assemble/assemble_test.go` (or a new `collaborator_test.go`):

```go
func TestCollaboratorRoleFileSeeded(t *testing.T) {
	base := filepath.Join("hardening", "image-files", "home", "agent", ".init-agent-data")
	b, err := os.ReadFile(filepath.Join(base, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "@COLLABORATOR.md") {
		t.Fatalf("hardening CLAUDE.md must @-include COLLABORATOR.md:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(base, "COLLABORATOR.md")); err != nil {
		t.Fatalf("default COLLABORATOR.md missing from the hardening payload: %v", err)
	}
}
```

(If this package already has an embed-FS assertion helper in `embed_test.go`, an equivalent assertion against the embedded FS is even better; the invariant — include line present + default file exists — is what matters. Ensure the test file imports `os`, `path/filepath`, `strings`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/assemble/ -run TestCollaboratorRoleFileSeeded -v`
Expected: FAIL — `@COLLABORATOR.md` absent / no default file.

- [ ] **Step 3: Add the include + default file**

Append the include to the hardening `CLAUDE.md` so it reads:

```
@PROGRESSIVE_DISCLOSURE.md
@SANDBOX.md
@COLLABORATOR.md
```

Create the default `COLLABORATOR.md`:

```
# (no collaborator role active)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/assemble/ -v`
Expected: PASS. Then `go test ./...` — clean.

- [ ] **Step 5: Commit**

```bash
git add internal/assemble/hardening/image-files/home/agent/.init-agent-data/
git commit -m "feat(image): seed COLLABORATOR.md role include (hardening layer, default empty)"
```

---

### Task 6: docs

**Files:**
- Modify: `docs/usage/at-cove-config.md` (`collaborators.prompt`/`default`)
- Modify/rename: the `connect` usage doc → `chat` (and the `--kit-dir` change wherever a kit-dir positional is documented)
- Modify: `docs/OVERVIEW.md`, `docs/usage/INDEX.md`, `docs/orchestration/` (the collaborator session's plan-vs-implement boundary)

**Interfaces:** none (docs).

- [ ] **Step 1: Confirm the doc surface**

Run: `grep -rn 'connect\|kit-dir\|\[kit-dir\]' docs/usage docs/OVERVIEW.md docs/orchestration | grep -v superpowers`
Expected: the command references + kit-dir positional mentions to update.

- [ ] **Step 2: (docs — no code test)**

- [ ] **Step 3: Update the docs**

- `docs/usage/at-cove-config.md`: document `collaborators.<class>.prompt` (role injected as `CLAUDE.md` context) and `.default` (the default when `chat` gets no collaborator); note at-most-one-default and that `<common>` is role-free; note secrets are usually empty (connectors).
- The interactive-session usage doc: rename `connect` → `chat`; document the optional collaborator positional + selection rule; replace any `[kit-dir]` positional with `--kit-dir`. Bump `updated: 2026-07-14` and fix its `INDEX.md` row.
- `docs/OVERVIEW.md`: the command is `chat`; the collaborator session's boundary (plan → dispatch to Linear; only review/troubleshoot may fix in place; GitHub/Linear via connectors).
- Any `docs/orchestration/` doc describing the interactive/human classes: point at `chat` + the boundary.
- Ensure every command doc that showed `at-cove <cmd> <kit-dir>` now shows `at-cove <cmd> --kit-dir <dir>`.

- [ ] **Step 4: Verify links + commit**

Run: `grep -rn '\[kit-dir\]\|at-cove connect' docs/usage docs/OVERVIEW.md docs/orchestration | grep -v superpowers || echo "clean"`
Expected: `clean` (no stale positional-kit-dir or `connect` references in live docs). Scope a docs checker to `docs/usage` if available.

```bash
git add docs/
git commit -m "docs(chat): collaborator prompt/default, chat rename, --kit-dir across commands"
```

---

## Notes for the executor

- **Task 1 is a broad but mechanical sweep** — the hard part is migrating every `main_test.go` invocation from a positional kit-dir to `--kit-dir`. Grep, don't guess. After Task 1, `go test ./...` must be green.
- **The `board-*` skill content is NOT in this plan** (spec Appendix B) — do not author intake/plan/execute here.
- Collaborators use the human's connectors for GitHub/Linear — **do not add any token minting on the chat path**; `mint.Expander(r, store.Global, "")` (empty repo) is correct, exactly as the old `connect` used it.
- The role prompt is not a secret; writing it to `/agent-data/COLLABORATOR.md` in cleartext is intended.
- After Task 4, a kit with no collaborators must still `chat` into a plain session (behavior-preserving rename).
