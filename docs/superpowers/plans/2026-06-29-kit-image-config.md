# Kit `image:` Config Block — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:**
Add a declarative `image:` block to a kit's `config.yml` (`setup-script`, `paths`, `env`, `allowed-domains`)
that cove translates into the correct sealed image mechanisms, strictly additively.

**Architecture:**
`internal/kit` parses the new block into `ImageConfig`.
`internal/assemble.Assemble` gains the config and, after the winning hardening copy,
writes cove-owned consumables (a squid allow-list file, a `.cove/` setup manifest, and `.cove/paths`+`.cove/env`).
Two new shell helpers shipped in the sealed hardening layer consume those at docker-build time;
the Dockerfile calls them.
A build-time collision check rejects kit `image-files` that would be silently overwritten by hardening.

**Tech Stack:** Go 1.22 (stdlib only), `gopkg.in/yaml.v3` (already a dep), bash (helper scripts), docker build (consumes the assembled context).

## Global Constraints

- Go stdlib only (plus the existing `gopkg.in/yaml.v3`); no new dependencies.
- **Strictly additive:** kit declarations only extend the hardened baseline; never remove/override it.
- **Sealed hardening stays sealed:** the kit supplies data only; the Dockerfile/squid.conf are edited once here to add fixed extension points, never per-kit.
- `image-files/.cove/` is reserved for cove; a kit file there is an error.
- `/etc/environment` must keep a **single** `PATH=` line (pam_env is last-wins); kit paths are **appended**.
- cove **always** writes the kit consumables (`allowed_domains.kit.txt`, `.cove/setup-manifest`, `.cove/paths`, `.cove/env`) even when empty, so the helpers/squid never reference a missing file.
- Helper scripts honor env overrides (`COVE_SETUP_MANIFEST`, `COVE_DIR`, `COVE_ENV_FILE`) so they are hermetically testable; defaults are the production paths.

---

### Task 1: Build-time collision check

Reject a build when a kit `image-files/` path also exists in the sealed hardening tree (it would be silently overwritten). Independent of the `image:` block.

**Files:**
- Modify: `internal/assemble/assemble.go`
- Test: `internal/assemble/assemble_test.go`

**Interfaces:**
- Produces: `collisions(kitDir string) ([]string, error)` — sorted slash-separated relative paths present in both the kit overlay and `hardening/image-files`. `Assemble` calls it and errors if non-empty.

- [ ] **Step 1: Update the existing test that relied on silent override, and add a collision test**

In `internal/assemble/assemble_test.go`, the existing `TestAssembleLayersAndKey` writes `image-files/etc/nftables.conf = "PWNED"` and asserts hardening silently wins. That path now collides → Assemble must error. Remove the shadow lines from that test (delete the `mustWrite(... "etc/nftables.conf" ... "PWNED")` line and the later block asserting `nftables.conf != "PWNED"`), keeping the benign `note.txt` assertions. Then add:

```go
func TestAssembleRejectsCollision(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")
	// etc/nftables.conf is shipped by the hardening layer; shadowing it must fail.
	mustWrite(t, filepath.Join(kitDir, "image-files/etc/nftables.conf"), "PWNED")

	err := Assemble(kitDir, buildDir, []byte("k\n"))
	if err == nil {
		t.Fatal("expected a collision error, got nil")
	}
	if !strings.Contains(err.Error(), "etc/nftables.conf") {
		t.Fatalf("error should name the colliding path: %v", err)
	}
}
```

