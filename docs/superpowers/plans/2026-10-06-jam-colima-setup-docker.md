# at-jam colima setup-docker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `at-jam colima setup-docker` (idempotently writes the Sysbox provision hook + `sysbox-runc` runtime into the default colima config) and `at-jam colima check-docker` (verifies the runtime is registered), per [the spec](../specs/2026-10-06-jam-colima-setup-docker-design.md).

**Architecture:** A new pure package `internal/colimacfg` (bytes-in/bytes-out YAML edit on `yaml.v3` Nodes + a tiny line diff) holds all the logic. The colima backend's Sysbox probe is extracted into an exported `colima.HasSysboxRuntime(runner.Runner)` shared by the at-cove preflight and the new check. `cmd/at-jam/colima.go` is thin I/O: resolve the path from injected `getenv`, read, `Apply`, then dry-run diff or backup + atomic write, and print next steps.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3` (already in `go.mod`), `internal/cli`, `internal/runner` (`runner.Fake` for hermetic tests).

## Global Constraints

- Sysbox version floor: `0.7.1` (`colimacfg.MinSysboxVersion`); version format `MAJOR.MINOR.PATCH`, no `v` prefix.
- Runtime path: `/usr/bin/sysbox-runc`.
- Hook marker line (verbatim): `# managed by at-jam colima setup-docker — re-run it to change; edits here are overwritten`.
- Config path: `$COLIMA_HOME/default/colima.yaml`, else `$HOME/.colima/default/colima.yaml`. No `--profile`.
- The command never restarts colima; it prints `colima restart` + `at-jam colima check-docker`.
- A no-op `Apply` returns the input bytes unchanged; the command then writes nothing (no `.bak`).
- Tests are hermetic: no real colima/docker/network. No new dependencies.
- Toolchain in this sandbox: see `docs/DEVELOPMENT.md` (`GOPROXY`/`GOPATH`); run tests with `go test` or `just test`.
- Docs are updated in the same change (AGENTS.md rule); run the `docs-audit` skill before the PR.

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/colimacfg/colimacfg.go` (create) | `Options`, `Change`, `MinSysboxVersion`, `SysboxRuntimePath`, `Marker`, `ValidateVersion`, `HookScript`, `Apply` + node helpers |
| `internal/colimacfg/diff.go` (create) | `Diff(a, b []byte) string` for `--dry-run` |
| `internal/colimacfg/{colimacfg,diff}_test.go` (create) | fixture tests |
| `internal/backend/colima/colima.go` (modify `requireSysboxRuntime`, ~L109-128) | extract exported `HasSysboxRuntime`; error names the new command |
| `internal/backend/colima/sysbox_test.go` (create) | probe + message tests |
| `cmd/at-jam/colima.go` (create) | `cmdColima`, path resolution, setup/check subcommands, atomic write |
| `cmd/at-jam/colima_test.go` (create) | command tests against a temp `COLIMA_HOME` |
| `cmd/at-jam/main.go` (modify `run`, command list ~L95) | register `colima` |
| `docs/usage/docker-in-sandbox.md`, `docs/usage/INDEX.md`, `docs/usage/jam/INDEX.md` (modify) | docs |

---

### Task 1: `internal/colimacfg` — the pure config edit and diff

**Files:**
- Create: `internal/colimacfg/colimacfg.go`, `internal/colimacfg/diff.go`
- Test: `internal/colimacfg/colimacfg_test.go`, `internal/colimacfg/diff_test.go`

**Interfaces:**
- Consumes: nothing new (`gopkg.in/yaml.v3`).
- Produces (used by Task 3):
  - `const MinSysboxVersion = "0.7.1"`, `const SysboxRuntimePath = "/usr/bin/sysbox-runc"`, `const Marker = "…"`
  - `type Options struct{ SysboxVersion string }`, `type Change struct{ What string }`
  - `func ValidateVersion(v string) error`
  - `func HookScript(version string) string`
  - `func Apply(in []byte, o Options) ([]byte, []Change, error)`
  - `func Diff(a, b []byte) string`

- [ ] **Step 1: Write the failing tests**

`internal/colimacfg/colimacfg_test.go`:

```go
package colimacfg

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var opts = Options{SysboxVersion: MinSysboxVersion}

// freshConfig mirrors the shape of colima's generated default config: comments,
// an empty `docker: {}` and an empty `provision: []`.
const freshConfig = `# Number of CPUs to be allocated to the virtual machine.
cpu: 2

# Docker daemon configuration that maps directly to daemon.json.
docker: {}

# Initial provisioning scripts to run.
provision: []

disk: 100
`

// parsed is the decoded shape the assertions inspect.
type parsed struct {
	Docker    map[string]any `yaml:"docker"`
	Provision []struct {
		Mode   string `yaml:"mode"`
		Script string `yaml:"script"`
	} `yaml:"provision"`
	CPU  int `yaml:"cpu"`
	Disk int `yaml:"disk"`
}

func decode(t *testing.T, b []byte) parsed {
	t.Helper()
	var p parsed
	if err := yaml.Unmarshal(b, &p); err != nil {
		t.Fatalf("output is not valid YAML: %v\n%s", err, b)
	}
	return p
}

func runtimePath(p parsed) any {
	rt, _ := p.Docker["runtimes"].(map[string]any)
	sb, _ := rt["sysbox-runc"].(map[string]any)
	return sb["path"]
}

