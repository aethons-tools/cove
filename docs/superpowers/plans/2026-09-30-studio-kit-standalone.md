# StudioKit — Standalone Managed-Kit Concept Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the derived managed kit with a small, directly-authored `StudioKit` (base, egress, secrets, orient prompt, build args); the jam launcher builds and raises brokered coves from it, and `runtime.launcher.install-manifest` is removed.

**Architecture:** A new `internal/studio` package owns the `StudioKit` type, its codec, a build-digest over build-affecting fields (base + egress + build-args), the Anthropic-excluding egress ceiling, a built-in default kit, and the Raise-time prompt composer. `internal/jam` stores studio kits in the existing registry (type-discriminated), resolves a role's kit to a `KitRef` whose `Digest` is the build-digest, and composes the session prompt at Raise. The launcher tags images by build-digest (`cove-kit:<digest>`), so a prompt-only edit reuses the cached image. The derive-from-a-full-`kit.Config` path (`ManagedKit`/`EnsureManagedKit*`) and the install-manifest are removed. `at-cove` and `install.Manifest` are untouched.

**Tech Stack:** Go (stdlib + `internal/{studio,jam,jam/launcher,backend,backend/colima,assemble,baseimage,basedigest,kit,connect,runner}`), hermetic tests via `runner.Fake` + fake backend/launcher, `just test` / `just build` / `just lint`.

**Spec:** `docs/superpowers/specs/2026-09-30-studio-kit-standalone-design.md`

## Global Constraints

