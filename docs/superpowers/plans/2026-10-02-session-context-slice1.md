# Session context — slice 1 (bundle + delivery) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every Jam-raised session gets a compiled context bundle (Boilerplate per session kind + Kit) delivered as an appended system prompt on every turn plus leaf files under `/agent-data/context/`, replacing `studio.ComposePrompt`.

**Architecture:** A pure package `internal/jam/sessionctx` compiles `Inputs → Bundle{Core, Files, Fingerprint, Layers}`. `Supervisor.Raise` compiles it and puts it on `RaiseSpec.Context`; the launcher stages it as JSON in tmpfs beside the prompt; cove-master loads it into `agentrun.Config.Context`; the agent wrapper writes the files atomically once at start and adds `--append-system-prompt-file <dir>/CORE.md --system-prompt-snapshot off` to every `claude` spawn. The Launch prompt alone stays the `-p` positional argument.

**Tech Stack:** Go 1.27 (stdlib only: `crypto/sha256`, `encoding/json`, `os`, `path/filepath`, `strings`, `unicode/utf8`), `just test`, `go test`.

**Spec:** [`docs/superpowers/specs/2026-10-02-session-context-layers-design.md`](../specs/2026-10-02-session-context-layers-design.md) — this plan is **Slice 1** only.

## Global Constraints

- Delivery order is Boilerplate → Kit (→ Studio → Project → Role → Jam in later slices). Precedence text, verbatim: `Sandbox hardening is enforced and cannot be overridden. Otherwise, on conflict the later section wins: Jam rules > Role > Project > Studio > Kit > Boilerplate. Your task (the first message) works within all of them.`
- Core budgets (bytes, UTF-8): Boilerplate 2400, Kit 800.
- Empty layer → no section, no header, no placeholder.
- Compile never fails a raise: over-budget cores are truncated at a line boundary with `(truncated — see <layer>/CORE-full.md)` and the full text written as that leaf.
- Bundle files live under `/agent-data/context/` (`CORE.md`, `INDEX.md`, `<layer>/<leaf>.md`); cove-master owns the directory and replaces it wholesale.
- `claude` flags: `--append-system-prompt-file /agent-data/context/CORE.md --system-prompt-snapshot off` — both required whenever a bundle is in effect, neither when it is not.
- No secret value, credential name, or identity token ever appears in a bundle.
- Tests are hermetic (no VM, no network). TDD: failing test first.
- Docs updated in the same PR (AGENTS.md rule). Template files under `internal/assemble/*/image-files/` are sandbox payload, not this repo's config.
- Dev sandbox: run Go with `GOPROXY=https://proxy.golang.org,direct` if `GOPROXY=direct` is set (golang.org/x and google.golang.org are not reachable directly).

## Review Focus

1. **A leaf name that escapes the directory** (`../x.md`, `a/b.md`, `/etc/x`, empty) — expected: Compile drops it with a warning; the agentrun writer independently refuses any non-local path. Pinned in Task 1 and Task 5.
2. **The context directory cannot be written** (missing parent, permission) — expected: cove-master logs a warning and runs `claude` *without* the two flags (claude hard-fails on a missing `--append-system-prompt-file`). Pinned in Task 5.
3. **An older launcher / no context file / malformed JSON** — expected: cove-master starts the agent exactly as today (no flags, no directory). Pinned in Task 5.
4. **Truncating multibyte text** — expected: never splits a UTF-8 rune; falls back to a rune boundary when there is no newline before the budget. Pinned in Task 1.
5. **A cove re-raised on an existing `/agent-data` volume** — expected: leftover files from the previous bundle are gone after the write. Pinned in Task 5.

---

## File structure

| File | Responsibility |
|------|----------------|
| `internal/jam/sessionctx/sessionctx.go` (create) | Types, constants, `Compile`, truncation, fingerprint, INDEX/leaf rendering |
| `internal/jam/sessionctx/boilerplate.go` (create) | `Boilerplate(SessionFacts) Layer` — per-kind text + the changing-the-kit leaf |
| `internal/jam/sessionctx/sessionctx_test.go`, `boilerplate_test.go` (create) | Table tests |
| `internal/jam/supervisor.go` (modify ~29-57, ~240-249) | `RaiseSpec.Context`; compile at raise |
| `internal/standing/standing.go` (modify ~200-228), `internal/jam/sessions.go` (modify ~214-254) | Drop per-kind preambles (now Boilerplate) |
| `internal/studio/prompt.go`, `prompt_test.go` (delete) | Superseded by sessionctx |
| `internal/studio/studiokit.go` (modify `Validate`) | Kit prompt ≤ 800 bytes |
| `internal/connect/covemaster.go` (modify) | Stage bundle JSON to `/dev/shm/cove-agent-context`; `AT_COVE_AGENT_CONTEXT_FILE` |
| `internal/jam/launcher/launcher.go` (modify ~160) | Pass `spec.Context` |
| `cmd/cove-master/main.go` (modify `buildAgentConfig`) | Load the bundle |
| `internal/agentrun/context.go` (create), `workload.go` (modify) | Atomic write + claude flags |
| `internal/assemble/hardening/image-files/home/agent/.init-agent-data/SANDBOX.md`, `COLLABORATOR.md` (modify); `internal/connect/connect.go:329-336` | Jam branch; empty default |
| Docs: `docs/usage/jam/session-context.md` (create), `docs/usage/jam/INDEX.md`, `kits.md`, `coves.md`, `standing-sessions.md`, `personal-sessions.md`, `docs/OVERVIEW.md` | Owner doc + pointers |

---

### Task 1: `sessionctx` — types and `Compile`

**Files:**
- Create: `internal/jam/sessionctx/sessionctx.go`
- Create: `internal/jam/sessionctx/boilerplate.go` (stub in this task: returns `Layer{}`; Task 2 fills it)
- Test: `internal/jam/sessionctx/sessionctx_test.go`

**Interfaces:**
- Produces:
  - `const Dir = "/agent-data/context"`; `const KindEphemeral, KindPersonal, KindStanding = "ephemeral", "personal", "standing"`; `const LayerBoilerplate, LayerKit = "boilerplate", "kit"`; `const BudgetBoilerplate, BudgetKit = 2400, 800`
  - `type Leaf struct { Name, ReadWhen, Body string }` (json `name`, `read_when`, `body`)
  - `type Layer struct { Core string; Leaves []Leaf }` (json `core`, `leaves,omitempty`); `func (l Layer) Empty() bool`
  - `type SessionFacts struct { Kind, Name, Project, Role, Owner, Kit string }` — `Kit` is `KitRef.String()` or ""
  - `type Inputs struct { Session SessionFacts; Kit Layer }`
  - `type Bundle struct { Core string; Files map[string]string; Fingerprint string; Layers map[string]string; Warnings []string }` (json `core`, `files`, `fingerprint`, `layers`, `warnings,omitempty`)
  - `func Compile(in Inputs) Bundle`
  - `func ValidLeafName(name string) bool`
  - `func Boilerplate(f SessionFacts) Layer` (stub here)