func apply(t *testing.T, in string, o Options) (string, []Change) {
	t.Helper()
	out, ch, err := Apply([]byte(in), o)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return string(out), ch
}

func TestApplyFreshConfig(t *testing.T) {
	out, ch := apply(t, freshConfig, opts)
	if len(ch) != 2 {
		t.Fatalf("changes = %+v, want runtime + hook", ch)
	}
	p := decode(t, []byte(out))
	if runtimePath(p) != SysboxRuntimePath {
		t.Fatalf("runtime path = %v\n%s", runtimePath(p), out)
	}
	if len(p.Provision) != 1 || p.Provision[0].Mode != "system" || p.Provision[0].Script != HookScript(MinSysboxVersion) {
		t.Fatalf("provision = %+v", p.Provision)
	}
	if p.CPU != 2 || p.Disk != 100 {
		t.Fatalf("other keys lost: %+v", p)
	}
	for _, c := range []string{"# Number of CPUs", "# Docker daemon configuration", "# Initial provisioning"} {
		if !strings.Contains(out, c) {
			t.Fatalf("comment %q lost:\n%s", c, out)
		}
	}
	if !strings.Contains(out, "script: |") {
		t.Fatalf("script must render as a literal block:\n%s", out)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	once, _ := apply(t, freshConfig, opts)
	twice, ch := apply(t, once, opts)
	if len(ch) != 0 || twice != once {
		t.Fatalf("second Apply changed things: %+v\n%s", ch, twice)
	}
}

func TestApplyNoOpReturnsInputBytes(t *testing.T) {
	// Already set up, but formatted unusually (4-space indent, quoted path):
	// a no-op must not reformat it.
	in, _ := apply(t, freshConfig, opts)
	in = strings.Replace(in, "path: /usr/bin/sysbox-runc", `path: "/usr/bin/sysbox-runc"`, 1)
	out, ch := apply(t, in, opts)
	if len(ch) != 0 || out != in {
		t.Fatalf("no-op must return input bytes: %+v\n%s", ch, out)
	}
}

func TestApplyReplacesOldVersionHook(t *testing.T) {
	old, _ := apply(t, freshConfig, opts)
	out, ch := apply(t, old, Options{SysboxVersion: "0.8.0"})
	if len(ch) != 1 || !strings.Contains(ch[0].What, "updated") {
		t.Fatalf("changes = %+v", ch)
	}
	p := decode(t, []byte(out))
	if len(p.Provision) != 1 || !strings.Contains(p.Provision[0].Script, `ver="0.8.0"`) {
		t.Fatalf("provision = %+v", p.Provision)
	}
}

func TestApplyKeepsUserHooksAndDockerKeys(t *testing.T) {
	in := `docker:
  features:
    buildkit: true
  runtimes:
    crun:
      path: /usr/bin/crun
provision:
  - mode: user
    script: echo first
  - mode: system
    script: echo second
`
	out, _ := apply(t, in, opts)
	p := decode(t, []byte(out))
	if f, _ := p.Docker["features"].(map[string]any); f["buildkit"] != true {
		t.Fatalf("docker.features lost:\n%s", out)
	}
	if rt, _ := p.Docker["runtimes"].(map[string]any); rt["crun"] == nil {
		t.Fatalf("other runtime lost:\n%s", out)
	}
	if len(p.Provision) != 3 || p.Provision[0].Script != "echo first" || p.Provision[1].Script != "echo second" || !strings.Contains(p.Provision[2].Script, Marker) {
		t.Fatalf("hook order/content wrong: %+v", p.Provision)
	}
}

func TestApplyEmptyFile(t *testing.T) {
	for _, in := range []string{"", "# only a comment\n", "---\n"} {
		out, ch := apply(t, in, opts)
		p := decode(t, []byte(out))
		if len(ch) != 2 || runtimePath(p) != SysboxRuntimePath || len(p.Provision) != 1 {
			t.Fatalf("empty file %q: %+v\n%s", in, ch, out)
		}
	}
}

func TestApplyOverwritesDifferentRuntimePath(t *testing.T) {
	out, ch := apply(t, "docker:\n  runtimes:\n    sysbox-runc:\n      path: /opt/sysbox-runc\n", opts)
	if runtimePath(decode(t, []byte(out))) != SysboxRuntimePath {
		t.Fatalf("path not overwritten:\n%s", out)
	}
	if len(ch) == 0 || !strings.Contains(ch[0].What, "/opt/sysbox-runc") {
		t.Fatalf("overwrite must be reported: %+v", ch)
	}
}

func TestApplyDedupesMarkedHooks(t *testing.T) {
	once, _ := apply(t, freshConfig, opts)
	p := decode(t, []byte(once))
	var doc yaml.Node // re-encode with a second marked entry appended
	if err := yaml.Unmarshal([]byte(once), &doc); err != nil {
		t.Fatal(err)
	}
	prov := value(doc.Content[0], "provision")
	prov.Content = append(prov.Content, hookNode(p.Provision[0].Script))
	b, _ := yaml.Marshal(&doc)
	out, ch := apply(t, string(b), opts)
	if got := decode(t, []byte(out)); len(got.Provision) != 1 {
		t.Fatalf("duplicates kept: %+v", got.Provision)
	}
	if len(ch) != 1 || !strings.Contains(ch[0].What, "duplicate") {
		t.Fatalf("changes = %+v", ch)
	}
}

func TestApplyRejectsNonMappings(t *testing.T) {
	for _, in := range []string{"docker: true\n", "docker:\n  runtimes: [a]\n", "provision: nope\n", "- a\n"} {
		if _, _, err := Apply([]byte(in), opts); err == nil {
			t.Errorf("Apply(%q) must error", in)
		}
	}
}

func TestValidateVersion(t *testing.T) {
	for _, v := range []string{"0.7.1", "0.7.2", "0.8.0", "1.0.0"} {
		if err := ValidateVersion(v); err != nil {
			t.Errorf("ValidateVersion(%q) = %v", v, err)
		}
	}
	for _, v := range []string{"0.6.9", "0.7.0", "latest", "1.2", "v0.7.1", ""} {
		if err := ValidateVersion(v); err == nil {
			t.Errorf("ValidateVersion(%q) must error", v)
		}
	}
}
```

`internal/colimacfg/diff_test.go`:

```go
package colimacfg

import "testing"

func TestDiff(t *testing.T) {
	got := Diff([]byte("a\nb\nc\n"), []byte("a\nx\nc\nd\n"))
	want := "  a\n- b\n+ x\n  c\n+ d\n"
	if got != want {
		t.Fatalf("Diff:\n%s\nwant:\n%s", got, want)
	}
	if got := Diff(nil, []byte("a\n")); got != "+ a\n" {
		t.Fatalf("Diff from empty = %q", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/colimacfg/`
Expected: FAIL — build errors (`undefined: Apply`, `undefined: Diff`, …).

- [ ] **Step 3: Implement**

`internal/colimacfg/colimacfg.go`:

```go
// Package colimacfg edits a colima config (colima.yaml) so its VM can run
// docker:true sandboxes: a provision hook that installs Sysbox on every boot and
// the sysbox-runc runtime registered through colima's docker: passthrough (colima
// regenerates /etc/docker/daemon.json each start, so that is the durable seam).
// It is pure bytes-in/bytes-out; the at-jam colima command owns the file I/O.
package colimacfg

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// MinSysboxVersion is the oldest Sysbox CE proven to run a docker:true sandbox;
// 0.6.x predates time-namespace support and fails container create.
const MinSysboxVersion = "0.7.1"

// SysboxRuntimePath is where the Sysbox .deb installs sysbox-runc in the VM.
const SysboxRuntimePath = "/usr/bin/sysbox-runc"

// Marker identifies the provision hook this package owns, so a re-run replaces
// it rather than appending a duplicate, and leaves the operator's hooks alone.
const Marker = "# managed by at-jam colima setup-docker — re-run it to change; edits here are overwritten"

// Options parameterizes Apply.
type Options struct {
	// SysboxVersion is the Sysbox CE release the hook installs (MAJOR.MINOR.PATCH, ≥ MinSysboxVersion).
	SysboxVersion string
}

// Change is one human-readable edit Apply made.
type Change struct{ What string }

var semver = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

// ValidateVersion rejects a version that isn't MAJOR.MINOR.PATCH or is older
// than MinSysboxVersion.
func ValidateVersion(v string) error {
	got, ok := parseVersion(v)
	if !ok {
		return fmt.Errorf("sysbox version %q: want MAJOR.MINOR.PATCH (e.g. %s)", v, MinSysboxVersion)
	}
	floor, _ := parseVersion(MinSysboxVersion)
	for i := range got {
		if got[i] != floor[i] {
			if got[i] < floor[i] {
				return fmt.Errorf("sysbox version %s is older than the %s floor (0.6.x lacks time-namespace support)", v, MinSysboxVersion)
			}
			return nil
		}
	}
	return nil
}

func parseVersion(v string) ([3]int, bool) {
	m := semver.FindStringSubmatch(v)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, true
}

// HookScript renders the provision hook's script for a Sysbox version. It is
// idempotent on the binary, so it installs once and is a no-op on later boots.
func HookScript(version string) string {
	return `#!/usr/bin/env bash
` + Marker + `
set -euo pipefail
command -v sysbox-runc >/dev/null 2>&1 && exit 0
arch="$(dpkg --print-architecture)"
ver="` + version + `"
apt-get update && apt-get install -y jq
curl -fsSL -o /tmp/sysbox.deb \
  "https://github.com/nestybox/sysbox/releases/download/v${ver}/sysbox-ce_${ver}.linux_${arch}.deb"
apt-get install -y /tmp/sysbox.deb
`
}

// Apply returns in with the Sysbox provision hook and the sysbox-runc runtime
// set, plus the changes it made. When nothing needs changing it returns in
// unchanged (byte for byte) and no changes, so a no-op run never reformats the
// file. Comments survive a changing run; indentation and quoting are normalized.
func Apply(in []byte, o Options) ([]byte, []Change, error) {
	if err := ValidateVersion(o.SysboxVersion); err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(in, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse colima config: %w", err)
	}
	if doc.Kind == 0 { // empty file
		doc = yaml.Node{Kind: yaml.DocumentNode}
	}
	if len(doc.Content) == 0 || isNull(doc.Content[0]) { // comment-only, or a bare `---`
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("colima config: top level is not a mapping")
	}
	var changes []Change
	c, err := ensureRuntime(root)
	if err != nil {
		return nil, nil, err
	}
	changes = append(changes, c...)
	c, err = ensureHook(root, o.SysboxVersion)
	if err != nil {
		return nil, nil, err
	}
	changes = append(changes, c...)
	if len(changes) == 0 {
		return in, nil, nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, nil, fmt.Errorf("encode colima config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, nil, fmt.Errorf("encode colima config: %w", err)
	}
	return buf.Bytes(), changes, nil
}

// ensureRuntime sets docker.runtimes.sysbox-runc.path, creating the docker and
// runtimes mappings as needed and leaving every other key alone.
func ensureRuntime(root *yaml.Node) ([]Change, error) {
	docker, err := mapping(root, "docker")
	if err != nil {
		return nil, err
	}
	runtimes, err := mapping(docker, "runtimes")
	if err != nil {
		return nil, fmt.Errorf("docker.%w", err)
	}
	sysbox, err := mapping(runtimes, "sysbox-runc")
	if err != nil {
		return nil, fmt.Errorf("docker.runtimes.%w", err)
	}
	path := value(sysbox, "path")
	switch {
	case path == nil:
		setScalar(sysbox, "path", SysboxRuntimePath)
		return []Change{{What: "registered docker.runtimes.sysbox-runc (path " + SysboxRuntimePath + ")"}}, nil
	case path.Kind != yaml.ScalarNode || path.Value != SysboxRuntimePath:
		old := path.Value
		*path = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: SysboxRuntimePath}
		return []Change{{What: fmt.Sprintf("changed docker.runtimes.sysbox-runc path %q → %q", old, SysboxRuntimePath)}}, nil
	}
	return nil, nil
}

// ensureHook adds or replaces the one marked provision entry, keeping the
// operator's other hooks and their order, and dropping duplicate marked entries.
func ensureHook(root *yaml.Node, version string) ([]Change, error) {
	prov := value(root, "provision")
	if prov == nil || isNull(prov) {
		prov = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		set(root, "provision", prov)
	}
	if prov.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("colima config: provision is not a list")
	}
	want := HookScript(version)
	var changes []Change
	var kept []*yaml.Node
	found := false
	for _, e := range prov.Content {
		if !isMarked(e) {
			kept = append(kept, e)
			continue
		}
		if found {
			changes = append(changes, Change{What: "removed a duplicate at-jam Sysbox provision hook"})
			continue
		}
		found = true
		if mode := value(e, "mode"); mode == nil || mode.Value != "system" || value(e, "script").Value != want {
			*e = *hookNode(want)
			changes = append(changes, Change{What: "updated the Sysbox provision hook (Sysbox " + version + ")"})
		}
		kept = append(kept, e)
	}
	if !found {
		kept = append(kept, hookNode(want))
		changes = append(changes, Change{What: "added the Sysbox provision hook (Sysbox " + version + ")"})
	}
	prov.Content = kept
	if prov.Style == yaml.FlowStyle { // `provision: []` — render the entries as a block list
		prov.Style = 0
	}
	return changes, nil
}

func isMarked(e *yaml.Node) bool {
	if e.Kind != yaml.MappingNode {
		return false
	}
	s := value(e, "script")
	return s != nil && s.Kind == yaml.ScalarNode && strings.Contains(s.Value, Marker)
}

func hookNode(script string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setScalar(n, "mode", "system")
	set(n, "script", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: script, Style: yaml.LiteralStyle})
	return n
}

// mapping returns m[key] as a mapping, creating it when absent or null (colima's
// fresh config writes `docker: {}`). A present non-mapping value is an error.
func mapping(m *yaml.Node, key string) (*yaml.Node, error) {
	v := value(m, key)
	if v == nil || isNull(v) {
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		set(m, key, n)
		return n, nil
	}
	if v.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s is not a mapping in the colima config", key)
	}
	if v.Style == yaml.FlowStyle { // `docker: {}` — render the new keys as a block
		v.Style = 0
	}
	return v, nil
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

func value(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func set(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			// Keep the value's comments when replacing a null/{} placeholder.
			v.LineComment, v.FootComment = m.Content[i+1].LineComment, m.Content[i+1].FootComment
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

func setScalar(m *yaml.Node, key, v string) {
	set(m, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
}
```

`internal/colimacfg/diff.go`:

```go
package colimacfg

import "strings"

// Diff renders a minimal line diff of a → b for --dry-run: unchanged lines
// prefixed "  ", removed "- ", added "+ ". Colima configs are small, so a plain
// O(n·m) LCS is fine.
func Diff(a, b []byte) string {
	x, y := lines(a), lines(b)
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var sb strings.Builder
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			sb.WriteString("  " + x[i] + "\n")
			i, j = i+1, j+1
		case i < len(x) && (j == len(y) || lcs[i+1][j] >= lcs[i][j+1]):
			sb.WriteString("- " + x[i] + "\n")
			i++
		default:
			sb.WriteString("+ " + y[j] + "\n")
			j++
		}
	}
	return sb.String()
}

func lines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/colimacfg/ && go vet ./internal/colimacfg/`
Expected: `ok  github.com/aethons-tools/cove/internal/colimacfg`

- [ ] **Step 5: Commit**

```bash
git add internal/colimacfg
git commit -m "feat(colimacfg): pure colima-config edit for the Sysbox prerequisite"
```

---

### Task 2: share the Sysbox probe from the colima backend

**Files:**
- Modify: `internal/backend/colima/colima.go` — replace the `requireSysboxRuntime` doc comment + function (between `initArgs` and the `preflight` comment)
- Test: `internal/backend/colima/sysbox_test.go` (create)

**Interfaces:**
- Consumes: existing `dargs`, `runner.Runner`; test reuses the existing `sysboxRuntimesOutput` const from `colima_test.go`.
- Produces (used by Task 3): `func HasSysboxRuntime(r runner.Runner) (bool, error)` — calls `docker --context colima info -f {{json .Runtimes}}`; errors contain `cannot query docker runtimes` (unreachable) or `cannot parse docker runtimes` (garbage).

- [ ] **Step 1: Write the failing tests**

`internal/backend/colima/sysbox_test.go`:

```go
package colima

import (
	"errors"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

// TestHasSysboxRuntime pins the shared probe at-cove's preflight and
// `at-jam colima check-docker` both use: present, absent, unreachable, garbage.
func TestHasSysboxRuntime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		res     runner.FakeResult
		want    bool
		wantErr string
	}{
		{"present", runner.FakeResult{Stdout: sysboxRuntimesOutput}, true, ""},
		{"absent", runner.FakeResult{Stdout: `{"runc":{"path":"runc"}}`}, false, ""},
		{"unreachable", runner.FakeResult{Err: errors.New("exit 1")}, false, "cannot query docker runtimes"},
		{"garbage", runner.FakeResult{Stdout: "not json"}, false, "cannot parse docker runtimes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &runner.Fake{Outputs: []runner.FakeResult{tc.res}}
			got, err := HasSysboxRuntime(f)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("HasSysboxRuntime = %v, %v; want %v", got, err, tc.want)
			}
			if c := f.Calls[0]; c.Name != "docker" || strings.Join(c.Args, " ") != "--context colima info -f {{json .Runtimes}}" {
				t.Fatalf("probe call = %+v", c)
			}
		})
	}
}

