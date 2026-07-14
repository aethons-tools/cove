# at-mint Plan 1 — Demand/Supply `Store` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Invert the secret model so kits *demand* secrets and the machine *supplies* them per-kit, out of source control — implemented as a rewritten `internal/usersecret` `Store` (sectioned `minters:`/`global:`/`kits:` across `secrets.yml` + `secrets.local.yml`) with a four-source resolver.

**Architecture:** `usersecret.Load(ymlPath, localPath)` parses two host-side files into one `Store`. `Store.Plan(kitName, kitPath, demanded, expand)` resolves each demanded secret with precedence `secrets.local.yml`(by kit path) → `secrets.yml` `kits:`(by kit name) → fail-closed, honoring four sources: `value`, `command`, `global` (delegate to a named shared supply), `mint` (expand a named minter profile via an injected `expand` callback — nil until Plan 3). `global:` and `minters:` are inert libraries reachable *only* by explicit reference, so a kit gets a secret only through an entry an operator wrote under it. Kit `secrets:` become demand-only (no `command:`); `cmd/at-cove` passes `(kitName, kitPath)` into `Plan`.

**Tech Stack:** Go 1.22, `gopkg.in/yaml.v3` (the only third-party dep). Hermetic tests via `internal/runner.Fake`.

## Global Constraints

- **No new dependencies.** Only the standard library and `gopkg.in/yaml.v3`. (Verify: `go.mod` require block is unchanged after this plan.)
- **Secrets never hit disk, argv, or logs.** Values flow in memory only; resolver commands run on the host via `runner.OutputEnv`. This plan does not print or persist any resolved value.
- **Fail-closed.** An unresolved demand is surfaced (required demands abort; optional demands warn and are omitted). No partial/best-effort secret maps.
- **The hardening layer is a security boundary** and is not touched by this plan.
- **`global:`/`minters:` are inert.** They are never matched by demand name — only reached through an explicit `global:`/`mint:` reference under a specific kit (or path). A demand whose name equals a `global`/`minter` key but has no `kits:`/`local` entry resolves to *unresolved*.
- **Precedence, exactly:** `secrets.local.yml` (keyed by canonical kit path) → `secrets.yml` `kits:` (keyed by kit `name`) → fail-closed. `local` `minters:`/`global:` override `yml` ones by key.
- **`mint:` is parse-and-validate only in this plan.** A `mint:` referencing a missing profile is a load-time error; a `mint:`-sourced demand resolved through `Plan` with a nil `expand` returns an error naming the secret (real expansion arrives in Plan 3).
- **Canonical kit path** means the symlink-resolved absolute path of the kit directory (`filepath.EvalSymlinks` on the absolute kit dir; fall back to the cleaned absolute path if EvalSymlinks fails because the dir does not exist).

---

## File Structure

- `internal/usersecret/source.go` (new) — the `Source` four-way union + validation.
- `internal/usersecret/minter.go` (new) — the `Minter`/`GitHubMinter`/`AnthropicMinter`/`OIDC`/`Auth0` tagged-union types + validation. (Parsed and validated here; consumed by Plans 2–3.)
- `internal/usersecret/usersecret.go` (rewrite) — the sectioned `Store`, `Load(ymlPath, localPath)`.
- `internal/usersecret/plan.go` (rewrite) — `Plan(kitName, kitPath, demanded, expand)`.
- `internal/usersecret/*_test.go` (new/rewritten) — hermetic unit tests per file.
- `internal/kit/config.go` (modify) — `SecretConfig` becomes demand-only (drop `Command`); `GitTokenSpec()` returns the demand name only; validation updated.
- `internal/kit/config_test.go` (modify) — drop command-in-kit expectations.
- `cmd/at-cove/main.go` (modify) — `doWork`/`doDispatch` build demands and call the new `Plan`; `planRequired` re-expressed against it; add `canonicalKitPath`.
- `cmd/at-cove/*_test.go` (modify) — update to the new resolution.
- `kits/reference-worker/config.yml` (modify) — demand-only kit; delete `mint-github-token.sh` references (the script itself is removed in Plan 2).
- `kits/reference-worker/RUNBOOK.md` (modify) — document the machine-side `secrets.yml` supply (`kits:`/`global:`, `command:` sources for now).
- `docs/usage/at-cove-secrets.md`, `docs/usage/at-cove-config.md`, `docs/OVERVIEW.md` (modify) — the demand/supply model.

Note the `Source.Value` is a `*string` so an explicit empty-string literal (`value: ""`) is distinguishable from "field absent"; this matters for the exactly-one-source validation.

---

### Task 1: The `Source` four-way union

**Files:**
- Create: `internal/usersecret/source.go`
- Test: `internal/usersecret/source_test.go`

**Interfaces:**
- Produces:
  - `type Source struct { Value *string; Command []string; Global string; Mint string }`
  - `func (s Source) Kind() (string, error)` — returns exactly one of `"value"`, `"command"`, `"global"`, `"mint"`, or an error if zero or more than one is set.

- [ ] **Step 1: Write the failing test**

```go
package usersecret

import "testing"

func TestSourceKind(t *testing.T) {
	empty := ""
	val := "x"
	cases := []struct {
		name string
		src  Source
		want string
		err  bool
	}{
		{"value", Source{Value: &val}, "value", false},
		{"empty-value-is-still-a-source", Source{Value: &empty}, "value", false},
		{"command", Source{Command: []string{"gh", "auth"}}, "command", false},
		{"global", Source{Global: "shared"}, "global", false},
		{"mint", Source{Mint: "prof"}, "mint", false},
		{"none", Source{}, "", true},
		{"two", Source{Global: "a", Mint: "b"}, "", true},
		{"value-and-command", Source{Value: &val, Command: []string{"x"}}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.src.Kind()
			if c.err {
				if err == nil {
					t.Fatalf("want error, got kind %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("Kind() = %q, want %q", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/usersecret/ -run TestSourceKind -v`