- [ ] **Step 1: Write the failing tests**

```go
package sessionctx

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func compileKit(core string, leaves ...Leaf) Bundle {
	return Compile(Inputs{Session: SessionFacts{Kind: KindStanding, Name: "s", Project: "p", Role: "r", Kit: "web@v3"}, Kit: Layer{Core: core, Leaves: leaves}})
}

func TestCompileHeaderAndOrder(t *testing.T) {
	b := compileKit("KITCORE")
	if !strings.HasPrefix(b.Core, "# Session context\n") {
		t.Fatalf("core must open with the header:\n%s", b.Core)
	}
	if !strings.Contains(b.Core, Precedence) {
		t.Fatal("core must state precedence verbatim")
	}
	kit := strings.Index(b.Core, "## Kit — web@v3")
	if kit < 0 || !strings.Contains(b.Core[kit:], "KITCORE") {
		t.Fatalf("kit section missing:\n%s", b.Core)
	}
	if bp := strings.Index(b.Core, "## Boilerplate"); bp >= 0 && bp > kit {
		t.Fatal("boilerplate must precede kit")
	}
}

func TestCompileOmitsEmptyLayer(t *testing.T) {
	b := compileKit("   \n")
	if strings.Contains(b.Core, "## Kit") {
		t.Fatalf("empty kit layer must emit nothing:\n%s", b.Core)
	}
	if _, ok := b.Layers[LayerKit]; ok {
		t.Fatal("empty layer must have no fingerprint entry")
	}
}

func TestCompileLeavesListedAndWritten(t *testing.T) {
	b := compileKit("K", Leaf{Name: "tools.md", ReadWhen: "you need a tool version", Body: "Go 1.27"})
	if !strings.Contains(b.Core, "- kit/tools.md — read when you need a tool version") {
		t.Fatalf("core must list the leaf:\n%s", b.Core)
	}
	f, ok := b.Files["kit/tools.md"]
	if !ok || !strings.Contains(f, "read_when: you need a tool version") || !strings.HasSuffix(strings.TrimSpace(f), "Go 1.27") {
		t.Fatalf("leaf file wrong: %q", f)
	}
	idx := b.Files["INDEX.md"]
	if !strings.Contains(idx, "| kit/tools.md | you need a tool version |") {
		t.Fatalf("INDEX must list every leaf:\n%s", idx)
	}
	for p := range b.Files {
		if p != "INDEX.md" && !strings.Contains(idx, "| "+p+" |") {
			t.Errorf("file %s not in INDEX", p)
		}
	}
}

func TestCompileDropsUnsafeLeafNames(t *testing.T) {
	for _, bad := range []string{"../x.md", "a/b.md", "/etc/x.md", "", ".md", "x.txt", "CORE.md"} {
		b := compileKit("K", Leaf{Name: bad, ReadWhen: "w", Body: "b"})
		for p := range b.Files {
			if p != "INDEX.md" {
				t.Errorf("%q: unsafe leaf written as %q", bad, p)
			}
		}
		if len(b.Warnings) == 0 {
			t.Errorf("%q: want a warning", bad)
		}
	}
}

func TestCompileTruncatesOverBudget(t *testing.T) {
	long := strings.Repeat("line of kit text\n", 100) // 1700 bytes > 800
	b := compileKit(long)
	sec := b.Core[strings.Index(b.Core, "## Kit"):]
	if !strings.Contains(sec, "(truncated — see kit/CORE-full.md)") {
		t.Fatalf("want truncation note:\n%s", sec)
	}
	if b.Files["kit/CORE-full.md"] == "" || !strings.Contains(b.Files["kit/CORE-full.md"], strings.TrimSpace(long)) {
		t.Fatal("full core must be kept as a leaf")
	}
	if len(b.Warnings) == 0 {
		t.Fatal("truncation must warn")
	}
}

func TestTruncateNeverSplitsARune(t *testing.T) {
	s := strings.Repeat("é", 1000) // 2000 bytes, no newline
	got := truncateCore(s, 800)
	if !utf8.ValidString(got) || len(got) > 800 {
		t.Fatalf("invalid or over-budget: len=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestCompileDeterministic(t *testing.T) {
	a := compileKit("K", Leaf{Name: "b.md", ReadWhen: "w", Body: "x"}, Leaf{Name: "a.md", ReadWhen: "w", Body: "y"})
	for range 20 {
		if b := compileKit("K", Leaf{Name: "b.md", ReadWhen: "w", Body: "x"}, Leaf{Name: "a.md", ReadWhen: "w", Body: "y"}); b.Fingerprint != a.Fingerprint || b.Core != a.Core {
			t.Fatal("same inputs must give the same bytes")
		}
	}
	if c := compileKit("K2"); c.Fingerprint == a.Fingerprint {
		t.Fatal("different inputs must change the fingerprint")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jam/sessionctx/`
Expected: FAIL — package/identifiers undefined.

- [ ] **Step 3: Implement**

`internal/jam/sessionctx/sessionctx.go`:

```go
// Package sessionctx compiles a Jam session's layered context — Boilerplate,
// Kit (and, in later slices, Studio, Project, Role, Jam) — into a Bundle: a
// short always-on core, delivered as an appended system prompt every turn, plus
// leaf files the agent opens on demand under Dir. Pure: no I/O. See
// docs/usage/jam/session-context.md.
package sessionctx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Dir is where cove-master writes a bundle inside the cove.
const Dir = "/agent-data/context"

// Session kinds (mirror jam.SessionKind*; "" is ephemeral).
const (
	KindEphemeral = "ephemeral"
	KindPersonal  = "personal"
	KindStanding  = "standing"
)

// Layer names, in delivery order; each is also the leaf directory name.
const (
	LayerBoilerplate = "boilerplate"
	LayerKit         = "kit"
)

// Core budgets in bytes.
const (
	BudgetBoilerplate = 2400
	BudgetKit         = 800
)

// Precedence is stated once, verbatim, at the top of every core.
const Precedence = "Sandbox hardening is enforced and cannot be overridden. Otherwise, on conflict the later section wins: " +
	"Jam rules > Role > Project > Studio > Kit > Boilerplate. Your task (the first message) works within all of them."

// Leaf is one on-demand file of a layer.
type Leaf struct {
	Name     string `json:"name"`      // file name under the layer dir, e.g. "tools.md"
	ReadWhen string `json:"read_when"` // one line: when to open it
	Body     string `json:"body"`      // markdown
}

// Layer is one layer's contribution: an always-on core and on-demand leaves.
type Layer struct {
	Core   string `json:"core"`
	Leaves []Leaf `json:"leaves,omitempty"`
}

// Empty reports whether the layer contributes nothing.
func (l Layer) Empty() bool { return strings.TrimSpace(l.Core) == "" && len(l.Leaves) == 0 }

// SessionFacts identify the session the bundle is for.
type SessionFacts struct {
	Kind    string // KindEphemeral ("" too) | KindPersonal | KindStanding
	Name    string // standing session name
	Project string
	Role    string
	Owner   string // personal session owner
	Kit     string // KitRef.String(), "" = no kit
}

// Inputs are everything Compile needs. Later slices add Studio/Project/Role/Jam.
type Inputs struct {
	Session SessionFacts
	Kit     Layer
}

// Bundle is the compiled context. Files are relative to Dir.
type Bundle struct {
	Core        string            `json:"core"`
	Files       map[string]string `json:"files"`
	Fingerprint string            `json:"fingerprint"`
	Layers      map[string]string `json:"layers"` // layer → fingerprint, for change notices
	Warnings    []string          `json:"warnings,omitempty"`
}

var leafNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*\.md$`)

