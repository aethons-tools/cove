# Secret supply fallback for work + dispatch (Option A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the well-known secrets (`AT_TASK_GIT_TOKEN`, `AT_DISPATCH_TRACKER_TOKEN`, `AT_DISPATCH_WEBHOOK_SECRET`) — and general secrets in a dispatch kit — be supplied out-of-band from the user's `~/.config/at-cove/secrets.yml` instead of requiring a resolver `command` committed in the kit. Today only `connect` consults `secrets.yml`; `work` and `dispatch` are command-only. This wires the same demand/supply merge into `work` and `dispatch` and relaxes the well-known-secret validation to make `command` optional.

**Architecture:** `connect` already does the merge (`usersecret.Load` → `store.Plan(demanded)`). We factor the "resolve one required secret via the supply store, fail closed" step into a small testable helper `planRequired`, reuse it in `doDispatch` (the tracker token) and `doWork` (the code-host token), use `store.Plan` for `work`'s general (agent-injected) secrets, and change `checkWellKnownSecrets` from "requires a command" to "must be present (command optional)". The credential air-gap is unchanged: the git token stays a distinct `Options.GitToken` spec, never merged into the agent bucket.

**Tech Stack:** Go 1.22 — **no new dependencies** (`internal/usersecret`, `internal/secret` already exist). TDD; every commit builds + `go test ./...` green; `gofmt` clean.

## Global Constraints

- **No new `go.mod`/`go.sum` deps.** Verify unchanged after every task.
- **Well-known secrets become command-optional:** each declared well-known key must be *present* (typo protection), but its `command` may be empty; unknown keys under `source-control.github.secrets` / `tracker.linear.secrets` are still rejected.
- **Fail closed on required secrets:** if a required secret (dispatch's `AT_DISPATCH_TRACKER_TOKEN`, work's `AT_TASK_GIT_TOKEN`) has neither a kit `command` nor a `secrets.yml` entry, the command errors (non-zero) and names the missing secret + the `secrets.yml` path. General (agent-injected) `work` secrets that are unresolved **warn, non-fatal** (mirroring `connect`) and are left unset.
- **Air-gap preserved:** the code-host token remains a distinct `Options.GitToken` spec — never merged into the general `Secrets` bucket that reaches the agent step. A `secrets.yml`-supplied literal git token is allowed (Option A accepts a standing value; per-step resolution still runs — a literal simply yields the same value each step).
- **`connect` is unchanged** (it already does the supply merge).
- **Resolution precedence** (from `store.Plan`): a kit `command` wins; else a `secrets.yml` scalar → literal value; else a `secrets.yml` list → resolver command; else unresolved.

---

## Task 1: relax well-known-secret validation to command-optional

**Files:** `internal/kit/config.go`, `internal/kit/config_test.go`, `docs/usage/at-cove-config.md`.

**Interfaces produced:** `checkWellKnownSecrets(field string, got map[string]SecretConfig, allowed ...string) error` — now requires each `allowed` key to be *present* (command optional), still rejects unknown keys.

- [ ] **Step 1: Update the tests**

In `internal/kit/config_test.go`, add a test that a command-less well-known secret is now valid, and confirm the present/unknown rules still hold:

```go
func TestParseConfigWellKnownSecretCommandOptional(t *testing.T) {
	// A well-known secret may omit its command (supplied from secrets.yml at run time).
	src := `
name: k
tracker:
  linear:
    team: COV
    poll-interval: 60s
    states: { ready: Todo, in-progress: In Progress, in-review: In Review, done: Done, needs-input: Needs Input, blocked: Backlog }
    secrets:
      AT_DISPATCH_TRACKER_TOKEN: {}
      AT_DISPATCH_WEBHOOK_SECRET: {}
`
	if _, err := ParseConfig([]byte(src)); err != nil {
		t.Fatalf("command-less well-known secrets must be valid: %v", err)
	}
}

func TestParseConfigWellKnownSecretMissingKeyRejected(t *testing.T) {
	// Dropping a required well-known key is still an error (typo protection).
	src := `
name: k
tracker:
  linear:
    team: COV
    poll-interval: 60s
    states: { ready: Todo, in-progress: In Progress, in-review: In Review, done: Done, needs-input: Needs Input, blocked: Backlog }
    secrets:
      AT_DISPATCH_TRACKER_TOKEN: {}
`
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("missing AT_DISPATCH_WEBHOOK_SECRET must be rejected")
	}
}
```