Add `"strings"` to the test imports.

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/assemble/ -run TestAssembleRejectsCollision -v`
Expected: FAIL (Assemble returns nil; no collision detection yet).

- [ ] **Step 3: Implement `collisions` and call it in `Assemble`**

In `internal/assemble/assemble.go`, add imports `"fmt"`, `"path"`, `"sort"`, `"strings"`. Add the function:

```go
// collisions returns the image-files paths present in BOTH the kit overlay and
// the sealed hardening tree. Such a path would be silently overwritten by the
// winning hardening copy, so Assemble rejects the build instead of surprising
// the kit author.
func collisions(kitDir string) ([]string, error) {
	localIF := filepath.Join(kitDir, "image-files")
	if _, err := os.Stat(localIF); err != nil {
		return nil, nil // no kit overlay → nothing to collide
	}
	var hits []string
	err := filepath.WalkDir(localIF, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(localIF, p)
		if err != nil {
			return err
		}
		hp := path.Join("hardening/image-files", filepath.ToSlash(rel))
		if _, err := fs.Stat(hardeningFS, hp); err == nil {
			hits = append(hits, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(hits)
	return hits, nil
}
```

Then at the top of `Assemble`, immediately after the `os.MkdirAll(buildDir, …)` block, add:

```go
	if hits, err := collisions(kitDir); err != nil {
		return err
	} else if len(hits) > 0 {
		return fmt.Errorf("kit image-files collide with the sealed hardening layer (these would be silently overwritten — rename or remove them): %s", strings.Join(hits, ", "))
	}
```

- [ ] **Step 4: Run the assemble tests**

Run: `go test ./internal/assemble/ -v`
Expected: PASS (collision test passes; updated `TestAssembleLayersAndKey` passes).

- [ ] **Step 5: Commit**

```bash
git add internal/assemble/assemble.go internal/assemble/assemble_test.go
git commit -m "feat(assemble): reject kit image-files that collide with hardening"
```

---

### Task 2: `ImageConfig` schema + parsing

Add the `image:` block to the kit config type with validation. Pure config; no assemble changes.

**Files:**
- Modify: `internal/kit/config.go`
- Test: `internal/kit/config_test.go`

**Interfaces:**
- Produces: `kit.ImageConfig{ SetupScript []string; Paths []string; Env map[string]string; AllowedDomains []string }` and a new `Config.Image ImageConfig` field (yaml `image`). Consumed by `assemble.Assemble` in Task 3.

- [ ] **Step 1: Write the failing parse test**

Add to `internal/kit/config_test.go`:

```go
func TestParseConfigImage(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
backend: colima
image:
  setup-script:
    - .install-files/install.sh
  paths:
    - /usr/local/go/bin
  env:
    GOROOT: /usr/local/go
  allowed-domains:
    - .example.com
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Image.SetupScript) != 1 || cfg.Image.SetupScript[0] != ".install-files/install.sh" {
		t.Fatalf("SetupScript = %v", cfg.Image.SetupScript)
	}
	if len(cfg.Image.Paths) != 1 || cfg.Image.Paths[0] != "/usr/local/go/bin" {
		t.Fatalf("Paths = %v", cfg.Image.Paths)
	}
	if cfg.Image.Env["GOROOT"] != "/usr/local/go" {
		t.Fatalf("Env = %v", cfg.Image.Env)
	}
	if len(cfg.Image.AllowedDomains) != 1 || cfg.Image.AllowedDomains[0] != ".example.com" {
		t.Fatalf("AllowedDomains = %v", cfg.Image.AllowedDomains)
	}
}

func TestParseConfigImageAbsent(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\nbackend: colima\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Image.SetupScript) != 0 || len(cfg.Image.Paths) != 0 || len(cfg.Image.Env) != 0 || len(cfg.Image.AllowedDomains) != 0 {
		t.Fatalf("absent image must be zero-valued, got %+v", cfg.Image)
	}
}

func TestParseConfigImageRejectsEmptyScript(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nbackend: colima\nimage:\n  setup-script:\n    - \"\"\n"))
	if err == nil || !strings.Contains(err.Error(), "setup-script") {
		t.Fatalf("expected empty setup-script error, got %v", err)
	}
}
```

Ensure `config_test.go` imports `"strings"` (add if missing).

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/kit/ -run TestParseConfigImage -v`
Expected: FAIL to compile (`cfg.Image` undefined).

- [ ] **Step 3: Implement the type, field, and validation**

In `internal/kit/config.go`, add `"strings"` to imports. Add the type above `Config`:

```go
// ImageConfig declares additive, build-time customisations of the sandbox image.
// cove translates each field to the correct sealed mechanism; every field is
// additive to the hardened baseline and never overrides it.
type ImageConfig struct {
	SetupScript    []string          `yaml:"setup-script"`    // kit-relative scripts run as root at build, in place
	Paths          []string          `yaml:"paths"`           // appended to PATH in /etc/environment
	Env            map[string]string `yaml:"env"`             // KEY=VALUE written to /etc/environment
	AllowedDomains []string          `yaml:"allowed-domains"` // added to the squid egress allow-list
}
```