// ValidLeafName reports whether name is a safe single-segment leaf file name.
// CORE-full.md is reserved for truncation.
func ValidLeafName(name string) bool {
	return leafNameRE.MatchString(name) && name != "CORE.md" && name != "INDEX.md"
}

type section struct {
	name, title string
	layer       Layer
	budget      int
}

// Compile builds the bundle. It never fails: unsafe leaves are dropped and
// over-budget cores truncated, each with a Warning.
func Compile(in Inputs) Bundle {
	kitTitle := "Kit"
	if in.Session.Kit != "" {
		kitTitle += " — " + in.Session.Kit
	}
	sections := []section{
		{LayerBoilerplate, "Boilerplate", Boilerplate(in.Session), BudgetBoilerplate},
		{LayerKit, kitTitle, in.Kit, BudgetKit},
	}
	b := Bundle{Files: map[string]string{}, Layers: map[string]string{}}
	var core strings.Builder
	fmt.Fprintf(&core, "# Session context\n\n%s\n\nDetail lives in %s/ — open a file only when its \"read when\" matches your task. Map: %s/INDEX.md\n", Precedence, Dir, Dir)
	type row struct{ path, when string }
	var index []row
	for _, s := range sections {
		if s.layer.Empty() {
			continue
		}
		l := s.layer
		c := strings.TrimSpace(l.Core)
		if len(c) > s.budget {
			b.Warnings = append(b.Warnings, fmt.Sprintf("%s core is %d bytes (budget %d); truncated", s.name, len(c), s.budget))
			note := fmt.Sprintf("\n(truncated — see %s/CORE-full.md)", s.name)
			l.Leaves = append(slices.Clone(l.Leaves), Leaf{Name: "CORE-full.md", ReadWhen: "the " + s.name + " core above was truncated and you need the rest", Body: c})
			c = truncateCore(c, s.budget-len(note)) + note
		}
		fmt.Fprintf(&core, "\n## %s\n", s.title)
		if c != "" {
			core.WriteString(c + "\n")
		}
		h := sha256.New()
		h.Write([]byte(c))
		for _, lf := range l.Leaves {
			if !ValidLeafName(lf.Name) && lf.Name != "CORE-full.md" {
				b.Warnings = append(b.Warnings, fmt.Sprintf("%s leaf %q dropped: unsafe name", s.name, lf.Name))
				continue
			}
			p := s.name + "/" + lf.Name
			when := oneLine(lf.ReadWhen)
			fmt.Fprintf(&core, "- %s — read when %s\n", p, when)
			body := fmt.Sprintf("---\nsummary: %s layer — %s\nread_when: %s\ntier: leaf\n---\n\n%s\n", s.name, lf.Name, when, strings.TrimSpace(lf.Body))
			b.Files[p] = body
			index = append(index, row{p, when})
			h.Write([]byte(p + "\x00" + body + "\x00"))
		}
		b.Layers[s.name] = hex.EncodeToString(h.Sum(nil))
	}
	var idx strings.Builder
	idx.WriteString("# Session context index\n\nOpen a file only when its \"read when\" matches your task.\n\n| File | Read when |\n|------|-----------|\n")
	for _, r := range index {
		fmt.Fprintf(&idx, "| %s | %s |\n", r.path, r.when)
	}
	b.Files["INDEX.md"] = idx.String()
	b.Core = core.String()
	b.Fingerprint = fingerprint(b.Core, b.Files)
	return b
}

// truncateCore cuts s to at most max bytes, at the last newline if there is
// one, else at a rune boundary.
func truncateCore(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 0 {
		return ""
	}
	cut := s[:max]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		return strings.TrimRight(cut[:i], "\n")
	}
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(s, "|", "/")
}