- **Module** `github.com/aethons-tools/cove`; build/test via `just` (`just test` hermetic, `just build`, `just lint`).
- **TDD**, failing test first; hermetic (no Docker/VM/network), driving `runner.Fake` + fakes. Real-substrate tests behind the `integration` build tag.
- **Plan/execute split preserved:** pure *plan* (studio codec/digest/ceiling/compose, ref resolution) stays testable without a VM; *execution* (build/raise) stays in the launcher/backend.
- **COV-208 by construction:** the studio egress **ceiling excludes** `anthropic.com`, `claude.com`, `claude.ai` (and subdomains); the excluded set is **surfaced** (prepare log + `show`). No author-time rejection of the authored egress list.
- **Secrets never hit disk, argv, or logs:** a build-arg name may not collide with a declared secret demand or a reserved secret name; secret values are resolved/injected at Raise only, never passed to the build.
- **Base gate ON, no escape hatch** for studio kits (`AllowUnverified=false`): an unblessed base fails the prepare loudly.
- **Build-digest excludes the prompt:** the image content key is over build-affecting fields only (`base`, `egress`, `build-args`); the registry `(id,version)` keys the whole definition (prompt included).
- **`at-cove` untouched.** `install.Manifest` and `at-cove install` stay; only the jam launcher's *consumption* of a manifest is removed. The one intentional break is `runtime.launcher.install-manifest` (removed, **no shim**).
- **Tag-safe ids:** a `KitRef.Digest` becomes the docker tag `cove-kit:<digest>` (64-hex, tag-safe); a studio kit `Name` used as a registry id must match `[A-Za-z0-9_.-]+`.
- **Commit footer** on every commit:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```

## File Structure

**New — `internal/studio/`:**
- `studiokit.go` — `StudioKit`, `Base`, `Kind` const, `ParseStudioKit`, `(StudioKit).ToJSON`, `Validate`.
- `digest.go` — `BuildDigest` (over build-affecting fields), the sorted-egress canonicalization.
- `egress.go` — `Ceiling(authored) (ceiling, excluded []string)`, the Anthropic-root predicate (moved here from `jam/managedkit.go`).
- `default.go` — `DefaultStudioKit()` (blessed base, minimal egress, generic prompt).
- `prompt.go` — `PromptLayers`, `ComposePrompt`, `JamBoilerplate` const.
- `*_test.go` alongside each.

**Modified — `internal/jam/`:**
- `kit.go` — `KitDefinition` carries a `studio.StudioKit` (field `Kit`, was `Config kit.Config`).
- `managedkit.go` → renamed responsibilities: remove `ManagedKit`/`EnsureManagedKit`/`EnsureManagedKitFor`/`ManagedVariantID`/`ManagedKitID`/strip helpers; add `EnsureStudioKit`, `StudioKitRef`, `EnsureDefaultStudioKit`; rewrite `ResolveKitDefinition` for studio kits. (Rename file to `studiokit.go` in jam.)
- `supervisor.go` — `kitRefFor` resolves studio kits; `managedKit`→`defaultStudioKit`, `SetManagedKit`→`SetDefaultStudioKit`; `Raise` composes the prompt.

**Modified — launcher/backend:**
- `internal/backend/backend.go` — `KitImageBuilder.BuildKitImage` gains a `buildArgs map[string]string` param.
- `internal/backend/colima/colima.go` — `BuildKitImage` threads `buildArgs` into `docker build --build-arg`.
- `internal/jam/launcher/inventory.go` — `imageTag` uses `ref.Digest`.
- `internal/jam/launcher/prepare.go` — assemble studio egress, always `ResolveKitBase(def.Kit.Base.Ref)` (gate on), `ErrDockerfileContextUnsupported`, pass build-args, log excluded roots.
- `internal/jam/launcher/launcher.go` — `Config` drops `Image`/`ImageDigest`/`BaseImage`; `defaultAssemble` uses `def.Kit`.

**Modified — cmd/docs:**
- `cmd/at-jam/config.go` — remove `launcherConfig.InstallManifest`; `validateLauncher` drops its check.
- `cmd/at-jam/main.go` — remove install-manifest read; seed `EnsureDefaultStudioKit`; `launcher.New` without manifest fields; `sup.SetDefaultStudioKit(defaultRef)`.
- `cmd/at-jam/main.go` (kit push) + `cmd/at-jam/*` (`kit show`/`studio show`) — validate studio-kit YAML; surface ceiling + exclusions.
- `docs/usage/jam/serve.md`, `docs/usage/jam/kits.md`, `docs/usage/jam/coves.md`, and a migration note.

---

## Task 1: `studio.StudioKit` type, codec, and validation

**Files:**
- Create: `internal/studio/studiokit.go`
- Test: `internal/studio/studiokit_test.go`

**Interfaces:**
- Consumes: `internal/kit` (`SecretConfig`).
- Produces:
  - `const Kind = "studio"`
  - `type Base struct { Ref string; Dockerfile string; Context map[string]string }`
  - `type StudioKit struct { Kind string; Name string; Base Base; Egress []string; BuildArgs map[string]string; Secrets map[string]kit.SecretConfig; Prompt string }`
  - `func ParseStudioKit(data []byte) (StudioKit, error)` — strict YAML (`KnownFields`), requires `kind: studio` and a tag-safe `name`, runs `Validate`.
  - `func (sk StudioKit) ToJSON() ([]byte, error)` — deterministic canonical JSON (sorted keys).
  - `func (sk StudioKit) Validate() error`
  - `func TagSafeName(name string) bool`

- [ ] **Step 1: Write the failing test.**

```go
// internal/studio/studiokit_test.go
package studio

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kit"
)

func TestParseStudioKitRoundTrip(t *testing.T) {
	src := []byte(`kind: studio
name: web
base:
  ref: ghcr.io/acme/web@sha256:abc
egress:
  - github.com
  - pkg.go.dev
build-args:
  NODE_VERSION: "20"
secrets:
  AT_TASK_GIT_TOKEN:
    description: git push token
prompt: |
  You maintain the web service.
`)
	sk, err := ParseStudioKit(src)
	if err != nil {
		t.Fatalf("ParseStudioKit: %v", err)
	}
	if sk.Name != "web" || sk.Base.Ref != "ghcr.io/acme/web@sha256:abc" {
		t.Fatalf("bad parse: %+v", sk)
	}
	if sk.BuildArgs["NODE_VERSION"] != "20" || sk.Secrets["AT_TASK_GIT_TOKEN"].Description == "" {
		t.Fatalf("bad parse: %+v", sk)
	}
	b, err := sk.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic: re-marshal equal, and canonical key order (base before egress).
	b2, _ := sk.ToJSON()
	if string(b) != string(b2) {
		t.Fatal("ToJSON not deterministic")
	}
	if !strings.Contains(string(b), `"kind":"studio"`) {
		t.Fatalf("canonical JSON missing kind: %s", b)
	}
}

func TestParseStudioKitRejectsUnknownField(t *testing.T) {
	_, err := ParseStudioKit([]byte("kind: studio\nname: web\nworkers: {}\n"))
	if err == nil {
		t.Fatal("want strict-decode error on an unknown field")
	}
}

func TestParseStudioKitRequiresKindAndName(t *testing.T) {
	if _, err := ParseStudioKit([]byte("name: web\n")); err == nil {
		t.Fatal("want error when kind is missing")
	}
	if _, err := ParseStudioKit([]byte("kind: studio\n")); err == nil {
		t.Fatal("want error when name is missing")
	}
	if _, err := ParseStudioKit([]byte("kind: studio\nname: bad/name\n")); err == nil {
		t.Fatal("want error on a non-tag-safe name")
	}
}
```

- [ ] **Step 2: Run to verify it fails.**

Run: `go test ./internal/studio/ -run TestParseStudioKit -v`
Expected: FAIL — package/functions not defined.

- [ ] **Step 3: Implement `internal/studio/studiokit.go`.**

```go
// Package studio defines the StudioKit — the small, directly-authored kit a
// brokered ("studio") cove is built and raised from. It deliberately does NOT
// reuse kit.Config: a studio kit carries only a base, egress, secret demands,
// build args, and an orienting prompt.
package studio

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/aethons-tools/cove/internal/kit"
	"gopkg.in/yaml.v3"
)

// Kind is the discriminator stored with every studio kit, distinguishing it
// from a full kit.Config entry sharing the jam kit registry.
const Kind = "studio"

// Base names the image a studio kit builds FROM, exactly one of three ways:
//   - Ref set          → build FROM that (gated) image ref.
//   - Dockerfile set   → build a context (Dockerfile + Context files); the build
//     is DEFERRED (PrepareKit returns ErrDockerfileContextUnsupported).
//   - both empty       → the blessed default base.
// Ref and Dockerfile are mutually exclusive.
type Base struct {
	Ref        string            `yaml:"ref,omitempty" json:"ref,omitempty"`
	Dockerfile string            `yaml:"dockerfile,omitempty" json:"dockerfile,omitempty"`
	Context    map[string]string `yaml:"context,omitempty" json:"context,omitempty"`
}

// StudioKit is the parsed contents of a studio kit's config. Build-affecting
// fields (Base, Egress, BuildArgs) key the built image (see BuildDigest);
// raise-time fields (Secrets, Prompt) do not.
type StudioKit struct {
	Kind      string                      `yaml:"kind" json:"kind"`
	Name      string                      `yaml:"name" json:"name"`
	Base      Base                        `yaml:"base,omitempty" json:"base,omitempty"`
	Egress    []string                    `yaml:"egress,omitempty" json:"egress,omitempty"`
	BuildArgs map[string]string           `yaml:"build-args,omitempty" json:"buildArgs,omitempty"`
	Secrets   map[string]kit.SecretConfig `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Prompt    string                      `yaml:"prompt,omitempty" json:"prompt,omitempty"`
}

// ParseStudioKit unmarshals and validates studio-kit YAML. Unknown fields are
// rejected (KnownFields) to catch typos — including a full-kit field wrongly
// placed on a studio kit.
func ParseStudioKit(data []byte) (StudioKit, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var sk StudioKit
	if err := dec.Decode(&sk); err != nil {
		return StudioKit{}, fmt.Errorf("studio kit: %w", err)
	}
	if err := sk.Validate(); err != nil {
		return StudioKit{}, err
	}
	return sk, nil
}

// Validate enforces the studio-kit invariants.
func (sk StudioKit) Validate() error {
	if sk.Kind != Kind {
		return fmt.Errorf("studio kit: kind must be %q, got %q", Kind, sk.Kind)
	}
	if sk.Name == "" {
		return fmt.Errorf("studio kit: name is required")
	}
	if !TagSafeName(sk.Name) {
		return fmt.Errorf("studio kit: name %q is not tag-safe (allowed: [A-Za-z0-9_.-])", sk.Name)
	}
	if sk.Base.Ref != "" && sk.Base.Dockerfile != "" {
		return fmt.Errorf("studio kit %q: base.ref and base.dockerfile are mutually exclusive", sk.Name)
	}
	// Build-args must never collide with a secret demand or a reserved secret
	// name — secrets reach the session at raise, never the build (argv/logs).
	for k := range sk.BuildArgs {
		if _, ok := sk.Secrets[k]; ok {
			return fmt.Errorf("studio kit %q: build-arg %q collides with a secret demand", sk.Name, k)
		}
		if kit.IsReservedSecretName(k) {
			return fmt.Errorf("studio kit %q: build-arg %q is a reserved secret name", sk.Name, k)
		}
	}
	return nil
}

// ToJSON is the deterministic canonical form stored in the kit registry and
// hashed. encoding/json emits struct fields in declaration order and map keys
// sorted, so identical kits yield identical bytes.
func (sk StudioKit) ToJSON() ([]byte, error) {
	if sk.Kind == "" {
		sk.Kind = Kind
	}
	return json.Marshal(sk)
}

// TagSafeName reports whether name may be a docker-tag / registry-id component.
func TagSafeName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Add the exported reserved-secret predicate in `internal/kit`.**

`Validate` calls `kit.IsReservedSecretName`, which does not exist yet (the set `reservedSecretNames` is package-private, `internal/kit/config.go:893`). Add a thin exported accessor.

```go
// internal/kit/config.go  (add near reservedSecretNames, ~line 900)

// IsReservedSecretName reports whether n is a reserved subsystem secret name
// (AT_TASK_GIT_TOKEN, AT_DISPATCH_*, the ADC demand). Exported so other packages
// (e.g. internal/studio build-arg validation) can reject collisions without
// duplicating the set.
func IsReservedSecretName(n string) bool { return reservedSecretNames[n] }
```

- [ ] **Step 5: Run the tests to verify they pass.**

Run: `go test ./internal/studio/ ./internal/kit/ -run 'StudioKit|ReservedSecret' -v`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/studio/studiokit.go internal/studio/studiokit_test.go internal/kit/config.go
git commit -m "feat(studio): StudioKit type, strict codec, and validation"
```

---

## Task 2: Build-digest and the Anthropic-excluding ceiling

**Files:**
- Create: `internal/studio/digest.go`, `internal/studio/egress.go`, `internal/studio/default.go`
- Test: `internal/studio/digest_test.go`, `internal/studio/egress_test.go`

**Interfaces:**
- Produces:
  - `func BuildDigest(sk StudioKit) string` — hex sha256 over build-affecting fields only (Base, sorted Egress, BuildArgs).
  - `func Ceiling(authored []string) (ceiling, excluded []string)` — sorted+deduped authored egress minus the Anthropic roots; `excluded` is the sorted Anthropic entries removed.
  - `func DefaultStudioKit() StudioKit` — the built-in default (empty base → blessed default, minimal egress, generic prompt).

- [ ] **Step 1: Write the failing tests.**

```go
// internal/studio/digest_test.go
package studio

import "testing"

func TestBuildDigestIgnoresPrompt(t *testing.T) {
	a := StudioKit{Kind: Kind, Name: "web", Base: Base{Ref: "r"}, Egress: []string{"b.com", "a.com"}, Prompt: "one"}
	b := a
	b.Prompt = "two (edited)" // raise-time field — must not change the image key
	if BuildDigest(a) != BuildDigest(b) {
		t.Fatal("prompt edit must not change the build-digest")
	}
	c := a
	c.Egress = []string{"a.com", "b.com"} // reordered — same set
	if BuildDigest(a) != BuildDigest(c) {
		t.Fatal("egress order must not change the build-digest")
	}
	d := a
	d.BuildArgs = map[string]string{"X": "1"} // build-affecting change
	if BuildDigest(a) == BuildDigest(d) {
		t.Fatal("a build-arg change must change the build-digest")
	}
}
```

```go
// internal/studio/egress_test.go
package studio

import (
	"slices"
	"testing"
)

func TestCeilingExcludesAnthropic(t *testing.T) {
	ceiling, excluded := Ceiling([]string{"github.com", ".anthropic.com", "claude.ai", "pkg.go.dev", "api.anthropic.com"})
	if slices.Contains(ceiling, ".anthropic.com") || slices.Contains(ceiling, "claude.ai") || slices.Contains(ceiling, "api.anthropic.com") {
		t.Fatalf("ceiling must exclude Anthropic roots: %v", ceiling)
	}
	if !slices.Equal(ceiling, []string{"github.com", "pkg.go.dev"}) {
		t.Fatalf("ceiling = %v, want sorted non-Anthropic entries", ceiling)
	}
	if !slices.Equal(excluded, []string{".anthropic.com", "api.anthropic.com", "claude.ai"}) {
		t.Fatalf("excluded = %v, want the sorted Anthropic entries", excluded)
	}
}

func TestDefaultStudioKitIsAnthropicFree(t *testing.T) {
	sk := DefaultStudioKit()
	if err := sk.Validate(); err != nil {
		t.Fatalf("default kit invalid: %v", err)
	}
	_, excluded := Ceiling(sk.Egress)
	if len(excluded) != 0 {
		t.Fatalf("default kit egress must already be Anthropic-free, excluded=%v", excluded)
	}
	if sk.Base.Ref != "" || sk.Base.Dockerfile != "" {
		t.Fatal("default kit base must be empty (→ blessed default)")
	}
}
```

- [ ] **Step 2: Run to verify they fail.**

Run: `go test ./internal/studio/ -run 'BuildDigest|Ceiling|DefaultStudioKit' -v`
Expected: FAIL — functions not defined.

- [ ] **Step 3: Implement `digest.go`.**

```go
// internal/studio/digest.go
package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// BuildDigest is the content key of the image a studio kit builds. It hashes the
// BUILD-AFFECTING fields only — base, egress (order-normalized), build args — so
// a raise-time edit (prompt, secret demands) reuses the cached image, while any
// build input change yields a fresh image. Deterministic: sorted egress + JSON
// with sorted map keys.
func BuildDigest(sk StudioKit) string {
	egress := slices.Clone(sk.Egress)
	slices.Sort(egress)
	egress = slices.Compact(egress)
	payload := struct {
		Base      Base              `json:"base"`
		Egress    []string          `json:"egress"`
		BuildArgs map[string]string `json:"buildArgs"`
	}{Base: sk.Base, Egress: egress, BuildArgs: sk.BuildArgs}
	b, _ := json.Marshal(payload) // struct of JSON-safe fields; never errors
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Implement `egress.go`.**

```go
// internal/studio/egress.go
package studio

import (
	"slices"
	"strings"
)

// anthropicEgressRoots are the Anthropic-owned hosts a studio (brokered) cove
// must not reach directly — it talks to Anthropic only through the jam broker
// (COV-208). The studio egress CEILING structurally excludes these; the
// exclusion is surfaced (prepare log + `show`), never a silent policy rewrite.
var anthropicEgressRoots = []string{"anthropic.com", "claude.com", "claude.ai"}

// Ceiling returns the studio egress ceiling: the authored allow-list, sorted and
// deduped, with every Anthropic-owned entry removed; excluded is the sorted set
// that was removed (for surfacing). A leading "." (squid wildcard) is normalized
// away before matching.
func Ceiling(authored []string) (ceiling, excluded []string) {
	seen := map[string]bool{}
	for _, d := range authored {
		if seen[d] {
			continue
		}
		seen[d] = true
		if isAnthropicEgress(d) {
			excluded = append(excluded, d)
		} else {
			ceiling = append(ceiling, d)
		}
	}
	slices.Sort(ceiling)
	slices.Sort(excluded)
	return ceiling, excluded
}

func isAnthropicEgress(domain string) bool {
	h := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	for _, root := range anthropicEgressRoots {
		if h == root || strings.HasSuffix(h, "."+root) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Implement `default.go`.**

```go
// internal/studio/default.go
package studio

// DefaultStudioKitID is the registry id the built-in default studio kit is
// seeded under. role.Kit == "" raises this kit.
const DefaultStudioKitID = "default"

// DefaultStudioKit is the built-in default a brokered cove runs when its role
// names no kit: the blessed base (empty Base → blessed default), a minimal
// Anthropic-free egress ceiling, and a generic orienting prompt. It is seeded
// into the registry at serve start (see jam.EnsureDefaultStudioKit) so it is
// inspectable and versioned like any other kit.
func DefaultStudioKit() StudioKit {
	return StudioKit{
		Kind:   Kind,
		Name:   DefaultStudioKitID,
		Egress: []string{"github.com", "pkg.go.dev"},
		Prompt: "You are a studio agent running in a brokered at-cove sandbox. " +
			"Follow the task you are given; reach external services only through the approved allow-list.",
	}
}
```

- [ ] **Step 6: Run the tests to verify they pass.**

Run: `go test ./internal/studio/ -v`
Expected: PASS (all studio tests so far).

- [ ] **Step 7: Commit.**

```bash
git add internal/studio/digest.go internal/studio/egress.go internal/studio/default.go internal/studio/digest_test.go internal/studio/egress_test.go
git commit -m "feat(studio): build-digest, Anthropic-excluding ceiling, built-in default kit"
```

---

## Task 3: Raise-time prompt composer

**Files:**
- Create: `internal/studio/prompt.go`
- Test: `internal/studio/prompt_test.go`

**Interfaces:**
- Produces:
  - `const JamBoilerplate string`
  - `type PromptLayers struct { Kit, Project, Role, Launch string }`
  - `func ComposePrompt(l PromptLayers) string` — joins `JamBoilerplate` then the non-empty layers, in fixed order, separated by a blank line.

- [ ] **Step 1: Write the failing test.**

```go
// internal/studio/prompt_test.go
package studio

import (
	"strings"
	"testing"
)

func TestComposePromptOrdersAndOmitsEmpty(t *testing.T) {
	got := ComposePrompt(PromptLayers{Kit: "KIT", Launch: "LAUNCH"})
	if !strings.HasPrefix(got, JamBoilerplate) {
		t.Fatal("boilerplate must come first")
	}
	ik, il := strings.Index(got, "KIT"), strings.Index(got, "LAUNCH")
	if ik == -1 || il == -1 || ik > il {
		t.Fatalf("Kit must precede Launch; got %q", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("empty layers (Project/Role) must be omitted, no blank runs; got %q", got)
	}
}

func TestComposePromptAllLayers(t *testing.T) {
	got := ComposePrompt(PromptLayers{Kit: "K", Project: "P", Role: "R", Launch: "L"})
	order := []string{JamBoilerplate, "K", "P", "R", "L"}
	last := -1
	for _, s := range order {
		i := strings.Index(got, s)
		if i <= last {
			t.Fatalf("layer %q out of order in %q", s, got)
		}
		last = i
	}
}
```

- [ ] **Step 2: Run to verify it fails.**

Run: `go test ./internal/studio/ -run ComposePrompt -v`
Expected: FAIL — not defined.

- [ ] **Step 3: Implement `prompt.go`.**

```go
// internal/studio/prompt.go
package studio

import "strings"

// JamBoilerplate is the first, always-present prompt layer for a studio session.
const JamBoilerplate = "You are operating inside an at-cove hardened sandbox: an isolated filesystem " +
	"and an allow-listed network. Work within it; reach external services only through approved egress."

// PromptLayers are the raise-time layers composed into a studio session's prompt,
// each from its own source. Kit is the StudioKit's orienting prompt; Project and
// Role come from their configs (may be empty until those carry a prompt field);
// Launch is the per-raise workload prompt (RaiseSpec.Prompt).
type PromptLayers struct {
	Kit     string
	Project string
	Role    string
	Launch  string
}

// ComposePrompt joins JamBoilerplate and the non-empty layers, in fixed order,
// separated by a blank line. Assembled at RAISE (never baked into the image), so
// it is outside the build-digest.
func ComposePrompt(l PromptLayers) string {
	parts := []string{JamBoilerplate}
	for _, layer := range []string{l.Kit, l.Project, l.Role, l.Launch} {
		if strings.TrimSpace(layer) != "" {
			parts = append(parts, layer)
		}
	}
	return strings.Join(parts, "\n\n")
}
```

- [ ] **Step 4: Run to verify it passes.**

Run: `go test ./internal/studio/ -run ComposePrompt -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/studio/prompt.go internal/studio/prompt_test.go
git commit -m "feat(studio): raise-time layered prompt composer"
```

---

## Task 4: jam registry — studio kit storage, refs, and default seed; retire the derived kit

**Files:**
- Modify: `internal/jam/kit.go` (change `KitDefinition`)
- Rename+rewrite: `internal/jam/managedkit.go` → `internal/jam/studiokit.go`
- Test: rewrite `internal/jam/managedkit_test.go` → `internal/jam/studiokit_test.go`

**Interfaces:**
- Consumes: `Store` (`PushKit`/`GetKit`/`KitConfig`), `internal/studio` (Task 1–2), `KitRef`.
- Produces:
  - `KitDefinition` becomes `type KitDefinition struct { Ref KitRef; Kit studio.StudioKit }`.
  - `func EnsureStudioKit(store Store, sk studio.StudioKit) (KitRef, error)` — idempotent register under `sk.Name`; `Version` from the registry, `Digest = studio.BuildDigest(sk)`.
  - `func StudioKitRef(store Store, name string) (KitRef, error)` — resolve a registered studio kit's current version into a ref; fail closed if absent / not a studio kit / not tag-safe.
  - `func EnsureDefaultStudioKit(store Store) (KitRef, error)` — `EnsureStudioKit(store, studio.DefaultStudioKit())`.
  - `func ResolveKitDefinition(store Store, ref KitRef) (KitDefinition, bool, error)` — parse the registry's studio JSON into a `KitDefinition`.
- Removes: `ManagedKit`, `EnsureManagedKit`, `EnsureManagedKitFor`, `ensureManagedVariant`, `ManagedVariantID`, `ManagedKitID`, `tagSafeKitName`, `stripAnthropicEgress`, `isAnthropicEgress`, `anthropicEgressRoots`, `marshalKitConfig`.

- [ ] **Step 1: Change `KitDefinition` in `internal/jam/kit.go`.**

Replace (`internal/jam/kit.go`, current):
```go
type KitDefinition struct {
	Ref    KitRef
	Config kit.Config
}
```
with:
```go
type KitDefinition struct {
	Ref KitRef
	Kit studio.StudioKit
}
```
Update the import block of `kit.go`: drop `"github.com/aethons-tools/cove/internal/kit"` if now unused, add `"github.com/aethons-tools/cove/internal/studio"`.

- [ ] **Step 2: Write the failing test `internal/jam/studiokit_test.go`.**

```go
package jam

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/studio"
)

func newKitTestStore(t *testing.T) Store {
	t.Helper()
	st, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestEnsureStudioKitIdempotentThenBumps(t *testing.T) {
	st := newKitTestStore(t)
	sk := studio.StudioKit{Kind: studio.Kind, Name: "web", Egress: []string{"github.com"}}
	ref, err := EnsureStudioKit(st, sk)
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != "web" || ref.Version == 0 || ref.Digest != studio.BuildDigest(sk) {
		t.Fatalf("bad ref: %+v", ref)
	}
	ref2, _ := EnsureStudioKit(st, sk)
	if ref2.Version != ref.Version {
		t.Fatalf("unchanged kit must reuse version: %d vs %d", ref2.Version, ref.Version)
	}
	// A PROMPT-only edit bumps the registry version but keeps the build-digest.
	sk.Prompt = "edited"
	ref3, _ := EnsureStudioKit(st, sk)
	if ref3.Version == ref.Version {
		t.Fatal("a definition change must bump the registry version")
	}
	if ref3.Digest != ref.Digest {
		t.Fatal("a prompt-only edit must NOT change the build-digest")
	}
}

func TestStudioKitRefFailsClosed(t *testing.T) {
	st := newKitTestStore(t)
	if _, err := StudioKitRef(st, "missing"); err == nil {
		t.Fatal("want fail-closed on an absent kit")
	}
}

func TestResolveKitDefinitionParsesStudio(t *testing.T) {
	st := newKitTestStore(t)
	sk := studio.StudioKit{Kind: studio.Kind, Name: "web", Egress: []string{"github.com", ".anthropic.com"}}
	ref, _ := EnsureStudioKit(st, sk)
	def, ok, err := ResolveKitDefinition(st, ref)
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if def.Kit.Name != "web" || !slices.Contains(def.Kit.Egress, ".anthropic.com") {
		t.Fatalf("definition carries the authored kit verbatim (ceiling is applied at assemble): %+v", def.Kit)
	}
}
```

- [ ] **Step 3: Run to verify it fails.**

Run: `go test ./internal/jam/ -run 'StudioKit|ResolveKitDefinition' -v`
Expected: FAIL — `EnsureStudioKit`/`StudioKitRef` not defined (and old `managedkit_test.go` will also fail to compile — that is expected; it is replaced in Step 5).

- [ ] **Step 4: Implement `internal/jam/studiokit.go`.**

```go
package jam

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/aethons-tools/cove/internal/studio"
)

// EnsureStudioKit records sk in the kit registry under sk.Name and returns the
// reference a raise carries: {ID: name, Version: registry version, Digest:
// studio.BuildDigest(sk)}. Idempotent — an unchanged definition reuses the
// current version; any change bumps a new monotonic one. The registry stores the
// WHOLE definition (canonical JSON, prompt included); the ref's Digest is the
// BUILD-digest (build-affecting fields only), so a prompt-only edit bumps the
// version yet keeps the image key stable.
func EnsureStudioKit(store Store, sk studio.StudioKit) (KitRef, error) {
	if err := sk.Validate(); err != nil {
		return KitRef{}, err
	}
	text, err := sk.ToJSON()
	if err != nil {
		return KitRef{}, fmt.Errorf("studio kit %q: marshal: %w", sk.Name, err)
	}
	digest := studio.BuildDigest(sk)
	if k, ok := store.GetKit(sk.Name); ok && k.Current != 0 {
		if cur, ok := store.KitConfig(sk.Name, k.Current); ok && cur == string(text) {
			return KitRef{ID: sk.Name, Version: k.Current, Digest: digest}, nil
		}
	}
	version, err := store.PushKit(sk.Name, string(text))
	if err != nil {
		return KitRef{}, fmt.Errorf("studio kit %q: push: %w", sk.Name, err)
	}
	return KitRef{ID: sk.Name, Version: version, Digest: digest}, nil
}

// StudioKitRef resolves a registered studio kit's current version into a raise
// reference. Fails closed (no silent fallback) when name is not tag-safe, is
// absent, or its stored config is not a valid studio kit.
func StudioKitRef(store Store, name string) (KitRef, error) {
	if !studio.TagSafeName(name) {
		return KitRef{}, fmt.Errorf("kit name %q is not tag-safe", name)
	}
	text, ok := store.KitConfig(name, 0) // 0 = current
	if !ok {
		return KitRef{}, fmt.Errorf("studio kit %q not in registry", name)
	}
	sk, err := studio.ParseStudioKit([]byte(text))
	if err != nil {
		return KitRef{}, fmt.Errorf("studio kit %q: %w", name, err)
	}
	k, _ := store.GetKit(name)
	return KitRef{ID: name, Version: k.Current, Digest: studio.BuildDigest(sk)}, nil
}

// EnsureDefaultStudioKit seeds the built-in default studio kit into the registry
// (idempotent) and returns its ref — what role.Kit == "" raises.
func EnsureDefaultStudioKit(store Store) (KitRef, error) {
	return EnsureStudioKit(store, studio.DefaultStudioKit())
}

// ResolveKitDefinition fetches the full studio-kit definition a KitRef names —
// the chunky payload the supervisor resolves on an ErrKitNotReady miss before
// PrepareKit. ok=false when the registry has no such (id, version).
func ResolveKitDefinition(store Store, ref KitRef) (KitDefinition, bool, error) {
	text, ok := store.KitConfig(ref.ID, ref.Version)
	if !ok {
		return KitDefinition{}, false, nil
	}
	sk, err := studio.ParseStudioKit([]byte(text))
	if err != nil {
		return KitDefinition{}, false, fmt.Errorf("resolve kit %s: %w", ref, err)
	}
	return KitDefinition{Ref: ref, Kit: sk}, true, nil
}

// contentDigest is retained for any caller needing a text hash (unused by the
// studio ref path, which hashes build-affecting fields via studio.BuildDigest).
func contentDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
```

> If `contentDigest` ends up unused after all edits, delete it rather than leave a dead function (the linter flags it).

- [ ] **Step 5: Delete the old derived-kit code and its tests.**

```bash
git rm internal/jam/managedkit.go internal/jam/managedkit_test.go
```
The new `studiokit.go`/`studiokit_test.go` (Steps 2/4) replace them. The Anthropic-strip helpers now live in `internal/studio/egress.go` (Task 2).

- [ ] **Step 6: Run to verify it passes.**

Run: `go test ./internal/jam/ -run 'StudioKit|ResolveKitDefinition' -v`
Expected: PASS. (The package will still fail to BUILD because `supervisor.go` and `launcher` reference removed symbols — fixed in Tasks 6–7. Run the targeted studio tests here; full `./internal/jam/...` goes green after Task 7.)

- [ ] **Step 7: Commit.**

```bash
git add internal/jam/kit.go internal/jam/studiokit.go internal/jam/studiokit_test.go
git commit -m "feat(jam): studio-kit registry (EnsureStudioKit/StudioKitRef/default seed), retire derived kit"
```

---

## Task 5: `BuildKitImage` gains build-args

**Files:**
- Modify: `internal/backend/backend.go` (`KitImageBuilder`), `internal/backend/colima/colima.go`
- Test: `internal/backend/colima/colima_test.go` (or `baseimage_test.go`)

**Interfaces:**
- Produces: `BuildKitImage(buildDir, tag, base string, buildArgs map[string]string, noCache bool) (digest string, err error)` — a new `buildArgs` param before `noCache`, rendered as repeated `--build-arg k=v` (deterministic order) alongside the existing `BASE=` arg.

- [ ] **Step 1: Write the failing test.**

```go
// internal/backend/colima/colima_test.go  (new test)
func TestBuildKitImageInjectsBuildArgs(t *testing.T) {
	f := &runner.Fake{}
	c := New(f).(*Colima)
	if _, err := c.BuildKitImage(t.TempDir(), "cove-kit:deadbeef", "cove-base@sha256:base",
		map[string]string{"NODE_VERSION": "20"}, false); err != nil {
		t.Fatal(err)
	}
	build := findCall(f, "build") // existing helper in this test file locating the docker build argv
	if !contains(build, "--build-arg") || !contains(build, "NODE_VERSION=20") {
		t.Fatalf("build must inject the kit build-arg: %+v", f.Calls)
	}
	if !contains(build, "BASE=cove-base@sha256:base") {
		t.Fatalf("build must still inject the BASE arg: %+v", f.Calls)
	}
}
```

> Use the same call-locating/`contains` helpers already present in `colima_test.go`/`baseimage_test.go` (the dossier shows `contains(build, …)` and `f.Calls` usage). If the existing helper is named differently than `findCall`, use that name.

- [ ] **Step 2: Run to verify it fails.**

Run: `go test ./internal/backend/colima/ -run BuildKitImageInjectsBuildArgs -v`
Expected: FAIL — signature mismatch (too few args) / arg not present.

- [ ] **Step 3: Change the interface in `internal/backend/backend.go`.**

Replace the `BuildKitImage` line in `KitImageBuilder` (currently `BuildKitImage(buildDir, tag, base string, noCache bool) (digest string, err error)`) with:
```go
	// BuildKitImage builds the assembled context in buildDir into the tagged image,
	// FROM base (the Dockerfile's BASE arg; must be non-empty), injecting buildArgs
	// as additional --build-arg pairs. buildArgs values must never carry secrets.
	BuildKitImage(buildDir, tag, base string, buildArgs map[string]string, noCache bool) (digest string, err error)
```

- [ ] **Step 4: Thread `buildArgs` through colima (`internal/backend/colima/colima.go`).**

Update `BuildKitImage` (currently `func (c *Colima) BuildKitImage(buildDir, tag, base string, noCache bool)`) to accept `buildArgs map[string]string` and pass it into the shared build. In `buildFromBase` (where `buildArgs := []string{"build", "--progress=plain"}` … `append(buildArgs, "--build-arg", "BASE="+resolvedBase, ...)`), add the kit args in sorted order before `-t`:
```go
	// kit build-args (sorted for a deterministic argv), after BASE, before -t.
	keys := make([]string, 0, len(kitArgs))
	for k := range kitArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		buildArgs = append(buildArgs, "--build-arg", k+"="+kitArgs[k])
	}
```
Plumb `kitArgs map[string]string` from `BuildKitImage` into `buildFromBase` (add the param; `Install`'s internal caller passes `nil`). Ensure `sort` is imported.

- [ ] **Step 5: Update all other `BuildKitImage` call sites and fakes.**

Call sites to update to the new signature (`grep -rn "BuildKitImage" --include=*.go`):
- `internal/jam/launcher/prepare.go` — pass `def.Kit.BuildArgs` (done in Task 6).
- Test fakes: `internal/jam/launcher/launcher_test.go` `fakeOps.BuildKitImage` → add `buildArgs map[string]string` param (record it as `f.builtArgs = buildArgs`).

- [ ] **Step 6: Run to verify it passes.**

Run: `go test ./internal/backend/... -v`
Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add internal/backend/backend.go internal/backend/colima/colima.go internal/backend/colima/colima_test.go
git commit -m "feat(backend): BuildKitImage injects deterministic kit build-args"
```

---

## Task 6: Launcher prepare/raise on a StudioKit

**Files:**
- Modify: `internal/jam/launcher/launcher.go` (Config, defaultAssemble, Raise), `internal/jam/launcher/prepare.go`, `internal/jam/launcher/inventory.go`
- Add: `internal/jam/launcher/studioassemble.go` (studio egress context)
- Test: `internal/jam/launcher/prepare_test.go`, `internal/jam/launcher/launcher_test.go`

**Interfaces:**
- Consumes: `KitDefinition{Ref, Kit}` (Task 4), `studio.Ceiling`, `assemble.AssembleContext`, `Ops.ResolveKitBase`, `Ops.BuildKitImage(…, buildArgs, …)` (Task 5).
- Produces:
  - `var ErrDockerfileContextUnsupported = errors.New("studio kit: Dockerfile-context build not yet supported")`
  - `imageTag(r KitRef) string` → `"cove-kit:" + r.Digest`.

- [ ] **Step 1: Write the failing prepare tests.**

```go
// internal/jam/launcher/prepare_test.go  (new tests; adapt newPrepareLauncher from the dossier)
func TestPrepareStudioKitBuildsByDigestWithCeiling(t *testing.T) {
	ops := &fakeOps{resolvedBase: "blessed@sha256:def"}
	inv := &fakeInv{}
	var gotEgress assemble.Egress
	asm := func(def KitDefinition, buildDir string) error { return nil } // assemble seam; ceiling checked below via studioEgress
	l := newPrepareLauncher(ops, inv, asm)

	sk := studio.StudioKit{Kind: studio.Kind, Name: "web",
		Egress: []string{"github.com", ".anthropic.com"}, BuildArgs: map[string]string{"X": "1"}}
	ref := KitRef{ID: "web", Version: 1, Digest: studio.BuildDigest(sk)}
	st, err := l.PrepareKit(context.Background(), KitDefinition{Ref: ref, Kit: sk})
	if err != nil || st.State != KitReady {
		t.Fatalf("PrepareKit = %+v, %v", st, err)
	}
	if ops.builtTag != "cove-kit:"+ref.Digest {
		t.Fatalf("image tag must be the build-digest: %q", ops.builtTag)
	}
	if ops.resolvedFrom != "" { // Base.Ref == "" → blessed default
		t.Fatalf("ResolveKitBase called with %q, want empty (blessed default)", ops.resolvedFrom)
	}
	if ops.builtBase != "blessed@sha256:def" {
		t.Fatalf("build base = %q, want the resolved blessed base", ops.builtBase)
	}
	if ops.builtArgs["X"] != "1" {
		t.Fatalf("build-args not threaded: %+v", ops.builtArgs)
	}
	_ = gotEgress
}

func TestPrepareStudioKitDockerfileDeferred(t *testing.T) {
	l := newPrepareLauncher(&fakeOps{}, &fakeInv{}, func(KitDefinition, string) error { return nil })
	sk := studio.StudioKit{Kind: studio.Kind, Name: "web", Base: studio.Base{Dockerfile: "FROM x"}}
	ref := KitRef{ID: "web", Version: 1, Digest: studio.BuildDigest(sk)}
	_, err := l.PrepareKit(context.Background(), KitDefinition{Ref: ref, Kit: sk})
	if !errors.Is(err, ErrDockerfileContextUnsupported) {
		t.Fatalf("want ErrDockerfileContextUnsupported, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify they fail.**

Run: `go test ./internal/jam/launcher/ -run PrepareStudioKit -v`
Expected: FAIL — compile errors (old `def.Config`, `imageTag` by id-vN, `BuildKitImage` arity).

- [ ] **Step 3: `imageTag` by digest (`inventory.go`).**

Replace `func imageTag(r KitRef) string { return fmt.Sprintf("cove-kit:%s-v%d", r.ID, r.Version) }` with:
```go
// imageTag names the built image by the studio kit's BUILD-digest, so kits with
// identical build inputs share an image and a prompt-only edit reuses it.
func imageTag(r KitRef) string { return "cove-kit:" + r.Digest }
```

- [ ] **Step 4: Studio assemble context (`studioassemble.go`).**

```go
// internal/jam/launcher/studioassemble.go
package launcher

import (
	"github.com/aethons-tools/cove/internal/assemble"
	"github.com/aethons-tools/cove/internal/studio"
)

// studioEgress builds the assemble.Egress for a studio kit: Policy AND ceiling
// are the authored allow-list with Anthropic excluded (COV-208); Infra is the
// jam host so the cove can reach the broker. excluded is returned for surfacing.
func studioEgress(sk studio.StudioKit, jamHost string) (eg assemble.Egress, excluded []string) {
	ceiling, excl := studio.Ceiling(sk.Egress)
	infra := []string(nil)
	if jamHost != "" {
		infra = []string{jamHost}
	}
	return assemble.Egress{Policy: ceiling, Infra: infra}, excl
}
```

- [ ] **Step 5: Rewrite `defaultAssemble` and `PrepareKit`.**

In `launcher.go`, replace `defaultAssemble`:
```go
func (l *Launcher) defaultAssemble(def KitDefinition, buildDir string) error {
	eg, _ := studioEgress(def.Kit, l.cfg.JamHost)
	return assemble.AssembleContext(buildDir, l.cfg.PublicKey, eg, "")
}
```
In `prepare.go`, rewrite `PrepareKit`'s body after the inventory check:
```go
	if def.Kit.Base.Dockerfile != "" {
		return KitStatus{State: KitPreparing, Err: ErrDockerfileContextUnsupported.Error()}, fmt.Errorf("prepare kit %s: %w", ref, ErrDockerfileContextUnsupported)
	}
	buildDir := filepath.Join(l.cfg.BuildRoot, ref.Digest)
	if err := l.cfg.assemble(def, buildDir); err != nil {
		return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: assemble: %w", ref, err)
	}
	base, err := l.cfg.Ops.ResolveKitBase(def.Kit.Base.Ref) // gate ON; "" → blessed default
	if err != nil {
		return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: resolve base: %w", ref, err)
	}
	if _, err := l.cfg.Ops.BuildKitImage(buildDir, imageTag(ref), base, def.Kit.BuildArgs, false); err != nil {
		return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: build: %w", ref, err)
	}
	_, excluded := studioEgress(def.Kit, l.cfg.JamHost)
	l.cfg.Log.Info("prepared studio kit", "ref", ref.String(), "tag", imageTag(ref), "ceiling_excludes", excluded)
	return KitStatus{State: KitReady}, nil
```
Add `ErrDockerfileContextUnsupported` (and ensure `errors` imported) at the top of `prepare.go`:
```go
var ErrDockerfileContextUnsupported = errors.New("studio kit: Dockerfile-context build not yet supported")
```
Remove the old `ref.ID != jam.ManagedKitID` base branch and the `cfg.BaseImage` read.

- [ ] **Step 6: Drop dead Config fields (`launcher.go`).**

Remove `Image`, `ImageDigest`, `BaseImage` from `Config`. In `Raise`, delete the `image, digest := l.cfg.Image, l.cfg.ImageDigest` legacy path and the `if spec.Kit.ID != ""` guard — a studio raise always has a kit. New head of `Raise`:
```go
	name := naming.CoveContainer(spec.ActorID)
	if spec.Kit.ID == "" {
		return "", fmt.Errorf("raise %s: no kit (studio raises require a kit)", name)
	}
	ok, err := l.inv.Has(spec.Kit)
	if err != nil {
		return "", fmt.Errorf("raise %s: inventory: %w", name, err)
	}
	if !ok {
		return "", fmt.Errorf("raise %s: %w", spec.Kit, ErrKitNotReady)
	}
	image, digest := imageTag(spec.Kit), ""
	if _, err := l.cfg.Ops.RunEphemeral(image, digest, name, Label, l.cfg.DNS, []string{l.cfg.JamHost}, l.cfg.Docker); err != nil {
		return "", fmt.Errorf("raise %s: run: %w", name, err)
	}
```
(The `connect.LaunchCoveMaster(... Prompt: spec.Prompt ...)` call is unchanged — the composed prompt arrives via `spec.Prompt`, Task 7.)

- [ ] **Step 7: Update launcher test fakes/helpers.**

- `fakeOps` (`launcher_test.go`): add `builtArgs map[string]string`; record in `BuildKitImage` (Task 5 Step 5).
- `newLauncher`/`newPrepareLauncher`: drop `Image`/`ImageDigest`/`BaseImage` fields (removed from Config). Raise tests that asserted `ops.runImage == "cove-kit:managed-v3"` now assert `ops.runImage == "cove-kit:"+ref.Digest`; set `ref := jam.KitRef{ID:"web", Version:1, Digest: studio.BuildDigest(sk)}` and `inv.set(ref, true)`.

- [ ] **Step 8: Run to verify.**

Run: `go test ./internal/jam/launcher/ -v`
Expected: PASS.

- [ ] **Step 9: Commit.**

```bash
git add internal/jam/launcher/
git commit -m "feat(launcher): prepare/raise a StudioKit (digest-tagged image, Anthropic-excluded ceiling, build-args, Dockerfile deferred)"
```

---

## Task 7: Supervisor — resolve studio kits and compose the prompt at Raise

**Files:**
- Modify: `internal/jam/supervisor.go`
- Test: `internal/jam/supervisor_test.go`

**Interfaces:**
- Consumes: `StudioKitRef` (Task 4), `ResolveKitDefinition`, `studio.ComposePrompt`/`PromptLayers`.
- Produces:
  - Field rename `managedKit`→`defaultStudioKit`; method `SetManagedKit`→`SetDefaultStudioKit(ref KitRef)`.
  - `kitRefFor(role Role) (KitRef, bool, error)` resolves via `StudioKitRef` / the default ref.
  - Prompt composition inside `Raise`: `spec.Prompt = studio.ComposePrompt(...)` before `launcher.Raise`.

- [ ] **Step 1: Write the failing tests.**

```go
// internal/jam/supervisor_test.go  (update the harness + add tests)
func TestRaiseUsesRoleStudioKit(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	sk := studio.StudioKit{Kind: studio.Kind, Name: "web", Egress: []string{"github.com"}}
	if _, err := EnsureStudioKit(store, sk); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "dev", Kit: "web", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	if fl.gotSpec.Kit.ID != "web" || fl.gotSpec.Kit.Digest != studio.BuildDigest(sk) {
		t.Fatalf("raise spec.Kit = %+v, want the studio ref for web", fl.gotSpec.Kit)
	}
}

func TestRaiseComposesPrompt(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	sk := studio.StudioKit{Kind: studio.Kind, Name: "web", Prompt: "KITLAYER"}
	ref, _ := EnsureStudioKit(store, sk)
	sup.SetDefaultStudioKit(ref)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Prompt: "LAUNCHLAYER"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fl.gotSpec.Prompt, studio.JamBoilerplate) ||
		!strings.Contains(fl.gotSpec.Prompt, "KITLAYER") || !strings.Contains(fl.gotSpec.Prompt, "LAUNCHLAYER") {
		t.Fatalf("composed prompt missing layers: %q", fl.gotSpec.Prompt)
	}
}
```

Update `supTestKit` (dossier J21) — it PushRole with `Role{Name:"guest", ...}`; keep it. Add imports `"strings"`, `"github.com/aethons-tools/cove/internal/studio"`. Replace any remaining `EnsureManagedKit`/`SetManagedKit` references in existing supervisor tests with the studio equivalents (or delete the now-obsolete managed-kit tests `TestSupervisorPreparesKitOnNotReadyThenRaises`/`TestRaiseUsesRoleKit`, replacing them with the studio versions above).

- [ ] **Step 2: Run to verify it fails.**

Run: `go test ./internal/jam/ -run 'RaiseUsesRoleStudioKit|RaiseComposesPrompt' -v`
Expected: FAIL — `SetDefaultStudioKit` not defined; prompt not composed.

- [ ] **Step 3: Rename the field/method + rewrite `kitRefFor`.**

In `supervisor.go`: rename the struct field `managedKit *KitRef` → `defaultStudioKit *KitRef`; replace `func (s *Supervisor) SetManagedKit(ref KitRef) { s.managedKit = &ref }` with:
```go
func (s *Supervisor) SetDefaultStudioKit(ref KitRef) { s.defaultStudioKit = &ref }
```
Rewrite `kitRefFor`:
```go
func (s *Supervisor) kitRefFor(role Role) (KitRef, bool, error) {
	if role.Kit != "" {
		ref, err := StudioKitRef(s.store, role.Kit)
		if err != nil {
			return KitRef{}, false, err
		}
		return ref, true, nil
	}
	if s.defaultStudioKit != nil {
		return *s.defaultStudioKit, true, nil
	}
	return KitRef{}, false, nil
}
```

- [ ] **Step 4: Compose the prompt in `Raise`.**

In `Raise`, after the `if have { spec.Kit = ref }` block (kit stamped) and before `creds := LaunchCreds{...}`, add:
```go
	// Compose the session prompt from ordered layers (Jam boilerplate → Kit info
	// → Project → Role → launch). Resolve the kit's prompt from the registry; a
	// cheap local read, needed every raise (unlike the launcher-side lazy prepare).
	if spec.Kit.ID != "" {
		layers := studio.PromptLayers{Launch: spec.Prompt}
		if def, ok, derr := ResolveKitDefinition(s.store, spec.Kit); derr == nil && ok {
			layers.Kit = def.Kit.Prompt
		}
		// Project/Role prompt layers plug in here when those configs carry one.
		spec.Prompt = studio.ComposePrompt(layers)
	}
```
Add `"github.com/aethons-tools/cove/internal/studio"` to `supervisor.go`'s imports.

- [ ] **Step 5: Run to verify — full jam package now builds and passes.**

Run: `go test ./internal/jam/... -v`
Expected: PASS (studio kit + supervisor + launcher all green together).

- [ ] **Step 6: Commit.**

```bash
git add internal/jam/supervisor.go internal/jam/supervisor_test.go
git commit -m "feat(jam): resolve role→studio-kit and compose the session prompt at Raise"
```

---

## Task 8: cmd/at-jam wiring — remove install-manifest, seed the default studio kit

**Files:**
- Modify: `cmd/at-jam/config.go` (`launcherConfig`, `validateLauncher`), `cmd/at-jam/main.go` (launcher wiring)
- Test: `cmd/at-jam/config_test.go`

**Interfaces:**
- Consumes: `jam.EnsureDefaultStudioKit`, `launcher.New` (Config without `Image`/`ImageDigest`/`BaseImage`), `jam.Supervisor.SetDefaultStudioKit`.

- [ ] **Step 1: Write/adjust the failing config test.**

In `config_test.go`, the existing launcher-validation test (dossier: `config_test.go:235-304`) asserts `install-manifest is required`. Replace that expectation: a launcher block with only `runtime-addr` + `jam-host` is now VALID.
```go
func TestValidateLauncherNoLongerRequiresInstallManifest(t *testing.T) {
	c := serveConfig{}
	c.Runtime.Launcher = &launcherConfig{RuntimeAddr: "jam:443", JamHost: "jam"}
	if err := c.validateLauncher(); err != nil {
		t.Fatalf("launcher without install-manifest must validate: %v", err)
	}
}

func TestServeConfigRejectsInstallManifestKey(t *testing.T) {
	// install-manifest is retired (no shim); a config still setting it is flagged.
	keys := unknownServeKeys([]byte("runtime:\n  launcher:\n    install-manifest: /x\n    runtime-addr: jam:443\n    jam-host: jam\n"))
	// install-manifest is now unknown under runtime.launcher; the strict decode in
	// parseServeConfig will error. Assert the parse error instead:
	if _, err := parseServeConfig([]byte("runtime:\n  launcher:\n    install-manifest: /x\n")); err == nil {
		t.Fatal("want a strict-decode error on the retired install-manifest key")
	}
	_ = keys
}
```
> Note: `launcherConfig` is decoded by the same YAML decoder as `serveConfig`. If that decoder is NOT strict for nested blocks, drop `TestServeConfigRejectsInstallManifestKey` and rely on the field's removal (an unknown key is silently ignored) — verify with `parseServeConfig` behavior before asserting. Keep `TestValidateLauncherNoLongerRequiresInstallManifest` regardless.

- [ ] **Step 2: Run to verify it fails.**

Run: `go test ./cmd/at-jam/ -run 'Launcher|InstallManifest' -v`
Expected: FAIL — `validateLauncher` still requires install-manifest; field still present.

- [ ] **Step 3: Remove the field and its check.**

In `config.go`: delete `InstallManifest string \`yaml:"install-manifest"\`` from `launcherConfig`; delete the `if lc.InstallManifest == "" { return … }` block from `validateLauncher`.

- [ ] **Step 4: Rewire `main.go`.**

In the `if lc := cfg.Runtime.Launcher; lc != nil { … }` block (dossier I18):
- Delete the `install.Manifest` read/unmarshal, the `m.BaseRef == ""` check, and the `install` import if now unused.
- `launcher.New(...)` drops `Image: m.Image, ImageDigest: m.ImageDigest` and `BaseImage: m.BaseRef` (those Config fields were removed in Task 6).
- Replace `managedRef, err = jam.EnsureManagedKit(st, m.RunConfig)` with:
```go
	defaultRef, err := jam.EnsureDefaultStudioKit(st)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam: seed default studio kit:", err)
		return 1
	}
	haveDefaultKit = true
```
- Rename locals `managedRef`→`defaultRef`, `haveManagedKit`→`haveDefaultKit`; after `NewSupervisor`, `if haveDefaultKit { sup.SetDefaultStudioKit(defaultRef) }`.
- The identity pub key read (`pub, err := os.ReadFile(lc.IdentityFile + ".pub")`) stays; it is passed as `PublicKey: pub`.
- Update the log line: `log.Info("Jam launcher: colima", "runtime-addr", lc.RuntimeAddr, "default-kit", defaultRef.String())`.

- [ ] **Step 5: Run build + config tests.**

Run: `just build && go test ./cmd/at-jam/ -v`
Expected: build OK; tests PASS.

- [ ] **Step 6: Commit.**

```bash
git add cmd/at-jam/config.go cmd/at-jam/main.go cmd/at-jam/config_test.go
git commit -m "feat(at-jam): remove runtime.launcher.install-manifest; seed the default studio kit"
```

---

## Task 9: `at-jam kit push` validates studio kits; `show` surfaces the ceiling + exclusions

**Files:**
- Modify: `cmd/at-jam/main.go` (the `kit push` and `kit show` / `studio show` handlers)
- Test: `cmd/at-jam/main_test.go`

**Interfaces:**
- Consumes: `studio.ParseStudioKit`, `studio.Ceiling`.

- [ ] **Step 1: Locate the handlers.**

Run: `grep -n '"push"\|"show"\|kitPush\|kitShow\|studioShow' cmd/at-jam/main.go` to find the `kit push` / `kit show` command bodies (the role→kit slice added `kit push`).

- [ ] **Step 2: Write the failing test.**

```go
// cmd/at-jam/main_test.go
func TestKitPushValidatesStudioKit(t *testing.T) {
	// A studio-kit YAML is accepted; a bad one (unknown field) is rejected.
	good := "kind: studio\nname: web\negress:\n  - github.com\n"
	bad := "kind: studio\nname: web\nworkers: {}\n"
	if err := validatePushedKit([]byte(good)); err != nil {
		t.Fatalf("good studio kit rejected: %v", err)
	}
	if err := validatePushedKit([]byte(bad)); err == nil {
		t.Fatal("bad studio kit must be rejected at push")
	}
}

func TestStudioShowSurfacesExcludedRoots(t *testing.T) {
	ceiling, excluded := studioShowEgress([]string{"github.com", "claude.ai"})
	if len(ceiling) != 1 || ceiling[0] != "github.com" {
		t.Fatalf("ceiling=%v", ceiling)
	}
	if len(excluded) != 1 || excluded[0] != "claude.ai" {
		t.Fatalf("excluded=%v", excluded)
	}
}
```

- [ ] **Step 3: Implement the helpers + wire them.**

Add to `main.go`:
```go
// validatePushedKit validates a studio-kit config before it is stored, so a
// malformed kit is caught at push, not at raise.
func validatePushedKit(data []byte) error {
	_, err := studio.ParseStudioKit(data)
	return err
}

// studioShowEgress returns the effective ceiling and the excluded (Anthropic)
// roots for display, so an operator sees exactly what a studio kit can reach.
func studioShowEgress(authored []string) (ceiling, excluded []string) {
	return studio.Ceiling(authored)
}
```
Wire `validatePushedKit` into the `kit push` handler (call it on the read file bytes before `store.PushKit`). In the `kit show` / `studio show` handler, after loading the kit config, parse it and print two lines:
```
egress ceiling: <sorted ceiling, comma-separated>
excluded (COV-208): <sorted excluded, or "none">
```

- [ ] **Step 4: Run to verify.**

Run: `go test ./cmd/at-jam/ -run 'KitPush|StudioShow' -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add cmd/at-jam/main.go cmd/at-jam/main_test.go
git commit -m "feat(at-jam): validate studio kits on push; surface ceiling + COV-208 exclusions in show"
```

---

## Task 10: Documentation

**Files:**
- Modify: `docs/usage/jam/serve.md`, `docs/usage/jam/kits.md`, `docs/usage/jam/coves.md`, `docs/OVERVIEW.md` (kit-format section if it mentions the managed kit)
- Create: a migration note (append to `docs/usage/jam/renamed-from-harbor.md` or a new `docs/usage/jam/studio-kit-migration.md`) for the removed `install-manifest`.

**Interfaces:** docs only. Use the **docs-author** skill; route each fact to the doc that owns it; update the docs index if one lists these.

- [ ] **Step 1: `serve.md` — remove `runtime.launcher.install-manifest`.**

Delete the `install-manifest` row/paragraph (`serve.md:58,91,184`). State that `runtime.launcher` now needs only `runtime-addr` + `jam-host` (+ optional identity/DNS/docker), and that the managed cove's kit comes from the studio-kit registry (default seeded automatically).

- [ ] **Step 2: `kits.md` — document the StudioKit.**

Add the studio-kit schema (the five fields), the `kind: studio` discriminator, `at-jam kit push` of a studio kit, `kit show`/`studio show` output (ceiling + excluded roots), and role→kit (a role names a studio kit directly; `role.Kit==""` → the built-in `default`). Note the Dockerfile-context base is accepted but its build is deferred.

- [ ] **Step 3: `coves.md` / `OVERVIEW.md` — reconcile the managed-kit description.**

Replace any "managed kit = interactive base with Anthropic stripped" wording with "brokered coves run a **StudioKit**; its egress ceiling excludes Anthropic (COV-208)". Remove references to `EnsureManagedKit`/`install-manifest` in prose.

- [ ] **Step 4: Migration note.**

One short section: `runtime.launcher.install-manifest` is **removed, no shim**; a `jam.yml` still setting it must drop the key; the managed cove's base + config now come from the studio-kit registry (the `default` kit is seeded automatically; author others with `at-jam kit push`).

- [ ] **Step 5: Docs audit.**

Run the **docs-audit** skill (deterministic checker: orphans, dangling links, oversize, duplication, frontmatter). Fix anything it flags.

- [ ] **Step 6: Commit.**

```bash
git add docs/
git commit -m "docs(jam): StudioKit kit format, role→kit, and install-manifest removal"
```

---

## Task 11: Full verification

- [ ] **Step 1: Hermetic tests + build + lint.**

Run:
```bash
just test
just build
just lint
```
Expected: all green. If `just lint` flags the retained `contentDigest` (Task 4) as unused, delete it.

- [ ] **Step 2: Grep for stragglers.**

Run:
```bash
grep -rn "EnsureManagedKit\|ManagedKit\|install-manifest\|InstallManifest\|managedRef\|SetManagedKit\|\.Config\b.*KitDefinition" --include=*.go cmd/ internal/
```
Expected: no live references (test/doc mentions only where intentionally describing the removal). Any surviving `KitDefinition.Config` usage is a missed call site — fix it to `.Kit`.

- [ ] **Step 3: Confirm the decoupling end-to-end (hermetic).**

Run: `go test ./internal/... -run 'BuildDigest|PrepareStudioKit|RaiseComposesPrompt' -v`
Expected: PASS — a prompt-only edit keeps the build-digest (Task 2), the image is digest-tagged (Task 6), the prompt is composed at Raise (Task 7).

- [ ] **Step 4: Commit any lint fixes; open the PR.**

```bash
git add -A
git commit -m "chore(studio): lint + verification fixups"
```
Open a PR against `main` titled `feat(studio): standalone StudioKit; retire derived managed kit + install-manifest`, linking the spec and COV-211.

---

## Self-Review

**Spec coverage:**
- StudioKit type + 5 fields (build-affecting vs raise-time split) → Task 1, 2, 3.
- Base 3-way + gate ON + Dockerfile deferred → Task 1 (schema), Task 6 (`ResolveKitBase` gate on, `ErrDockerfileContextUnsupported`), `default.go` (empty→blessed).
- Egress ceiling-only + transparency → Task 2 (`Ceiling`+excluded), Task 6 (prepare log), Task 9 (`show`).
- Secrets demand model reuse → Task 1 (`kit.SecretConfig`, no resolver); injected at Raise via existing `CoveMasterOptions` (unchanged) — note: secret *resolution/injection* for demanded names reuses the existing broker/host path; this plan does not add new resolver wiring (the demand list is declarative), consistent with the spec.
- Build-args (map, content-keyed, no secrets) → Task 1 (validation), Task 2 (in BuildDigest), Task 5 (threaded to `docker build`).
- Prompt composer (Raise-time layers, outside image key) → Task 3, Task 7.
- Storage/versioning + build-digest decoupling → Task 4 (`EnsureStudioKit` version vs `BuildDigest`), Task 6 (digest tag).
- Retire derivation + install-manifest (no shim) + built-in default seeded → Task 4 (remove), Task 8 (wiring + seed).
- Testing (ceiling golden, digest stability, default seeded, role→kit fail-closed, collision rejected) → Tasks 2, 4, 6, 7, 9, 11.

**Placeholder scan:** none — every code step has concrete code; edit steps cite exact current anchors from the dossier.

**Type consistency:** `studio.StudioKit`/`studio.Base`/`studio.Kind`/`studio.BuildDigest`/`studio.Ceiling`/`studio.DefaultStudioKit`/`studio.DefaultStudioKitID`/`studio.ComposePrompt`/`studio.PromptLayers`/`studio.JamBoilerplate`; `jam.KitDefinition{Ref,Kit}`; `jam.EnsureStudioKit`/`StudioKitRef`/`EnsureDefaultStudioKit`/`ResolveKitDefinition`; `SetDefaultStudioKit`/`defaultStudioKit`; `imageTag(r)→"cove-kit:"+r.Digest`; `BuildKitImage(buildDir,tag,base,buildArgs,noCache)`; `ErrDockerfileContextUnsupported` — used consistently across tasks.

**Open verification for the implementer (flagged inline):** (a) whether the nested `launcherConfig` YAML decode is strict (Task 8 Step 1); (b) the exact name of the `contains`/call-locating helper in the colima test file (Task 5 Step 1); (c) whether any caller beyond supervisor/launcher reads `KitDefinition.Config` (Task 11 Step 2 grep).