// TestRequireSysboxRuntimeNamesSetupCommand: the preflight error points the
// operator at the one-command fix as well as the doc.
func TestRequireSysboxRuntimeNamesSetupCommand(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"runc":{"path":"runc"}}`}}}
	err := (&Colima{r: f}).requireSysboxRuntime()
	if err == nil || !strings.Contains(err.Error(), "at-jam colima setup-docker") || !strings.Contains(err.Error(), "docs/usage/docker-in-sandbox.md") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/backend/colima/ -run 'HasSysbox|NamesSetup'`
Expected: FAIL — `undefined: HasSysboxRuntime`.

- [ ] **Step 3: Implement** — replace the old `requireSysboxRuntime` (comment + body) with:

```go
// HasSysboxRuntime reports whether the colima VM's docker daemon registers the
// sysbox-runc runtime a docker:true instance needs (COV-117). It parses `docker
// info -f '{{json .Runtimes}}'`, a map of runtime name → config. An unreachable
// daemon or unparseable output is an error. Shared by the at-cove preflight and
// `at-jam colima check-docker`, so the two can't drift.
func HasSysboxRuntime(r runner.Runner) (bool, error) {
	out, err := r.Output("docker", dargs("info", "-f", "{{json .Runtimes}}")...)
	if err != nil {
		return false, fmt.Errorf("colima: cannot query docker runtimes (docker: %v)", err)
	}
	var runtimes map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &runtimes); err != nil {
		return false, fmt.Errorf("colima: cannot parse docker runtimes %q: %w", strings.TrimSpace(out), err)
	}
	_, ok := runtimes["sysbox-runc"]
	return ok, nil
}