Expected: FAIL — `undefined: Source` (package does not yet compile with the new type).

- [ ] **Step 3: Write minimal implementation**

```go
package usersecret

import "fmt"

// Source is how one demanded secret (or a global/profile field) is produced:
// exactly one of the four forms. Value is a *string so an explicit empty literal
// (value: "") is distinct from "unset".
//
//   value:   a literal string
//   command: a host argv whose trimmed stdout is the value
//   global:  delegate to a named entry in the store's global: library
//   mint:    mint via a named entry in the store's minters: library
type Source struct {
	Value   *string
	Command []string
	Global  string
	Mint    string
}

// Kind returns the single set form, or an error if zero or more than one is set.
func (s Source) Kind() (string, error) {
	kinds := make([]string, 0, 1)
	if s.Value != nil {
		kinds = append(kinds, "value")
	}
	if len(s.Command) > 0 {
		kinds = append(kinds, "command")
	}
	if s.Global != "" {
		kinds = append(kinds, "global")
	}
	if s.Mint != "" {
		kinds = append(kinds, "mint")
	}
	switch len(kinds) {
	case 1:
		return kinds[0], nil
	case 0:
		return "", fmt.Errorf("a supply must set exactly one of value/command/global/mint (set none)")
	default:
		return "", fmt.Errorf("a supply must set exactly one of value/command/global/mint (set %v)", kinds)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/usersecret/ -run TestSourceKind -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/usersecret/source.go internal/usersecret/source_test.go
git commit -m "feat(usersecret): Source four-way supply union (value/command/global/mint)"
```

---

### Task 2: The `Minter` tagged-union types

**Files:**
- Create: `internal/usersecret/minter.go`
- Test: `internal/usersecret/minter_test.go`

**Interfaces:**
- Produces:
  - `type Minter struct { GitHub *GitHubMinter; Anthropic *AnthropicMinter }`
  - `type GitHubMinter struct { AppID string; InstallID string; AppKey Source }`
  - `type AnthropicMinter struct { OIDC OIDC; Federation Federation }`
  - `type OIDC struct { Auth0 *Auth0 }`
  - `type Auth0 struct { Tenant, ClientID, Audience string; ClientSecret Source }`
  - `type Federation struct { Org, Rule, ServiceAccount, Workspace string }`
  - `func (m Minter) Validate() error` — exactly one provider set, and that provider's required fields present (github: app-id, install-id; anthropic: exactly one OIDC IdP + federation org/rule/service-account). Field-level secret `Source`s are validated with `Source.Kind()`.

- [ ] **Step 1: Write the failing test**

```go
package usersecret

import "testing"

func gh() *GitHubMinter    { return &GitHubMinter{AppID: "1", InstallID: "2", AppKey: Source{Value: strptr("k")}} }
func strptr(s string) *string { return &s }

func TestMinterValidate(t *testing.T) {
	okAnthropic := &AnthropicMinter{
		OIDC:       OIDC{Auth0: &Auth0{Tenant: "t", ClientID: "c", Audience: "a", ClientSecret: Source{Command: []string{"pass", "x"}}}},
		Federation: Federation{Org: "o", Rule: "fdrl_1", ServiceAccount: "svac_1"},
	}
	cases := []struct {
		name string
		m    Minter
		err  bool
	}{
		{"github-ok", Minter{GitHub: gh()}, false},
		{"anthropic-ok", Minter{Anthropic: okAnthropic}, false},
		{"no-provider", Minter{}, true},
		{"two-providers", Minter{GitHub: gh(), Anthropic: okAnthropic}, true},
		{"github-missing-appid", Minter{GitHub: &GitHubMinter{InstallID: "2", AppKey: Source{Value: strptr("k")}}}, true},
		{"github-bad-appkey-source", Minter{GitHub: &GitHubMinter{AppID: "1", InstallID: "2", AppKey: Source{}}}, true},
		{"anthropic-no-idp", Minter{Anthropic: &AnthropicMinter{Federation: okAnthropic.Federation}}, true},
		{"anthropic-missing-rule", Minter{Anthropic: &AnthropicMinter{OIDC: okAnthropic.OIDC, Federation: Federation{Org: "o", ServiceAccount: "svac_1"}}}, true},
		{"anthropic-bad-secret-source", Minter{Anthropic: &AnthropicMinter{OIDC: OIDC{Auth0: &Auth0{Tenant: "t", ClientID: "c", Audience: "a", ClientSecret: Source{}}}, Federation: okAnthropic.Federation}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.m.Validate()
			if c.err && err == nil {
				t.Fatal("want error, got nil")
			}
			if !c.err && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/usersecret/ -run TestMinterValidate -v`
Expected: FAIL — `undefined: Minter`.

- [ ] **Step 3: Write minimal implementation**