func fingerprint(core string, files map[string]string) string {
	h := sha256.New()
	h.Write([]byte(core + "\x00"))
	for _, k := range slices.Sorted(maps.Keys(files)) {
		h.Write([]byte(k + "\x00" + files[k] + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
```

`internal/jam/sessionctx/boilerplate.go` (stub for this task):

```go
package sessionctx

// Boilerplate is the always-present first layer for a session of f.Kind.
func Boilerplate(f SessionFacts) Layer { return Layer{} }
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/jam/sessionctx/`
Expected: PASS (`TestCompileHeaderAndOrder` passes with the stub because the Boilerplate section is simply absent).

- [ ] **Step 5: Commit**

```bash
git add internal/jam/sessionctx
git commit -m "feat(sessionctx): compile layered session context bundles"
```

---

### Task 2: Boilerplate per session kind

**Files:**
- Modify: `internal/jam/sessionctx/boilerplate.go`
- Test: `internal/jam/sessionctx/boilerplate_test.go`

**Interfaces:**
- Consumes: Task 1 types.
- Produces: `func Boilerplate(f SessionFacts) Layer` with core + leaf `changing-the-kit.md`.

- [ ] **Step 1: Write the failing tests**

```go
package sessionctx

import (
	"strings"
	"testing"
)

func TestBoilerplatePerKind(t *testing.T) {
	cases := []struct {
		f        SessionFacts
		want     []string
		mustNot  []string
	}{
		{SessionFacts{Kind: KindStanding, Name: "alice-bot", Project: "acme", Role: "reviewer"},
			[]string{`the standing session "alice-bot" for role reviewer in project acme`, "always pass `to`", "until an operator removes you"},
			[]string{"owner"}},
		{SessionFacts{Kind: KindPersonal, Owner: "alice", Project: "acme", Role: "pair"},
			[]string{"a personal session for alice", "`send` without `to` reaches alice", "until alice releases"},
			nil},
		{SessionFacts{Kind: "", Project: "acme", Role: "worker"},
			[]string{"an ephemeral worker session for role worker in project acme", "without `to` posts to your ticket"},
			[]string{"owner"}},
	}
	for _, c := range cases {
		l := Boilerplate(c.f)
		for _, w := range c.want {
			if !strings.Contains(l.Core, w) {
				t.Errorf("%s: missing %q in\n%s", c.f.Kind, w, l.Core)
			}
		}
		for _, w := range c.mustNot {
			if strings.Contains(l.Core, w) {
				t.Errorf("%s: must not contain %q", c.f.Kind, w)
			}
		}
		if len(l.Core) > BudgetBoilerplate {
			t.Errorf("%s: boilerplate %d bytes > budget %d", c.f.Kind, len(l.Core), BudgetBoilerplate)
		}
		for _, common := range []string{"one `claude -p` run", "Background processes", "allow-listed", "/home/agent/workspace", "/agent-data/reference/sandbox-hardening-limits.md"} {
			if !strings.Contains(l.Core, common) {
				t.Errorf("%s: missing common %q", c.f.Kind, common)
			}
		}
		if len(l.Leaves) != 1 || l.Leaves[0].Name != "changing-the-kit.md" || !strings.Contains(l.Leaves[0].Body, "at-jam kit push") {
			t.Errorf("%s: want the changing-the-kit leaf, got %+v", c.f.Kind, l.Leaves)
		}
	}
}

func TestCompiledBundleNeverTruncatesBoilerplate(t *testing.T) {
	for _, k := range []string{KindEphemeral, KindPersonal, KindStanding} {
		b := Compile(Inputs{Session: SessionFacts{Kind: k, Name: strings.Repeat("n", 64), Owner: strings.Repeat("o", 64), Project: strings.Repeat("p", 64), Role: strings.Repeat("r", 64)}})
		if len(b.Warnings) != 0 {
			t.Errorf("%s: %v", k, b.Warnings)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jam/sessionctx/ -run Boilerplate`
Expected: FAIL — empty core.

- [ ] **Step 3: Implement**

```go
package sessionctx

import (
	"fmt"
	"strings"
)

// Boilerplate is the always-present first layer: who this session is, the
// sandbox and turn model, and the intercom rules for its kind. It owns the
// facts the old per-kind preambles (standing.Prompt, personalPrompt) carried.
func Boilerplate(f SessionFacts) Layer {
	var who, comms string
	switch f.Kind {
	case KindStanding:
		who = fmt.Sprintf("the standing session %q for role %s in project %s", f.Name, f.Role, f.Project)
		comms = "You have no default recipient: always pass `to` (`list_targets` shows who you may message). " +
			"A message to you wakes you. You run until an operator removes you."
	case KindPersonal:
		who = fmt.Sprintf("a personal session for %s (role %s, project %s)", f.Owner, f.Role, f.Project)
		comms = fmt.Sprintf("Your owner is %[1]s: `send` without `to` reaches %[1]s, and their reply wakes you. "+
			"You stay open until %[1]s releases you.", f.Owner)
	default:
		who = fmt.Sprintf("an ephemeral worker session for role %s in project %s", f.Role, f.Project)
		comms = "`send` without `to` posts to your ticket. If you need input, ask there and end your turn; a reply wakes you."
	}
	core := strings.Join([]string{
		"You are " + who + ", running in a Jam-managed at-cove sandbox.",
		"- Sandbox: isolated filesystem; network egress is allow-listed through a proxy. A connection or proxy error to a host means it is not allowed — not a transient fault: don't retry or hunt for mirrors. Only /home/agent/workspace and /agent-data persist; installed packages and env tweaks reset when the cove is rebuilt.",
		"- Turns: each turn is one `claude -p` run. Background processes you start die when the turn ends — finish work within the turn. Ending your turn is how you wait.",
		"- Intercom: `send` messages people, `read` fetches your inbox, `commit` marks messages handled. " + comms,
		"- Changing the sandbox (a domain, a tool) is human-gated. Hardening limits: /agent-data/reference/sandbox-hardening-limits.md.",
	}, "\n")
	return Layer{Core: core, Leaves: []Leaf{{
		Name:     "changing-the-kit.md",
		ReadWhen: "you need an egress domain, tool or env var the sandbox lacks",
		Body:     changingTheKit,
	}}}
}

const changingTheKit = `# Changing the kit

You cannot rebuild your own cove or widen its access. When you need something the sandbox lacks:

1. Work out the exact change: an egress domain, a tool and version, an env var.
2. Message a human (your owner, or a project contact) with the change and why. Name where it goes: the role's studio kit ` + "`kit.yml`" + ` (` + "`egress:`" + `, ` + "`build-args:`" + `, ` + "`base:`" + `), or the role's egress policy (` + "`at-jam egress set`" + `), which an operator manages.
3. It takes effect after the kit is pushed (` + "`at-jam kit push`" + `) and the cove is re-raised; an egress-policy change applies at the next raise.

A one-off install or ` + "`export`" + ` in this session is fine for now but will not survive a rebuild.`
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/jam/sessionctx/`
Expected: PASS (all Task 1 and Task 2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/jam/sessionctx
git commit -m "feat(sessionctx): per-kind Jam boilerplate layer"
```

---

### Task 3: Compile at raise; drop `ComposePrompt` and the per-kind preambles; kit prompt budget

**Files:**
- Modify: `internal/jam/supervisor.go` (RaiseSpec ~29-57; Raise ~240-249)
- Modify: `internal/standing/standing.go` (~200-228), `internal/standing/standing_test.go` (~123, ~300)
- Modify: `internal/jam/sessions.go` (~214-254), `internal/jam/sessions_test.go` (~111, ~324)
- Modify: `internal/jam/supervisor_test.go` (`TestRaiseComposesPrompt` ~276-291)
- Modify: `internal/studio/studiokit.go` (`Validate`), test in `internal/studio/studiokit_test.go`
- Delete: `internal/studio/prompt.go`, `internal/studio/prompt_test.go`

**Interfaces:**
- Consumes: `sessionctx.Compile`, `sessionctx.Inputs`, `sessionctx.SessionFacts`, `sessionctx.Layer`, `sessionctx.BudgetKit`.
- Produces: `RaiseSpec.Context *sessionctx.Bundle` — always set by `Supervisor.Raise`; `RaiseSpec.Prompt` is the Launch prompt only.

- [ ] **Step 1: Write the failing tests**

Replace `TestRaiseComposesPrompt` in `internal/jam/supervisor_test.go`:

```go
// The session context is compiled at raise; the prompt stays the launch text.
func TestRaiseCompilesContext(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	sk := studio.StudioKit{Kind: studio.Kind, Prompt: "KITLAYER"}
	ref, _ := EnsureStudioKit(store, "web", sk)
	sup.SetDefaultStudioKit(ref)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Prompt: "LAUNCHLAYER", Name: "bot", SessionKind: SessionKindStanding}); err != nil {
		t.Fatal(err)
	}
	if fl.gotSpec.Prompt != "LAUNCHLAYER" {
		t.Fatalf("prompt must be the launch text only, got %q", fl.gotSpec.Prompt)
	}
	c := fl.gotSpec.Context
	if c == nil || !strings.Contains(c.Core, "KITLAYER") || !strings.Contains(c.Core, `standing session "bot"`) || !strings.Contains(c.Core, "## Kit — web@v") {
		t.Fatalf("context missing layers: %+v", c)
	}
}

// A raise with no kit still gets the boilerplate.
func TestRaiseContextWithoutKit(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, _, _ := supTestKit(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Prompt: "P"}); err != nil {
		t.Fatal(err)
	}
	if c := fl.gotSpec.Context; c == nil || !strings.Contains(c.Core, "## Boilerplate") || strings.Contains(c.Core, "## Kit") {
		t.Fatalf("want boilerplate only: %+v", c)
	}
}
```

(If `supTestKit` has no `guest` role by default, check `TestRaiseRequiresExistingRole`'s neighbours for the role it seeds and use that name.)

In `internal/standing/standing_test.go`, change the expectation at ~123 so the raised prompt is exactly the declared prompt (`wantPrompt := s.Prompt` / the literal declared text used in that test) and at ~300 assert `w.raised[0].Prompt` equals the declared prompt and `w.raised[0].SessionKind == jam.SessionKindStanding` and `w.raised[0].Name` is the session name.

In `internal/jam/sessions_test.go`, change ~111 to `s.Prompt != "help me"` (exact), and replace the `personalPrompt` test at ~324 with nothing (the function is deleted; its facts are covered by `TestBoilerplatePerKind`).

Add to `internal/studio/studiokit_test.go`:

```go
func TestValidateRejectsOverBudgetPrompt(t *testing.T) {
	sk := StudioKit{Kind: Kind, Prompt: strings.Repeat("x", sessionctx.BudgetKit+1)}
	if err := sk.Validate(); err == nil || !strings.Contains(err.Error(), "801 bytes") {
		t.Fatalf("want a budget error naming the size, got %v", err)
	}
	sk.Prompt = strings.Repeat("x", sessionctx.BudgetKit)
	if err := sk.Validate(); err != nil {
		t.Fatalf("at budget must pass: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jam/ ./internal/standing/ ./internal/studio/`
Expected: FAIL — `RaiseSpec.Context` undefined; prompt still composed; no budget check.

- [ ] **Step 3: Implement**

`internal/jam/supervisor.go` — add to `RaiseSpec`:

```go
	// Context is the compiled session context (sessionctx.Compile): Jam
	// boilerplate for the session kind, then the kit layer. Supervisor.Raise
	// always sets it; the launcher stages it for cove-master, which delivers
	// its core as an appended system prompt every turn.
	Context *sessionctx.Bundle
```

Replace the block at ~240-249 (`// Compose the session prompt …` through `spec.Prompt = studio.ComposePrompt(layers)` and its closing brace) with:

```go
	// Compile the session context (Jam boilerplate → kit; later slices add
	// studio, project, role, jam). The prompt stays the launch text alone.
	in := sessionctx.Inputs{Session: sessionctx.SessionFacts{
		Kind: spec.SessionKind, Name: spec.Name, Project: orDefaultProject(spec.Project), Role: spec.Role, Owner: spec.Owner,
	}}
	if spec.Kit.ID != "" {
		in.Session.Kit = spec.Kit.String()
		if def, ok, derr := ResolveKitDefinition(s.store, spec.Kit); derr == nil && ok {
			in.Kit = sessionctx.Layer{Core: def.Kit.Prompt}
		}
	}
	bundle := sessionctx.Compile(in)
	for _, w := range bundle.Warnings {
		if s.log != nil {
			s.log.Warn("raise: session context", "id", spec.ActorID, "warning", w)
		}
	}
	spec.Context = &bundle
```

Remove the now-unused `studio` import if nothing else in the file uses it (`go build` will say).

`internal/standing/standing.go`: in the `Raise` call use `Prompt: s.Prompt`; delete the `Prompt` function and its doc comment (and the `fmt` import if unused).

`internal/jam/sessions.go`: in the `Raise` call use `Prompt: b.Prompt`; delete `personalPrompt` and its doc comment.

`internal/studio/studiokit.go` `Validate`, before `return nil`:

```go
	// The prompt is the session context's always-on kit core (sessionctx).
	if n := len(strings.TrimSpace(sk.Prompt)); n > sessionctx.BudgetKit {
		return fmt.Errorf("studio kit: prompt is %d bytes; the kit core budget is %d — move detail out of the prompt", n, sessionctx.BudgetKit)
	}
```

Delete `internal/studio/prompt.go` and `internal/studio/prompt_test.go`.

- [ ] **Step 4: Run tests**

Run: `go build ./... && go test ./internal/jam/... ./internal/standing/ ./internal/studio/ ./internal/dispatcher/`
Expected: PASS. Then `grep -rn "ComposePrompt\|JamBoilerplate\|personalPrompt" --include=*.go .` → no output.

- [ ] **Step 5: Commit**

```bash
git add -A internal/jam internal/standing internal/studio
git commit -m "feat(jam): compile session context at raise; launch prompt stands alone"
```

---

### Task 4: Stage the bundle into the cove

**Files:**
- Modify: `internal/connect/covemaster.go`
- Modify: `internal/jam/launcher/launcher.go` (~160-171)
- Test: `internal/connect/covemaster_test.go`

**Interfaces:**
- Consumes: `sessionctx.Bundle`; `RaiseSpec.Context` (Task 3).
- Produces: `CoveMasterOptions.Context *sessionctx.Bundle`; VM file `/dev/shm/cove-agent-context` (bundle JSON); env `AT_COVE_AGENT_CONTEXT_FILE=/dev/shm/cove-agent-context` (only when Context is non-nil).

- [ ] **Step 1: Write the failing test**

Add to `internal/connect/covemaster_test.go` (same `runner.Fake` pattern as `TestLaunchCoveMasterInjectsAndLaunches`):

```go
func launchWith(t *testing.T, b *sessionctx.Bundle) *runner.Fake {
	t.Helper()
	fake := &runner.Fake{}
	err := LaunchCoveMaster(fake, CoveMasterOptions{
		Target: sshargs.Target{Host: "h", User: "agent", Port: 2222}, JamHost: "jam.example.com",
		RuntimeAddr: "jam.example.com:443", IdentityToken: "tok", LaunchSecret: "s",
		WorkDir: "/w", Prompt: "p", Context: b,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fake
}

// stdinWrites maps each "cat > <path>" ssh target to the stdin piped to it.
func stdinWrites(f *runner.Fake) map[string]string {
	out := map[string]string{}
	for _, c := range f.Calls {
		argv := strings.Join(c.Args, " ")
		if i := strings.Index(argv, "cat > "); c.Name == "ssh" && i >= 0 {
			out[strings.Fields(argv[i+len("cat > "):])[0]] = c.Stdin
		}
	}
	return out
}

func TestLaunchCoveMasterStagesContext(t *testing.T) {
	b := &sessionctx.Bundle{Core: "# Session context\n", Files: map[string]string{"INDEX.md": "x"}, Fingerprint: "fp", Layers: map[string]string{}}
	w := stdinWrites(launchWith(t, b))
	raw, ok := w[coveMasterContextVMPath]
	if !ok {
		t.Fatalf("no write to %s; writes=%v", coveMasterContextVMPath, w)
	}
	var got sessionctx.Bundle
	if err := json.Unmarshal([]byte(raw), &got); err != nil || !reflect.DeepEqual(&got, b) {
		t.Fatalf("staged bundle = %+v (%v), want %+v", got, err, b)
	}
	if env := w[coveMasterEnvVMPath]; !strings.Contains(env, "export AT_COVE_AGENT_CONTEXT_FILE='"+coveMasterContextVMPath+"'") {
		t.Fatalf("env missing AT_COVE_AGENT_CONTEXT_FILE:\n%s", env)
	}
}

func TestLaunchCoveMasterNoContextNoEnv(t *testing.T) {
	w := stdinWrites(launchWith(t, nil))
	if _, ok := w[coveMasterContextVMPath]; ok {
		t.Fatal("nil context must stage nothing")
	}
	if strings.Contains(w[coveMasterEnvVMPath], "AT_COVE_AGENT_CONTEXT_FILE") {
		t.Fatal("nil context must not export AT_COVE_AGENT_CONTEXT_FILE")
	}
}
```

Add imports `encoding/json`, `reflect`, and `github.com/aethons-tools/cove/internal/jam/sessionctx`.


- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/connect/ -run CoveMaster`
Expected: FAIL — `Context` field undefined.

- [ ] **Step 3: Implement**

In `internal/connect/covemaster.go`:

```go
	coveMasterContextVMPath = "/dev/shm/cove-agent-context"
```

(in the const block, with a comment: "the compiled session context (sessionctx.Bundle JSON), staged like the prompt"), add the field

```go
	// Context is the compiled session context; staged as JSON to tmpfs and
	// pointed at by AT_COVE_AGENT_CONTEXT_FILE. Nil = none (cove-master runs
	// claude without the context flags).
	Context *sessionctx.Bundle
```

and in `LaunchCoveMaster`, right after the prompt `writeVM`:

```go
	if o.Context != nil {
		cj, err := json.Marshal(o.Context)
		if err != nil {
			return fmt.Errorf("cove-master context: %w", err)
		}
		if err := writeVM(r, o.Target, string(cj), coveMasterContextVMPath); err != nil {
			return fmt.Errorf("cove-master context: %w", err)
		}
	}
```

and after the `AT_COVE_AGENT_PROMPT_FILE` export:

```go
	if o.Context != nil {
		fmt.Fprintf(&script, "export AT_COVE_AGENT_CONTEXT_FILE=%s\n", shellQuote(coveMasterContextVMPath))
	}
```

In `internal/jam/launcher/launcher.go`, add `Context: spec.Context,` to the `connect.CoveMasterOptions{…}` literal.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/connect/ ./internal/jam/launcher/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/connect internal/jam/launcher
git commit -m "feat(connect): stage the session context bundle for cove-master"
```

---

### Task 5: cove-master writes the bundle and launches claude with it

**Files:**
- Create: `internal/agentrun/context.go`, `internal/agentrun/context_test.go`
- Modify: `internal/agentrun/workload.go` (`Config`, `claudeArgs`, start of `Run`), `internal/agentrun/workload_test.go` (`TestRunSpawnArgs`)
- Modify: `cmd/cove-master/main.go` (`buildAgentConfig`), its test file (`grep -n buildAgentConfig cmd/cove-master/*_test.go`)

**Interfaces:**
- Consumes: `sessionctx.Bundle`, `sessionctx.Dir`; env `AT_COVE_AGENT_CONTEXT_FILE` (Task 4).
- Produces: `agentrun.Config.Context *sessionctx.Bundle`, `agentrun.Config.ContextDir string` (default `sessionctx.Dir`); `func writeContext(dir string, b sessionctx.Bundle) error`.

- [ ] **Step 1: Write the failing tests**

`internal/agentrun/context_test.go`:

```go
package agentrun

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func TestWriteContextReplacesWholesale(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "context")
	if err := os.MkdirAll(filepath.Join(dir, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "old", "stale.md"), []byte("stale"), 0o644)
	b := sessionctx.Bundle{Core: "CORE", Files: map[string]string{"INDEX.md": "I", "kit/tools.md": "T"}}
	if err := writeContext(dir, b); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"CORE.md": "CORE", "INDEX.md": "I", "kit/tools.md": "T"} {
		got, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil || string(got) != want {
			t.Errorf("%s: got %q, %v", p, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "old")); !os.IsNotExist(err) {
		t.Fatal("stale files from a previous bundle must be gone")
	}
	if _, err := os.Stat(dir + ".new"); !os.IsNotExist(err) {
		t.Fatal("staging dir must not linger")
	}
}

func TestWriteContextRefusesNonLocalPaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "context")
	for _, bad := range []string{"../escape.md", "/abs.md", "a/../../x.md", ""} {
		if err := writeContext(dir, sessionctx.Bundle{Core: "C", Files: map[string]string{bad: "x"}}); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.md")); !os.IsNotExist(err) {
		t.Fatal("nothing may be written outside the dir")
	}
}
```

In `internal/agentrun/workload_test.go` add:

```go
func TestRunSpawnArgsWithContext(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	mcp := mcpConfigFile(t, dir)
	cdir := filepath.Join(dir, "context")
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	b := &sessionctx.Bundle{Core: "CORE", Files: map[string]string{"INDEX.md": "I"}}
	w := New(Config{WorkDir: dir, Prompt: "do the thing", MCPConfigPath: mcp, Spawner: f, Context: b, ContextDir: cdir}, nil)
	if err := w.Run(context.Background(), &recordHandle{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--mcp-config", mcp, "--strict-mcp-config",
		"--append-system-prompt-file", filepath.Join(cdir, "CORE.md"), "--system-prompt-snapshot", "off", "do the thing"}
	if !slices.Equal(f.args, want) {
		t.Fatalf("args:\nwant %v\ngot  %v", want, f.args)
	}
	if got, _ := os.ReadFile(filepath.Join(cdir, "CORE.md")); string(got) != "CORE" {
		t.Fatalf("CORE.md = %q", got)
	}
}

// If the bundle cannot be written, run without the flags: claude hard-fails
// on a missing --append-system-prompt-file.
func TestRunContextWriteFailureRunsWithoutFlags(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, nil, 0o644)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w := New(Config{WorkDir: dir, Prompt: "p", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f,
		Context: &sessionctx.Bundle{Core: "C"}, ContextDir: filepath.Join(blocker, "context")}, nil)
	if err := w.Run(context.Background(), &recordHandle{}); err != nil {
		t.Fatal(err)
	}
	if hasArg(f.args, "--append-system-prompt-file") || hasArg(f.args, "--system-prompt-snapshot") {
		t.Fatalf("flags must be absent when the bundle was not written: %v", f.args)
	}
}
```

The existing `TestRunSpawnArgs` (no `Context`) stays unchanged — it pins "no bundle → no flags".

In the cove-master test file add (follow the file's existing `buildAgentConfig` test style for the fake `getenv`):

```go
func TestBuildAgentConfigLoadsContext(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "prompt"); os.WriteFile(pf, []byte("P"), 0o600)
	cf := filepath.Join(dir, "ctx"); os.WriteFile(cf, []byte(`{"core":"C","files":{"INDEX.md":"I"},"fingerprint":"f"}`), 0o600)
	env := map[string]string{"AT_COVE_AGENT_PROMPT_FILE": pf, "AT_COVE_AGENT_CONTEXT_FILE": cf}
	cfg, err := buildAgentConfig(func(k string) string { return env[k] })
	if err != nil || cfg.Context == nil || cfg.Context.Core != "C" {
		t.Fatalf("cfg.Context = %+v, err %v", cfg.Context, err)
	}
}

func TestBuildAgentConfigBadOrMissingContextIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "prompt"); os.WriteFile(pf, []byte("P"), 0o600)
	bad := filepath.Join(dir, "bad"); os.WriteFile(bad, []byte("{not json"), 0o600)
	for _, cf := range []string{"", filepath.Join(dir, "missing"), bad} {
		env := map[string]string{"AT_COVE_AGENT_PROMPT_FILE": pf, "AT_COVE_AGENT_CONTEXT_FILE": cf}
		cfg, err := buildAgentConfig(func(k string) string { return env[k] })
		if err != nil || cfg.Context != nil {
			t.Errorf("%q: want no context and no error, got %+v, %v", cf, cfg.Context, err)
		}
	}
}
```

(gofmt will split the one-line `os.WriteFile` statements; write them on separate lines.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/agentrun/ ./cmd/cove-master/`
Expected: FAIL — `writeContext`, `Config.Context`, `Config.ContextDir` undefined.

- [ ] **Step 3: Implement**

`internal/agentrun/context.go`:

```go
package agentrun

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// writeContext writes b under dir — CORE.md plus every file — by building
// dir+".new" and swapping it in, so the agent never sees a half-written tree
// and nothing from a previous bundle survives. Every path must be local.
func writeContext(dir string, b sessionctx.Bundle) error {
	files := map[string]string{"CORE.md": b.Core}
	for p, body := range b.Files {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("session context: refusing non-local path %q", p)
		}
		files[p] = body
	}
	staging := dir + ".new"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	for p, body := range files {
		full := filepath.Join(staging, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(staging, dir)
}
```

`internal/agentrun/workload.go`:
- `Config` gains:

```go
	// Context is the compiled session context; nil (an older launcher) runs
	// claude without it. Written under ContextDir once at Run start; its core is
	// passed with --append-system-prompt-file on every turn.
	Context *sessionctx.Bundle
	// ContextDir is where Context is written; empty defaults to sessionctx.Dir.
	ContextDir string
```

- `Workload` gains `contextCore string` (path of CORE.md when written, else "").
- In `New`: `if cfg.ContextDir == "" { cfg.ContextDir = sessionctx.Dir }`.
- In `Run`, right after the MCP-config guard:

```go
	if w.cfg.Context != nil {
		if err := writeContext(w.cfg.ContextDir, *w.cfg.Context); err != nil {
			// claude hard-fails on a missing --append-system-prompt-file, so run
			// without the context rather than not at all.
			w.log.Warn("agentrun: session context not written; running without it", "dir", w.cfg.ContextDir, "err", err.Error())
		} else {
			w.contextCore = filepath.Join(w.cfg.ContextDir, "CORE.md")
			w.log.Info("agentrun: session context applied", "fingerprint", short(w.cfg.Context.Fingerprint))
		}
	}
```

with `func short(s string) string { if len(s) > 12 { return s[:12] }; return s }` (gofmt-formatted) in `context.go`.
- `claudeArgs`: replace the final `return` with

```go
	args = append(args, "--dangerously-skip-permissions", "--mcp-config", w.cfg.MCPConfigPath, "--strict-mcp-config")
	if w.contextCore != "" {
		// snapshot off: the default replays the first turn's system prompt on
		// every --continue, which would hide context updates.
		args = append(args, "--append-system-prompt-file", w.contextCore, "--system-prompt-snapshot", "off")
	}
	return append(args, prompt)
```

`cmd/cove-master/main.go` `buildAgentConfig`, before `return cfg, nil`:

```go
	if cf := getenv("AT_COVE_AGENT_CONTEXT_FILE"); cf != "" {
		// Not fatal: a missing or bad bundle runs the agent without context.
		if raw, err := os.ReadFile(cf); err == nil {
			var b sessionctx.Bundle
			if json.Unmarshal(raw, &b) == nil {
				cfg.Context = &b
			}
		}
	}
```

(Add the `sessionctx` import.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agentrun/ ./cmd/cove-master/`
Expected: PASS (including the unchanged `TestRunSpawnArgs`).

- [ ] **Step 5: Commit**

```bash
git add internal/agentrun cmd/cove-master
git commit -m "feat(agentrun): deliver the session context as an appended system prompt"
```

---

### Task 6: Sealed image files no longer contradict Jam sessions

**Files:**
- Modify: `internal/assemble/hardening/image-files/home/agent/.init-agent-data/SANDBOX.md` (the "Changing the sandbox" paragraph)
- Modify: `internal/assemble/hardening/image-files/home/agent/.init-agent-data/COLLABORATOR.md` (→ empty file)
- Modify: `internal/connect/connect.go:329-336` (`writeCollaboratorRole` placeholder → empty body)
- Test: `internal/assemble/embed_test.go` (add), `internal/connect/connect_test.go` (adjust any assertion on the placeholder: `grep -n "no collaborator role" internal/connect/*_test.go`)

**Interfaces:** none (payload text).

- [ ] **Step 1: Write the failing tests**

In `internal/assemble/embed_test.go`:

```go
// SANDBOX.md is loaded in every sandbox; in a Jam session it must defer to the
// session context instead of prescribing the local at-cove kit path.
func TestSandboxMDDefersToJamContext(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/home/agent/.init-agent-data/SANDBOX.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "/agent-data/context/CORE.md") {
		t.Fatal("SANDBOX.md must route Jam sessions to their session context")
	}
	if !strings.Contains(s, ".at-cove/config.yml") {
		t.Fatal("SANDBOX.md must keep the local at-cove path")
	}
}

func TestCollaboratorDefaultIsEmpty(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/home/agent/.init-agent-data/COLLABORATOR.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "" {
		t.Fatalf("default COLLABORATOR.md must be empty, got %q", b)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/assemble/ -run 'SandboxMD|CollaboratorDefault'`
Expected: FAIL.

- [ ] **Step 3: Implement**

Replace the SANDBOX.md paragraph that starts `**Changing the sandbox is declarative and human-gated.**` with:

```markdown
**Changing the sandbox is declarative and human-gated.** You cannot rebuild your own
image and must never weaken the hardening. **If `/agent-data/context/CORE.md` exists,
you are a Jam session: its instructions for changing the kit apply, not the rest of
this paragraph.** Otherwise, the path is: edit `.at-cove/config.yml`, then ask the
human to run `at-cove recreate` on the host — the change does not take effect until
they do. Name the exact edit and why so they can review it.
```

Make `COLLABORATOR.md` a zero-byte file (`: > …/COLLABORATOR.md`). In `writeCollaboratorRole`, delete the `if body == "" { body = "# (no collaborator role active)\n" }` block and update its doc comment to "writes the role prompt (empty when none) …".

- [ ] **Step 4: Run tests**

Run: `go test ./internal/assemble/ ./internal/connect/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/assemble internal/connect
git commit -m "feat(image): SANDBOX.md defers to Jam session context; empty collaborator default"
```

---

### Task 7: Docs

**Files:**
- Create: `docs/usage/jam/session-context.md`
- Modify: `docs/usage/jam/INDEX.md` (one row), `docs/usage/jam/kits.md` (§`prompt` row + the "composed at raise" paragraph ~43-49), `docs/usage/jam/coves.md` (~63-66, ~106, ~183, ~193), `docs/usage/jam/standing-sessions.md` (the preamble note, `grep -n preamble`), `docs/usage/jam/personal-sessions.md` (any preamble mention), `docs/OVERVIEW.md` (`.init-agent-data` section ~405/558: COLLABORATOR default now empty; SANDBOX.md Jam branch)

- [ ] **Step 1: Write `docs/usage/jam/session-context.md`** (use the docs-author skill; leaf; ≤ the leaf budget in the progressive-disclosure reference)

```markdown
---
summary: How a Jam session learns its context — the layered bundle compiled at raise, its always-on core and on-demand leaves, and how cove-master delivers it.
read_when: You are writing a kit prompt, debugging what a session was told, or changing how session context is compiled or delivered.
owns: session-context layers, delivery order and precedence, core budgets, the /agent-data/context layout, the AT_COVE_AGENT_CONTEXT_FILE handoff
prereqs: coves.md
tier: leaf
updated: 2026-10-02
---

# Session context

At raise Jam compiles a **session context bundle** (`internal/jam/sessionctx`) from
layers, in delivery order:

| Layer | Source | Core budget |
|-------|--------|-------------|
| Boilerplate | built in, per session kind (ephemeral / personal / standing): sandbox, turn model, intercom rules | 2400 B |
| Kit | the studio kit's `prompt` ([kits.md](kits.md)) | 800 B — `kit push` rejects more |

(Studio, Project, Role and Jam layers follow — see the design spec.)

The **core** (`CORE.md`) states precedence once — hardening is absolute; otherwise
later layers win — then each non-empty layer's core and a one-line pointer to each of
its leaves. **Leaves** and `INDEX.md` hold the detail. An empty layer emits nothing.

## Delivery

1. `Supervisor.Raise` compiles the bundle; the launch prompt (task, brief, standing or
   personal prompt) stays the `claude -p` message on its own.
2. The launcher stages the bundle JSON at `/dev/shm/cove-agent-context` and exports
   `AT_COVE_AGENT_CONTEXT_FILE`.
3. cove-master writes it to `/agent-data/context/` (built beside it and swapped in;
   nothing from an earlier bundle survives) and runs every turn with
   `--append-system-prompt-file /agent-data/context/CORE.md --system-prompt-snapshot off`.
   `off` matters: by default claude replays the first turn's system prompt on every
   `--continue`.
4. No file, malformed JSON, or an unwritable directory → the agent runs without
   context (warned in `cove-master.log`), never not at all.

The bundle never carries secrets: only names of env keys and routes.
```

- [ ] **Step 2: Update the pointers** — replace each old description with one sentence + a link to `session-context.md` (do not restate the layer table):
  - `docs/usage/jam/INDEX.md` row: `| [session-context.md](session-context.md) | You are writing a kit prompt, debugging what a session was told at raise, or changing how session context is compiled or delivered. |`
  - `kits.md`: the `prompt` row → "The kit layer's always-on core (≤ 800 bytes) of the [session context](session-context.md)."; the "composed at raise from ordered layers…" sentence → "The session context is compiled at raise ([session-context.md](session-context.md)), so it lives outside the image."
  - `coves.md`: the `claude -p` command lines gain the two flags; add `AT_COVE_AGENT_CONTEXT_FILE  path to the compiled session context JSON (optional)` to the env table; replace "prompt composed at raise" with a link.
  - `standing-sessions.md` / `personal-sessions.md`: the preamble description → "The session's identity and intercom rules come from the Boilerplate layer of its [session context](session-context.md); the prompt you declare is delivered as-is."
  - `OVERVIEW.md`: `COLLABORATOR.md` default is empty; `SANDBOX.md` routes Jam sessions to `/agent-data/context/CORE.md`.
  - Bump `updated:` on each touched doc to 2026-10-02.

- [ ] **Step 3: Audit**

Run the docs-audit skill's checker (it prints orphans, dangling links, oversize docs). Expected: no new findings. Then `grep -rn "ComposePrompt\|composed at raise from ordered layers\|no collaborator role active" docs --include=*.md | grep -v superpowers` → no output.

- [ ] **Step 4: Full verification**

Run: `just test && just lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs
git commit -m "docs(jam): session context — layers, budgets, delivery"
```

---

## Out of scope for this slice (later slices of the spec)

Studio layer and `Destination.Note` (slice 2); `Role.Context`, `Project.Context`/`Resources`, Jam context, `StudioKit.Notes`, CLI and admin UI (slice 3); `GET /context`, per-turn refresh, change notice, per-kind resume prompts (slice 4). The ephemeral worker's `resultProtocol` stays in its Launch prompt.