If any existing test asserts that a **command-less** well-known secret is *rejected* (e.g. an assertion tied to "is required (with a resolver command)"), flip it to expect success. Re-grep: `grep -n "with a resolver command\|is required" internal/kit/config_test.go`. (`TestParseConfigRejectsUnknownTrackerSecret` and `TestParseConfigRejectsUnknownSourceControlSecret` stay — unknown keys are still rejected.)

- [ ] **Step 2: Run to confirm the new test fails**

Run: `go test ./internal/kit/ -run WellKnownSecret -v`
Expected: `TestParseConfigWellKnownSecretCommandOptional` FAILS (current code rejects the empty command); the missing-key test passes.

- [ ] **Step 3: Relax `checkWellKnownSecrets`**

In `internal/kit/config.go`, drop the `len(s.Command) == 0` requirement — require only presence:

```go
// checkWellKnownSecrets requires each allowed secret name to be present (a
// command is optional — a command-less entry is supplied from the user's
// secrets.yml at run time) and rejects any name outside the allowed set.
func checkWellKnownSecrets(field string, got map[string]SecretConfig, allowed ...string) error {
	want := map[string]bool{}
	for _, a := range allowed {
		want[a] = true
		if _, ok := got[a]; !ok {
			return fmt.Errorf("config.yml: %s.%s is required", field, a)
		}
	}
	for k := range got {
		if !want[k] {
			return fmt.Errorf("config.yml: %s: unknown secret %q (allowed: %v)", field, k, allowed)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to confirm green**

Run: `go test ./internal/kit/`
Expected: PASS (all kit tests, including the reference-kit test and the two new ones).

- [ ] **Step 5: Document the relaxed rule**

In `docs/usage/at-cove-config.md`, update the `source-control.github.secrets` and `tracker.linear.secrets` prose: a well-known secret **must be declared** but its `command` is **optional** — omit it to supply the value from `~/.config/at-cove/secrets.yml` (matched by name), exactly like general secrets. Update the validation-summary bullet that says a well-known secret "requires a resolver command" → "must be declared (command optional; else supplied from secrets.yml)". Bump `updated: 2026-07-13`. Follow the **docs-author** skill; link to [at-cove-secrets.md](at-cove-secrets.md) for the supply mechanics rather than restating them.

- [ ] **Step 6: Verify + commit**

```
go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/usage/
git diff --stat go.mod go.sum
```
Expected: green; docs-audit 0 errors (pre-existing warnings ok); deps unchanged.
```bash
git commit -am "feat(kit): well-known secrets are command-optional (supply from secrets.yml)"
```

---

## Task 2: wire the secrets.yml supply into `work` + `dispatch`

**Files:** `cmd/at-cove/main.go`, `cmd/at-cove/main_test.go`, `docs/usage/at-cove-secrets.md`.

**Interfaces produced:** `planRequired(store usersecret.Store, name string, kitCommand []string, secretsPath string) (secret.Spec, error)` — plans one required secret through the supply store (kit command wins, else `secrets.yml`), returning the resolvable spec, or an error naming the secret + path if unresolved.

**Interfaces consumed (already exist):** `usersecret.Load(path) (Store, error)`; `(Store).Plan([]secret.Spec) (resolvable []secret.Spec, unresolved []string)`; `secret.Resolve(r runner.Runner, extraEnv map[string]string, specs []secret.Spec) (map[string]string, error)`; `cfg.GitTokenSpec() (secret.Spec, bool)` (ok = key declared; Command may be empty). `configDir()` resolves `~/.config/at-cove` (honoring `XDG_CONFIG_HOME`).

- [ ] **Step 1: Write the failing helper test**

In `cmd/at-cove/main_test.go`, add:

```go
func TestPlanRequired(t *testing.T) {
	// kit command wins.
	sp, err := planRequired(usersecret.Store{"T": {Value: "fromfile"}}, "T", []string{"kitcmd"}, "/p")
	if err != nil || sp.Literal || len(sp.Command) != 1 || sp.Command[0] != "kitcmd" {
		t.Fatalf("kit command should win: %+v err=%v", sp, err)
	}
	// no kit command → supplied literal from the store.
	sp, err = planRequired(usersecret.Store{"T": {Value: "v"}}, "T", nil, "/p")
	if err != nil || !sp.Literal || sp.Value != "v" {
		t.Fatalf("store value should supply a literal: %+v err=%v", sp, err)
	}
	// neither → error naming the secret + path.
	if _, err := planRequired(usersecret.Store{}, "T", nil, "/p/secrets.yml"); err == nil ||
		!strings.Contains(err.Error(), "T") || !strings.Contains(err.Error(), "/p/secrets.yml") {
		t.Fatalf("unresolved must error naming the secret and path; got %v", err)
	}
}
```

Ensure `main_test.go` imports `github.com/aethons-tools/cove/internal/usersecret` and `strings` (add if missing).

- [ ] **Step 2: Run to confirm it fails**

Run: `go test ./cmd/at-cove/ -run TestPlanRequired`
Expected: build failure (`planRequired` undefined).

- [ ] **Step 3: Add the `planRequired` helper**

In `cmd/at-cove/main.go`:

```go
// planRequired resolves one required secret through the user's supply store: a
// kit command wins; else the secrets.yml entry supplies it (literal or command).
// It errors, naming the secret and the secrets.yml path, if neither provides it.
func planRequired(store usersecret.Store, name string, kitCommand []string, secretsPath string) (secret.Spec, error) {
	planned, unresolved := store.Plan([]secret.Spec{{Name: name, Command: kitCommand}})
	if len(unresolved) > 0 {
		return secret.Spec{}, fmt.Errorf("%s has no command in the kit and no entry in %s", name, secretsPath)
	}
	return planned[0], nil
}
```

- [ ] **Step 4: Wire `doDispatch` to resolve the tracker token via supply**

In `doDispatch`, replace the direct token resolution:

```go
	tokSpec := cfg.Tracker.Linear.Secrets["AT_DISPATCH_TRACKER_TOKEN"]
	out, err := runner.OS{}.Output(tokSpec.Command[0], tokSpec.Command[1:]...)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: resolve tracker token: %v\n", err)
		return 1
	}
	token := strings.TrimSuffix(out, "\n")