```go
package usersecret

import "fmt"

// Minter is a named minting profile: a tagged union over the code host / token
// kind. Exactly one provider is set. Parsed and validated here; the actual
// minting (running at-mint) is wired in later plans.
type Minter struct {
	GitHub    *GitHubMinter    `yaml:"github,omitempty"`
	Anthropic *AnthropicMinter `yaml:"anthropic,omitempty"`
}

// GitHubMinter mints a repo-scoped GitHub App installation token.
type GitHubMinter struct {
	AppID     string `yaml:"app-id"`
	InstallID string `yaml:"install-id"`
	AppKey    Source `yaml:"app-key"` // PEM: a value (path/content) | command | global
}

// AnthropicMinter mints an Anthropic sk-ant-oat01 via an OIDC IdP JWT (hop 1)
// exchanged through Anthropic federation (hop 2).
type AnthropicMinter struct {
	OIDC       OIDC       `yaml:"oidc"`
	Federation Federation `yaml:"federation"`
}

// OIDC is a tagged union over the identity provider that mints the upstream JWT.
type OIDC struct {
	Auth0 *Auth0 `yaml:"auth0,omitempty"`
}

// Auth0 mints an upstream JWT via the client-credentials grant.
type Auth0 struct {
	Tenant       string `yaml:"tenant"`
	ClientID     string `yaml:"client-id"`
	Audience     string `yaml:"audience"`
	ClientSecret Source `yaml:"client-secret"`
}

// Federation carries the Anthropic-side exchange identifiers.
type Federation struct {
	Org            string `yaml:"org"`
	Rule           string `yaml:"rule"`            // fdrl_...
	ServiceAccount string `yaml:"service-account"` // svac_...
	Workspace      string `yaml:"workspace,omitempty"`
}

// Validate checks the provider union and each provider's required fields.
func (m Minter) Validate() error {
	set := 0
	if m.GitHub != nil {
		set++
	}
	if m.Anthropic != nil {
		set++
	}
	if set != 1 {
		return fmt.Errorf("a minter must set exactly one provider (github|anthropic), set %d", set)
	}
	if g := m.GitHub; g != nil {
		if g.AppID == "" || g.InstallID == "" {
			return fmt.Errorf("github minter: app-id and install-id are required")
		}
		if _, err := g.AppKey.Kind(); err != nil {
			return fmt.Errorf("github minter: app-key: %w", err)
		}
	}
	if a := m.Anthropic; a != nil {
		if a.OIDC.Auth0 == nil {
			return fmt.Errorf("anthropic minter: oidc must set exactly one IdP (auth0)")
		}
		z := a.OIDC.Auth0
		if z.Tenant == "" || z.ClientID == "" || z.Audience == "" {
			return fmt.Errorf("anthropic minter: oidc.auth0 requires tenant, client-id, audience")
		}
		if _, err := z.ClientSecret.Kind(); err != nil {
			return fmt.Errorf("anthropic minter: oidc.auth0.client-secret: %w", err)
		}
		if a.Federation.Org == "" || a.Federation.Rule == "" || a.Federation.ServiceAccount == "" {
			return fmt.Errorf("anthropic minter: federation requires org, rule, service-account")
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/usersecret/ -run TestMinterValidate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/usersecret/minter.go internal/usersecret/minter_test.go
git commit -m "feat(usersecret): minter tagged-union types (github|anthropic, oidc/federation split)"
```

---

### Task 3: Sectioned `Store` + `Load(ymlPath, localPath)`

**Files:**
- Modify: `internal/usersecret/usersecret.go` (full rewrite of the file)
- Test: `internal/usersecret/usersecret_test.go` (rewrite)

**Interfaces:**
- Consumes: `Source` (Task 1), `Minter` (Task 2).
- Produces:
  - `type Store struct { Minters map[string]Minter; Global map[string]Source; Kits map[string]map[string]Source; Local map[string]map[string]Source }`
  - `func Load(ymlPath, localPath string) (Store, error)` — parses `secrets.yml` (sections `minters:`, `global:`, `kits:`) and `secrets.local.yml` (same sections; its `kits:` is keyed by kit path). Local `minters`/`global` override yml by key; local `kits` populate `Store.Local`. Missing files yield empty sections (no error). Validates every `Minter`, every `global`/`kits`/`local` `Source` (`Kind()`), that a `Source.Global` names an existing `global`, and that a `Source.Mint` names an existing `minter`.

The YAML shape a `Source` unmarshals from is a mapping with exactly one of `value|command|global|mint`. Implement `UnmarshalYAML` on `*Source` so both files decode cleanly.

- [ ] **Step 1: Write the failing test**

```go
package usersecret

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSections(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "secrets.yml")
	local := filepath.Join(dir, "secrets.local.yml")
	write(t, yml, `
minters:
  gh-cove:
    github: { app-id: "1", install-id: "2", app-key: { value: "/k.pem" } }
global:
  shared-tracker: { command: ["gh", "auth", "token"] }
kits:
  cove:
    AT_TASK_GIT_TOKEN: { command: ["at-mint-shim"] }
    AT_DISPATCH_TRACKER_TOKEN: { global: shared-tracker }
`)
	write(t, local, `
kits:
  /abs/checkout/cove:
    ANTHROPIC_AUTH_TOKEN: { value: "sk-ant-oat01-test" }
`)
	st, err := Load(yml, local)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Minters["gh-cove"]; !ok {
		t.Fatal("minter gh-cove not loaded")
	}
	if _, ok := st.Global["shared-tracker"]; !ok {
		t.Fatal("global shared-tracker not loaded")
	}
	if k, _ := st.Kits["cove"]["AT_DISPATCH_TRACKER_TOKEN"].Kind(); k != "global" {
		t.Fatalf("cove tracker source kind = %q, want global", k)
	}
	if _, ok := st.Local["/abs/checkout/cove"]["ANTHROPIC_AUTH_TOKEN"]; !ok {
		t.Fatal("local path entry not loaded")
	}
}

func TestLoadMissingFilesEmpty(t *testing.T) {
	st, err := Load(filepath.Join(t.TempDir(), "none.yml"), filepath.Join(t.TempDir(), "none.local.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Minters) != 0 || len(st.Global) != 0 || len(st.Kits) != 0 || len(st.Local) != 0 {
		t.Fatal("missing files should yield empty sections")
	}
}

func TestLoadDanglingGlobalRef(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "secrets.yml")
	write(t, yml, `