Add the field to `Config` (after `Loops`):

```go
	Image ImageConfig `yaml:"image"`
```

In `ParseConfig`, before `return cfg, nil`, add:

```go
	for i, s := range cfg.Image.SetupScript {
		if strings.TrimSpace(s) == "" {
			return Config{}, fmt.Errorf("config.yml: image.setup-script[%d]: must not be empty", i)
		}
	}
	for i, p := range cfg.Image.Paths {
		if strings.TrimSpace(p) == "" {
			return Config{}, fmt.Errorf("config.yml: image.paths[%d]: must not be empty", i)
		}
	}
	for k := range cfg.Image.Env {
		if strings.TrimSpace(k) == "" {
			return Config{}, fmt.Errorf("config.yml: image.env: keys must not be empty")
		}
	}
	for i, d := range cfg.Image.AllowedDomains {
		if strings.TrimSpace(d) == "" {
			return Config{}, fmt.Errorf("config.yml: image.allowed-domains[%d]: must not be empty", i)
		}
	}
```

- [ ] **Step 4: Run the kit tests**

Run: `go test ./internal/kit/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(kit): parse the image: config block (setup-script/paths/env/allowed-domains)"
```

---

### Task 3: `allowed-domains` + thread `ImageConfig` into `Assemble`

Change the `Assemble` signature to accept `kit.ImageConfig`, generate the additive squid allow-list file, and wire `squid.conf` to read it.

**Files:**
- Modify: `internal/assemble/assemble.go`
- Modify: `internal/assemble/hardening/image-files/etc/squid/squid.conf`
- Modify: `main.go` (`doBuild`)
- Test: `internal/assemble/assemble_test.go`

**Interfaces:**
- Consumes: `kit.ImageConfig` (Task 2).
- Produces: `Assemble(kitDir, buildDir string, pub []byte, img kit.ImageConfig) error`; writes `image-files/etc/squid/allowed_domains.kit.txt` (always).

- [ ] **Step 1: Write the failing test (new signature)**

In `internal/assemble/assemble_test.go`, add `"github.com/aethons-tools/cove/internal/kit"` to imports. Update the existing `Assemble(kitDir, buildDir, []byte("…"))` calls (in `TestAssembleLayersAndKey` and `TestAssembleRejectsCollision`) to pass a fourth arg `kit.ImageConfig{}`. Add:

```go
func TestAssembleAllowedDomains(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")
	img := kit.ImageConfig{AllowedDomains: []string{".example.com", "pkg.go.dev"}}
	if err := Assemble(kitDir, buildDir, []byte("k\n"), img); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(buildDir, "image-files/etc/squid/allowed_domains.kit.txt"))
	if !strings.Contains(got, ".example.com") || !strings.Contains(got, "pkg.go.dev") {
		t.Fatalf("kit allow-list = %q", got)
	}
}

func TestAssembleAllowedDomainsAlwaysWritten(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(kitDir, buildDir, []byte("k\n"), kit.ImageConfig{}); err != nil {
		t.Fatal(err)
	}
	// File must exist even with no domains, so squid.conf never references a missing file.
	if _, err := os.Stat(filepath.Join(buildDir, "image-files/etc/squid/allowed_domains.kit.txt")); err != nil {
		t.Fatalf("kit allow-list must always be written: %v", err)
	}
}

func TestSquidConfReferencesKitFile(t *testing.T) {
	got := read(t, "hardening/image-files/etc/squid/squid.conf")
	if !strings.Contains(got, "allowed_domains.kit.txt") {
		t.Fatalf("squid.conf must reference the kit allow-list: %q", got)
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/assemble/ -run 'Allowed|SquidConf' -v`
Expected: FAIL to compile (Assemble takes 3 args).

- [ ] **Step 3: Change the signature, add generation, update `main.go`**

In `internal/assemble/assemble.go`, change the signature:

```go
func Assemble(kitDir, buildDir string, pub []byte, img kit.ImageConfig) error {
```

Add `"github.com/aethons-tools/cove/internal/kit"` to imports. After the hardening copy (the `copyEmbed(hardeningFS, …)` block) and before the managed-key injection, add:

```go
	if err := writeAllowedDomains(buildDir, img.AllowedDomains); err != nil {
		return err
	}
```