// requireSysboxRuntime fails fast with an actionable message when the colima VM
// lacks the sysbox-runc runtime. at-cove detects but never installs Sysbox — the
// message points at the VM-side prerequisite. Preflight has already confirmed
// the daemon is reachable.
func (c *Colima) requireSysboxRuntime() error {
	ok, err := HasSysboxRuntime(c.r)
	if err != nil {
		return fmt.Errorf("docker:true preflight: %w", err)
	}
	if !ok {
		return fmt.Errorf("docker:true needs the Sysbox runtime (sysbox-runc) in the colima VM, but `docker info` does not list it. at-cove detects but does not install it — run `at-jam colima setup-docker` on the host (then `colima restart`), or install Sysbox CE in the colima Lima VM by hand and make it persist across `colima stop/start` via a colima provision hook, then retry. See docs/usage/docker-in-sandbox.md.")
	}
	return nil
}
```

- [ ] **Step 4: Run the whole package** (existing `TestCreateDockerPreflightRequiresSysbox` still asserts `sysbox-runc` + `Sysbox` in the message)

Run: `go test ./internal/backend/colima/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/backend/colima
git commit -m "refactor(colima): export HasSysboxRuntime; preflight names at-jam colima setup-docker"
```

---

### Task 3: the `at-jam colima` command

**Files:**
- Create: `cmd/at-jam/colima.go`
- Modify: `cmd/at-jam/main.go` — `run()` command list, insert before the `login` entry
- Test: `cmd/at-jam/colima_test.go` (create)

**Interfaces:**
- Consumes: Task 1 (`colimacfg.Apply/Diff/ValidateVersion/MinSysboxVersion/Change/Options`), Task 2 (`colima.HasSysboxRuntime`), `cli.ParseFlags`, `cli.Globals`.
- Produces: `func cmdColima(getenv func(string) string, r runner.Runner) func([]string, cli.Globals, io.Writer, io.Writer) int`; `func colimaConfigPath(getenv func(string) string) (string, error)`.

Exit codes: usage/flag/version errors → 2; I/O, parse, missing config, check failure → 1; success/no-op → 0. `--dry-run` works both as the subcommand flag and the global `at-jam --dry-run`.

- [ ] **Step 1: Write the failing tests**

`cmd/at-jam/colima_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/runner"
)

