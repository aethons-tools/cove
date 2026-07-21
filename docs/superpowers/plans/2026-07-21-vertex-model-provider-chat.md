# Claude on Vertex — `model-provider` (chat path) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an at-cove `chat` session run Claude Code against Google Vertex AI instead of first-party Anthropic, driven by a new non-secret `model-provider.vertex` block in `config.yml`.

**Architecture:** The provider block is an `env` map (Claude Code's Vertex config is entirely env-driven). Its *presence* switches `chat`'s auth step from Anthropic subscription OAuth to seeding a GCP Application Default Credentials (ADC) file (`GOOGLE_APPLICATION_CREDENTIALS`), and auto-derives the GCP egress domains into the kit-root allow-list. It is a **branch, not a fork**: the collaborator role, workspace clone, per-class egress, and secret transport are all unchanged. The worker (`work`/`dispatch`) path is out of scope for this plan (separate follow-up).

**Tech Stack:** Go (stdlib + `gopkg.in/yaml.v3`); hermetic tests drive `internal/runner.Fake` (no docker/network/VM).

## Global Constraints

- **Design source:** `docs/superpowers/specs/2026-07-21-vertex-model-provider-design.md`. Every task implements part of it.
- **Sealed hardening is untouched.** No edits under `internal/assemble/hardening/`. Vertex only *widens* the kit-root (additive) egress tier and *branches* the auth step.
- **The `env` map is kit-authored with no host gate** → a hardening denylist rejects protected keys at config validation (fail loud) and defensively drops them at injection. Protected set: `http_proxy`/`https_proxy`/`no_proxy` (+ uppercase), `CLAUDE_CONFIG_DIR`, `GOOGLE_APPLICATION_CREDENTIALS`, `PATH`.
- **Required Vertex env keys:** `ANTHROPIC_VERTEX_PROJECT_ID`, `CLOUD_ML_REGION`. at-cove sets `CLAUDE_CODE_USE_VERTEX=1` itself.
- **The GCP credential is a file, never session env.** It is resolved host-side (demand/supply model, well-known demand `GOOGLE_APPLICATION_CREDENTIALS_JSON`), kept out of the agent's secret env, and seeded to `/agent-data/.gcp-adc.json` (mode 077). A `gcloud`-produced `authorized_user` ADC is static (google-auth refreshes access tokens in-VM over `oauth2.googleapis.com`), so **seed-only, no save-back**.
- **Currency needs no change:** `install.CurrencyInputs.KitSourceTree` already hashes raw `config.yml`, so adding the block re-hashes automatically.
- **TDD, DRY, YAGNI, frequent commits.** Full hermetic suite: `just test` (or `go test ./...`). Keep tests hermetic (Fake runner).
- **Commit trailers** (every commit):
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98
  ```

---

### Task 1: Config — the `model-provider.vertex` block, validation, and env helpers

**Files:**
- Modify: `internal/kit/config.go`
- Test: `internal/kit/config_test.go`

**Interfaces:**
- Produces:
  - `type ModelProvider struct { Vertex *VertexProvider }`
  - `type VertexProvider struct { Env map[string]string }`
  - `Config.ModelProvider *ModelProvider` (yaml `model-provider`)
  - `func (c Config) Vertex() (*VertexProvider, bool)` — the provider + true when set
  - `func (c Config) VertexEnv() map[string]string` — effective session env (`CLAUDE_CODE_USE_VERTEX=1` + kit env, denylist-filtered); nil when not a Vertex kit
  - `var vertexProtectedEnvKeys map[string]bool`

- [ ] **Step 1: Write the failing tests**

Add to `internal/kit/config_test.go`:

```go
func TestParseConfig_VertexValid(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: my-proj
      CLOUD_ML_REGION: us-east5
      ANTHROPIC_MODEL: claude-opus-4-8
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	v, ok := cfg.Vertex()
	if !ok {
		t.Fatalf("Vertex() ok = false, want true")
	}
	if v.Env["ANTHROPIC_VERTEX_PROJECT_ID"] != "my-proj" {
		t.Fatalf("project id = %q", v.Env["ANTHROPIC_VERTEX_PROJECT_ID"])
	}
	env := cfg.VertexEnv()
	if env["CLAUDE_CODE_USE_VERTEX"] != "1" {
		t.Fatalf("VertexEnv missing CLAUDE_CODE_USE_VERTEX=1: %v", env)
	}
	if env["ANTHROPIC_MODEL"] != "claude-opus-4-8" || env["CLOUD_ML_REGION"] != "us-east5" {
		t.Fatalf("VertexEnv passthrough wrong: %v", env)
	}
}

func TestParseConfig_VertexMissingRequired(t *testing.T) {
	_, err := ParseConfig([]byte(`
name: k
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: my-proj
`))
	if err == nil || !strings.Contains(err.Error(), "CLOUD_ML_REGION is required") {
		t.Fatalf("want CLOUD_ML_REGION required error, got %v", err)
	}
}

func TestParseConfig_VertexRejectsProtectedKey(t *testing.T) {
	_, err := ParseConfig([]byte(`
name: k
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: my-proj
      CLOUD_ML_REGION: us
      https_proxy: http://evil:3128
`))
	if err == nil || !strings.Contains(err.Error(), "https_proxy") {
		t.Fatalf("want protected-key rejection for https_proxy, got %v", err)
	}
}

func TestVertexEnv_NilWhenNoProvider(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.VertexEnv() != nil {
		t.Fatalf("VertexEnv should be nil for a non-vertex kit")
	}
	if _, ok := cfg.Vertex(); ok {
		t.Fatalf("Vertex() ok = true for a non-vertex kit")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/kit/ -run 'Vertex' -v`
Expected: FAIL — `ParseConfig` rejects the unknown `model-provider` field (`KnownFields(true)`), and `cfg.Vertex`/`cfg.VertexEnv` are undefined.

- [ ] **Step 3: Add the types and Config field**

In `internal/kit/config.go`, add near the other config types (e.g. after `ImageConfig`):

```go
// ModelProvider switches the sandbox's agent off first-party Anthropic and onto a
// third-party Claude provider — a union keyed by provider name (vertex only today).
// Its presence is the switch; absent, the Anthropic auth paths are unchanged.
type ModelProvider struct {
	Vertex *VertexProvider `yaml:"vertex,omitempty"`
}

// VertexProvider configures Claude on Google Vertex AI. Because Claude Code's
// Vertex configuration is entirely environment-driven, the payload is a non-secret
// env map: at-cove demands the required keys and passes any other (non-protected)
// key through. The GCP *credential* is not here — it is a host-supplied file
// (see cmd/at-cove: the GOOGLE_APPLICATION_CREDENTIALS_JSON demand).
type VertexProvider struct {
	Env map[string]string `yaml:"env"`
}
```

Add the field to `Config` (after `Collaborators`):

```go
	ModelProvider *ModelProvider          `yaml:"model-provider,omitempty"`
```

- [ ] **Step 4: Add the denylist, accessors, and validation**

Add to `internal/kit/config.go`:

```go
// vertexProtectedEnvKeys are sealed-owned or security-relevant variables a kit's
// model-provider env map must never set. Unlike a *secret* (demanded by the kit,
// supplied by the machine — the operator is the gate), an env value is
// kit-authored with no host gate, and the per-session env file is *sourced* in the
// session shell, so an unchecked value would shadow the sealed /etc/environment
// (e.g. the proxy vars) and could defeat egress. Rejected at validation and
// dropped defensively at injection ("additive, sealed-wins" for env).
var vertexProtectedEnvKeys = map[string]bool{
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"CLAUDE_CONFIG_DIR":              true,
	"GOOGLE_APPLICATION_CREDENTIALS": true,
	"PATH":                           true,
}

// vertexRequiredEnvKeys must be present in the provider env map.
var vertexRequiredEnvKeys = []string{"ANTHROPIC_VERTEX_PROJECT_ID", "CLOUD_ML_REGION"}

// Vertex returns the Vertex provider config and true when the kit targets Vertex.
func (c Config) Vertex() (*VertexProvider, bool) {
	if c.ModelProvider == nil || c.ModelProvider.Vertex == nil {
		return nil, false
	}
	return c.ModelProvider.Vertex, true
}

// VertexEnv returns the effective non-secret session env for a Vertex kit:
// CLAUDE_CODE_USE_VERTEX=1 (at-cove-owned) plus every non-protected key from the
// kit's env map. Protected keys are dropped defensively (validation already
// rejected them). Returns nil for a non-Vertex kit. GOOGLE_APPLICATION_CREDENTIALS
// (the ADC file pointer) is set by connect, not here.
func (c Config) VertexEnv() map[string]string {
	v, ok := c.Vertex()
	if !ok {
		return nil
	}
	env := map[string]string{"CLAUDE_CODE_USE_VERTEX": "1"}
	for k, val := range v.Env {
		if vertexProtectedEnvKeys[k] {
			continue
		}
		env[k] = val
	}
	return env
}

// validateModelProvider enforces the provider union, required keys, and the
// hardening denylist.
func validateModelProvider(cfg Config) error {
	if cfg.ModelProvider == nil {
		return nil
	}
	v := cfg.ModelProvider.Vertex
	if v == nil {
		return fmt.Errorf("config.yml: model-provider: must set exactly one provider (vertex)")
	}
	for _, req := range vertexRequiredEnvKeys {
		if strings.TrimSpace(v.Env[req]) == "" {
			return fmt.Errorf("config.yml: model-provider.vertex.env.%s is required", req)
		}
	}
	for k := range v.Env {
		if vertexProtectedEnvKeys[k] {
			return fmt.Errorf("config.yml: model-provider.vertex.env: %q is a sealed-owned/security-relevant variable and cannot be set by a kit", k)
		}
	}
	return nil
}
```

- [ ] **Step 5: Call the validator in ParseConfig**

In `ParseConfig`, just before `return cfg, nil` (after the collaborators block), add:

```go
	if err := validateModelProvider(cfg); err != nil {
		return Config{}, err
	}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/kit/ -run 'Vertex' -v`
Expected: PASS (all four tests).

- [ ] **Step 7: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(config): add model-provider.vertex block with env map + hardening denylist

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98"
```

---

### Task 2: Egress — auto-derive GCP domains into the baked kit-root allow-list

**Files:**
- Modify: `internal/kit/config.go` (domain derivation)
- Modify: `internal/assemble/assemble.go` (take a root-domains slice)
- Modify: `internal/assemble/assemble_test.go` (caller signature + a vertex case)
- Modify: `cmd/at-cove/main.go:377` (pass `kit.RootDomains(cfg)`)
- Test: `internal/kit/config_test.go`

**Interfaces:**
- Consumes: `Config.Vertex()`, `unionDomains` (Task 1 / existing).
- Produces:
  - `func ProviderDomains(c Config) []string` — GCP domains for a Vertex kit (nil otherwise)
  - `func RootDomains(c Config) []string` — `unionDomains(c.Image.AllowedDomains, ProviderDomains(c))`
  - `Assemble(kitDir, buildDir string, pub []byte, rootDomains []string) error` (was `img kit.ImageConfig`)

- [ ] **Step 1: Write the failing tests (kit)**

Add to `internal/kit/config_test.go`:

```go
func TestProviderDomains_Vertex(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
image:
  allowed-domains: [example.com]
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: p
      CLOUD_ML_REGION: us-east5
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	root := RootDomains(cfg)
	joined := strings.Join(root, ",")
	for _, want := range []string{
		"example.com",                       // kit root preserved
		"aiplatform.googleapis.com",         // base vertex host
		"us-east5-aiplatform.googleapis.com", // regional host
		"oauth2.googleapis.com",             // ADC refresh
		"sts.googleapis.com",
		"iamcredentials.googleapis.com",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("RootDomains missing %q; got %v", want, root)
		}
	}
}

func TestProviderDomains_NilWhenNoProvider(t *testing.T) {
	cfg, _ := ParseConfig([]byte("name: k\nimage:\n  allowed-domains: [only.example]\n"))
	if ProviderDomains(cfg) != nil {
		t.Fatalf("ProviderDomains should be nil for a non-vertex kit")
	}
	if got := RootDomains(cfg); len(got) != 1 || got[0] != "only.example" {
		t.Fatalf("RootDomains = %v, want [only.example]", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/kit/ -run 'ProviderDomains' -v`
Expected: FAIL — `ProviderDomains`/`RootDomains` undefined.

- [ ] **Step 3: Implement the derivation (kit)**

Add to `internal/kit/config.go`:

```go
// ProviderDomains returns the additive egress domains a model provider needs,
// derived from the provider config, or nil when the kit targets no provider.
// For Vertex: the aiplatform inference host (region-templated), plus the GCP
// auth endpoints google-auth uses to refresh ADC access tokens in-VM.
func ProviderDomains(c Config) []string {
	v, ok := c.Vertex()
	if !ok {
		return nil
	}
	domains := []string{
		"aiplatform.googleapis.com", // default / global inference host
		"oauth2.googleapis.com",     // authorized_user ADC token refresh
		"sts.googleapis.com",        // WIF / external_account
		"iamcredentials.googleapis.com",
	}
	switch region := strings.TrimSpace(v.Env["CLOUD_ML_REGION"]); region {
	case "", "global":
		// aiplatform.googleapis.com already covers the global endpoint.
	case "us", "eu":
		domains = append(domains, "aiplatform."+region+".rep.googleapis.com")
	default:
		domains = append(domains, region+"-aiplatform.googleapis.com")
	}
	return domains
}

// RootDomains is the kit's effective baked egress allow-list: the kit's own
// image.allowed-domains unioned with any provider-derived domains. Assemble bakes
// this into allowed_domains.kit.txt.
func RootDomains(c Config) []string {
	return unionDomains(c.Image.AllowedDomains, ProviderDomains(c))
}
```

- [ ] **Step 4: Run to verify the kit tests pass**

Run: `go test ./internal/kit/ -run 'ProviderDomains' -v`
Expected: PASS.

- [ ] **Step 5: Change Assemble's signature**

In `internal/assemble/assemble.go`, change the function signature and the `writeAllowedDomains` call:

```go
func Assemble(kitDir, buildDir string, pub []byte, rootDomains []string) error {
```

and replace the call site inside it:

```go
	if err := writeAllowedDomains(buildDir, rootDomains); err != nil {
		return err
	}
```

Remove the now-unused `kit` import **only if** nothing else in the file references `kit` (it does not after this change — delete `"github.com/aethons-tools/cove/internal/kit"` from the import block).

- [ ] **Step 6: Update the assemble caller in main.go**

In `cmd/at-cove/main.go`, at the line that calls `assemble.Assemble` (~line 377), change:

```go
	return assemble.Assemble(kitDir, filepath.Join(kitDir, ".build"), pub, kit.RootDomains(cfg))
```

(`cfg` is the `kit.Config` in scope at that call site; `kit` is already imported.)

- [ ] **Step 7: Update the assemble tests + add a vertex case**

In `internal/assemble/assemble_test.go`, update the three `Assemble(...)` callers to pass a `[]string` instead of an `ImageConfig`:
- the `kit.ImageConfig{}` calls → `nil`
- the `img` call → `img.AllowedDomains` (keep constructing `img` for its `.AllowedDomains`, or inline the slice)

Then add a subtest asserting the vertex-derived domains land in the baked file:

```go
func TestAssemble_VertexDomainsBaked(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(kitDir, ".build")
	cfg, err := kit.ParseConfig([]byte(`
name: k
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: p
      CLOUD_ML_REGION: us-east5
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if err := Assemble(kitDir, buildDir, []byte("k\n"), kit.RootDomains(cfg)); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(buildDir, "image-files/etc/squid/allowed_domains.kit.txt"))
	if err != nil {
		t.Fatalf("read baked domains: %v", err)
	}
	if !strings.Contains(string(b), "us-east5-aiplatform.googleapis.com") ||
		!strings.Contains(string(b), "oauth2.googleapis.com") {
		t.Fatalf("baked kit domains missing vertex hosts:\n%s", b)
	}
}
```

Ensure the test file imports `os`, `path/filepath`, `strings`, and `github.com/aethons-tools/cove/internal/kit` (add any missing).

- [ ] **Step 8: Run assemble + build to verify**

Run: `go test ./internal/assemble/ -run 'Assemble' -v && go build ./...`
Expected: PASS and a clean build (the main.go caller and all test callers now match the new signature).

- [ ] **Step 9: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go internal/assemble/assemble.go internal/assemble/assemble_test.go cmd/at-cove/main.go
git commit -m "feat(egress): auto-derive Vertex GCP domains into the baked kit-root allow-list

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98"
```

---

### Task 3: connect — inject provider env and seed the GCP ADC, skipping Anthropic OAuth

**Files:**
- Modify: `internal/connect/connect.go`
- Test: `internal/connect/connect_test.go`

**Interfaces:**
- Consumes: `secret.Spec`, `runner.Runner`, `sshargs` (existing).
- Produces:
  - `Options.ExtraEnv map[string]string` — non-secret env merged into the launch env (secrets win on key collision)
  - `Options.Vertex *VertexAuth` — when set, `Connect` seeds the ADC and skips Anthropic OAuth
  - `type VertexAuth struct { ADC []byte }`
  - const `gcpADCVMPath = "/agent-data/.gcp-adc.json"`

- [ ] **Step 1: Write the failing test**

Add to `internal/connect/connect_test.go` (reuse the file's existing `fakeBackend`, `fakeInhibitor`, `calledWith` helpers). This asserts a Vertex connect seeds the ADC, sets the pointer + provider env, and never probes/logs in via `claude auth`:

```go
type fakeTransport struct{ env map[string]string }

func (t *fakeTransport) Launch(_ sshargs.Target, env map[string]string) error {
	t.env = env
	return nil
}

func TestConnect_VertexSeedsADCAndSkipsOAuth(t *testing.T) {
	r := &runner.Fake{}
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, Options{
		Container:     "c1",
		IdentityFile:  "id",
		KnownHostsDir: t.TempDir(),
		ExtraEnv:      map[string]string{"CLAUDE_CODE_USE_VERTEX": "1", "CLOUD_ML_REGION": "us-east5"},
		Vertex:        &VertexAuth{ADC: []byte(`{"type":"authorized_user"}`)},
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// The ADC was seeded to the VM path via stdin (never argv).
	seeded := false
	for _, c := range r.Calls {
		if calledWith([]runner.Call{c}, gcpADCVMPath) && strings.Contains(c.Stdin, "authorized_user") {
			seeded = true
		}
	}
	if !seeded {
		t.Fatalf("ADC was not seeded to %s via stdin; calls: %+v", gcpADCVMPath, r.Calls)
	}
	// Anthropic OAuth was skipped entirely.
	if calledWith(r.Calls, "claude auth status") || calledWith(r.Calls, "claude auth login") {
		t.Fatalf("vertex connect must not run claude auth; calls: %+v", r.Calls)
	}
	// The launch env carries the provider env plus the ADC pointer.
	if tr.env["CLAUDE_CODE_USE_VERTEX"] != "1" || tr.env["CLOUD_ML_REGION"] != "us-east5" {
		t.Fatalf("launch env missing provider vars: %v", tr.env)
	}
	if tr.env["GOOGLE_APPLICATION_CREDENTIALS"] != gcpADCVMPath {
		t.Fatalf("GOOGLE_APPLICATION_CREDENTIALS = %q, want %q", tr.env["GOOGLE_APPLICATION_CREDENTIALS"], gcpADCVMPath)
	}
}
```

If the test file already defines a transport double, reuse it instead of adding `fakeTransport` (check for an existing `Launch` implementation first to avoid a duplicate type).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/connect/ -run 'Vertex' -v`
Expected: FAIL — `Options.ExtraEnv`, `Options.Vertex`, `VertexAuth`, `gcpADCVMPath` undefined.

- [ ] **Step 3: Add the option fields, type, and const**

In `internal/connect/connect.go`, add to the `const (...)` block:

```go
	// gcpADCVMPath is where a Vertex session's Application Default Credentials file
	// is seeded (GOOGLE_APPLICATION_CREDENTIALS points here). A gcloud
	// authorized_user ADC is static — google-auth refreshes access tokens in-VM
	// over oauth2.googleapis.com — so connect seeds it and never saves it back.
	gcpADCVMPath = "/agent-data/.gcp-adc.json"
```

Add to the `Options` struct:

```go
	// ExtraEnv is non-secret env merged into the launch env (e.g. a model
	// provider's CLAUDE_CODE_USE_VERTEX / CLOUD_ML_REGION). Resolved secrets win on
	// a key collision. Never carries credential material.
	ExtraEnv map[string]string
	// Vertex, when set, makes this a Claude-on-Vertex session: connect seeds the
	// GCP ADC file and points GOOGLE_APPLICATION_CREDENTIALS at it, instead of the
	// Anthropic subscription OAuth flow.
	Vertex *VertexAuth
```

Add the type near `WorkspaceClone`:

```go
// VertexAuth carries the GCP Application Default Credentials seeded into a Vertex
// session. The ADC is resolved host-side and never enters the agent's secret env.
type VertexAuth struct {
	ADC []byte // an ADC file (e.g. a gcloud authorized_user credential)
}
```

- [ ] **Step 4: Branch the auth step and merge env in Connect**

In `Connect`, replace the auth block:

```go
	if !o.SkipAuth {
		if err := ensureAuthenticated(r, tgt, o.CredentialsFile, stderr); err != nil {
			return err
		}
	}
```

with:

```go
	if !o.SkipAuth {
		if o.Vertex != nil {
			if err := seedVertexCredentials(r, tgt, o.Vertex.ADC); err != nil {
				return err
			}
		} else {
			if err := ensureAuthenticated(r, tgt, o.CredentialsFile, stderr); err != nil {
				return err
			}
		}
	}
```

Immediately after `env, err := secret.Resolve(r, nil, o.Secrets)` (and its error check), merge the provider env and set the ADC pointer:

```go
	// Provider (non-secret) env: apply without clobbering a resolved secret.
	for k, v := range o.ExtraEnv {
		if _, taken := env[k]; !taken {
			env[k] = v
		}
	}
	if o.Vertex != nil {
		env["GOOGLE_APPLICATION_CREDENTIALS"] = gcpADCVMPath
	}
```

Guard the post-launch credential save so it never runs for a Vertex session (the ADC is static — nothing to save back). Change:

```go
	if !o.SkipAuth {
		if err := saveCredentials(r, tgt, o.CredentialsFile); err != nil {
```

to:

```go
	if !o.SkipAuth && o.Vertex == nil {
		if err := saveCredentials(r, tgt, o.CredentialsFile); err != nil {
```

- [ ] **Step 5: Add seedVertexCredentials**

Add to `internal/connect/connect.go` (near `seedCredentials`):

```go
// seedVertexCredentials writes the GCP Application Default Credentials into the VM
// over ssh stdin (umask 077, never on argv), the same in-memory transport used for
// the Anthropic login and secrets. Seed-only: an authorized_user ADC is static, so
// there is no save-back — google-auth refreshes access tokens in-VM.
func seedVertexCredentials(r runner.Runner, tgt sshargs.Target, adc []byte) error {
	args := append(sshargs.Base(tgt), "umask 077; cat > "+gcpADCVMPath)
	if err := r.RunStdin(bytes.NewReader(adc), "ssh", args...); err != nil {
		return fmt.Errorf("seeding GCP credentials into sandbox: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Run to verify the test passes**

Run: `go test ./internal/connect/ -run 'Vertex' -v`
Expected: PASS.

- [ ] **Step 7: Run the full connect suite (guard against regressions)**

Run: `go test ./internal/connect/ -v`
Expected: PASS (the Anthropic path is unchanged; `Vertex == nil` preserves prior behavior).

- [ ] **Step 8: Commit**

```bash
git add internal/connect/connect.go internal/connect/connect_test.go
git commit -m "feat(connect): seed GCP ADC + inject provider env for Vertex sessions, skip OAuth

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98"
```

---

### Task 4: `chat` wiring — resolve the GCP credential and pass the Vertex branch

**Files:**
- Modify: `cmd/at-cove/main.go` (`doChat` + a new `vertexPlan` helper + `gcpADCDemand` const)
- Test: `cmd/at-cove/main_test.go`

**Interfaces:**
- Consumes: `Config.Vertex()`, `Config.VertexEnv()` (Task 1); `connect.Options.{ExtraEnv,Vertex}`, `connect.VertexAuth` (Task 3); existing `planRequired(store, expand, kitName, kitPath, name, secretsPath) (secret.Spec, error)`; `secret.Resolve`; `usersecret.Store`; `mint.Expander`.
- Produces:
  - const `gcpADCDemand = "GOOGLE_APPLICATION_CREDENTIALS_JSON"`
  - `func vertexPlan(cfg kit.Config, store usersecret.Store, expand usersecret.MintExpander, kitName, kitPath, secretsPath string, r runner.Runner) (*connect.VertexAuth, map[string]string, error)`

- [ ] **Step 1: Write the failing test**

Add to `cmd/at-cove/main_test.go`:

```go
func TestVertexPlan_ResolvesADCAndEnv(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.yml")
	localPath := filepath.Join(dir, "secrets.local.yml")
	if err := os.WriteFile(secretsPath, []byte(`
kits:
  vkit:
    GOOGLE_APPLICATION_CREDENTIALS_JSON:
      value: '{"type":"authorized_user","refresh_token":"r"}'
`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		t.Fatalf("usersecret.Load: %v", err)
	}
	cfg, err := kit.ParseConfig([]byte(`
name: vkit
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: p
      CLOUD_ML_REGION: us-east5
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	r := &runner.Fake{}
	expand := mint.Expander(r, store.Global, "")
	va, env, err := vertexPlan(cfg, store, expand, "vkit", "/canon/vkit", secretsPath, r)
	if err != nil {
		t.Fatalf("vertexPlan: %v", err)
	}
	if !strings.Contains(string(va.ADC), "authorized_user") {
		t.Fatalf("ADC not resolved: %q", va.ADC)
	}
	if env["CLAUDE_CODE_USE_VERTEX"] != "1" || env["ANTHROPIC_VERTEX_PROJECT_ID"] != "p" {
		t.Fatalf("vertex env wrong: %v", env)
	}
}

func TestVertexPlan_FailsClosedWhenUnsupplied(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.yml")
	localPath := filepath.Join(dir, "secrets.local.yml")
	if err := os.WriteFile(secretsPath, []byte("kits: {}\n"), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		t.Fatalf("usersecret.Load: %v", err)
	}
	cfg, _ := kit.ParseConfig([]byte("name: vkit\nmodel-provider:\n  vertex:\n    env:\n      ANTHROPIC_VERTEX_PROJECT_ID: p\n      CLOUD_ML_REGION: us\n"))
	r := &runner.Fake{}
	expand := mint.Expander(r, store.Global, "")
	if _, _, err := vertexPlan(cfg, store, expand, "vkit", "/canon/vkit", secretsPath, r); err == nil {
		t.Fatalf("want a fail-closed error when the ADC is unsupplied")
	}
}
```

Confirm `main_test.go` imports `os`, `path/filepath`, `strings`, and the packages `kit`, `usersecret`, `mint`, `runner` (add any missing to its import block).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/at-cove/ -run 'VertexPlan' -v`
Expected: FAIL — `vertexPlan` and `gcpADCDemand` undefined.

- [ ] **Step 3: Add the const and helper**

In `cmd/at-cove/main.go`, add near the other well-known secret constants / helpers (e.g. beside `workspaceClonePlan`):

```go
// gcpADCDemand is the well-known demand name a Vertex kit's GCP Application Default
// Credentials are supplied under (machine-side, in secrets.yml/secrets.local.yml).
// It is resolved host-side and seeded into the VM as a file — it never enters the
// agent's session env.
const gcpADCDemand = "GOOGLE_APPLICATION_CREDENTIALS_JSON"

// vertexPlan resolves a Vertex kit's GCP credential host-side (kept out of the
// session secret env) and returns it plus the non-secret provider env. Fails
// closed (via planRequired) when no supply is wired for the kit.
func vertexPlan(cfg kit.Config, store usersecret.Store, expand usersecret.MintExpander, kitName, kitPath, secretsPath string, r runner.Runner) (*connect.VertexAuth, map[string]string, error) {
	spec, err := planRequired(store, expand, kitName, kitPath, gcpADCDemand, secretsPath)
	if err != nil {
		return nil, nil, err
	}
	resolved, err := secret.Resolve(r, nil, []secret.Spec{spec})
	if err != nil {
		return nil, nil, err
	}
	adc := resolved[gcpADCDemand]
	if strings.TrimSpace(adc) == "" {
		return nil, nil, fmt.Errorf("vertex kit %q: resolved GCP credential %s is empty", kitName, gcpADCDemand)
	}
	return &connect.VertexAuth{ADC: []byte(adc)}, cfg.VertexEnv(), nil
}
```

- [ ] **Step 4: Wire the branch into doChat**

In `doChat` (`cmd/at-cove/main.go`), after the `store, err := usersecret.Load(...)` block and the `expand := mint.Expander(r, store.Global, "")` line (around line 775), add:

```go
	var vertexAuth *connect.VertexAuth
	var vertexEnv map[string]string
	if _, isVertex := cfg.Vertex(); isVertex {
		if vertexAuth, vertexEnv, err = vertexPlan(cfg, store, expand, st.Name, kitPath, secretsPath, r); err != nil {
			return err
		}
	}
```

Then, in the `connect.Connect(...)` call's `connect.Options{...}` literal (around line 873), add the two fields:

```go
		ExtraEnv:           vertexEnv,
		Vertex:             vertexAuth,
```

(Leave `CredentialsFile` as-is: `connect` ignores it when `Vertex != nil`.)

- [ ] **Step 5: Run to verify the tests pass**

Run: `go test ./cmd/at-cove/ -run 'VertexPlan' -v && go build ./...`
Expected: PASS and clean build.

- [ ] **Step 6: Run the whole hermetic suite**

Run: `just test`
Expected: PASS across all packages.

- [ ] **Step 7: Commit**

```bash
git add cmd/at-cove/main.go cmd/at-cove/main_test.go
git commit -m "feat(chat): wire the Vertex branch — resolve GCP ADC host-side, pass provider env

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98"
```

---

### Task 5: Documentation

**Files:**
- Modify: `docs/OVERVIEW.md` (Authentication + egress sections)
- Modify: `docs/usage/at-cove-config.md` (the `model-provider` block)
- Modify: `docs/usage/at-cove-secrets.md` (the `GOOGLE_APPLICATION_CREDENTIALS_JSON` supply)
- Modify: `docs/usage/INDEX.md` only if a new doc is added (it is not — edits route to owning docs)

This task uses the **docs-author** skill to route each change to the doc that owns the subject and keep `docs/INDEX.md`/cross-links valid. No code; no test cycle. Right-sized as one task because the changes are small and share one subject.

- [ ] **Step 1: Invoke docs-author and make the edits**

Content to land (author into the owning docs, matching their progressive-disclosure style):
- **`docs/usage/at-cove-config.md`** — a `model-provider` section: the `vertex.env` map, the required keys (`ANTHROPIC_VERTEX_PROJECT_ID`, `CLOUD_ML_REGION`), that at-cove sets `CLAUDE_CODE_USE_VERTEX=1`, the hardening denylist (protected keys rejected), and that GCP egress domains are auto-derived. Note scope: kit-global, chat path (worker path is a documented follow-up).
- **`docs/OVERVIEW.md` Authentication** — a Vertex subsection: a `model-provider.vertex` kit authenticates `chat` via a seeded GCP ADC file (`GOOGLE_APPLICATION_CREDENTIALS` → `/agent-data/.gcp-adc.json`), skipping subscription OAuth; the ADC is supplied host-side under the well-known demand `GOOGLE_APPLICATION_CREDENTIALS_JSON`, seeded (not saved back, since an authorized_user ADC is static), and refreshed in-VM over `oauth2.googleapis.com`.
- **`docs/OVERVIEW.md` egress section** — note the kit-root allow-list auto-gains the GCP hosts for a Vertex kit (region-templated aiplatform host + `oauth2`/`sts`/`iamcredentials.googleapis.com`); sealed base unchanged.
- **`docs/usage/at-cove-secrets.md`** — the `GOOGLE_APPLICATION_CREDENTIALS_JSON` demand for a Vertex kit: supply it under `kits: <kit>:` (e.g. `{ command: ["cat", "~/.config/gcloud/application_default_credentials.json"] }`), resolved host-side and seeded as a file, never injected as env.

- [ ] **Step 2: Run the docs checker**

Use the **docs-audit** skill (or run its checker) to verify no orphans/dangling links/oversize/duplication.
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add docs/
git commit -m "docs: document the Vertex model-provider (chat path) — config, auth, egress, secret supply

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98"
```

---

## Out of scope (follow-up plan: the worker path)

Designed in the spec (§7) but **not** in this plan:
- Provider-aware fail-closed bearer gate in `cmd/at-cove/main.go` (~L1342): a Vertex worker requires the GCP credential to resolve, not `ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY`.
- A short-lived, host-minted GCP credential (`at-mint` `vertex` provider — impersonated-SA or WIF `external_account`), tmpfs-delivered per run.
- Per-worker-class provider scoping (this plan is kit-global).

These become a second plan once the chat path is on `main`.

## Self-Review

- **Spec coverage:** §4 config block → Task 1; §5 egress auto-derivation → Task 2; §6 chat credential (seed ADC, skip OAuth) → Tasks 3–4; §2 constraint (ADC file only, no bare token) → Tasks 3–4; hardening denylist → Task 1 (validate) + Task 1/`VertexEnv` (defensive drop); §7 worker → explicitly deferred; docs → Task 5. §3/§9 open questions resolved: region→endpoint is region-templated (Task 2); ADC placement is `/agent-data` seed-only (Task 3, corrected from the spec's "save back" — an authorized_user ADC is static); custom `ANTHROPIC_VERTEX_BASE_URL` egress folding is left for the worker/follow-up plan.
- **Placeholders:** none — every code and test step is complete.
- **Type consistency:** `Vertex()`/`VertexEnv()`/`ProviderDomains`/`RootDomains` (kit), `Options.ExtraEnv`/`Options.Vertex`/`VertexAuth`/`gcpADCVMPath` (connect), `gcpADCDemand`/`vertexPlan` (main) are defined once and consumed with matching signatures across tasks.