Add the helper:

```go
// writeAllowedDomains writes the kit's additive squid allow-list. Always written
// (empty list → header only) so the sealed squid.conf can reference it
// unconditionally without squid erroring on a missing ACL file.
func writeAllowedDomains(buildDir string, domains []string) error {
	dst := filepath.Join(buildDir, "image-files/etc/squid/allowed_domains.kit.txt")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Kit-declared egress domains (config.yml image.allowed-domains).\n")
	b.WriteString("# Additive to the sealed base allowed_domains.txt; leading dot = subdomains.\n")
	for _, d := range domains {
		b.WriteString(d)
		b.WriteString("\n")
	}
	return os.WriteFile(dst, []byte(b.String()), 0o644)
}
```

In `main.go` `doBuild`, load the config and pass `cfg.Image`. Replace the body after the `dryRun` early-return so it reads:

```go
	cfg, err := kit.Load(kitDir)
	if err != nil {
		return err
	}
	if err := kit.EnsureGitignore(kitDir); err != nil {
		return err
	}
	_, pub, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	return assemble.Assemble(kitDir, buildDir, pub, cfg.Image)
```

(`kit` is already imported in `main.go`.)

Edit `internal/assemble/hardening/image-files/etc/squid/squid.conf` to reference the kit file via a **second** ACL with its own `http_access allow`
(the single-line multi-file `dstdomain` form only loads the first file at runtime —
kit domains get silently denied even though `squid -k parse` is clean):

```
acl allowed_domains     dstdomain "/etc/squid/allowed_domains.txt"
acl allowed_kit_domains dstdomain "/etc/squid/allowed_domains.kit.txt"
...
http_access allow allowed_domains
http_access allow allowed_kit_domains
```

- [ ] **Step 4: Run assemble tests + full build**

Run: `go test ./internal/assemble/ -v && go build ./...`
Expected: PASS and a clean build (confirms `main.go` compiles with the new signature).

- [ ] **Step 5: Commit**

```bash
git add internal/assemble/assemble.go internal/assemble/assemble_test.go internal/assemble/hardening/image-files/etc/squid/squid.conf main.go
git commit -m "feat(image): render image.allowed-domains into an additive squid allow-list"
```

---

### Task 4: `setup-script` (manifest + runner + Dockerfile), and the reserved `.cove/` guard

Generate an ordered setup manifest, ship a runner that executes scripts in place as root, replace the hardcoded `install.sh` Dockerfile line, and reject a kit using the reserved `.cove/` namespace.

**Files:**
- Modify: `internal/assemble/assemble.go`
- Create: `internal/assemble/hardening/image-files/usr/local/lib/cove/run-setup.sh`
- Modify: `internal/assemble/hardening/Dockerfile`
- Test: `internal/assemble/assemble_test.go`
- Test: `internal/assemble/image_scripts_test.go`

**Interfaces:**
- Consumes: `kit.ImageConfig.SetupScript` (Task 2), `img` param (Task 3).
- Produces: `image-files/.cove/setup-manifest` (in-image absolute script paths, one per line, ordered; always written). A kit `image-files/.cove` is an error.

- [ ] **Step 1: Write failing assemble tests (manifest + missing-script + reserved guard)**

Add to `internal/assemble/assemble_test.go`:

```go
func TestAssembleSetupManifest(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")
	mustWrite(t, filepath.Join(kitDir, "image-files/.install-files/install.sh"), "#!/bin/bash\n")
	img := kit.ImageConfig{SetupScript: []string{".install-files/install.sh"}}
	if err := Assemble(kitDir, buildDir, []byte("k\n"), img); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(buildDir, "image-files/.cove/setup-manifest"))
	if got != "/.install-files/install.sh\n" {
		t.Fatalf("manifest = %q", got)
	}
}

func TestAssembleSetupMissingScript(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")
	img := kit.ImageConfig{SetupScript: []string{".install-files/nope.sh"}}
	err := Assemble(kitDir, buildDir, []byte("k\n"), img)
	if err == nil || !strings.Contains(err.Error(), "nope.sh") {
		t.Fatalf("expected missing-script error naming the path, got %v", err)
	}
}

func TestAssembleRejectsReservedCove(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")
	mustWrite(t, filepath.Join(kitDir, "image-files/.cove/x"), "nope")
	err := Assemble(kitDir, buildDir, []byte("k\n"), kit.ImageConfig{})
	if err == nil || !strings.Contains(err.Error(), ".cove") {
		t.Fatalf("expected reserved-namespace error, got %v", err)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/assemble/ -run 'Setup|ReservedCove' -v`