const freshColima = "cpu: 2\ndocker: {}\nprovision: []\n"

// colimaHome writes cfg as the default profile's colima.yaml under a temp
// COLIMA_HOME (mode 0600) and returns its getenv and the config path.
func colimaHome(t *testing.T, cfg string) (func(string) string, string) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, "default", "colima.yaml")
	if cfg != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return func(k string) string {
		if k == "COLIMA_HOME" {
			return home
		}
		return ""
	}, path
}

func runColima(getenv func(string) string, r runner.Runner, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := cmdColima(getenv, r)(args, cli.Globals{}, &out, &errb)
	return code, out.String(), errb.String()
}

func TestColimaConfigPath(t *testing.T) {
	got, err := colimaConfigPath(func(k string) string { return map[string]string{"HOME": "/h"}[k] })
	if err != nil || got != "/h/.colima/default/colima.yaml" {
		t.Fatalf("HOME fallback = %q, %v", got, err)
	}
	got, _ = colimaConfigPath(func(k string) string { return map[string]string{"HOME": "/h", "COLIMA_HOME": "/c"}[k] })
	if got != "/c/default/colima.yaml" {
		t.Fatalf("COLIMA_HOME = %q", got)
	}
	if _, err := colimaConfigPath(func(string) string { return "" }); err == nil {
		t.Fatal("no HOME/COLIMA_HOME must error")
	}
}