kits:
  cove:
    X: { global: nope }
`)
	if _, err := Load(yml, filepath.Join(dir, "missing.local.yml")); err == nil {
		t.Fatal("want error for global: referencing a missing shared supply")
	}
}

func TestLoadDanglingMintRef(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "secrets.yml")
	write(t, yml, `
kits:
  cove:
    X: { mint: nope }
`)
	if _, err := Load(yml, filepath.Join(dir, "missing.local.yml")); err == nil {
		t.Fatal("want error for mint: referencing a missing minter profile")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/usersecret/ -run 'TestLoad' -v`
Expected: FAIL — old `Load(path string)` signature / `Store` is a map, so it will not compile.

- [ ] **Step 3: Write minimal implementation**

Replace the entire contents of `internal/usersecret/usersecret.go` with:

```go
// Package usersecret loads the host-side secret-supply files
// (~/.config/at-cove/secrets.yml and secrets.local.yml). These are the "supply"
// side: kits declare *demands* (secret names), the machine supplies them here,
// per-kit, out of source control. secrets.yml keys kits by name; secrets.local.yml
// keys them by canonical kit path (an escape hatch for collisions and testing).
package usersecret

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Store is the parsed supply. Minters and Global are inert libraries: reachable
// only through an explicit mint:/global: reference under a specific kit (or path).
type Store struct {
	Minters map[string]Minter
	Global  map[string]Source
	Kits    map[string]map[string]Source // secrets.yml: kit name -> secret -> source
	Local   map[string]map[string]Source // secrets.local.yml: kit path -> secret -> source
}

// file is the on-disk shape of each secrets file.
type file struct {
	Minters map[string]Minter            `yaml:"minters"`
	Global  map[string]Source            `yaml:"global"`
	Kits    map[string]map[string]Source `yaml:"kits"`
}

// UnmarshalYAML decodes a supply mapping into exactly one Source form.
func (s *Source) UnmarshalYAML(node *yaml.Node) error {
	var raw struct {
		Value   *string  `yaml:"value"`
		Command []string `yaml:"command"`
		Global  string   `yaml:"global"`
		Mint    string   `yaml:"mint"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	s.Value, s.Command, s.Global, s.Mint = raw.Value, raw.Command, raw.Global, raw.Mint
	if _, err := s.Kind(); err != nil {
		return err
	}
	return nil
}

// Load parses both supply files into one Store. Missing files contribute empty
// sections. Local minters/global override yml by key; local kits populate Local.
// It validates every minter and every source, and that every global:/mint:
// reference resolves to a defined shared supply / minter profile.
func Load(ymlPath, localPath string) (Store, error) {
	yml, err := readFile(ymlPath)
	if err != nil {
		return Store{}, err
	}
	local, err := readFile(localPath)
	if err != nil {
		return Store{}, err
	}
	st := Store{
		Minters: map[string]Minter{},
		Global:  map[string]Source{},
		Kits:    map[string]map[string]Source{},
		Local:   map[string]map[string]Source{},
	}
	for k, v := range yml.Minters {
		st.Minters[k] = v
	}
	for k, v := range local.Minters { // local overrides yml
		st.Minters[k] = v
	}
	for k, v := range yml.Global {
		st.Global[k] = v
	}
	for k, v := range local.Global {
		st.Global[k] = v
	}
	st.Kits = yml.Kits
	if st.Kits == nil {
		st.Kits = map[string]map[string]Source{}
	}
	st.Local = local.Kits
	if st.Local == nil {
		st.Local = map[string]map[string]Source{}
	}
	if err := st.validate(); err != nil {
		return Store{}, err
	}
	return st, nil
}