Expected: FAIL (manifest not written; no guards).

- [ ] **Step 3: Implement the reserved guard and manifest generation**

In `internal/assemble/assemble.go`, add the reserved-namespace check right after the collision check in `Assemble`:

```go
	if _, err := os.Stat(filepath.Join(kitDir, "image-files", ".cove")); err == nil {
		return fmt.Errorf("kit image-files/.cove is reserved for cove-generated build files; rename or remove it")
	}
```

After the `writeAllowedDomains` call, add:

```go
	if err := writeSetupManifest(kitDir, buildDir, img.SetupScript); err != nil {
		return err
	}
```

Add the helper:

```go
// writeSetupManifest writes the ordered list of in-image absolute script paths
// for the build-time runner. Each entry is interpreted relative to the kit's
// image-files root: on disk kitDir/image-files/<entry>, in the image /<entry>
// (the file is placed there by `COPY image-files/. /.`). Always written (empty
// list → empty file) so the runner can read it unconditionally.
func writeSetupManifest(kitDir, buildDir string, scripts []string) error {
	var b strings.Builder
	for _, s := range scripts {
		onDisk := filepath.Join(kitDir, "image-files", filepath.FromSlash(s))
		info, err := os.Stat(onDisk)
		if err != nil {
			return fmt.Errorf("image.setup-script %q: %w", s, err)
		}
		if info.IsDir() {
			return fmt.Errorf("image.setup-script %q: is a directory, not a script", s)
		}
		b.WriteString(path.Clean("/" + filepath.ToSlash(s)))
		b.WriteString("\n")
	}
	dst := filepath.Join(buildDir, "image-files/.cove/setup-manifest")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(b.String()), 0o644)
}
```

- [ ] **Step 4: Run assemble tests**

Run: `go test ./internal/assemble/ -run 'Setup|ReservedCove' -v`
Expected: PASS.

- [ ] **Step 5: Create the runner script**

Create `internal/assemble/hardening/image-files/usr/local/lib/cove/run-setup.sh`:

```bash
#!/usr/bin/env bash
# Run kit setup-scripts (config.yml image.setup-script) in order, as root, each
# in its own directory so a script can reference sibling files. The manifest
# lists in-image absolute script paths, one per line, written by cove's assemble
# step. Build-time network is open, so scripts may curl/apt-get.
set -euo pipefail

manifest="${COVE_SETUP_MANIFEST:-/.cove/setup-manifest}"
[ -f "$manifest" ] || exit 0

while IFS= read -r script; do
	[ -n "$script" ] || continue
	( cd "$(dirname "$script")" && bash "$script" )
done < "$manifest"
```

- [ ] **Step 6: Write the runner's hermetic bash test**

Create `internal/assemble/image_scripts_test.go`:

```go
package assemble

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

func TestRunSetupScriptRunsInOrderInPlace(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	// Two scripts in different dirs; each appends its name and cwd to a shared log.
	log := filepath.Join(dir, "log")
	aDir := filepath.Join(dir, "a")
	bDir := filepath.Join(dir, "b")
	mustWrite(t, filepath.Join(aDir, "one.sh"), "printf 'one %s\\n' \"$(pwd)\" >> '"+log+"'\n")
	mustWrite(t, filepath.Join(bDir, "two.sh"), "printf 'two %s\\n' \"$(pwd)\" >> '"+log+"'\n")
	manifest := filepath.Join(dir, "manifest")
	mustWrite(t, manifest, filepath.Join(aDir, "one.sh")+"\n"+filepath.Join(bDir, "two.sh")+"\n")

	cmd := exec.Command("bash", "hardening/image-files/usr/local/lib/cove/run-setup.sh")
	cmd.Env = append(os.Environ(), "COVE_SETUP_MANIFEST="+manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run-setup.sh failed: %v\n%s", err, out)
	}
	got := read(t, log)
	want := "one " + aDir + "\ntwo " + bDir + "\n"
	if got != want {
		t.Fatalf("setup ran wrong order/cwd:\n got %q\nwant %q", got, want)
	}
}
```