func TestColimaSetupDockerMissingConfig(t *testing.T) {
	getenv, _ := colimaHome(t, "")
	code, _, errs := runColima(getenv, &runner.Fake{}, "setup-docker")
	if code != 1 || !strings.Contains(errs, "run 'colima start' once") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestColimaSetupDockerDryRunWritesNothing(t *testing.T) {
	getenv, path := colimaHome(t, freshColima)
	code, out, errs := runColima(getenv, &runner.Fake{}, "setup-docker", "--dry-run")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if !strings.Contains(out, "+       path: /usr/bin/sysbox-runc") || !strings.Contains(out, "- docker: {}") {
		t.Fatalf("dry-run must print a diff:\n%s", out)
	}
	if b, _ := os.ReadFile(path); string(b) != freshColima {
		t.Fatalf("dry-run wrote the config:\n%s", b)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote a backup: %v", err)
	}
}

func TestColimaSetupDockerWritesThenNoOps(t *testing.T) {
	getenv, path := colimaHome(t, freshColima)
	code, out, errs := runColima(getenv, &runner.Fake{}, "setup-docker")
	if code != 0 || !strings.Contains(out, "colima restart") || !strings.Contains(out, "won't upgrade") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errs)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "sysbox-runc:") || !strings.Contains(string(b), "mode: system") {
		t.Fatalf("config not updated:\n%s", b)
	}
	if bak, _ := os.ReadFile(path + ".bak"); string(bak) != freshColima {
		t.Fatalf("backup = %q, want the original", bak)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 kept", info.Mode().Perm())
	}
	code, out, _ = runColima(getenv, &runner.Fake{}, "setup-docker")
	if code != 0 || !strings.Contains(out, "already set up") {
		t.Fatalf("second run must no-op: code=%d stdout=%q", code, out)
	}
	if again, _ := os.ReadFile(path); !bytes.Equal(again, b) {
		t.Fatal("second run changed the file")
	}
}

func TestColimaSetupDockerRejectsOldVersion(t *testing.T) {
	getenv, _ := colimaHome(t, freshColima)
	code, _, errs := runColima(getenv, &runner.Fake{}, "setup-docker", "--sysbox-version", "0.6.9")
	if code != 2 || !strings.Contains(errs, "older than") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestColimaCheckDocker(t *testing.T) {
	getenv := func(string) string { return "" }
	ok := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"sysbox-runc":{"path":"/usr/bin/sysbox-runc"}}`}}}
	if code, out, _ := runColima(getenv, ok, "check-docker"); code != 0 || !strings.Contains(out, "ok:") {
		t.Fatalf("present: code=%d out=%q", code, out)
	}
	absent := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"runc":{}}`}}}
	if code, _, errs := runColima(getenv, absent, "check-docker"); code != 1 || !strings.Contains(errs, "at-jam colima setup-docker") {
		t.Fatalf("absent: code=%d stderr=%q", code, errs)
	}
}