```

with:

```go
	secretsPath := filepath.Join(configDir(), "secrets.yml")
	store, err := usersecret.Load(secretsPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: %v\n", err)
		return 1
	}
	tokSpec := cfg.Tracker.Linear.Secrets["AT_DISPATCH_TRACKER_TOKEN"]
	planned, err := planRequired(store, "AT_DISPATCH_TRACKER_TOKEN", tokSpec.Command, secretsPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: %v\n", err)
		return 1
	}
	resolved, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{planned})
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: resolve tracker token: %v\n", err)
		return 1
	}
	token := resolved["AT_DISPATCH_TRACKER_TOKEN"]
```

(`filepath`, `usersecret`, `secret`, `runner` are already imported in `main.go`.)

- [ ] **Step 5: Wire `doWork` to resolve general secrets + the git token via supply**

In `doWork`, replace the current secret block:

```go
	specs := make([]secret.Spec, 0, len(cfg.Secrets))
	for name, s := range cfg.Secrets {
		specs = append(specs, secret.Spec{Name: name, Command: s.Command})
	}
	gitTok, ok := cfg.GitTokenSpec()
	if !ok {
		fmt.Fprintf(stderr, "at-cove: kit %q declares no source-control.github.secrets AT_TASK_GIT_TOKEN\n", cfg.Name)
		return 1
	}