func readFile(path string) (file, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return file{}, nil
	}
	if err != nil {
		return file{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f file
	if err := dec.Decode(&f); err != nil {
		return file{}, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func (st Store) validate() error {
	for name, m := range st.Minters {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("minters.%s: %w", name, err)
		}
	}
	check := func(where string, entries map[string]map[string]Source) error {
		for kit, secrets := range entries {
			for name, src := range secrets {
				kind, err := src.Kind()
				if err != nil {
					return fmt.Errorf("%s.%s.%s: %w", where, kit, name, err)
				}
				switch kind {
				case "global":
					if _, ok := st.Global[src.Global]; !ok {
						return fmt.Errorf("%s.%s.%s: global %q is not defined", where, kit, name, src.Global)
					}
				case "mint":
					if _, ok := st.Minters[src.Mint]; !ok {
						return fmt.Errorf("%s.%s.%s: mint %q is not a defined minter", where, kit, name, src.Mint)
					}
				}
			}
		}
	return nil
	}
	if err := check("kits", st.Kits); err != nil {
		return err
	}
	return check("local", st.Local)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/usersecret/ -run 'TestLoad' -v`
Expected: PASS. (The package will not fully build until Task 4 rewrites `plan.go`; run `go vet ./internal/usersecret/` — it is expected to fail only on the old `Plan` until Task 4. If the vet failure blocks the test, do Task 4's Step 3 in the same commit boundary — see Task 4.)

> Note to implementer: Tasks 3 and 4 both rewrite this package's API; the package will not compile between them. Land them as one reviewable unit if needed — write both failing tests first, then both implementations, then commit once. If you prefer separate commits, temporarily keep the old `Plan` compiling is *not* worth it here; batch 3+4.

- [ ] **Step 5: Commit** (may be combined with Task 4 — see note)

```bash
git add internal/usersecret/usersecret.go internal/usersecret/usersecret_test.go
git commit -m "feat(usersecret): sectioned Store + two-file Load (minters/global/kits)"
```

---

### Task 4: `Plan(kitName, kitPath, demanded, expand)`

**Files:**
- Modify: `internal/usersecret/plan.go` (full rewrite)
- Test: `internal/usersecret/plan_test.go` (new)

**Interfaces:**
- Consumes: `Store` (Task 3), `Source` (Task 1), `secret.Spec` (`internal/secret`).
- Produces:
  - `type MintExpander func(profileName string, m Minter, demandName string) (secret.Spec, error)`
  - `func (st Store) Plan(kitName, kitPath string, demanded []string, expand MintExpander) (resolvable []secret.Spec, unresolved []string, err error)`
  - Precedence per demand: `Local[kitPath][name]` → `Kits[kitName][name]` → append to `unresolved`.
  - Source → `secret.Spec`: `value` → `{Name, Value, Literal:true}`; `command` → `{Name, Command}`; `global` → resolve the named global `Source` (which is itself `value`/`command`; a `global` pointing at a `global`/`mint` is a hard error); `mint` → `expand(profile, minter, name)` (if `expand == nil`, return `err`).
  - `err` is non-nil only for a *structural* fault (a `global` whose target is itself `global`/`mint`; a `mint` with a nil `expander`; an `expand` failure). A demand with no matching entry is *unresolved*, never an error — the caller decides whether that demand is required.

- [ ] **Step 1: Write the failing test**

```go
package usersecret

import (
	"testing"

	"github.com/aethons-tools/cove/internal/secret"
)

func TestPlanPrecedenceAndSources(t *testing.T) {
	val := "lit"
	st := Store{
		Global:  map[string]Source{"g": {Command: []string{"gcmd"}}},
		Minters: map[string]Minter{"m": {GitHub: gh()}},
		Kits: map[string]map[string]Source{
			"cove": {
				"A": {Command: []string{"acmd"}},
				"B": {Global: "g"},
				"C": {Value: &val},
			},
		},
		Local: map[string]map[string]Source{
			"/p/cove": {"A": {Value: &val}}, // overrides Kits["cove"]["A"]
		},
	}
	expand := func(profile string, m Minter, name string) (secret.Spec, error) {
		return secret.Spec{Name: name, Command: []string{"at-mint", profile}}, nil
	}
	got, unresolved, err := st.Plan("cove", "/p/cove", []string{"A", "B", "C", "MISSING"}, expand)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(unresolved) != 1 || unresolved[0] != "MISSING" {
		t.Fatalf("unresolved = %v, want [MISSING]", unresolved)
	}
	byName := map[string]secret.Spec{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if !byName["A"].Literal || byName["A"].Value != "lit" { // local override wins
		t.Fatalf("A should resolve from local literal, got %+v", byName["A"])
	}
	if byName["B"].Command[0] != "gcmd" { // global delegation
		t.Fatalf("B should resolve via global to gcmd, got %+v", byName["B"])
	}
	if !byName["C"].Literal {
		t.Fatalf("C should be a literal, got %+v", byName["C"])
	}
}

func TestPlanGlobalIsInert(t *testing.T) {
	// A demand whose name equals a global key but has no kits entry is unresolved,
	// never auto-supplied from global.
	st := Store{
		Global: map[string]Source{"shared-tracker": {Command: []string{"gh"}}},
		Kits:   map[string]map[string]Source{"cove": {}},
	}
	got, unresolved, err := st.Plan("cove", "/p", []string{"shared-tracker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || len(unresolved) != 1 {
		t.Fatalf("global must be inert: got=%v unresolved=%v", got, unresolved)
	}
}

func TestPlanMintNeedsExpander(t *testing.T) {
	st := Store{
		Minters: map[string]Minter{"m": {GitHub: gh()}},
		Kits:    map[string]map[string]Source{"cove": {"T": {Mint: "m"}}},
	}
	if _, _, err := st.Plan("cove", "/p", []string{"T"}, nil); err == nil {
		t.Fatal("mint: with nil expander must error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/usersecret/ -run TestPlan -v`
Expected: FAIL — old `Plan(demanded []secret.Spec)` signature.

- [ ] **Step 3: Write minimal implementation**

Replace the entire contents of `internal/usersecret/plan.go` with:

```go
package usersecret

import (
	"fmt"

	"github.com/aethons-tools/cove/internal/secret"
)

// MintExpander turns a resolved minter profile into a runnable secret.Spec (an
// at-mint invocation). Injected by the caller; nil until the minting wiring lands.
type MintExpander func(profileName string, m Minter, demandName string) (secret.Spec, error)

// Plan resolves each demanded secret name to a secret.Spec, with precedence
// secrets.local.yml (by kit path) -> secrets.yml kits: (by kit name) -> unresolved.
// A demand with no matching entry is returned in unresolved (the caller decides if
// it is required). A structural fault (a global pointing at a non-terminal source,
// or a mint with no expander) returns err. minters:/global: are never matched by
// demand name; they are reached only via an explicit source under the kit.
func (st Store) Plan(kitName, kitPath string, demanded []string, expand MintExpander) (resolvable []secret.Spec, unresolved []string, err error) {
	for _, name := range demanded {
		src, ok := st.lookup(kitName, kitPath, name)
		if !ok {
			unresolved = append(unresolved, name)
			continue
		}
		spec, e := st.resolve(name, src, expand)
		if e != nil {
			return nil, nil, fmt.Errorf("secret %q for kit %q: %w", name, kitName, e)
		}
		resolvable = append(resolvable, spec)
	}
	return resolvable, unresolved, nil
}

func (st Store) lookup(kitName, kitPath, name string) (Source, bool) {
	if m, ok := st.Local[kitPath]; ok {
		if s, ok := m[name]; ok {
			return s, true
		}
	}
	if m, ok := st.Kits[kitName]; ok {
		if s, ok := m[name]; ok {
			return s, true
		}
	}
	return Source{}, false
}

func (st Store) resolve(name string, src Source, expand MintExpander) (secret.Spec, error) {
	kind, err := src.Kind()
	if err != nil {
		return secret.Spec{}, err
	}
	switch kind {
	case "value":
		return secret.Spec{Name: name, Value: *src.Value, Literal: true}, nil
	case "command":
		return secret.Spec{Name: name, Command: src.Command}, nil
	case "global":
		g, ok := st.Global[src.Global]
		if !ok {
			return secret.Spec{}, fmt.Errorf("global %q is not defined", src.Global)
		}
		gk, err := g.Kind()
		if err != nil {
			return secret.Spec{}, fmt.Errorf("global %q: %w", src.Global, err)
		}
		switch gk {
		case "value":
			return secret.Spec{Name: name, Value: *g.Value, Literal: true}, nil
		case "command":
			return secret.Spec{Name: name, Command: g.Command}, nil
		default:
			return secret.Spec{}, fmt.Errorf("global %q must be a value or command, not %s", src.Global, gk)
		}
	case "mint":
		m, ok := st.Minters[src.Mint]
		if !ok {
			return secret.Spec{}, fmt.Errorf("mint %q is not a defined minter", src.Mint)
		}
		if expand == nil {
			return secret.Spec{}, fmt.Errorf("mint %q requires at-mint (not wired in this build)", src.Mint)
		}
		return expand(src.Mint, m, name)
	default:
		return secret.Spec{}, fmt.Errorf("unhandled source kind %q", kind)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/usersecret/ -v`
Expected: PASS (all usersecret tests). Also run `go build ./internal/usersecret/` — expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/usersecret/plan.go internal/usersecret/plan_test.go
git commit -m "feat(usersecret): Plan(kitName,kitPath,demanded,expand) — path>name>fail-closed, four sources"
```

---

### Task 5: Kit `secrets:` become demand-only

**Files:**
- Modify: `internal/kit/config.go` (`SecretConfig`, `GitTokenSpec`, validation)
- Modify: `internal/kit/config_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `type SecretConfig struct { Description string }` (drop `Command`).
  - `func (c Config) GitTokenName() (string, bool)` replaces `GitTokenSpec()` — returns `("AT_TASK_GIT_TOKEN", true)` when the kit declares that demand under `source-control.github.secrets`, else `("", false)`.

- [ ] **Step 1: Write the failing test**

In `internal/kit/config_test.go`, replace any test asserting a kit-side `command:` on a secret with these:

```go
func TestSecretConfigIsDemandOnly(t *testing.T) {
	// A command: under a kit secret is now an unknown field (KnownFields(true)).
	_, err := ParseConfig([]byte(`
name: k
secrets:
  FOO: { description: "d", command: ["x"] }
`))
	if err == nil {
		t.Fatal("want error: command: is no longer allowed under a kit secret")
	}
}

func TestGitTokenNameDemandOnly(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
source-control:
  github:
    project: o/r
    secrets:
      AT_TASK_GIT_TOKEN: { description: "push+PR token" }
`))
	if err != nil {
		t.Fatal(err)
	}
	name, ok := cfg.GitTokenName()
	if !ok || name != "AT_TASK_GIT_TOKEN" {
		t.Fatalf("GitTokenName() = %q,%v", name, ok)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/kit/ -run 'TestSecretConfig|TestGitTokenName' -v`
Expected: FAIL — `command:` still parses (no error), and `GitTokenName` is undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/kit/config.go`:

1. Change the `SecretConfig` type (remove the `Command` field and its doc reference to secrets.yml command):

```go
// SecretConfig is a kit's *demand* for a secret (keyed by its env var name in a
// secrets map): a name plus a human description of its purpose. How the value is
// produced is a machine-side concern (see internal/usersecret) — a kit never
// carries a resolver command.
type SecretConfig struct {
	Description string `yaml:"description"`
}
```

2. Replace `GitTokenSpec` with `GitTokenName` (and drop the `secret` import if now unused — check with `goimports`/build):

```go
// GitTokenName reports the code-host token demand the kit declares under
// source-control.github.secrets, if present. The value is supplied machine-side;
// the kit only names the demand (the structural air-gap: it lives at a distinct
// schema location from the root/agent secrets).
func (c Config) GitTokenName() (string, bool) {
	if c.SourceControl == nil || c.SourceControl.GitHub == nil {
		return "", false
	}
	if _, ok := c.SourceControl.GitHub.Secrets["AT_TASK_GIT_TOKEN"]; !ok {
		return "", false
	}
	return "AT_TASK_GIT_TOKEN", true
}
```

3. If removing the `secret` import breaks the build, delete `"github.com/aethons-tools/cove/internal/secret"` from `config.go`'s imports. (`checkWellKnownSecrets`/`rejectReservedSecretNames` operate on `map[string]SecretConfig` and do not use `secret`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/kit/ -v`
Expected: PASS. Fix any sibling test in `config_test.go` that constructed a `SecretConfig{Command: ...}` — change it to `SecretConfig{Description: ...}`.

- [ ] **Step 5: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(kit): kit secrets are demand-only (drop command:); GitTokenName replaces GitTokenSpec"
```

---

### Task 6: Rewire `cmd/at-cove` to the demand/supply `Plan`

**Files:**
- Modify: `cmd/at-cove/main.go` (`doConnect`, `doWork`, `doDispatch`, `planRequired`; add `canonicalKitPath`)
- Modify: `cmd/at-cove/*_test.go` as needed

> There are **three** secret-resolution sites in `main.go`, all using the old `usersecret.Load(path)` / `store.Plan(demanded)` API: `doConnect` (~line 313, resolves demands from recorded *state*), `doWork` (~594), and `doDispatch` (via `planRequired`, ~494/682). All three must move to the two-file `Load` + new `Plan`. `planRequired` is shared by `doWork` and `doDispatch`.

**Interfaces:**
- Consumes: `usersecret.Load(ymlPath, localPath)`, `usersecret.Store.Plan(kitName, kitPath, demanded, expand)` (nil expander), `kit.GitTokenName()`.
- Produces: no new exported API; behavior — `work`/`dispatch` resolve secrets from the machine-side files keyed by `(kitName, canonical kitPath)`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/at-cove/main_test.go` (or the file holding doWork/doDispatch tests) a test that a demand resolved from `secrets.yml` `kits:` reaches dispatch. Model it on the existing work/dispatch tests (they already stub the backend/runner). Minimum viable assertion — the canonical-path helper:

```go
func TestCanonicalKitPath(t *testing.T) {
	dir := t.TempDir()
	got := canonicalKitPath(dir)
	if !filepath.IsAbs(got) {
		t.Fatalf("canonicalKitPath(%q) = %q, want absolute", dir, got)
	}
}
```

(Extend the existing doWork/doDispatch tests to construct a two-file secrets store under a temp `XDG_CONFIG_HOME` with a `kits:` entry for the kit name, and assert resolution succeeds. Reuse whatever temp-config-dir pattern those tests already use; if they set `XDG_CONFIG_HOME`, write `secrets.yml` there.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-cove/ -run TestCanonicalKitPath -v`
Expected: FAIL — `undefined: canonicalKitPath`.

- [ ] **Step 3: Write minimal implementation**

1. Add the helper near `configDir()` in `main.go`:

```go
// canonicalKitPath returns the symlink-resolved absolute path of a kit dir — the
// key secrets.local.yml uses to disambiguate same-named kits. Falls back to the
// cleaned absolute path when the dir cannot be resolved.
func canonicalKitPath(kitDir string) string {
	abs, err := filepath.Abs(kitDir)
	if err != nil {
		abs = filepath.Clean(kitDir)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}
```

2. Replace `planRequired` with a Store-based helper:

```go
// planRequired resolves one required demand for a kit through the supply store.
// It errors, naming the secret and the secrets files, if nothing supplies it.
func planRequired(store usersecret.Store, kitName, kitPath, name, secretsPath string) (secret.Spec, error) {
	specs, unresolved, err := store.Plan(kitName, kitPath, []string{name}, nil)
	if err != nil {
		return secret.Spec{}, err
	}
	if len(unresolved) > 0 {
		return secret.Spec{}, fmt.Errorf("%s has no supply entry for kit %q in %s (or secrets.local.yml)", name, kitName, secretsPath)
	}
	return specs[0], nil
}
```

3. In `doWork`, replace the secrets block (lines ~593–618) with the two-file load, name+path keys, and demand lists:

```go
	secretsPath := filepath.Join(configDir(), "secrets.yml")
	localPath := filepath.Join(configDir(), "secrets.local.yml")
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	kitPath := canonicalKitPath(kitDir)
	// General (agent-injected) secrets: demand names from the kit; unresolved warn.
	demanded := make([]string, 0, len(cfg.Secrets))
	for name := range cfg.Secrets {
		demanded = append(demanded, name)
	}
	specs, unresolved, err := store.Plan(cfg.Name, kitPath, demanded, nil)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	for _, name := range unresolved {
		fmt.Fprintf(stderr, "at-cove: warning: secret %q has no supply for kit %q in %s; it will not be set\n", name, cfg.Name, secretsPath)
	}
	// The code-host token stays a distinct demand (the air-gap); required, fail closed.
	gitName, ok := cfg.GitTokenName()
	if !ok {
		fmt.Fprintf(stderr, "at-cove: kit %q declares no source-control.github.secrets AT_TASK_GIT_TOKEN\n", cfg.Name)
		return 1
	}
	gitTok, err := planRequired(store, cfg.Name, kitPath, gitName, secretsPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
```

The `dispatchrun.Dispatch(...)` call below is unchanged (`Secrets: specs`, `GitToken: gitTok`).

4. In `doDispatch`, replace the secrets block (lines ~681–698):

```go
	secretsPath := filepath.Join(configDir(), "secrets.yml")
	localPath := filepath.Join(configDir(), "secrets.local.yml")
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: %v\n", err)
		return 1
	}
	kitPath := canonicalKitPath(kitDir)
	planned, err := planRequired(store, cfg.Name, kitPath, "AT_DISPATCH_TRACKER_TOKEN", secretsPath)
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

5. In `doConnect`, replace the state-demand secrets block (the `demanded := make([]secret.Spec, len(st.Secrets))` loop through `store.Plan(demanded)` warning loop) with the two-file load + new `Plan`, keyed by the kit name recorded in state (`st.Name`) and this checkout's canonical path. `doConnect` resolves demand *names* from recorded state; it never mints (nil expander):

```go
	// Demand (from state) resolved against supply (the machine-side secrets files),
	// keyed by the kit name recorded in state and this checkout's canonical path.
	demanded := make([]string, len(st.Secrets))
	for i, s := range st.Secrets {
		demanded[i] = s.Name
	}
	secretsPath := filepath.Join(configDir(), "secrets.yml")
	localPath := filepath.Join(configDir(), "secrets.local.yml")
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		return err
	}
	specs, unresolved, err := store.Plan(st.Name, canonicalKitPath(kitDir), demanded, nil)
	if err != nil {
		return err
	}
	for _, name := range unresolved {
		fmt.Fprintf(stderr, "at-cove: warning: secret %q is demanded but has no supply for kit %q in %s; it will not be set\n", name, st.Name, secretsPath)
	}
```

(`st.Name` is the kit name in `state.State`; `st.Secrets[i].Name` is the demanded name — the recorded `.Command` is no longer used for resolution.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/at-cove/ -v`
Expected: PASS. Then the whole build: `go build ./...` — clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-cove/
git commit -m "feat(at-cove): work/dispatch resolve secrets via demand/supply Store (kitName+path keyed)"
```

---

### Task 7: Reference kit + docs (demand/supply model)

**Files:**
- Modify: `kits/reference-worker/config.yml`
- Modify: `kits/reference-worker/RUNBOOK.md`
- Modify: `docs/usage/at-cove-secrets.md`, `docs/usage/at-cove-config.md`, `docs/OVERVIEW.md`

**Interfaces:** none (config + docs).

- [ ] **Step 1: Write the failing test**

The reference kit is parsed by an existing test (search: `grep -rn "reference-worker" internal cmd --include=*_test.go`). If one loads it, it will now fail because the kit still carries `command:` under its secrets. Run it first to confirm:

Run: `go test ./... 2>&1 | grep -A3 reference` (identify the failing loader test)
Expected: FAIL — the reference kit's `command:` secrets no longer parse.

If no test loads the reference kit, add one to `internal/kit/config_test.go`:

```go
func TestReferenceKitParses(t *testing.T) {
	data, err := os.ReadFile("../../kits/reference-worker/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseConfig(data); err != nil {
		t.Fatalf("reference kit must parse: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/kit/ -run TestReferenceKitParses -v`
Expected: FAIL — `command:` under the reference kit's secrets is now an unknown field.

- [ ] **Step 3: Write minimal implementation**

Rewrite `kits/reference-worker/config.yml` to demand-only (remove every `command:` under a secret; keep `description`s). The secrets sections become:

```yaml
source-control:
  github:
    project: your-org/your-repo
    main-branch: main
    secrets:
      # DEMAND only. The value is supplied machine-side (see RUNBOOK: secrets.yml).
      AT_TASK_GIT_TOKEN:
        description: per-task GitHub App installation token — push + PR on the repo, minted per git step

tracker:
  linear:
    team: your-team-key
    # ... states unchanged ...
    secrets:
      AT_DISPATCH_TRACKER_TOKEN:  { description: "Linear API token for the scheduler" }
      AT_DISPATCH_WEBHOOK_SECRET: { description: "Linear webhook signing secret" }

secrets:
  ANTHROPIC_AUTH_TOKEN:
    description: short-lived Anthropic bearer for the worker agent (supplied machine-side)
```

(Replace the previous root `ANTHROPIC_API_KEY: {}` with `ANTHROPIC_AUTH_TOKEN` per the spec's worker-auth decision. The collaborator `LINEAR_TOKEN` entry, if present, becomes `{ description: ... }`.)

Then rewrite the `RUNBOOK.md` secrets section to document the machine-side `~/.config/at-cove/secrets.yml`, e.g.:

````markdown
## Supplying secrets (machine-side, never committed)

The kit only *demands* secrets. Supply them in `~/.config/at-cove/secrets.yml`,
keyed by this kit's `name`:

```yaml
kits:
  reference-worker:
    AT_TASK_GIT_TOKEN:         { command: ["mint-github-token.sh"] }   # Plan 3: { mint: gh-<name> }
    ANTHROPIC_AUTH_TOKEN:      { command: ["your-anthropic-mint.sh"] } # Plan 3: { mint: anthropic-<org> }
    AT_DISPATCH_TRACKER_TOKEN: { global: linear-token }
global:
  linear-token: { command: ["gh", "auth", "token"] }
```

`~/.config/at-cove/secrets.local.yml` (keyed by the kit's absolute path) overrides
the above for collisions or temporary test values.
````

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/kit/ -run TestReferenceKitParses -v && go test ./... `
Expected: PASS across the repo.

- [ ] **Step 5: Update the docs and commit**

Update the three docs to describe: kit `secrets:` are demand-only; the machine supplies via `secrets.yml`/`secrets.local.yml` with sources `value`/`command`/`global` (and, forward-referenced, `mint`); precedence path→name→fail-closed; `global:`/`minters:` are inert. Keep each doc's frontmatter `read_when`/`owns` accurate and cross-links resolving.

```bash
git add kits/reference-worker/ docs/
git commit -m "docs(secrets): demand-only kits; machine-side supply files; reference kit + RUNBOOK"
```

---

## Notes for the executor

- **Batch Tasks 3 and 4** if the package fails to compile between them (they jointly replace the `usersecret` API). Write both failing tests, then both implementations, then run `go test ./internal/usersecret/ -v`.
- After Task 6, run the full suite: `just test` (or `go test ./...`). Everything must be green — this plan is a complete, self-contained deliverable even though `at-mint` and `mint:` expansion arrive in Plans 2–3. The reference kit uses `command:` supply until Plan 3 switches it to `mint:`.
- Do **not** implement `at-mint` or `mint:` expansion here. `Plan`'s `expand` argument is nil throughout Plan 1; a `mint:` supply is a *validated but not-yet-runnable* source, which is correct for this plan.