func TestColimaUnknownSubcommand(t *testing.T) {
	if code, _, _ := runColima(func(string) string { return "" }, &runner.Fake{}, "bogus"); code != 2 {
		t.Fatalf("code=%d", code)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/at-jam/ -run Colima`
Expected: FAIL — `undefined: cmdColima`.

- [ ] **Step 3: Implement**

`cmd/at-jam/colima.go`:

```go
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/backend/colima"
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/colimacfg"
	"github.com/aethons-tools/cove/internal/runner"
)

// cmdColima returns the `at-jam colima` group: host-side setup of the colima VM
// docker:true kits need (docs/usage/docker-in-sandbox.md). getenv resolves the
// config path (COLIMA_HOME, HOME) and r runs the check's docker probe, both
// injected so the command stays hermetic in tests.
func cmdColima(getenv func(string) string, r runner.Runner) func([]string, cli.Globals, io.Writer, io.Writer) int {
	return func(args []string, g cli.Globals, stdout, stderr io.Writer) int {
		if len(args) == 0 {
			fmt.Fprintln(stderr, "at-jam colima: expected setup-docker|check-docker")
			return 2
		}
		switch sub, rest := args[0], args[1:]; sub {
		case "setup-docker":
			return colimaSetupDocker(rest, g, getenv, stdout, stderr)
		case "check-docker":
			return colimaCheckDocker(rest, r, stdout, stderr)
		default:
			fmt.Fprintf(stderr, "at-jam colima: unknown subcommand %q (expected setup-docker|check-docker)\n", sub)
			return 2
		}
	}
}

// colimaConfigPath is the default profile's colima.yaml — the only profile a cove
// uses, since the colima backend pins docker to the `colima` context.
func colimaConfigPath(getenv func(string) string) (string, error) {
	home := getenv("COLIMA_HOME")
	if home == "" {
		h := getenv("HOME")
		if h == "" {
			return "", fmt.Errorf("cannot locate the colima config: neither COLIMA_HOME nor HOME is set")
		}
		home = filepath.Join(h, ".colima")
	}
	return filepath.Join(home, "default", "colima.yaml"), nil
}

func colimaSetupDocker(args []string, g cli.Globals, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("colima setup-docker", flag.ContinueOnError)
	version := fs.String("sysbox-version", colimacfg.MinSysboxVersion, "Sysbox CE release the provision hook installs (≥ "+colimacfg.MinSysboxVersion+")")
	dry := fs.Bool("dry-run", false, "print the change as a diff and write nothing")
	if _, code, ok := cli.ParseFlags(fs, args, stdout, stderr); !ok {
		return code
	}
	if err := colimacfg.ValidateVersion(*version); err != nil {
		fmt.Fprintln(stderr, "at-jam colima setup-docker:", err)
		return 2
	}
	path, err := colimaConfigPath(getenv)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam colima setup-docker:", err)
		return 1
	}
	in, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Fprintf(stderr, "at-jam colima setup-docker: colima config not found at %s — run 'colima start' once to create it\n", path)
		return 1
	} else if err != nil {
		fmt.Fprintln(stderr, "at-jam colima setup-docker:", err)
		return 1
	}
	out, changes, err := colimacfg.Apply(in, colimacfg.Options{SysboxVersion: *version})
	if err != nil {
		fmt.Fprintf(stderr, "at-jam colima setup-docker: %s: %v\n", path, err)
		return 1
	}
	if len(changes) == 0 {
		fmt.Fprintf(stdout, "colima config already set up for docker:true (%s)\n", path)
		fmt.Fprintln(stdout, "verify the running VM with: at-jam colima check-docker")
		return 0
	}
	if *dry || g.DryRun {
		fmt.Fprintf(stdout, "would change %s:\n", path)
		printChanges(stdout, changes)
		fmt.Fprintln(stdout)
		fmt.Fprint(stdout, colimacfg.Diff(in, out))
		return 0
	}
	if err := writeColimaConfig(path, in, out); err != nil {
		fmt.Fprintln(stderr, "at-jam colima setup-docker:", err)
		return 1
	}
	fmt.Fprintf(stdout, "updated %s (backup: %s.bak):\n", path, path)
	printChanges(stdout, changes)
	fmt.Fprintf(stdout, `
next:
  colima restart                  # runs the hook; restarts every cove in the VM
  at-jam colima check-docker      # confirm sysbox-runc is registered

note: the hook skips the install when sysbox-runc is already present, so it
won't upgrade an older Sysbox in an existing VM — upgrade that once by hand:
  colima ssh -- sudo sh -c 'curl -fsSL -o /tmp/sysbox.deb https://github.com/nestybox/sysbox/releases/download/v%[1]s/sysbox-ce_%[1]s.linux_$(dpkg --print-architecture).deb && apt-get install -y /tmp/sysbox.deb'
`, *version)
	return 0
}

func printChanges(w io.Writer, changes []colimacfg.Change) {
	for _, c := range changes {
		fmt.Fprintln(w, "  -", c.What)
	}
}

