# at-jam Credentials-Supply File Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move at-jam's three inline credential strategies out of the serve config into a protected `~/.config/at-jam/credentials.yml`, so the serve config only *names* credentials and secrets never sit near source control.

**Architecture:** Port at-cove's demand/supply split to the service. Extend `internal/usersecret` (already at-cove's supply model) with a flat `credentials:` section, a `LoadFlat` loader, and a `PlanFlat` resolver. The serve config keeps name-only demands plus `*-cred` references; `at-jam serve` loads the file, plans the demand set into `map[string]secret.Spec`, fails closed on any unsupplied demand, and hands the specs to the existing broker/resolvers unchanged.

**Tech Stack:** Go (stdlib + `gopkg.in/yaml.v3`), the existing `internal/{usersecret,secret,mint,runner}` packages. Hermetic tests via `runner.Fake`.

## Global Constraints

- **Module:** `github.com/aethons-tools/cove`. Binaries `at-cove`, `at-jam`.
- **Build/test:** `just test` (hermetic — drives `internal/runner.Fake`, no Docker/network/VM). Keep every new test hermetic. `just build`, `just lint`.
- **TDD:** write the failing test first, watch it fail, then the minimal implementation.
- **Secrets never hit disk, argv, or logs.** Resolved values and `secret.Spec`s are never logged; only credential *names* may appear in errors.
- **Frequent commits:** one commit per task (each task ends green).
- **Docs in the same change:** no task is "done" until the repo docs it touches are updated (Task 6 owns the doc changes for the whole feature).
- **Attribution — end every commit body with:**
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```
- **Spec:** `docs/superpowers/specs/2026-09-30-jam-credentials-file-design.md`.

## File Structure

- `internal/usersecret/usersecret.go` — add `Credentials map[string]Source` to `file` + `Store`; add `LoadFlat`; add flat validation (Task 1).
- `internal/usersecret/plan.go` — add `PlanFlat` (Task 2).
- `internal/usersecret/flat_test.go` — new; tests for `LoadFlat`/validation (Task 1) and `PlanFlat` (Task 2).
- `cmd/at-jam/config.go` — `credentials-file` field, name-only `credentials:` validation, `credentialsFilePath`/`atJamConfigDir`/`demandedCredentials` helpers (Task 3); `bot-token-cred`/`tracker-token-cred` + deprecation detection (Task 4).
- `cmd/at-jam/config_test.go` — parse/validation tests (Tasks 3, 4).
- `cmd/at-jam/main.go` — `serve` wiring: `LoadFlat` + `PlanFlat` + fail-closed + three resolution sites (Task 5).
- `cmd/at-jam/serve_wiring_test.go` — new; serve-level wiring test (Task 5).
- `docs/usage/jam/credentials.md` (new), `docs/usage/jam/serve.md`, `docs/usage/jam/INDEX.md`, `docs/usage/jam/renamed-from-harbor.md` (Task 6).

---

### Task 1: `usersecret` — flat `Credentials`, `LoadFlat`, validation

**Files:**
- Modify: `internal/usersecret/usersecret.go`
- Test: `internal/usersecret/flat_test.go` (create)

**Interfaces:**
- Consumes: existing `Source` (`internal/usersecret/source.go`), `Minter.Validate()`, `readFile`.
- Produces:
  - `Store.Credentials map[string]Source` — the flat, service-style supply (name → source).
  - `func LoadFlat(path string) (Store, error)` — loads one file (no `.local`); missing file ⇒ empty `Store`, no error; validates minters and that every `credentials:` `global:`/`mint:` reference resolves.

- [ ] **Step 1: Write the failing tests**

Create `internal/usersecret/flat_test.go`:

```go
package usersecret

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "credentials.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFlat_ParsesCredentials(t *testing.T) {
	p := writeTemp(t, `
global:
  gh: { command: ["gh", "auth", "token"] }
credentials:
  jam-db: { value: "pw" }
  git-pat: { global: gh }
`)
	st, err := LoadFlat(p)
	if err != nil {
		t.Fatalf("LoadFlat: %v", err)
	}
	if len(st.Credentials) != 2 {
		t.Fatalf("want 2 credentials, got %d", len(st.Credentials))
	}
	if st.Credentials["jam-db"].Value == nil || *st.Credentials["jam-db"].Value != "pw" {
		t.Fatalf("jam-db value not parsed: %+v", st.Credentials["jam-db"])
	}
}

func TestLoadFlat_MissingFileIsEmpty(t *testing.T) {
	st, err := LoadFlat(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatalf("missing file should be empty, not error: %v", err)
	}
	if len(st.Credentials) != 0 {
		t.Fatalf("want empty store, got %d credentials", len(st.Credentials))
	}
}

