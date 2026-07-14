# at-mint Plan 3 — wire `mint:` end-to-end Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the `mint:` supply source runnable — turn a `minters:` profile into an `at-mint <provider>` invocation via a new `internal/mint` expander, wire that expander into `at-cove`'s three secret-resolution sites, and switch the reference kit to `mint:` profiles.

**Architecture:** `mint.Expander(r, globals)` returns a `usersecret.MintExpander` that maps a `usersecret.Minter` to a `secret.Spec`: non-secret identifiers become `at-mint` **flags** (in `Command`), and resolved secret material becomes per-spec **env** (a new `secret.Spec.Env`, merged by `secret.Resolve` into the command's environment — in memory, never on argv). `cmd/at-cove` builds the expander and passes it to `Store.Plan`/`planRequired` in place of the `nil` used until now. The GitHub App key given as a literal path stays a `--app-key-file` flag; a command/global-sourced key or the Auth0 client secret flows via env.

**Tech Stack:** Go 1.22, standard library + the in-repo `internal/{runner,secret,usersecret}`. No new dependencies. Hermetic tests via `runner.Fake`.

## Global Constraints

- **No new dependencies.** `go.mod`/`go.sum` unchanged.
- **Secrets never on disk/argv/logs.** Resolved secret material (Auth0 client secret; a command/global-sourced GitHub App key) flows to `at-mint` ONLY via per-spec env (in memory). It must never appear in a `secret.Spec.Command` (argv) or in a log/error. A GitHub App key given as a literal *path* is non-secret and is a `--app-key-file` flag.
- **The air-gap holds.** The git token remains a distinct demand resolved into `dispatchrun.Options.GitToken` and minted fresh per git step; adding `Env` to its spec changes nothing about when/where it runs — the App key (in the spec's Env) is consumed host-side to run `at-mint`, and only `at-mint`'s stdout (the minted token) crosses into the VM. The agent step still runs on `base` secrets only.
- **Fail-closed.** A `mint:` referencing a minter whose secret field can't resolve (missing global, failing command) aborts resolution with an error naming the demand.
- **`global`/`minters` stay inert** (Plan 1 invariant) — the expander is reached only through an explicit `{mint: profile}` an operator wrote under a kit.
- **`at-mint`'s contract is unchanged** (Plan 2): flags = non-secret, env = secret. The expander MUST honor it — put nothing secret in the argv it builds.

---

## File Structure

- `internal/secret/secret.go` (modify) — add `Spec.Env map[string]string`; `Resolve` merges it into each command spec's env.
- `internal/secret/secret_test.go` (modify) — a test that `Spec.Env` reaches the command's env and not its argv.
- `internal/mint/mint.go` (new) — `Expander`, `githubSpec`, `anthropicSpec`, `resolveSource`.
- `internal/mint/mint_test.go` (new) — hermetic expander tests.
- `cmd/at-cove/main.go` (modify) — build `mint.Expander(...)` in `doWork`/`doDispatch`/`doConnect`; thread it through `planRequired`; replace the three `nil` expanders.
- `cmd/at-cove/main_test.go` (modify) — a test that a `{mint: …}` supply resolves to an `at-mint` spec through the wired path.
- `kits/reference-worker/RUNBOOK.md` (modify) — machine-side supply uses `minters:` profiles + `{mint: …}`.
- `docs/usage/at-cove-secrets.md`, `docs/usage/at-mint.md`, `docs/OVERVIEW.md` (modify) — `mint:` is now runnable.

---

### Task 1: `secret.Spec.Env` — per-spec command env

**Files:**
- Modify: `internal/secret/secret.go`
- Modify: `internal/secret/secret_test.go`

**Interfaces:**
- Produces: `secret.Spec` gains `Env map[string]string`; `Resolve` merges `s.Env` (spec wins) over `extraEnv` for each command spec's `OutputEnv` call. Literal specs and specs with a nil `Env` behave exactly as before.

- [ ] **Step 1: Write the failing test**

Add to `internal/secret/secret_test.go`:

```go
func TestResolveMergesSpecEnv(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "minted-token\n"}}}
	specs := []Spec{{
		Name:    "TOK",
		Command: []string{"at-mint", "github", "--app-id", "1"},
		Env:     map[string]string{"AT_MINT_GITHUB_APP_KEY": "SECRETPEM"},
	}}
	out, err := Resolve(f, map[string]string{"COVE_RUN_REPO": "o/r"}, specs)
	if err != nil {
		t.Fatal(err)
	}
	if out["TOK"] != "minted-token" {
		t.Fatalf("TOK = %q", out["TOK"])
	}
	if len(f.Calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(f.Calls))
	}
	c := f.Calls[0]
	// the secret is in the command ENV, not the argv
	joinedArgs := strings.Join(append([]string{c.Name}, c.Args...), " ")
	if strings.Contains(joinedArgs, "SECRETPEM") {
		t.Fatalf("secret leaked into argv: %q", joinedArgs)
	}
	joinedEnv := strings.Join(c.Env, " ")
	if !strings.Contains(joinedEnv, "AT_MINT_GITHUB_APP_KEY=SECRETPEM") {
		t.Fatalf("spec env not applied: %v", c.Env)
	}
	if !strings.Contains(joinedEnv, "COVE_RUN_REPO=o/r") {
		t.Fatalf("extraEnv not applied: %v", c.Env)
	}
}
```

Ensure the test file imports `"strings"` and `"github.com/aethons-tools/cove/internal/runner"` (add if missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/secret/ -run TestResolveMergesSpecEnv -v`
Expected: FAIL — `unknown field 'Env' in struct literal`.

- [ ] **Step 3: Write minimal implementation**

In `internal/secret/secret.go`, add the field to `Spec`:

```go
type Spec struct {
	Name    string
	Command []string
	Value   string
	Literal bool
	// Env is extra KEY=VALUE environment for THIS spec's command only, merged over
	// the caller's extraEnv (spec wins). It carries resolved secret material (e.g. a
	// minter's client secret / App-key content) to the command in memory — never on
	// argv. Nil for ordinary specs.
	Env map[string]string
}
```

Change the command branch of `Resolve` to merge the spec env. Replace the loop body's command case:

```go
func Resolve(r runner.Runner, extraEnv map[string]string, specs []Spec) (map[string]string, error) {
	env := make(map[string]string, len(specs))
	for _, s := range specs {
		if s.Literal {
			env[s.Name] = s.Value
			continue
		}
		if len(s.Command) == 0 {
			return nil, fmt.Errorf("secret %q: empty command", s.Name)
		}
		child := flattenEnv(mergeEnv(extraEnv, s.Env))
		out, err := r.OutputEnv(child, s.Command[0], s.Command[1:]...)
		if err != nil {
			return nil, fmt.Errorf("secret %q: resolver command failed: %w", s.Name, err)
		}
		env[s.Name] = strings.TrimSuffix(out, "\n")
	}
	return env, nil
}

// mergeEnv returns base with over applied on top (over wins). base/over may be nil.
func mergeEnv(base, over map[string]string) map[string]string {
	if len(over) == 0 {
		return base
	}
	m := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		m[k] = v
	}
	for k, v := range over {
		m[k] = v
	}
	return m
}
```

(The previously hoisted `extra := flattenEnv(extraEnv)` line is removed — flattening now happens per spec because each spec may add its own env.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/secret/ -v`
Expected: PASS (the new test plus the existing ones — the old behavior is unchanged when `Env` is nil).

- [ ] **Step 5: Commit**

```bash
git add internal/secret/secret.go internal/secret/secret_test.go
git commit -m "feat(secret): per-spec Spec.Env merged into command env (in-memory secret inputs)"
```

---

### Task 2: `internal/mint` — profile → `at-mint` spec

**Files:**
- Create: `internal/mint/mint.go`
- Test: `internal/mint/mint_test.go`

**Interfaces:**
- Consumes: `usersecret.{Minter,GitHubMinter,AnthropicMinter,Auth0,Source,MintExpander}`, `secret.Spec`, `runner.Runner`.
- Produces:
  - `func Expander(r runner.Runner, globals map[string]usersecret.Source) usersecret.MintExpander`
  - The returned expander maps a `github` profile to `at-mint github --app-id … --install-id … [--app-key-file <path>]` (+ `AT_MINT_GITHUB_APP_KEY` env when the key is command/global-sourced), and an `anthropic` profile to `at-mint anthropic --auth0-… --anthropic-…` (+ `AT_MINT_AUTH0_CLIENT_SECRET` env).

- [ ] **Step 1: Write the failing test**

```go
package mint

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/usersecret"
)

func strptr(s string) *string { return &s }

func TestGithubProfilePathKeyIsFlagNotEnv(t *testing.T) {
	m := usersecret.Minter{GitHub: &usersecret.GitHubMinter{
		AppID: "123", InstallID: "456", AppKey: usersecret.Source{Value: strptr("/etc/cove/gh.pem")},
	}}
	spec, err := Expander(&runner.Fake{}, nil)("gh", m, "AT_TASK_GIT_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(spec.Command, " ")
	if got != "at-mint github --app-id 123 --install-id 456 --app-key-file /etc/cove/gh.pem" {
		t.Fatalf("argv = %q", got)
	}
	if len(spec.Env) != 0 {
		t.Fatalf("path key must not set env, got %v", spec.Env)
	}
	if spec.Name != "AT_TASK_GIT_TOKEN" {
		t.Fatalf("name = %q", spec.Name)
	}
}

func TestGithubProfileCommandKeyGoesToEnv(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "PEMCONTENT\n"}}}
	m := usersecret.Minter{GitHub: &usersecret.GitHubMinter{
		AppID: "1", InstallID: "2", AppKey: usersecret.Source{Command: []string{"cat", "/k"}},
	}}
	spec, err := Expander(f, nil)("gh", m, "AT_TASK_GIT_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(spec.Command, " "), "PEMCONTENT") {
		t.Fatal("key content leaked into argv")
	}
	if spec.Env["AT_MINT_GITHUB_APP_KEY"] != "PEMCONTENT" {
		t.Fatalf("env = %v", spec.Env)
	}
	if strings.Contains(strings.Join(spec.Command, " "), "--app-key-file") {
		t.Fatal("command-sourced key must not use --app-key-file")
	}
}

func TestAnthropicProfileFlagsAndSecretEnv(t *testing.T) {
	m := usersecret.Minter{Anthropic: &usersecret.AnthropicMinter{
		OIDC: usersecret.OIDC{Auth0: &usersecret.Auth0{
			Tenant: "t.us.auth0.com", ClientID: "cid", Audience: "aud",
			ClientSecret: usersecret.Source{Value: strptr("shh")},
		}},
		Federation: usersecret.Federation{Org: "o", Rule: "fdrl_1", ServiceAccount: "svac_1", Workspace: "wrkspc_1"},
	}}
	spec, err := Expander(&runner.Fake{}, nil)("a", m, "ANTHROPIC_AUTH_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(spec.Command, " ")
	want := "at-mint anthropic --auth0-tenant t.us.auth0.com --auth0-client-id cid --auth0-audience aud " +
		"--anthropic-org o --anthropic-rule fdrl_1 --anthropic-service-account svac_1 --anthropic-workspace wrkspc_1"
	if got != want {
		t.Fatalf("argv = %q", got)
	}
	if strings.Contains(got, "shh") {
		t.Fatal("client secret leaked into argv")
	}
	if spec.Env["AT_MINT_AUTH0_CLIENT_SECRET"] != "shh" {
		t.Fatalf("env = %v", spec.Env)
	}
}

func TestAnthropicOmitsEmptyWorkspace(t *testing.T) {
	m := usersecret.Minter{Anthropic: &usersecret.AnthropicMinter{
		OIDC:       usersecret.OIDC{Auth0: &usersecret.Auth0{Tenant: "t", ClientID: "c", Audience: "a", ClientSecret: usersecret.Source{Value: strptr("s")}}},
		Federation: usersecret.Federation{Org: "o", Rule: "fdrl_1", ServiceAccount: "svac_1"},
	}}
	spec, _ := Expander(&runner.Fake{}, nil)("a", m, "T")
	if strings.Contains(strings.Join(spec.Command, " "), "--anthropic-workspace") {
		t.Fatal("empty workspace must be omitted")
	}
}

func TestGlobalDelegationResolvesSecret(t *testing.T) {
	globals := map[string]usersecret.Source{"shared": {Value: strptr("fromglobal")}}
	m := usersecret.Minter{Anthropic: &usersecret.AnthropicMinter{
		OIDC:       usersecret.OIDC{Auth0: &usersecret.Auth0{Tenant: "t", ClientID: "c", Audience: "a", ClientSecret: usersecret.Source{Global: "shared"}}},
		Federation: usersecret.Federation{Org: "o", Rule: "fdrl_1", ServiceAccount: "svac_1"},
	}}
	spec, err := Expander(&runner.Fake{}, globals)("a", m, "T")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["AT_MINT_AUTH0_CLIENT_SECRET"] != "fromglobal" {
		t.Fatalf("global delegation failed: %v", spec.Env)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mint/ -v`
Expected: FAIL — the package/`Expander` does not exist.

- [ ] **Step 3: Write minimal implementation**

```go
// Package mint turns a usersecret minter profile into a runnable at-mint
// invocation: non-secret identifiers become flags, resolved secret material
// becomes per-spec env (never argv). It is at-cove's usersecret.MintExpander.
package mint

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/usersecret"
)

// Expander returns a MintExpander bound to a host runner (for resolving a
// profile's command/global-sourced secret fields) and the store's global library.
func Expander(r runner.Runner, globals map[string]usersecret.Source) usersecret.MintExpander {
	return func(profileName string, m usersecret.Minter, demandName string) (secret.Spec, error) {
		switch {
		case m.GitHub != nil:
			return githubSpec(r, globals, demandName, m.GitHub)
		case m.Anthropic != nil:
			return anthropicSpec(r, globals, demandName, m.Anthropic)
		default:
			return secret.Spec{}, fmt.Errorf("minter %q: no provider set", profileName)
		}
	}
}

func githubSpec(r runner.Runner, globals map[string]usersecret.Source, name string, g *usersecret.GitHubMinter) (secret.Spec, error) {
	argv := []string{"at-mint", "github", "--app-id", g.AppID, "--install-id", g.InstallID}
	var env map[string]string
	kind, err := g.AppKey.Kind()
	if err != nil {
		return secret.Spec{}, fmt.Errorf("github minter app-key: %w", err)
	}
	if kind == "value" {
		// a literal value is a filesystem path to the PEM (non-secret) -> flag
		argv = append(argv, "--app-key-file", *g.AppKey.Value)
	} else {
		// command/global -> resolved key CONTENT (secret) -> env
		content, err := resolveSource(r, globals, g.AppKey)
		if err != nil {
			return secret.Spec{}, fmt.Errorf("github minter app-key: %w", err)
		}
		env = map[string]string{"AT_MINT_GITHUB_APP_KEY": content}
	}
	return secret.Spec{Name: name, Command: argv, Env: env}, nil
}

func anthropicSpec(r runner.Runner, globals map[string]usersecret.Source, name string, a *usersecret.AnthropicMinter) (secret.Spec, error) {
	z := a.OIDC.Auth0
	if z == nil {
		return secret.Spec{}, fmt.Errorf("anthropic minter: only the auth0 IdP is supported")
	}
	argv := []string{
		"at-mint", "anthropic",
		"--auth0-tenant", z.Tenant,
		"--auth0-client-id", z.ClientID,
		"--auth0-audience", z.Audience,
		"--anthropic-org", a.Federation.Org,
		"--anthropic-rule", a.Federation.Rule,
		"--anthropic-service-account", a.Federation.ServiceAccount,
	}
	if a.Federation.Workspace != "" {
		argv = append(argv, "--anthropic-workspace", a.Federation.Workspace)
	}
	val, err := resolveSource(r, globals, z.ClientSecret)
	if err != nil {
		return secret.Spec{}, fmt.Errorf("anthropic minter client-secret: %w", err)
	}
	return secret.Spec{Name: name, Command: argv, Env: map[string]string{"AT_MINT_AUTH0_CLIENT_SECRET": val}}, nil
}

// resolveSource resolves a minter's secret field to a literal string. A value is
// used as-is (for a client secret it IS the secret; for an app-key the value
// branch is handled by the caller as a path). command runs on the host; global
// delegates to a terminal (value/command) shared supply.
func resolveSource(r runner.Runner, globals map[string]usersecret.Source, src usersecret.Source) (string, error) {
	kind, err := src.Kind()
	if err != nil {
		return "", err
	}
	switch kind {
	case "value":
		return *src.Value, nil
	case "command":
		out, err := r.OutputEnv(nil, src.Command[0], src.Command[1:]...)
		if err != nil {
			return "", fmt.Errorf("resolver command failed: %w", err)
		}
		return strings.TrimSuffix(out, "\n"), nil
	case "global":
		g, ok := globals[src.Global]
		if !ok {
			return "", fmt.Errorf("global %q is not defined", src.Global)
		}
		gk, err := g.Kind()
		if err != nil {
			return "", fmt.Errorf("global %q: %w", src.Global, err)
		}
		if gk == "global" || gk == "mint" {
			return "", fmt.Errorf("global %q must be a value or command", src.Global)
		}
		return resolveSource(r, globals, g)
	default:
		return "", fmt.Errorf("a minter secret cannot be a %s source", kind)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mint/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mint/mint.go internal/mint/mint_test.go
git commit -m "feat(mint): profile -> at-mint spec expander (flags non-secret, secrets to Spec.Env)"
```

---

### Task 3: wire the expander into `cmd/at-cove`

**Files:**
- Modify: `cmd/at-cove/main.go`
- Modify: `cmd/at-cove/main_test.go`

**Interfaces:**
- Consumes: `mint.Expander`, `usersecret.Store.Global`.
- Produces: `planRequired` gains an `expand usersecret.MintExpander` parameter; the three resolution sites build `mint.Expander(<runner>, store.Global)` and pass it to `Store.Plan`/`planRequired` in place of `nil`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/at-cove/main_test.go` a test that a `{mint: …}` git-token supply resolves through `planRequired` to an `at-mint` spec (previously the `nil` expander made this error):

```go
func TestPlanRequiredExpandsMint(t *testing.T) {
	appKey := "/etc/cove/gh.pem"
	store := usersecret.Store{
		Minters: map[string]usersecret.Minter{
			"gh": {GitHub: &usersecret.GitHubMinter{AppID: "1", InstallID: "2", AppKey: usersecret.Source{Value: &appKey}}},
		},
		Kits: map[string]map[string]usersecret.Source{
			"k": {"AT_TASK_GIT_TOKEN": {Mint: "gh"}},
		},
	}
	expand := mint.Expander(&runner.Fake{}, store.Global)
	spec, err := planRequired(store, expand, "k", "/p", "AT_TASK_GIT_TOKEN", "/cfg/secrets.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Command) == 0 || spec.Command[0] != "at-mint" || spec.Command[1] != "github" {
		t.Fatalf("expected an at-mint github spec, got %v", spec.Command)
	}
}
```

Add the imports the test needs to `main_test.go` if missing: `"github.com/aethons-tools/cove/internal/mint"`, `"github.com/aethons-tools/cove/internal/runner"`, `"github.com/aethons-tools/cove/internal/usersecret"`.

Also update the EXISTING `planRequired` call in whatever test currently calls it (the Plan 1 test `TestPlanRequired…`) to the new 6-arg signature: insert a `nil` (or a real expander) as the second argument.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-cove/ -run TestPlanRequiredExpandsMint -v`
Expected: FAIL — `planRequired` takes 5 args, not 6 (compile error), or (once you add the param) the call sites don't yet pass an expander.

- [ ] **Step 3: Write minimal implementation**

1. Change `planRequired` to accept and forward the expander:

```go
func planRequired(store usersecret.Store, expand usersecret.MintExpander, kitName, kitPath, name, secretsPath string) (secret.Spec, error) {
	specs, unresolved, err := store.Plan(kitName, kitPath, []string{name}, expand)
	if err != nil {
		return secret.Spec{}, err
	}
	if len(unresolved) > 0 {
		return secret.Spec{}, fmt.Errorf("%s has no supply entry for kit %q in %s (or secrets.local.yml)", name, kitName, secretsPath)
	}
	return specs[0], nil
}
```

2. In `doWork`, after loading the store and computing `kitPath`, build the expander (use the injected runner `r`), and pass it to both `store.Plan` and `planRequired`:

```go
	expand := mint.Expander(r, store.Global)
	specs, unresolved, err := store.Plan(cfg.Name, kitPath, demanded, expand)
	// ... (unchanged unresolved warning loop) ...
	gitTok, err := planRequired(store, expand, cfg.Name, kitPath, gitName, secretsPath)
```

3. In `doConnect`, build the expander with `r` and pass it to `store.Plan`:

```go
	expand := mint.Expander(r, store.Global)
	specs, unresolved, err := store.Plan(st.Name, canonicalKitPath(kitDir), demanded, expand)
```

4. In `doDispatch`, build the expander with `runner.OS{}` (doDispatch has no injected runner) and pass it to `planRequired`:

```go
	expand := mint.Expander(runner.OS{}, store.Global)
	planned, err := planRequired(store, expand, cfg.Name, kitPath, "AT_DISPATCH_TRACKER_TOKEN", secretsPath)
```

5. Add the import `"github.com/aethons-tools/cove/internal/mint"` to `main.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/at-cove/ -v`
Expected: PASS. Then `go build ./...` and `go test ./...` — clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-cove/
git commit -m "feat(at-cove): wire mint.Expander into work/dispatch/connect (mint: now runnable)"
```

---

### Task 4: reference kit + docs — `mint:` profiles

**Files:**
- Modify: `kits/reference-worker/RUNBOOK.md`
- Modify: `docs/usage/at-cove-secrets.md`, `docs/usage/at-mint.md`, `docs/OVERVIEW.md`

**Interfaces:** none (docs).

- [ ] **Step 1: Confirm the "not yet runnable" caveats to update**

Run: `grep -rn 'not yet runnable\|forward-looking\|not-yet-runnable\|command: \["at-mint' docs/usage/ kits/reference-worker/RUNBOOK.md`
Expected: hits in `at-cove-secrets.md`, `at-mint.md`, and the RUNBOOK's bare-`command:` examples — these move to `mint:` / lose the caveat.

- [ ] **Step 2: (no code test — docs)**

- [ ] **Step 3: Make the changes**

1. `kits/reference-worker/RUNBOOK.md` — replace the bare-`command:` supply block with `minters:` profiles + `{mint: …}` demands:

````markdown
```yaml
# ~/.config/at-cove/secrets.yml (host-side, never committed)
minters:
  gh-cove:
    github:
      app-id: "123456"
      install-id: "7890"
      app-key: /etc/cove/gh-app.pem            # a path (non-secret) -> --app-key-file
  anthropic-cove:
    anthropic:
      oidc:
        auth0:
          tenant: your-tenant.us.auth0.com
          client-id: YOUR_CLIENT_ID
          audience: urn:cove:anthropic-wif
          client-secret: { command: ["pass", "cove/auth0-secret"] }   # from a manager
      federation:
        org: YOUR_ORG_UUID
        rule: fdrl_...
        service-account: svac_...
kits:
  reference-worker:
    AT_TASK_GIT_TOKEN:    { mint: gh-cove }
    ANTHROPIC_AUTH_TOKEN: { mint: anthropic-cove }
    AT_DISPATCH_TRACKER_TOKEN: { command: ["gh", "auth", "token"] }
```

at-cove builds the `at-mint` invocation from the profile: non-secret identifiers
become flags, and a `command:`/`global:`-sourced secret (the Auth0 client secret,
or an App key not given as a path) is passed to `at-mint` as env in memory —
never on argv. `COVE_RUN_REPO` is injected per run. A bare
`command: ["at-mint", "github", …]` still works if you prefer to inline it.
````

Remove the prose that said the Anthropic client secret must sit in the host env "in the interim" and that `mint:` is a later plan.

2. `docs/usage/at-cove-secrets.md` — in the four-sources section, change the `mint:` description from "parses and validates but not yet runnable" to runnable: "`mint: <profile>` mints the value by running `at-mint <provider>` built from the named `minters:` profile (non-secret fields → flags, resolved secrets → env)." Add or update the `minters:` example accordingly. Bump `updated: 2026-07-14`.

3. `docs/usage/at-mint.md` — add a short note that `at-mint` is normally invoked *for* you via a `mint:` profile (at-cove assembles the flags/env from the profile); the direct-`command:` form is the manual alternative. Link to [at-cove-secrets.md](at-cove-secrets.md) for the profile schema. Bump `updated`.

4. `docs/OVERVIEW.md` — in the roadmap, move "the `mint:` supply expansion" from "Designed but deferred" to "Implemented", noting `at-mint` is now driven by `minters:` profiles end-to-end.

After edits: `grep -rn 'not yet runnable\|not-yet-runnable' docs/usage kits/reference-worker/RUNBOOK.md` must be empty (no stale caveat).

- [ ] **Step 4: Verify**

Run: `go test ./internal/kit/ -run TestReferenceWorkerKitConfig -v` (reference kit config unaffected — it only DEMANDS; the RUNBOOK is machine-side) and `go test ./...` (whole suite green). If a docs checker is available, run it scoped to `docs/usage`.

- [ ] **Step 5: Commit**

```bash
git add kits/reference-worker/RUNBOOK.md docs/
git commit -m "docs(mint): mint: profiles are runnable — reference kit + secrets/at-mint/overview"
```

---

## Notes for the executor

- After Task 3, `go build ./...` and `go test ./...` must be green — this is where `mint:` becomes runnable end-to-end.
- The air-gap is unchanged: a `{mint: <github>}` git token still resolves to `dispatchrun.Options.GitToken`, minted fresh per git step; the App-key content (if command/global-sourced) lives in that spec's `Env` and is consumed host-side to run `at-mint` — only `at-mint`'s stdout (the token) enters the VM.
- Do not put any secret on the argv the expander builds. The only secret-bearing channel is `secret.Spec.Env`.
- Real end-to-end minting was proven manually against live Auth0/Anthropic (the `anthropic` provider) and the GitHub path has shell-script parity + unit tests; this plan does not add network/integration tests (keep them hermetic; a maintainer-run `integration` test may come later).