- [ ] **Step 7: Edit the Dockerfile to call the runner**

In `internal/assemble/hardening/Dockerfile`, replace the three-line `RUN chmod +x /.install-files/install.sh && … && rm -rf /.install-files` block (currently lines ~60–62) with:

```dockerfile
# Run kit-declared setup scripts (config.yml image.setup-script) as root, each
# in place. Replaces the old hardcoded /.install-files/install.sh convention.
RUN /usr/local/lib/cove/run-setup.sh
```

- [ ] **Step 8: Run tests + lint the new script**

Run: `go test ./internal/assemble/ -v`
Expected: PASS.
Run (if installed): `shellcheck internal/assemble/hardening/image-files/usr/local/lib/cove/run-setup.sh`
Expected: no findings.

- [ ] **Step 9: Commit**

```bash
git add internal/assemble/assemble.go internal/assemble/assemble_test.go internal/assemble/image_scripts_test.go internal/assemble/hardening/image-files/usr/local/lib/cove/run-setup.sh internal/assemble/hardening/Dockerfile
git commit -m "feat(image): run image.setup-script via a generated manifest; reserve .cove/"
```

---

### Task 5: `paths` + `env` (`.cove/paths`, `.cove/env`, apply helper, Dockerfile)

Generate the path/env consumables, ship a helper that folds them into `/etc/environment` (single merged `PATH=` line), call it after the base env write, and drop `/.cove` from the final image.

**Files:**
- Modify: `internal/assemble/assemble.go`
- Create: `internal/assemble/hardening/image-files/usr/local/lib/cove/apply-image-env.sh`
- Modify: `internal/assemble/hardening/Dockerfile`
- Test: `internal/assemble/assemble_test.go`
- Test: `internal/assemble/image_scripts_test.go`

**Interfaces:**
- Consumes: `kit.ImageConfig.Paths`, `kit.ImageConfig.Env` (Task 2).
- Produces: `image-files/.cove/paths` (one per line, order preserved) and `image-files/.cove/env` (`KEY=VALUE`, sorted), both always written.

- [ ] **Step 1: Write the failing generation test**

Add to `internal/assemble/assemble_test.go`:

```go
func TestAssembleImageEnv(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")
	img := kit.ImageConfig{
		Paths: []string{"/usr/local/go/bin", "/home/agent/go/bin"},
		Env:   map[string]string{"GOROOT": "/usr/local/go", "GOPATH": "/home/agent/go"},
	}
	if err := Assemble(kitDir, buildDir, []byte("k\n"), img); err != nil {
		t.Fatal(err)
	}
	paths := read(t, filepath.Join(buildDir, "image-files/.cove/paths"))
	if paths != "/usr/local/go/bin\n/home/agent/go/bin\n" {
		t.Fatalf("paths = %q", paths)
	}
	env := read(t, filepath.Join(buildDir, "image-files/.cove/env"))
	if env != "GOPATH=/home/agent/go\nGOROOT=/usr/local/go\n" { // sorted by key
		t.Fatalf("env = %q", env)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/assemble/ -run TestAssembleImageEnv -v`
Expected: FAIL (files absent).

- [ ] **Step 3: Implement generation**

In `internal/assemble/assemble.go`, after the `writeSetupManifest` call in `Assemble`, add:

```go
	if err := writeImageEnv(buildDir, img.Paths, img.Env); err != nil {
		return err
	}
```

Add the helper:

```go
// writeImageEnv writes the kit's additive PATH segments and env vars for the
// build-time apply helper. Both files are always written (empty when unset).
// Paths keep declaration order; env is sorted by key for a deterministic image.
func writeImageEnv(buildDir string, paths []string, env map[string]string) error {
	dir := filepath.Join(buildDir, "image-files/.cove")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var pb strings.Builder
	for _, p := range paths {
		pb.WriteString(p)
		pb.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "paths"), []byte(pb.String()), 0o644); err != nil {
		return err
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var eb strings.Builder
	for _, k := range keys {
		eb.WriteString(k)
		eb.WriteString("=")
		eb.WriteString(env[k])
		eb.WriteString("\n")
	}
	return os.WriteFile(filepath.Join(dir, "env"), []byte(eb.String()), 0o644)
}
```