```

with:

```go
	secretsPath := filepath.Join(configDir(), "secrets.yml")
	store, err := usersecret.Load(secretsPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	// General (agent-injected) secrets: kit command, else secrets.yml; unresolved warn.
	demanded := make([]secret.Spec, 0, len(cfg.Secrets))
	for name, s := range cfg.Secrets {
		demanded = append(demanded, secret.Spec{Name: name, Command: s.Command})
	}
	specs, unresolved := store.Plan(demanded)
	for _, name := range unresolved {
		fmt.Fprintf(stderr, "at-cove: warning: secret %q has no command and no entry in %s; it will not be set\n", name, secretsPath)
	}
	// The code-host token stays a distinct spec (the air-gap); required, fail closed.
	gitDemand, ok := cfg.GitTokenSpec()
	if !ok {
		fmt.Fprintf(stderr, "at-cove: kit %q declares no source-control.github.secrets AT_TASK_GIT_TOKEN\n", cfg.Name)
		return 1
	}
	gitTok, err := planRequired(store, gitDemand.Name, gitDemand.Command, secretsPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
```

Leave the `dispatchrun.Options{… Secrets: specs, GitToken: gitTok …}` literal as-is (both names still bind). **Check for a name clash:** `doWork` may already declare `err` earlier via `:=`; if the compiler reports "no new variables on left side of :=" at the `store, err :=` line, split it (`var store usersecret.Store; store, err = …`) or reuse the existing `err`. Verify with `go build`.

- [ ] **Step 6: Add a dispatch supply-path test**

In `cmd/at-cove/main_test.go`, add a test that a **command-less** `AT_DISPATCH_TRACKER_TOKEN` resolves from a `secrets.yml` under the temp config dir. Model it on the existing dispatch tests (they write a kit dir and call `run([]string{"dispatch", dir}, …)`); point `XDG_CONFIG_HOME` at a temp dir containing `at-cove/secrets.yml`:

```go
func TestDispatchTrackerTokenFromSecretsYML(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	if err := os.MkdirAll(filepath.Join(cfgHome, "at-cove"), 0o755); err != nil {
		t.Fatal(err)
	}
	// secrets.yml supplies the tracker token as a literal.
	if err := os.WriteFile(filepath.Join(cfgHome, "at-cove", "secrets.yml"),
		[]byte("AT_DISPATCH_TRACKER_TOKEN: supplied-tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A valid dispatch kit whose tracker token is command-LESS.
	p := writeDispatchConfig(t, strings.Replace(dispatchGoodConfig,
		`AT_DISPATCH_TRACKER_TOKEN: { command: ["true"] }`, `AT_DISPATCH_TRACKER_TOKEN: {}`, 1))
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", p}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	// It must get PAST token resolution (past "kit OK"); it then fails connecting to
	// Linear (no network), which is fine — the point is the token resolved from secrets.yml.
	if !strings.Contains(out.String(), "kit OK") {
		t.Fatalf("expected to reach token resolution + connect; stdout=%q stderr=%q", out.String(), errOut.String())
	}
	if strings.Contains(errOut.String(), "AT_DISPATCH_TRACKER_TOKEN has no command") {
		t.Fatalf("token should have resolved from secrets.yml; stderr=%q", errOut.String())
	}
	_ = code
}
```

> Adjust `writeDispatchConfig`/`dispatchGoodConfig` references to match the current helper names in `main_test.go` (re-grep). If `dispatchGoodConfig`'s token line differs textually, replace the *actual* line. The assertion is: it reaches "kit OK" and does **not** print the unresolved-token error. (Confirm the current dispatch tests' exact style with `grep -n "writeDispatchConfig\|dispatchGoodConfig\|kit OK" cmd/at-cove/main_test.go`.)

- [ ] **Step 7: Document that work + dispatch consult secrets.yml**

In `docs/usage/at-cove-secrets.md`, update the scope: the `secrets.yml` supply store is consulted by `connect` **and now `work` + `dispatch`** — any kit-demanded secret (general or well-known) without a `command` is supplied from it; a *required* secret (dispatch's tracker token, work's code-host token) that resolves from neither is a fail-closed error, while an unresolved general/agent secret warns and is left unset. Bump `updated: 2026-07-13`. Follow **docs-author**; keep the supply-file format section (scalar = value, list = command) as the single source — link, don't restate.

- [ ] **Step 8: Verify + commit**

```
go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/
go test ./cmd/at-cove/ -run 'PlanRequired|DispatchTrackerTokenFromSecretsYML' -v
go build -tags integration ./internal/dispatchrun/ ./internal/dispatch/...
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/usage/
git diff --stat go.mod go.sum
```
Expected: green; the two new tests pass; docs-audit 0 errors; deps unchanged.
```bash
git commit -am "feat(cli): work + dispatch resolve secrets from secrets.yml (command-optional, fail-closed)"
```

---

## Final verification (whole change)

- [ ] `go build ./...`, `go vet ./...`, `go test ./...` green; `gofmt -l cmd/ internal/` empty; `go.mod`/`go.sum` unchanged; integration builds compile.
- [ ] A dispatch kit with `AT_DISPATCH_TRACKER_TOKEN: {}` (no command) resolves the token from `~/.config/at-cove/secrets.yml`; with neither command nor supply it fails closed naming the secret + path.
- [ ] A work kit with a command-less `AT_TASK_GIT_TOKEN` resolves it from `secrets.yml`; the token stays a distinct `Options.GitToken` (never in the agent bucket); unresolved general secrets warn (non-fatal).
- [ ] `connect` behavior unchanged.
- [ ] `at-cove-config.md` (well-known command-optional) and `at-cove-secrets.md` (work/dispatch consult secrets.yml) updated; docs-audit clean.

## Notes

- **Why a helper (`planRequired`):** the "kit command or secrets.yml, else fail closed" logic is shared by the tracker token and the code-host token and lives in `cmd/at-cove` (awkward to reach through the full `work` backend path), so factoring it out gives a direct unit test without standing up Colima.
- **Air-gap unchanged:** the code-host token is planned separately and passed as `Options.GitToken`; it is never part of the general `Secrets` slice the agent step receives. A supplied *literal* git token is permitted (Option A's accepted trade-off); the per-step resolution in `dispatchrun` still runs (a literal just yields the same value each step; a supplied *command* — e.g. the minter — still runs fresh per step).
- **Scope:** this is Option A only. A git-token-specific guard (Option B) and env-var supply (Option C) are deliberately out of scope.