func TestLoadFlat_UndefinedGlobalReference(t *testing.T) {
	p := writeTemp(t, `
credentials:
  git-pat: { global: missing }
`)
	if _, err := LoadFlat(p); err == nil {
		t.Fatal("want error for undefined global reference, got nil")
	}
}

func TestLoadFlat_UndefinedMintReference(t *testing.T) {
	p := writeTemp(t, `
credentials:
  anthropic: { mint: missing }
`)
	if _, err := LoadFlat(p); err == nil {
		t.Fatal("want error for undefined mint reference, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usersecret/ -run TestLoadFlat -v`
Expected: FAIL — `undefined: LoadFlat` and `st.Credentials undefined`.

- [ ] **Step 3: Add the `Credentials` field and `LoadFlat`**

In `internal/usersecret/usersecret.go`, add `Credentials` to both `Store` and `file`:

```go
// Store is the parsed supply. Minters and Global are inert libraries: reachable
// only through an explicit mint:/global: reference under a specific kit (or path),
// or a flat credentials entry (LoadFlat).
type Store struct {
	Minters     map[string]Minter
	Global      map[string]Source
	Kits        map[string]map[string]Source // secrets.yml: kit name -> secret -> source
	Local       map[string]map[string]Source // secrets.local.yml: kit path -> secret -> source
	Credentials map[string]Source            // credentials.yml (LoadFlat): name -> source (at-jam)
}

// file is the on-disk shape of each secrets file.
type file struct {
	Minters     map[string]Minter            `yaml:"minters"`
	Global      map[string]Source            `yaml:"global"`
	Kits        map[string]map[string]Source `yaml:"kits"`
	Credentials map[string]Source            `yaml:"credentials"`
}
```

Add `LoadFlat` and a flat validator below `Load`:

```go
// LoadFlat parses a single flat supply file (~/.config/at-jam/credentials.yml)
// into a Store: minters:/global: inert libraries plus a flat credentials: map of
// name -> source. Unlike Load there is no kit partitioning and no .local file. A
// missing file yields an empty Store (the caller fails closed on an unsupplied
// demand). It validates every minter and that every credentials global:/mint:
// reference resolves to a defined library entry.
func LoadFlat(path string) (Store, error) {
	f, err := readFile(path)
	if err != nil {
		return Store{}, err
	}
	st := Store{
		Minters:     map[string]Minter{},
		Global:      map[string]Source{},
		Credentials: map[string]Source{},
	}
	for k, v := range f.Minters {
		st.Minters[k] = v
	}
	for k, v := range f.Global {
		st.Global[k] = v
	}
	for k, v := range f.Credentials {
		st.Credentials[k] = v
	}
	if err := st.validateFlat(); err != nil {
		return Store{}, err
	}
	return st, nil
}

func (st Store) validateFlat() error {
	for name, m := range st.Minters {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("minters.%s: %w", name, err)
		}
	}
	return st.checkSources("credentials", st.Credentials)
}

// checkSources validates each source's kind and that any global:/mint: reference
// resolves. Shared by validateFlat and the kit-partitioned validate.
func (st Store) checkSources(where string, entries map[string]Source) error {
	for name, src := range entries {
		kind, err := src.Kind()
		if err != nil {
			return fmt.Errorf("%s.%s: %w", where, name, err)
		}
		switch kind {
		case "global":
			if _, ok := st.Global[src.Global]; !ok {
				return fmt.Errorf("%s.%s: global %q is not defined", where, name, src.Global)
			}
		case "mint":
			if _, ok := st.Minters[src.Mint]; !ok {
				return fmt.Errorf("%s.%s: mint %q is not a defined minter", where, name, src.Mint)
			}
		}
	}
	return nil
}
```

Then DRY the existing `validate()` — replace its inner `check` closure body to delegate to `checkSources` (keeps one validation path):

```go
func (st Store) validate() error {
	for name, m := range st.Minters {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("minters.%s: %w", name, err)
		}
	}
	check := func(where string, entries map[string]map[string]Source) error {
		for kit, secrets := range entries {
			if err := st.checkSources(where+"."+kit, secrets); err != nil {
				return err
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

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/usersecret/ -run TestLoadFlat -v`
Expected: PASS (all four).

- [ ] **Step 5: Run the whole package (no regressions in existing `Load`/`validate` tests)**

Run: `go test ./internal/usersecret/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/usersecret/usersecret.go internal/usersecret/flat_test.go
git commit -m "$(cat <<'EOF'
feat(usersecret): flat Credentials supply + LoadFlat

Add a flat credentials: section, a single-file LoadFlat loader (no kit
partitioning, no .local), and a shared checkSources validator reused by the
existing kit-partitioned validate. Foundation for at-jam's credentials file.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

### Task 2: `usersecret.PlanFlat`

**Files:**
- Modify: `internal/usersecret/plan.go`
- Test: `internal/usersecret/flat_test.go` (append)

**Interfaces:**
- Consumes: `Store.Credentials` (Task 1), the existing unexported `resolve(name, src, expand)`, `MintExpander`.
- Produces: `func (st Store) PlanFlat(demanded []string, expand MintExpander) (resolvable []secret.Spec, unresolved []string, err error)` — resolves each demanded name against `Credentials`; a name with no entry goes to `unresolved`; a structural fault (undefined global/mint, missing expander) returns `err`. Specs are keyed by `Name` (set to the demand name) so callers can index a resolved map by credential name.

- [ ] **Step 1: Write the failing tests**

Append to `internal/usersecret/flat_test.go`:

```go
import "github.com/aethons-tools/cove/internal/secret" // add to the import block

func TestPlanFlat_ValueAndCommand(t *testing.T) {
	pw := "pw"
	st := Store{Credentials: map[string]Source{
		"jam-db":  {Value: &pw},
		"git-pat": {Command: []string{"gh", "auth", "token"}},
	}}
	specs, unresolved, err := st.PlanFlat([]string{"jam-db", "git-pat"}, nil)
	if err != nil {
		t.Fatalf("PlanFlat: %v", err)
	}
	if len(unresolved) != 0 {
		t.Fatalf("want none unresolved, got %v", unresolved)
	}
	byName := map[string]secret.Spec{}
	for _, s := range specs {
		byName[s.Name] = s
	}
	if !byName["jam-db"].Literal || byName["jam-db"].Value != "pw" {
		t.Fatalf("jam-db spec wrong: %+v", byName["jam-db"])
	}
	if len(byName["git-pat"].Command) != 3 {
		t.Fatalf("git-pat command wrong: %+v", byName["git-pat"])
	}
}

func TestPlanFlat_UnresolvedDemand(t *testing.T) {
	st := Store{Credentials: map[string]Source{}}
	specs, unresolved, err := st.PlanFlat([]string{"absent"}, nil)
	if err != nil {
		t.Fatalf("PlanFlat: %v", err)
	}
	if len(specs) != 0 || len(unresolved) != 1 || unresolved[0] != "absent" {
		t.Fatalf("want absent unresolved, got specs=%v unresolved=%v", specs, unresolved)
	}
}

func TestPlanFlat_MintUsesExpander(t *testing.T) {
	st := Store{
		Minters:     map[string]Minter{"m": {}},
		Credentials: map[string]Source{"anthropic": {Mint: "m"}},
	}
	expand := func(profile string, m Minter, demand string) (secret.Spec, error) {
		return secret.Spec{Name: demand, Command: []string{"at-mint", "anthropic"}}, nil
	}
	specs, _, err := st.PlanFlat([]string{"anthropic"}, expand)
	if err != nil {
		t.Fatalf("PlanFlat: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "anthropic" || specs[0].Command[0] != "at-mint" {
		t.Fatalf("mint expansion wrong: %+v", specs)
	}
}

func TestPlanFlat_MintWithoutExpanderErrors(t *testing.T) {
	st := Store{
		Minters:     map[string]Minter{"m": {}},
		Credentials: map[string]Source{"anthropic": {Mint: "m"}},
	}
	if _, _, err := st.PlanFlat([]string{"anthropic"}, nil); err == nil {
		t.Fatal("want error minting without an expander, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usersecret/ -run TestPlanFlat -v`
Expected: FAIL — `st.PlanFlat undefined`.

- [ ] **Step 3: Implement `PlanFlat`**

Append to `internal/usersecret/plan.go`:

```go
// PlanFlat resolves each demanded credential name to a secret.Spec against the
// flat Credentials map (LoadFlat). A name with no entry is returned in unresolved
// (the caller decides if it is required). A structural fault (an undefined
// global:/mint:, or a mint: with no expander) returns err. minters:/global: are
// never matched by demand name; they are reached only via a credentials source.
func (st Store) PlanFlat(demanded []string, expand MintExpander) (resolvable []secret.Spec, unresolved []string, err error) {
	for _, name := range demanded {
		src, ok := st.Credentials[name]
		if !ok {
			unresolved = append(unresolved, name)
			continue
		}
		spec, e := st.resolve(name, src, expand)
		if e != nil {
			return nil, nil, fmt.Errorf("credential %q: %w", name, e)
		}
		resolvable = append(resolvable, spec)
	}
	return resolvable, unresolved, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/usersecret/ -run TestPlanFlat -v`
Expected: PASS (all four).

- [ ] **Step 5: Full package**

Run: `go test ./internal/usersecret/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/usersecret/plan.go internal/usersecret/flat_test.go
git commit -m "$(cat <<'EOF'
feat(usersecret): PlanFlat resolves flat credential demands to specs

Resolve a demand list against the flat Credentials map into []secret.Spec +
unresolved, reusing the shared resolve() (value/command/global/mint). This is
the seam at-jam serve feeds into jam.NewSecretResolver.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

### Task 3: serve-config — name-only `credentials:` + `credentials-file`

**Files:**
- Modify: `cmd/at-jam/config.go`
- Test: `cmd/at-jam/config_test.go` (append)

**Interfaces:**
- Consumes: existing `serveConfig`, `credSpec` (kept only to *detect* a now-illegal inline strategy).
- Produces:
  - field `CredentialsFile string` (`yaml:"credentials-file"`) on `serveConfig`.
  - `func (c serveConfig) validateCredentials() error` — errors if any `credentials:` entry carries an inline `command:`/`value:`.
  - `func (c serveConfig) demandedCredentials() []string` — sorted keys of `c.Credentials`.
  - `func (c serveConfig) credentialsFilePath() string` — `CredentialsFile` if set, else `atJamConfigDir()/credentials.yml`.
  - `const credentialsFileHint` — the reusable "supply it in the credentials file" suffix.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/at-jam/config_test.go`:

```go
func TestValidateCredentials_InlineStrategyRejected(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  anthropic-key:
    command: ["at-mint", "anthropic"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateCredentials(); err == nil {
		t.Fatal("want error for an inline command under credentials:, got nil")
	}
}

func TestValidateCredentials_NameOnlyOK(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  anthropic-key:
  git-pat:
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateCredentials(); err != nil {
		t.Fatalf("name-only credentials should validate: %v", err)
	}
	got := cfg.demandedCredentials()
	if len(got) != 2 || got[0] != "anthropic-key" || got[1] != "git-pat" {
		t.Fatalf("demandedCredentials = %v", got)
	}
}

func TestCredentialsFilePath_DefaultUnderXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	var cfg serveConfig
	if got := cfg.credentialsFilePath(); got != "/tmp/xdg/at-jam/credentials.yml" {
		t.Fatalf("default path = %q", got)
	}
	cfg.CredentialsFile = "/etc/jam/creds.yml"
	if got := cfg.credentialsFilePath(); got != "/etc/jam/creds.yml" {
		t.Fatalf("explicit path = %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/at-jam/ -run 'TestValidateCredentials|TestCredentialsFilePath' -v`
Expected: FAIL — `validateCredentials`/`demandedCredentials`/`credentialsFilePath`/`CredentialsFile` undefined.

- [ ] **Step 3: Add the field and helpers**

In `cmd/at-jam/config.go`, add the field to `serveConfig` (next to `Credentials`):

```go
	// CredentialsFile is the protected supply file that resolves each demanded
	// credential's strategy (value/command/global/mint). Empty ⇒ the XDG default
	// (~/.config/at-jam/credentials.yml). Strategies never live in this serve
	// config — see docs/usage/jam/credentials.md.
	CredentialsFile string              `yaml:"credentials-file"`
	Credentials     map[string]credSpec `yaml:"credentials"`
```

Add the hint constant, the validator, and the helpers (place near `credConfigured`):

```go
// credentialsFileHint is the shared tail for every "an inline secret is no longer
// allowed here" error — it points the operator at the supply file + its doc.
const credentialsFileHint = "supply its strategy in the at-jam credentials file (see docs/usage/jam/credentials.md)"

// validateCredentials enforces the demand/supply split: a serve-config
// credentials: entry names a credential only; an inline command:/value: (the old
// form) is a hard error pointing at the credentials file.
func (c serveConfig) validateCredentials() error {
	for name, cs := range c.Credentials {
		if len(cs.Command) > 0 || cs.Value != "" {
			return fmt.Errorf("credentials.%s: an inline command/value is no longer allowed — list the name only and %s", name, credentialsFileHint)
		}
	}
	return nil
}

// demandedCredentials is the sorted set of credential names the serve config
// demands (the credentials: keys). The supply file must resolve every one.
func (c serveConfig) demandedCredentials() []string {
	out := make([]string, 0, len(c.Credentials))
	for name := range c.Credentials {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// credentialsFilePath is the supply file to load: the explicit credentials-file
// when set, else the XDG default ~/.config/at-jam/credentials.yml.
func (c serveConfig) credentialsFilePath() string {
	if c.CredentialsFile != "" {
		return c.CredentialsFile
	}
	return filepath.Join(atJamConfigDir(), "credentials.yml")
}

// atJamConfigDir mirrors atCoveConfigDir: $XDG_CONFIG_HOME/at-jam, else
// ~/.config/at-jam.
func atJamConfigDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "at-jam")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "at-jam")
}
```

(`sort`, `os`, `path/filepath` are already imported in `config.go`.)

**Do not delete `credSpecs`/`toSpec` in this task.** They still have live callers in `main.go` (the broker specs and the two token-resolution sites), so removing them now would break the build. Task 5 moves those call sites and deletes both methods there. This task is purely additive: the new field + helpers + validator. The `credSpec` type also stays — Task 4 uses it to *detect* deprecated inline blocks.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/at-jam/ -run 'TestValidateCredentials|TestCredentialsFilePath' -v`
Expected: PASS.

- [ ] **Step 5: Build the package (still compiles — `credSpecs`/`toSpec` retained)**

Run: `go build ./cmd/at-jam/`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add cmd/at-jam/config.go cmd/at-jam/config_test.go
git commit -m "$(cat <<'EOF'
feat(at-jam): name-only credentials demands + credentials-file field

Serve config gains credentials-file (XDG default ~/.config/at-jam/credentials.yml)
and a validateCredentials that rejects an inline command/value under credentials:.
Add demandedCredentials + credentialsFilePath helpers. Wiring lands in a later task.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

### Task 4: serve-config — `bot-token-cred` + `tracker-token-cred` (with deprecation errors)

**Files:**
- Modify: `cmd/at-jam/config.go`
- Test: `cmd/at-jam/config_test.go` (append)

**Interfaces:**
- Consumes: `credConfigured` (unchanged — its allowed set is the `Credentials` keys), `credentialsFileHint` (Task 3).
- Produces:
  - `discordConfig.BotTokenCred string` (`yaml:"bot-token-cred"`) + `discordConfig.DeprecatedBotToken *credSpec` (`yaml:"bot-token"`).
  - `requisitionerConfig.TrackerTokenCred string` (`yaml:"tracker-token-cred"`) + `requisitionerConfig.DeprecatedTrackerToken *credSpec` (`yaml:"tracker-token"`).
  - `validateDiscord`/`validateRequisitioner` now: reject the deprecated inline field, require the `*-cred` field, and require it to name a demanded credential.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/at-jam/config_test.go`:

```go
func TestValidateDiscord_InlineBotTokenRejected(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  discord-bot:
runtime:
  discord:
    bot-token: { value: "x" }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateDiscord(); err == nil {
		t.Fatal("want error for inline runtime.discord.bot-token, got nil")
	}
}

func TestValidateDiscord_CredReferenceOK(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  discord-bot:
runtime:
  discord:
    bot-token-cred: discord-bot
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateDiscord(); err != nil {
		t.Fatalf("bot-token-cred reference should validate: %v", err)
	}
}

func TestValidateDiscord_CredMustBeDemanded(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
runtime:
  discord:
    bot-token-cred: nope
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateDiscord(); err == nil {
		t.Fatal("want error: bot-token-cred names an undemanded credential")
	}
}

func TestValidateRequisitioner_InlineTrackerTokenRejected(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  linear-bot:
runtime:
  requisitioner:
    role: worker
    max-concurrent: 1
    linear: { team: T }
    tracker-token: { value: "x" }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateRequisitioner(); err == nil {
		t.Fatal("want error for inline runtime.requisitioner.tracker-token, got nil")
	}
}

func TestValidateRequisitioner_CredReferenceOK(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  linear-bot:
runtime:
  requisitioner:
    role: worker
    max-concurrent: 1
    linear: { team: T }
    tracker-token-cred: linear-bot
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateRequisitioner(); err != nil {
		t.Fatalf("tracker-token-cred reference should validate: %v", err)
	}
}
```

(If the existing `TestValidateDiscord`/`TestValidateRequisitioner` tests assert the old inline `bot-token`/`tracker-token` shape, update them to the `*-cred` form in this step — grep `bot-token`/`tracker-token` in `config_test.go` and `serve_integration_test.go`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/at-jam/ -run 'TestValidateDiscord|TestValidateRequisitioner' -v`
Expected: FAIL — `BotTokenCred`/`TrackerTokenCred` undefined and old assertions break.

- [ ] **Step 3: Change the structs and validators**

`discordConfig`:

```go
type discordConfig struct {
	// BotTokenCred names a demanded credential; the token is supplied by the
	// at-jam credentials file, never inline here.
	BotTokenCred string `yaml:"bot-token-cred"`
	// DeprecatedBotToken detects the removed inline form; a set value is a hard
	// error pointing at the credentials file.
	DeprecatedBotToken *credSpec `yaml:"bot-token"`
}
```

`requisitionerConfig` — replace the `TrackerToken credSpec` line with:

```go
	TrackerTokenCred string `yaml:"tracker-token-cred"`
	// DeprecatedTrackerToken detects the removed inline form (see discordConfig).
	DeprecatedTrackerToken *credSpec `yaml:"tracker-token"`
```

`validateDiscord`:

```go
func (c serveConfig) validateDiscord() error {
	d := c.Runtime.Discord
	if d == nil {
		return nil
	}
	if d.DeprecatedBotToken != nil {
		return fmt.Errorf("runtime.discord.bot-token is no longer inline — set runtime.discord.bot-token-cred: <name> and %s", credentialsFileHint)
	}
	if d.BotTokenCred == "" {
		return fmt.Errorf("runtime.discord.bot-token-cred is required")
	}
	if !c.credConfigured(d.BotTokenCred) {
		return fmt.Errorf("runtime.discord.bot-token-cred %q is not a demanded credential", d.BotTokenCred)
	}
	return nil
}
```

`validateRequisitioner` — add, after the existing `d.Linear == nil` check:

```go
	if d.DeprecatedTrackerToken != nil {
		return fmt.Errorf("runtime.requisitioner.tracker-token is no longer inline — set runtime.requisitioner.tracker-token-cred: <name> and %s", credentialsFileHint)
	}
	if d.TrackerTokenCred == "" {
		return fmt.Errorf("runtime.requisitioner.tracker-token-cred is required")
	}
	if !c.credConfigured(d.TrackerTokenCred) {
		return fmt.Errorf("runtime.requisitioner.tracker-token-cred %q is not a demanded credential", d.TrackerTokenCred)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/at-jam/ -run 'TestValidateDiscord|TestValidateRequisitioner' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-jam/config.go cmd/at-jam/config_test.go
git commit -m "$(cat <<'EOF'
feat(at-jam): bot-token-cred + tracker-token-cred references

Convert the last two inline serve-config secrets (discord bot-token, requisitioner
tracker-token) into name references into the credentials file, with a hard
migration error if the old inline form is present. References must name a demanded
credential.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

### Task 5: `at-jam serve` wiring — load, plan, fail closed, three resolution sites

**Files:**
- Modify: `cmd/at-jam/main.go`, `cmd/at-jam/config.go` (delete `credSpecs`/`toSpec` now that call sites move)
- Test: `cmd/at-jam/serve_wiring_test.go` (create)

**Interfaces:**
- Consumes: `usersecret.LoadFlat`, `Store.PlanFlat`, `mint.Expander` (Tasks 1–2); `cfg.credentialsFilePath()`, `cfg.demandedCredentials()`, `cfg.validateCredentials()` (Task 3); `cfg.Runtime.Discord.BotTokenCred`, `dc.TrackerTokenCred` (Task 4).
- Produces: the `serve` path builds `specs map[string]secret.Spec` from the file (not from the serve config), fails closed on any unresolved demand, and resolves the postgres password / discord bot-token / tracker-token by indexing `specs` by credential name.

- [ ] **Step 1: Write the failing test**

Create `cmd/at-jam/serve_wiring_test.go`. This drives the exported `serve`-planning helper we extract in Step 3 (`planCredentials`), so the test stays hermetic (no listener, no broker):

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlanCredentials_FailsClosedOnUnsupplied(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "credentials.yml")
	if err := os.WriteFile(credFile, []byte("credentials:\n  git-pat: { value: pat }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := serveConfig{
		CredentialsFile: credFile,
		Credentials:     map[string]credSpec{"git-pat": {}, "missing": {}},
	}
	_, err := planCredentials(cfg)
	if err == nil {
		t.Fatal("want fail-closed error naming the unsupplied credential, got nil")
	}
}

func TestPlanCredentials_ResolvesSpecsByName(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "credentials.yml")
	if err := os.WriteFile(credFile, []byte("credentials:\n  jam-db: { value: pw }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := serveConfig{
		CredentialsFile: credFile,
		Credentials:     map[string]credSpec{"jam-db": {}},
	}
	specs, err := planCredentials(cfg)
	if err != nil {
		t.Fatalf("planCredentials: %v", err)
	}
	if s, ok := specs["jam-db"]; !ok || !s.Literal || s.Value != "pw" {
		t.Fatalf("jam-db spec wrong: %+v ok=%v", specs["jam-db"], ok)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-jam/ -run TestPlanCredentials -v`
Expected: FAIL — `planCredentials` undefined.

- [ ] **Step 3: Extract `planCredentials` and rewire `serve`**

Add the imports to `cmd/at-jam/main.go`'s import block:

```go
	"github.com/aethons-tools/cove/internal/mint"
	"github.com/aethons-tools/cove/internal/usersecret"
```

Add the helper (near the other `serveConfig` helpers, e.g. end of `main.go` or in `config.go`; keep it in `main.go` since it uses `runner`/`mint`):

```go
// planCredentials loads the protected credentials file, resolves every demanded
// credential to a secret.Spec keyed by name, and fails closed if the serve config
// demands a credential the file does not supply. The broker and every downstream
// resolver index the returned map by credential name.
func planCredentials(cfg serveConfig) (map[string]secret.Spec, error) {
	path := cfg.credentialsFilePath()
	store, err := usersecret.LoadFlat(path)
	if err != nil {
		return nil, fmt.Errorf("credentials file: %w", err)
	}
	demanded := cfg.demandedCredentials()
	specSlice, unresolved, err := store.PlanFlat(demanded, mint.Expander(runner.OS{}, store.Global, ""))
	if err != nil {
		return nil, err
	}
	if len(unresolved) > 0 {
		return nil, fmt.Errorf("credential(s) demanded in the serve config but not supplied by %s: %s", path, strings.Join(unresolved, ", "))
	}
	specs := make(map[string]secret.Spec, len(specSlice))
	for _, s := range specSlice {
		specs[s.Name] = s
	}
	return specs, nil
}
```

In the `serve` function, add the `validateCredentials` call alongside the other `validate*` calls (after `validatePool`, before building `log`):

```go
	if err := cfg.validateCredentials(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
```

Replace `specs := cfg.credSpecs()` (currently `main.go:1458`) with:

```go
	specs, err := planCredentials(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
```

Replace the discord bot-token resolution (`main.go:1739`):

```go
		tokEnv, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[cfg.Runtime.Discord.BotTokenCred]})
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: discord bot-token:", err)
			return 1
		}
		discordTok = tokEnv[cfg.Runtime.Discord.BotTokenCred]
```

Replace the requisitioner tracker-token resolution (`main.go:1754`):

```go
		tokEnv, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[dc.TrackerTokenCred]})
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: requisitioner tracker-token:", err)
			return 1
		}
		token := tokEnv[dc.TrackerTokenCred]
```

Now delete the dead `credSpecs`/`toSpec` from `config.go` (their only callers are gone):

```
Remove: func (c serveConfig) credSpecs() map[string]secret.Spec { ... }
Remove: func (cs credSpec) toSpec(name string) secret.Spec { ... }
```

- [ ] **Step 4: Run the new test + build**

Run: `go test ./cmd/at-jam/ -run TestPlanCredentials -v && go build ./...`
Expected: PASS, and the whole module builds (no lingering `credSpecs`/`toSpec` references).

- [ ] **Step 5: Run the full at-jam package tests**

Run: `go test ./cmd/at-jam/`
Expected: PASS. (Fix any older serve tests that constructed inline `credSpec` credentials or set `BotToken`/`TrackerToken` — point them at a temp credentials file + `*-cred` references.)

- [ ] **Step 6: Whole-module hermetic test**

Run: `just test`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/at-jam/main.go cmd/at-jam/config.go cmd/at-jam/serve_wiring_test.go
git commit -m "$(cat <<'EOF'
feat(at-jam): resolve credentials from the protected file at serve time

serve now loads ~/.config/at-jam/credentials.yml (or credentials-file), plans the
demanded set into secret.Spec by name, and fails closed on any unsupplied demand.
The broker, postgres password, discord bot-token and requisitioner tracker-token
all index that one specs map. Inline strategies are gone from the serve config.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

### Task 6: Documentation

**Files:**
- Create: `docs/usage/jam/credentials.md`
- Modify: `docs/usage/jam/serve.md`, `docs/usage/jam/INDEX.md`, `docs/usage/jam/renamed-from-harbor.md`

**Interfaces:** none (docs). Use the **docs-author** skill to keep the change progressively disclosed and `INDEX.md` in sync.

- [ ] **Step 1: Create `docs/usage/jam/credentials.md`**

Frontmatter + content owning the credentials-file format and the service demand/supply split. Required frontmatter keys (match sibling leaves like `serve.md`): `summary`, `read_when`, `owns`, `prereqs`, `tier: leaf`, `updated: 2026-09-30`. Content must cover:
- The file location (`credentials-file` field, XDG default `~/.config/at-jam/credentials.yml`, mode 0600, never committed).
- The three sections: `minters:` / `global:` (inert libraries) and the flat `credentials:` map (name → `value`/`command`/`global`/`mint`), with the worked example from the spec (`anthropic-key`/`git-pat`/`jam-db`/`discord-bot`/`linear-bot`).
- The demand/supply split: the serve config demands by name (`credentials:` list) and references (`store-postgres.password-cred`, `runtime.discord.bot-token-cred`, `runtime.requisitioner.tracker-token-cred`, a destination's `cred-name`, pool `cred-name`); the file supplies. Fail-closed on an unsupplied demand.
- A cross-link to `../at-cove-secrets.md` as the sibling model that owns the shared `Source`/`minters:` vocabulary — **point, do not re-explain** (single source of truth).

- [ ] **Step 2: Rewrite the credentials section of `docs/usage/jam/serve.md`**

- Update the serve-config YAML example: add `credentials-file`, make `credentials:` name-only, change `runtime.discord.bot-token` → `bot-token-cred`, and add `runtime.requisitioner.tracker-token-cred`.
- Update the key table row for `credentials.<name>`: it now says "the credentials the broker injects, **named only**; strategies live in the credentials file" and links to `credentials.md`. Add rows for `credentials-file`, `runtime.discord.bot-token-cred`, `runtime.requisitioner.tracker-token-cred`.
- Generalize the "never inline" rule: it applied only to the DB password (`password-cred`); now **all** credentials follow it. Update that sentence.

- [ ] **Step 3: Add the `credentials.md` row to `docs/usage/jam/INDEX.md`**

Add one table row: `[credentials.md](credentials.md) | You are supplying the real secrets a Jam brokers/uses — writing ~/.config/at-jam/credentials.yml, choosing value/command/global/mint per credential, or wiring credentials-file — and want the file format and the demand/supply split.`

- [ ] **Step 4: Note the rename in `docs/usage/jam/renamed-from-harbor.md`**

Add a short note that `runtime.discord.bot-token` and `runtime.requisitioner.tracker-token` inline forms are removed in favor of `*-cred` references (a hard error, not a silent alias — unlike the harbor renames). Only add if it fits that doc's existing "deprecated name" shape; otherwise put the migration note in `credentials.md` and skip this file.

- [ ] **Step 5: Verify links + docs health**

Run: use the **docs-audit** skill (deterministic checker) over `docs/`.
Expected: no orphans, no dangling links, `credentials.md` reachable from `INDEX.md`, no oversize/duplication findings.

- [ ] **Step 6: Commit**

```bash
git add docs/usage/jam/credentials.md docs/usage/jam/serve.md docs/usage/jam/INDEX.md docs/usage/jam/renamed-from-harbor.md
git commit -m "$(cat <<'EOF'
docs(jam): credentials-supply file (credentials.md) + serve.md/INDEX updates

Document the protected ~/.config/at-jam/credentials.yml, the credentials-file
field, name-only serve-config demands, and the bot-token-cred/tracker-token-cred
references. Move the "never inline" rule from the DB password to all credentials.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

## Post-plan notes (not tasks)

- **Operator migration (manual, uncommitted files):** the live `dev/jam.dev.yml` and `.at-cove/config.yml` are locally staged and never committed (see memory). After this lands, the operator moves the three inline strategies from `dev/jam.dev.yml` into a new `~/.config/at-jam/credentials.yml` and replaces them with names + `*-cred` references. Not a repo change — do not commit these files.
- **PR:** one PR against `main` for the whole branch (Tasks 1–6). AGENTS.md requires docs updated in the same change — Task 6 satisfies that.

## Self-Review

- **Spec coverage:** file format → Task 6 + Task 1 (`Credentials`/`LoadFlat`); `credentials-file` + name-only demands → Task 3; `bot-token-cred`/`tracker-token-cred` + deprecation → Task 4; resolution flow + fail-closed → Tasks 2, 5; reference integrity (password/bot/tracker-cred must be demanded) → Task 4 (validators) + Task 5 (postgres path already references by name); anti-mining (inert until demanded) → preserved by `PlanFlat` only resolving demanded names (Task 2); secrets-never-logged → unchanged, only names in errors; testing matrix → Tasks 1,2,3,4,5; docs → Task 6. All spec sections map to a task.
- **Placeholder scan:** no TBD/TODO; every code step shows the code; the one soft step (Task 6 content) enumerates exactly what the doc must contain and defers to docs-author for prose, which is appropriate for a doc task.
- **Type consistency:** `LoadFlat(path) (Store, error)`, `PlanFlat(demanded []string, expand MintExpander) ([]secret.Spec, []string, error)`, `Store.Credentials map[string]Source`, `serveConfig.CredentialsFile`, `discordConfig.BotTokenCred`, `requisitionerConfig.TrackerTokenCred`, `planCredentials(serveConfig) (map[string]secret.Spec, error)`, `credentialsFileHint` — all used consistently across tasks. `credSpec` retained through Task 4 (for deprecation detection), `credSpecs`/`toSpec` deleted only in Task 5 after call sites move.