- [ ] **Step 4: Run to confirm pass**

Run: `go test ./internal/assemble/ -run TestAssembleImageEnv -v`
Expected: PASS.

- [ ] **Step 5: Create the apply helper**

Create `internal/assemble/hardening/image-files/usr/local/lib/cove/apply-image-env.sh`:

```bash
#!/usr/bin/env bash
# Fold kit-declared env (image.env) and PATH additions (image.paths) into
# /etc/environment so pam_env exposes them to every SSH session — interactive or
# not, login or not. env vars are appended as their own KEY=VALUE lines; path
# entries are appended to the SINGLE existing PATH= line (never a second PATH=
# line, since pam_env is last-wins). The base PATH/env written by the Dockerfile
# is preserved — this is strictly additive.
set -euo pipefail

cove_dir="${COVE_DIR:-/.cove}"
env_file="${COVE_ENV_FILE:-/etc/environment}"

if [ -s "$cove_dir/env" ]; then
	while IFS= read -r line; do
		[ -n "$line" ] || continue
		printf '%s\n' "$line" >> "$env_file"
	done < "$cove_dir/env"
fi

if [ -s "$cove_dir/paths" ]; then
	add=""
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		add="${add}:${p}"
	done < "$cove_dir/paths"
	if grep -q '^PATH=' "$env_file"; then
		sed -i "\\#^PATH=#s#\$#${add}#" "$env_file"
	else
		printf 'PATH=%s\n' "${add#:}" >> "$env_file"
	fi
fi
```

- [ ] **Step 6: Write the apply helper's hermetic bash test**

Add `"strings"` to the import block of `internal/assemble/image_scripts_test.go` (now genuinely used), then add:

```go
func TestApplyImageEnvMergesPathAndAppendsEnv(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	coveDir := filepath.Join(dir, "cove")
	mustWrite(t, filepath.Join(coveDir, "paths"), "/usr/local/go/bin\n/home/agent/go/bin\n")
	mustWrite(t, filepath.Join(coveDir, "env"), "GOROOT=/usr/local/go\n")
	envFile := filepath.Join(dir, "environment")
	mustWrite(t, envFile, "PATH=/usr/local/bin:/usr/bin:/bin\nCLAUDE_CONFIG_DIR=/agent-data\n")

	cmd := exec.Command("bash", "hardening/image-files/usr/local/lib/cove/apply-image-env.sh")
	cmd.Env = append(os.Environ(), "COVE_DIR="+coveDir, "COVE_ENV_FILE="+envFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply-image-env.sh failed: %v\n%s", err, out)
	}
	got := read(t, envFile)

	// Exactly one PATH= line, with base preserved and kit segments appended.
	if n := strings.Count(got, "\nPATH=") + boolToInt(strings.HasPrefix(got, "PATH=")); n != 1 {
		t.Fatalf("must remain a single PATH= line, got %d in:\n%s", n, got)
	}
	if !strings.Contains(got, "PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/go/bin:/home/agent/go/bin") {
		t.Fatalf("PATH not merged correctly:\n%s", got)
	}
	if !strings.Contains(got, "\nGOROOT=/usr/local/go\n") {
		t.Fatalf("env var not appended:\n%s", got)
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 7: Edit the Dockerfile — apply env, then drop /.cove**

In `internal/assemble/hardening/Dockerfile`, after the block that appends the base lines to `/etc/environment` (the `RUN printf … >> /etc/environment` ending ~line 76), and before `EXPOSE 2222`, add:

```dockerfile
# Fold kit-declared PATH/env (config.yml image.paths/image.env) into
# /etc/environment, so pam_env exposes them to every session — the mechanism
# bashrc cannot reach for non-interactive SSH command execution.
RUN /usr/local/lib/cove/apply-image-env.sh