// writeColimaConfig backs the original up to <path>.bak, then replaces path
// atomically (temp file in the same dir + rename), keeping its file mode.
func writeColimaConfig(path string, orig, next []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if err := os.WriteFile(path+".bak", orig, mode); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".colima.yaml.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(next); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func colimaCheckDocker(args []string, r runner.Runner, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("colima check-docker", flag.ContinueOnError)
	if _, code, ok := cli.ParseFlags(fs, args, stdout, stderr); !ok {
		return code
	}
	ok, err := colima.HasSysboxRuntime(r)
	if err != nil {
		fmt.Fprintf(stderr, "at-jam colima check-docker: %v — is colima running? (colima start)\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stderr, "at-jam colima check-docker: sysbox-runc is not registered in the colima VM's docker — run `at-jam colima setup-docker`, then `colima restart`")
		return 1
	}
	fmt.Fprintln(stdout, "ok: sysbox-runc is registered — docker:true kits can run")
	return 0
}
```

In `cmd/at-jam/main.go` `run()`, add before the `login` entry (`runner` is already imported):

```go
			{Name: "colima", Brief: "set up the host colima VM for docker:true kits (setup-docker|check-docker) — edits the colima config, no admin API", Run: cmdColima(getenv, runner.OS{})},
```

- [ ] **Step 4: Run tests + lint**

Run: `go test ./cmd/at-jam/ && just lint`
Expected: `ok` and a clean lint.

- [ ] **Step 5: Smoke it by hand against a temp home**

```bash
d=$(mktemp -d); mkdir -p $d/default; printf 'cpu: 2\ndocker: {}\nprovision: []\n' > $d/default/colima.yaml
COLIMA_HOME=$d go run ./cmd/at-jam colima setup-docker --dry-run   # diff, file untouched
COLIMA_HOME=$d go run ./cmd/at-jam colima setup-docker             # writes + .bak + next steps
COLIMA_HOME=$d go run ./cmd/at-jam colima setup-docker             # "already set up"
```

- [ ] **Step 6: Commit**

```bash
git add cmd/at-jam/colima.go cmd/at-jam/colima_test.go cmd/at-jam/main.go
git commit -m "feat(jam): at-jam colima setup-docker|check-docker"
```

---

### Task 4: docs

**Files:**
- Modify: `docs/usage/docker-in-sandbox.md` (frontmatter + "Prerequisite" section)
- Modify: `docs/usage/INDEX.md` (docker-in-sandbox row)
- Modify: `docs/usage/jam/INDEX.md` (new row)

Use the **docs-author** skill; the docker-in-sandbox doc owns the command's usage — don't duplicate it elsewhere.

- [ ] **Step 1: `docker-in-sandbox.md` frontmatter** — set `updated: 2026-10-06`; append to `owns:` "…, and the `at-jam colima setup-docker`/`check-docker` commands that automate it"; in `read_when:` change "installing Sysbox in the colima VM" to "installing Sysbox in the colima VM (`at-jam colima setup-docker`)".

- [ ] **Step 2: Prerequisite section** — directly after its opening paragraph (ending "…register the runtime through colima's own config instead."), insert:

````markdown
**The quick way — `at-jam colima setup-docker`.** On the colima host, run:

```console
$ at-jam colima setup-docker --dry-run   # show the change as a diff, write nothing
$ at-jam colima setup-docker             # write it (backs up colima.yaml.bak first)
$ colima restart                         # runs the hook — restarts every cove in the VM
$ at-jam colima check-docker             # ok: sysbox-runc is registered
```

It edits the default profile's config (`$COLIMA_HOME/default/colima.yaml`, else
`~/.colima/default/colima.yaml` — the profile behind the `colima` docker context
at-cove uses) and writes exactly the two pieces below: one provision hook it owns
(marked `# managed by at-jam colima setup-docker`; re-running replaces it, your
other hooks are kept) and the `sysbox-runc` runtime entry. It is idempotent — a
re-run with nothing to change leaves the file untouched. `--sysbox-version`
(default and minimum `0.7.1`) picks the release. A run that *does* change the file
keeps your comments but normalizes its formatting (indentation, blank lines). It
never restarts colima for you. `check-docker` runs the same probe as at-cove's
preflight and exits non-zero when the runtime is missing.

To do it by hand instead (or to see what the command writes):
````

Then keep the existing manual steps **1.** and **2.** unchanged. In the "Version floor" note paragraph that begins "> The provision hook is idempotent on the binary", append: "`at-jam colima setup-docker` prints the one-line upgrade command after it writes the hook."

- [ ] **Step 3: `docs/usage/INDEX.md`** — in the docker-in-sandbox row, change "the one-time Sysbox setup in the colima VM (install hook + colima `docker:` runtime registration)" to "the one-time Sysbox setup in the colima VM (`at-jam colima setup-docker`, or by hand: install hook + colima `docker:` runtime registration)".

- [ ] **Step 4: `docs/usage/jam/INDEX.md`** — append a row to the "Which doc for which task" table:

```markdown
| [../docker-in-sandbox.md](../docker-in-sandbox.md#prerequisite-install-sysbox-in-the-colima-vm-one-time) | You are preparing a Jam host's colima VM for `docker: true` kits — `at-jam colima setup-docker` / `check-docker`. |
```

and set its frontmatter `updated: 2026-10-06`.

- [ ] **Step 5: Audit** — run the **docs-audit** skill; fix any finding (dangling anchor, oversize, duplication).

- [ ] **Step 6: Commit**

```bash
git add docs/usage/docker-in-sandbox.md docs/usage/INDEX.md docs/usage/jam/INDEX.md
git commit -m "docs: at-jam colima setup-docker for the Sysbox prerequisite"
```

---

### Task 5: full verification

- [ ] `just test` — all green.
- [ ] `just lint` — clean.
- [ ] `git diff main --stat` — only the files listed in File Structure (+ the spec/plan).