# Drop cove's build-time consumables from the final image.
RUN rm -rf /.cove
```

- [ ] **Step 8: Run tests + lint**

Run: `go test ./internal/assemble/ -v`
Expected: PASS (all assemble + bash helper tests).
Run (if installed): `shellcheck internal/assemble/hardening/image-files/usr/local/lib/cove/*.sh && hadolint internal/assemble/hardening/Dockerfile`
Expected: no findings.

- [ ] **Step 9: Commit**

```bash
git add internal/assemble/assemble.go internal/assemble/assemble_test.go internal/assemble/image_scripts_test.go internal/assemble/hardening/image-files/usr/local/lib/cove/apply-image-env.sh internal/assemble/hardening/Dockerfile
git commit -m "feat(image): fold image.paths/image.env into /etc/environment via pam_env"
```

---

### Task 6: Migrate the in-repo `.at-cove` kit off the bashrc footgun

Apply the new block to the dogfood kit and remove the bashrc writes that started this effort.

**Files:**
- Modify: `.at-cove/config.yml`
- Modify: `.at-cove/image-files/.install-files/install.sh`

**Interfaces:** none (configuration/data only).

- [ ] **Step 1: Confirm the kit is tracked before editing**

Run: `git ls-files .at-cove/config.yml .at-cove/image-files/.install-files/install.sh`
Expected: both paths listed. If either is absent (untracked local kit), STOP and ask the user before editing it.

- [ ] **Step 2: Add the `image:` block to the kit config**

Append to `.at-cove/config.yml`:

```yaml
image:
  setup-script:
    - .install-files/install.sh
  paths:
    - /usr/local/go/bin
    - /home/agent/go/bin
  env:
    GOROOT: /usr/local/go
    GOPATH: /home/agent/go
```

- [ ] **Step 3: Remove the bashrc writes from install.sh**

In `.at-cove/image-files/.install-files/install.sh`, delete the three lines:

```sh
echo 'export GOROOT=/usr/local/go' >> /etc/bash.bashrc
echo 'export GOPATH=/home/agent/go' >> /etc/bash.bashrc
echo 'export PATH=$PATH:$GOROOT/bin:$GOPATH/bin' >> /etc/bash.bashrc
```

(The Go env now comes from `image.paths`/`image.env` via `/etc/environment`, which non-interactive SSH sessions inherit. `go` itself works from `/usr/local/go/bin` on PATH; `GOROOT`/`GOPATH` are set explicitly for clarity.)

- [ ] **Step 4: Validate config parse + full suite + lint**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all tests PASS; `go vet` clean; `gofmt -l` prints nothing.
Run (if installed): `hadolint internal/assemble/hardening/Dockerfile`
Expected: no findings.

Note: a real image rebuild (`at-cove recreate <kit>` then verifying `go version` and `stat /home/agent` inside the sandbox) requires a running colima/docker host and is performed manually on the Mac — it is not part of this hermetic suite.

- [ ] **Step 5: Commit**

```bash
git add .at-cove/config.yml .at-cove/image-files/.install-files/install.sh
git commit -m "chore(kit): move Go env to image.paths/env; drop the bashrc footgun"
```

---

## Self-Review

**Spec coverage:**
- Config schema (`image:` with all four keys) → Task 2. ✓
- Strictly additive / sealed hardening → enforced by design across Tasks 3–5 (separate files referenced/appended; collision check Task 1; reserved `.cove/` Task 4). ✓
- setup-script (in place, root, build-time, replaces hardcoded line) → Task 4. ✓
- paths + env (single merged PATH line, pam_env) → Task 5. ✓
- allowed-domains (separate kit file, second `dstdomain` ACL + own `http_access allow`) → Task 3. ✓
- Collision check (separable) → Task 1. ✓
- Error handling (missing script, reserved namespace, collision) → Tasks 1/4. ✓
- Testing (config parse, assemble Fake-FS, collision, PATH single-line via bash helper tests, hadolint) → Tasks 1–5. ✓
- Migration (kit config + install.sh) → Task 6. ✓

**Placeholder scan:** none — every code/shell block is complete; commands have expected output.

**Type consistency:** `Assemble(kitDir, buildDir string, pub []byte, img kit.ImageConfig) error` is introduced in Task 3 and used identically in Tasks 4–5 tests. `kit.ImageConfig` field names (`SetupScript`, `Paths`, `Env`, `AllowedDomains`) match between Task 2 and their consumers. Helper names (`collisions`, `writeAllowedDomains`, `writeSetupManifest`, `writeImageEnv`) are each defined once and called once. Env override names (`COVE_SETUP_MANIFEST`, `COVE_DIR`, `COVE_ENV_FILE`) match between the scripts (Tasks 4/5) and their bash tests.
