# Session context — slice 2 (generated Studio layer) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every Jam session's context gains a generated **Studio** layer listing what it can actually reach: its granted destinations and how to use them (env keys, git routing, a new human-written `Destination.Note`), its effective egress, and who it may message.

**Architecture:** `sessionctx` gets `StudioFacts` and a pure `Studio(StudioFacts) Layer` renderer that spills to leaves instead of truncating; `Compile` places it after Kit. `Supervisor.Raise` gathers the facts from data Jam already has — `ScopesFor` + `ListDestinations` (destinations), the role's egress policy or the kit's `studio.Ceiling` (egress), `ListTargets` (message targets). `Destination` gains an optional `Note` (≤ 300 bytes), editable from the CLI and the admin UI. An authored-layer lint warns when the kit prompt restates a Studio-owned fact.

**Tech Stack:** Go 1.27 stdlib; `just test`, `go test`.

**Spec:** [`docs/superpowers/specs/2026-10-02-session-context-layers-design.md`](../specs/2026-10-02-session-context-layers-design.md) — **Slice 2**. Builds on slice 1 (merged, #309).

## Global Constraints

- Delivery order: Boilerplate → Kit → **Studio** (→ Project → Role → Jam later). Studio core budget **1600 bytes**; a generated layer **never truncates** — it spills rows to `studio/destinations.md` / `studio/egress.md` / `studio/targets.md`.
- `Destination.Note`: optional, ≤ **300 bytes**, the human-written "translation" (e.g. "`gh` goes via Jam: pass `-R $GH_HOST/<owner>/<repo>`").
- **No secrets:** render destination name, upstream, env **keys** (never values/templates), git routing, note. Never `CredName`, `IdentityIn`, `Apply`, credential maps, env values, or tokens.
- Egress shown = the role's policy domains when set (Jam guarantees they are within the kit ceiling — the in-box helper refuses otherwise), else `studio.Ceiling(kit.Egress)`; neither known → say "the image's default allow-list". Always add: "plus the sealed base and Jam's own routes".
- Message targets = `jam.ListTargets` for the raised actor (a personal session's override already limits it to its owner); mark the owner "your owner".
- Lint never fails anything: it appends `Bundle.Warnings`, which `Raise` logs.
- Tests hermetic; TDD; docs in the same PR; GOPROXY note from slice 1 applies (`GOPROXY=https://proxy.golang.org,direct`).

## Review Focus

1. **A destination env value holding `{token}` or a credential name** — expected: neither ever appears in any bundle byte. Pinned in Task 2 (renderer) and Task 3 (end-to-end through `Raise`).
2. **A role granting many destinations / a long egress list** — expected: core stays ≤ 1600 bytes, nothing is lost (full rows in leaves), no truncation warning. Pinned in Task 2.
3. **A scope naming a destination that no longer exists** — expected: skipped silently (as `ConnectorFor` does), no panic. Pinned in Task 3.
4. **A role with an egress policy set to empty** (`Domains: []`) — expected: "no extra hosts" wording, not "the image's default". Pinned in Task 2.
5. **A note with newlines or `|`** — expected: rendered on one line, never breaking the INDEX table or a bullet. Pinned in Task 2.

---

## File structure

| File | Responsibility |
|------|----------------|
| `internal/jam/policy.go` (modify `Destination`) | `Note` field |
| `internal/jam/destedit.go` (modify `ValidateDestination`) | Note ≤ 300 bytes |
| `cmd/at-jam/main.go` (~304-365) | `destination add --note`; `list` shows it |
| `internal/jam/adminui/dest_detail.go` (`destFromForm`), `templates/dest_fields.html`, `templates/destination.html` | Note in the form and the detail page |
| `internal/jam/sessionctx/studio.go` (create), `studio_test.go` (create) | `StudioFacts`, `Studio()` renderer with spill |
| `internal/jam/sessionctx/sessionctx.go` (modify) | `LayerStudio`, `BudgetStudio`, `Inputs.Studio`, Compile order, lint |
| `internal/jam/studiofacts.go` (create), `studiofacts_test.go` (create) | `studioFacts(store, actor, spec, kitEgress)` gather |
| `internal/jam/supervisor.go` (modify compile block) | wire facts in |
| Docs: `docs/usage/jam/session-context.md`, `connector.md`, `comms-addressing.md`, `ui-pages.md` | Studio layer + Note |

---

### Task 1: `Destination.Note`

**Files:**
- Modify: `internal/jam/policy.go` (`Destination` struct), `internal/jam/destedit.go` (`ValidateDestination`)
- Modify: `cmd/at-jam/main.go` (destination flags ~304-318; `list` ~350-365)
- Modify: `internal/jam/adminui/dest_detail.go` (`destFromForm`), `internal/jam/adminui/templates/dest_fields.html`, `internal/jam/adminui/templates/destination.html`
- Test: `internal/jam/destedit_test.go`, `internal/jam/adminui/dest_detail_test.go`

**Interfaces:**
- Produces: `Destination.Note string` (`json:"note,omitempty" yaml:"note,omitempty"`); `const MaxDestinationNote = 300`.

- [ ] **Step 1: Write the failing tests**

In `internal/jam/destedit_test.go`, add a case to the `TestValidateDestination` table:

```go
		"long note":    {Destination{Name: "x", Route: "/x/", Upstream: "https://x", Note: strings.Repeat("n", MaxDestinationNote+1)}, "note"},
```

and a new test:

```go
func TestValidateDestinationAcceptsNoteAtLimit(t *testing.T) {
	d := Destination{Name: "x", Route: "/x/", Upstream: "https://x", Note: strings.Repeat("n", MaxDestinationNote)}
	if err := ValidateDestination(d, credIs("gh-pat")); err != nil {
		t.Fatalf("a %d-byte note must pass: %v", MaxDestinationNote, err)
	}
}
```

In `internal/jam/adminui/dest_detail_test.go` (`TestCreateDestinationAllFields`), add `"note": {"use git over https"}` to the posted `url.Values` and extend the stored check with `|| d.Note != "use git over https"`. Add:

```go
func TestDestinationDetailShowsNote(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, credAny, nil)
	if rec := post(t, h, "/ui/destinations", url.Values{
		"name": {"gh"}, "route": {"/api/v3/"}, "upstream": {"https://api.github.com"},
		"identity-in": {"bearer"}, "apply": {"bearer"}, "note": {"pass -R $GH_HOST/owner/repo"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	body := get(t, h, "/ui/destinations/gh").Body.String()
	for _, want := range []string{"pass -R $GH_HOST/owner/repo", `name="note"`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jam/ -run Destination && go test ./internal/jam/adminui/ -run Destination`
Expected: FAIL — `Note`/`MaxDestinationNote` undefined.

- [ ] **Step 3: Implement**

`policy.go`, in `Destination` after `Git`:

```go
	// Note is an optional human-written usage hint shown to sessions granted
	// this destination (the Studio layer of their session context), e.g. how a
	// tool must be pointed at the route. Not a secret; ≤ MaxDestinationNote bytes.
	Note string `json:"note,omitempty" yaml:"note,omitempty"`
```

`destedit.go`:

```go
// MaxDestinationNote bounds Destination.Note: it rides in every granted
// session's always-on context.
const MaxDestinationNote = 300
```

and in `ValidateDestination` before `ValidateEnv`:

```go
	if len(d.Note) > MaxDestinationNote {
		return writeErr(http.StatusBadRequest, "note is %d bytes; at most %d", len(d.Note), MaxDestinationNote)
	}
```

`cmd/at-jam/main.go`: after the `--git` flag, `fs.StringVar(&d.Note, "note", "", "usage hint shown to sessions granted this destination (≤300 bytes)")`; in `list`, after the git suffix: `if dd.Note != "" { ob += ", note" }`.

`adminui/dest_detail.go` `destFromForm`: add `Note: strings.TrimSpace(r.FormValue("note")),`.

`templates/dest_fields.html`, after the env `<label class="wide">…</label>`:

```html
  <label class="wide">Note <span class="hint">shown to sessions granted this destination — how to use it (≤ 300 bytes)</span><input name="note" value="{{.Dest.Note}}" maxlength="300" spellcheck="false"></label>
```

`templates/destination.html`, after the OAuth beta row:

```html
        <dt>Note</dt><dd>{{if .Dest.Note}}{{.Dest.Note}}{{else}}<span class="unset">none</span>{{end}}</dd>
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/jam/ ./internal/jam/adminui/ ./cmd/at-jam/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam cmd/at-jam
git commit -m "feat(jam): Destination.Note — a usage hint for granted sessions"
```

---

### Task 2: `sessionctx` Studio renderer

**Files:**
- Create: `internal/jam/sessionctx/studio.go`, `internal/jam/sessionctx/studio_test.go`
- Modify: `internal/jam/sessionctx/sessionctx.go` (constants, `Inputs`, `Compile` sections)

**Interfaces:**
- Produces:
  - `const LayerStudio = "studio"`, `const BudgetStudio = 1600`
  - `type StudioDestination struct { Name, Upstream string; EnvKeys []string; Git bool; Note string }`
  - `type StudioTarget struct { Target, Who string }` — `Target` is `human:<name>`/`channel:<name>`, `Who` a short description
  - `type StudioFacts struct { Destinations []StudioDestination; Egress []string; EgressKnown bool; Targets []StudioTarget }` — `EgressKnown` false = "the image's default allow-list"
  - `func Studio(f StudioFacts) Layer`
  - `Inputs.Studio StudioFacts`

- [ ] **Step 1: Write the failing tests** (`studio_test.go`)

```go
package sessionctx

import (
	"fmt"
	"strings"
	"testing"
)

func ghFacts() StudioFacts {
	return StudioFacts{
		Destinations: []StudioDestination{
			{Name: "anthropic", Upstream: "https://api.anthropic.com", EnvKeys: []string{"ANTHROPIC_BASE_URL"}},
			{Name: "github-api", Upstream: "https://api.github.com", EnvKeys: []string{"GH_ENTERPRISE_TOKEN", "GH_HOST"}, Note: "pass -R $GH_HOST/<owner>/<repo>"},
			{Name: "git", Upstream: "https://github.com", Git: true},
		},
		Egress:      []string{"github.com", "proxy.golang.org"},
		EgressKnown: true,
		Targets:     []StudioTarget{{Target: "human:alice", Who: "your owner"}, {Target: "channel:ops", Who: "channel"}},
	}
}

func TestStudioCoreListsWhatTheSessionCanReach(t *testing.T) {
	l := Studio(ghFacts())
	for _, want := range []string{
		"`github-api` → https://api.github.com",
		"env GH_ENTERPRISE_TOKEN, GH_HOST",
		"pass -R $GH_HOST/<owner>/<repo>",
		"`git` → https://github.com",
		"https://github.com/ is routed through Jam",
		"github.com, proxy.golang.org",
		"plus the sealed base and Jam's own routes",
		"`human:alice` — your owner",
	} {
		if !strings.Contains(l.Core, want) {
			t.Errorf("core missing %q:\n%s", want, l.Core)
		}
	}
	if len(l.Leaves) != 0 {
		t.Errorf("a small studio must not spill: %+v", l.Leaves)
	}
}

func TestStudioEmptyIsEmpty(t *testing.T) {
	if l := Studio(StudioFacts{}); !l.Empty() {
		t.Fatalf("no facts → empty layer, got %+v", l)
	}
}

func TestStudioEgressWording(t *testing.T) {
	if c := Studio(StudioFacts{EgressKnown: true}).Core; !strings.Contains(c, "no hosts beyond the sealed base and Jam's own routes") {
		t.Errorf("empty policy wording wrong:\n%s", c)
	}
	if c := Studio(StudioFacts{Targets: []StudioTarget{{Target: "human:a", Who: "x"}}}).Core; !strings.Contains(c, "the image's default allow-list") {
		t.Errorf("unknown egress wording wrong:\n%s", c)
	}
}

func TestStudioNoteIsOneLine(t *testing.T) {
	f := ghFacts()
	f.Destinations[1].Note = "line one\nline | two"
	c := Studio(f).Core
	if !strings.Contains(c, "line one line / two") {
		t.Fatalf("note must be flattened to one line:\n%s", c)
	}
}

func TestStudioSpillsInsteadOfTruncating(t *testing.T) {
	var f StudioFacts
	for i := range 40 {
		f.Destinations = append(f.Destinations, StudioDestination{Name: fmt.Sprintf("dest-%02d", i), Upstream: "https://upstream.example.com/path", EnvKeys: []string{"SOME_LONG_ENV_KEY"}})
	}
	for i := range 80 {
		f.Egress = append(f.Egress, fmt.Sprintf("host-%02d.example.org", i))
	}
	f.EgressKnown = true
	for i := range 30 {
		f.Targets = append(f.Targets, StudioTarget{Target: fmt.Sprintf("human:person-%02d", i), Who: "project contact"})
	}
	l := Studio(f)
	if len(l.Core) > BudgetStudio {
		t.Fatalf("core %d bytes > budget %d", len(l.Core), BudgetStudio)
	}
	leaves := map[string]string{}
	for _, lf := range l.Leaves {
		leaves[lf.Name] = lf.Body
	}
	for name, want := range map[string]string{"destinations.md": "dest-39", "egress.md": "host-79.example.org", "targets.md": "human:person-29"} {
		if !strings.Contains(leaves[name], want) {
			t.Errorf("leaf %s must hold the full list (missing %q)", name, want)
		}
	}
	b := Compile(Inputs{Session: SessionFacts{Kind: KindStanding, Name: "n", Project: "p", Role: "r"}, Studio: f})
	if len(b.Warnings) != 0 {
		t.Fatalf("a generated layer must never truncate: %v", b.Warnings)
	}
}

func TestCompilePlacesStudioAfterKit(t *testing.T) {
	b := Compile(Inputs{Session: SessionFacts{Kind: KindStanding, Name: "n", Project: "p", Role: "r", Kit: "web@v1"}, Kit: Layer{Core: "K"}, Studio: ghFacts()})
	k, s := strings.Index(b.Core, "## Kit"), strings.Index(b.Core, "## Studio")
	if k < 0 || s < 0 || s < k {
		t.Fatalf("want Kit then Studio:\n%s", b.Core)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jam/sessionctx/ -run Studio`
Expected: FAIL — `StudioFacts`/`Studio` undefined.

- [ ] **Step 3: Implement**

`sessionctx.go`: add `LayerStudio = "studio"` to the layer consts, `BudgetStudio = 1600` to the budgets, `Studio StudioFacts` to `Inputs`, and in `Compile` append after the kit section:

```go
		{LayerStudio, "Studio", Studio(in.Studio), BudgetStudio},
```

`studio.go`:

```go
package sessionctx

import (
	"fmt"
	"strings"
)

// StudioDestination is one granted destination as a session should see it:
// what it reaches and how to use it. Never credentials or env values.
type StudioDestination struct {
	Name, Upstream string
	EnvKeys        []string // sorted env var names the connector sets
	Git            bool     // https://github.com/ is routed through it
	Note           string   // Destination.Note
}

// StudioTarget is one message target the session may `send` to.
type StudioTarget struct {
	Target string // "human:<name>" | "channel:<name>"
	Who    string // e.g. "your owner", "project contact @alice", "channel"
}

// StudioFacts are what a session can actually reach, gathered at raise.
type StudioFacts struct {
	Destinations []StudioDestination
	Egress       []string // effective allow-list (sorted)
	EgressKnown  bool     // false = the image's default list (not known to Jam)
	Targets      []StudioTarget
}

func (f StudioFacts) empty() bool {
	return len(f.Destinations) == 0 && len(f.Egress) == 0 && !f.EgressKnown && len(f.Targets) == 0
}

// Studio renders the generated Studio layer. It never exceeds BudgetStudio:
// when the full form is too long, each list moves to its own leaf and the core
// keeps a one-line summary per list.
func Studio(f StudioFacts) Layer {
	if f.empty() {
		return Layer{}
	}
	dests, egress, targets := studioDestinations(f), studioEgress(f), studioTargets(f)
	full := strings.Join(nonEmpty(dests, egress, targets), "\n")
	if len(full) <= BudgetStudio {
		return Layer{Core: full}
	}
	var l Layer
	var core []string
	if dests != "" {
		names := make([]string, len(f.Destinations))
		for i, d := range f.Destinations {
			names[i] = d.Name
		}
		core = append(core, fmt.Sprintf("Destinations via Jam (%d): %s.", len(names), strings.Join(names, ", ")))
		l.Leaves = append(l.Leaves, Leaf{Name: "destinations.md", ReadWhen: "you need how to reach a granted service (env, git routing, notes)", Body: dests})
	}
	if len(f.Egress) > 0 {
		core = append(core, fmt.Sprintf("Egress: %d allowed hosts, plus the sealed base and Jam's own routes.", len(f.Egress)))
		l.Leaves = append(l.Leaves, Leaf{Name: "egress.md", ReadWhen: "a host fails to connect and you need to know whether it is allowed", Body: egress})
	} else if egress != "" {
		core = append(core, egress)
	}
	if targets != "" {
		core = append(core, fmt.Sprintf("Message targets: %d (`list_targets` shows them).", len(f.Targets)))
		l.Leaves = append(l.Leaves, Leaf{Name: "targets.md", ReadWhen: "you need who a message target is", Body: targets})
	}
	l.Core = strings.Join(core, "\n")
	return l
}

func studioDestinations(f StudioFacts) string {
	if len(f.Destinations) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Destinations via Jam (credentials are injected for you; never ask for them):")
	for _, d := range f.Destinations {
		fmt.Fprintf(&b, "\n- `%s` → %s", d.Name, d.Upstream)
		if len(d.EnvKeys) > 0 {
			fmt.Fprintf(&b, "; env %s", strings.Join(d.EnvKeys, ", "))
		}
		if d.Git {
			b.WriteString("; https://github.com/ is routed through Jam")
		}
		if n := oneLine(d.Note); n != "" {
			b.WriteString(" — " + n)
		}
	}
	return b.String()
}

func studioEgress(f StudioFacts) string {
	switch {
	case len(f.Egress) > 0:
		return "Egress (other hosts are blocked): " + strings.Join(f.Egress, ", ") + ", plus the sealed base and Jam's own routes."
	case f.EgressKnown:
		return "Egress: no hosts beyond the sealed base and Jam's own routes."
	default:
		return "Egress: the image's default allow-list, plus Jam's own routes."
	}
}

func studioTargets(f StudioFacts) string {
	if len(f.Targets) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Message targets:")
	for _, t := range f.Targets {
		fmt.Fprintf(&b, "\n- `%s` — %s", t.Target, oneLine(t.Who))
	}
	return b.String()
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
```

Note: `TestStudioEgressWording`'s first case (`EgressKnown` only) is non-empty by `empty()`'s definition, so it renders.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/jam/sessionctx/`
Expected: PASS (slice 1 tests unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/jam/sessionctx
git commit -m "feat(sessionctx): generated Studio layer — destinations, egress, targets"
```

---

### Task 3: Gather Studio facts at raise

**Files:**
- Create: `internal/jam/studiofacts.go`, `internal/jam/studiofacts_test.go`
- Modify: `internal/jam/supervisor.go` (the compile block added in slice 1)
- Test: `internal/jam/supervisor_test.go`

**Interfaces:**
- Consumes: `sessionctx.StudioFacts`, `StudioDestination`, `StudioTarget` (Task 2); `Destination.Note` (Task 1); existing `ScopesFor`, `ListTargets`, `Destination.ClientEnv`, `Destination.GitRouted`, `studio.Ceiling`.
- Produces: `func studioFacts(store Store, a Actor, owner string, roleEgress *EgressPolicy, kitEgress []string, haveKit bool, now time.Time) sessionctx.StudioFacts`

- [ ] **Step 1: Write the failing tests**

`studiofacts_test.go`:

```go
package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func TestStudioFactsFromScope(t *testing.T) {
	_, store, _ := supTestKit(t, &fakeLauncher{})
	for _, d := range []Destination{
		{Name: "github-api", Route: "/api/v3/", Upstream: "https://api.github.com", CredName: "gh-pat-SECRETNAME",
			Env: map[string]string{"GH_HOST": "{host}", "GH_ENTERPRISE_TOKEN": "{token}"}, Note: "pass -R $GH_HOST/o/r"},
		{Name: "git", Route: "/git/", Upstream: "https://github.com", Git: true},
	} {
		if err := store.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	a := Actor{ID: "w1", Grants: []Grant{{Project: "default", Role: "dev"}}}
	if err := store.PutRole("default", Role{Name: "dev", Scope: Scope{Destinations: []string{"github-api", "git", "ghost"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	f := studioFacts(store, a, "", nil, []string{"github.com", "api.anthropic.com"}, true, time.Now())
	if len(f.Destinations) != 2 || f.Destinations[0].Name != "git" || f.Destinations[1].Name != "github-api" {
		t.Fatalf("destinations = %+v (sorted, unknown 'ghost' skipped)", f.Destinations)
	}
	gh := f.Destinations[1]
	if strings.Join(gh.EnvKeys, ",") != "GH_ENTERPRISE_TOKEN,GH_HOST" || gh.Note != "pass -R $GH_HOST/o/r" || !f.Destinations[0].Git {
		t.Fatalf("destination facts wrong: %+v", f.Destinations)
	}
	if !f.EgressKnown || strings.Join(f.Egress, ",") != "github.com" {
		t.Fatalf("egress = %v known=%v; want the kit ceiling (anthropic excluded)", f.Egress, f.EgressKnown)
	}
	b := sessionctx.Compile(sessionctx.Inputs{Session: sessionctx.SessionFacts{Kind: "standing", Name: "n"}, Studio: f})
	all := b.Core
	for _, body := range b.Files {
		all += body
	}
	for _, secret := range []string{"gh-pat-SECRETNAME", "{token}", "{host}"} {
		if strings.Contains(all, secret) {
			t.Errorf("bundle leaks %q", secret)
		}
	}
}

func TestStudioFactsEgressSources(t *testing.T) {
	_, store, _ := supTestKit(t, &fakeLauncher{})
	a := Actor{ID: "w1"}
	if f := studioFacts(store, a, "", &EgressPolicy{Domains: []string{"b.com", "a.com"}}, []string{"x.com"}, true, time.Now()); strings.Join(f.Egress, ",") != "a.com,b.com" || !f.EgressKnown {
		t.Errorf("role policy wins, sorted: %v", f.Egress)
	}
	if f := studioFacts(store, a, "", &EgressPolicy{Domains: []string{}}, []string{"x.com"}, true, time.Now()); len(f.Egress) != 0 || !f.EgressKnown {
		t.Errorf("empty policy = known, no hosts: %+v", f)
	}
	if f := studioFacts(store, a, "", nil, nil, false, time.Now()); f.EgressKnown {
		t.Errorf("no policy and no kit = unknown: %+v", f)
	}
}
```

Add to `supervisor_test.go`:

```go
// A personal session's Studio layer names its owner as its one target.
func TestRaiseContextStudioNamesOwner(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	if err := store.AddHuman("default", Human{Name: "alice", Handle: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "p1", Project: "default", Role: "guest", Owner: "alice", SessionKind: SessionKindPersonal, Prompt: "P"}); err != nil {
		t.Fatal(err)
	}
	if c := fl.gotSpec.Context; c == nil || !strings.Contains(c.Core, "`human:alice` — your owner") {
		t.Fatalf("studio layer must name the owner: %+v", c)
	}
}
```

(`supTestKit` seeds role `guest` with no addressing; the personal-session override set by `Raise` authorizes `human:alice` alone.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jam/ -run 'StudioFacts|StudioNamesOwner'`
Expected: FAIL — `studioFacts` undefined.

- [ ] **Step 3: Implement**

`studiofacts.go`:

```go
package jam

import (
	"maps"
	"slices"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/studio"
)

// studioFacts gathers what a raised session can actually reach, for its
// session context's Studio layer: the destinations in its scopes (unknown names
// skipped, as ConnectorFor does), its effective egress (the role's policy, else
// the kit's ceiling, else unknown), and its message targets. Only names, upstreams,
// env KEYS and notes leave here — never credentials or env values.
func studioFacts(store Store, a Actor, owner string, roleEgress *EgressPolicy, kitEgress []string, haveKit bool, now time.Time) sessionctx.StudioFacts {
	var f sessionctx.StudioFacts
	byName := map[string]Destination{}
	for _, d := range store.ListDestinations() {
		byName[d.Name] = d
	}
	inScope := map[string]bool{}
	for _, s := range ScopesFor(store, a) {
		for _, n := range s.Destinations {
			inScope[n] = true
		}
	}
	for _, n := range slices.Sorted(maps.Keys(inScope)) {
		d, ok := byName[n]
		if !ok {
			continue
		}
		f.Destinations = append(f.Destinations, sessionctx.StudioDestination{
			Name: d.Name, Upstream: d.Upstream, EnvKeys: slices.Sorted(maps.Keys(d.ClientEnv())), Git: d.GitRouted(), Note: d.Note,
		})
	}
	switch {
	case roleEgress != nil:
		f.Egress, f.EgressKnown = slices.Sorted(slices.Values(roleEgress.Domains)), true
	case haveKit:
		f.Egress, _ = studio.Ceiling(kitEgress)
		f.EgressKnown = true
	}
	for _, t := range ListTargets(a, store.GetRole, store.GetRoster, now) {
		who := "project contact"
		switch {
		case t.Kind == "channel":
			who = "channel"
		case t.Name == owner:
			who = "your owner"
		case t.Handle != "":
			who = "project contact @" + t.Handle
		}
		f.Targets = append(f.Targets, sessionctx.StudioTarget{Target: t.Kind + ":" + t.Name, Who: who})
	}
	return f
}
```

`supervisor.go`, in the slice-1 compile block: keep the kit's egress alongside its prompt and add the Studio facts before `Compile`:

```go
	var kitEgress []string
	haveKit := false
	if spec.Kit.ID != "" {
		in.Session.Kit = spec.Kit.String()
		if def, ok, derr := ResolveKitDefinition(s.store, spec.Kit); derr == nil && ok {
			in.Kit = sessionctx.Layer{Core: def.Kit.Prompt}
			kitEgress, haveKit = def.Kit.Egress, true
		}
	}
	in.Studio = studioFacts(s.store, actor, spec.Owner, spec.Egress, kitEgress, haveKit, s.now())
```

(`actor` is the value `Raise` already looked up for `ConnectorFor`; `spec.Egress` is already set from the role above.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/jam/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam
git commit -m "feat(jam): gather Studio facts at raise for the session context"
```

---

### Task 4: Lint authored layers against Studio-owned facts

**Files:**
- Modify: `internal/jam/sessionctx/sessionctx.go` (`Compile`)
- Test: `internal/jam/sessionctx/sessionctx_test.go`

**Interfaces:**
- Consumes: `Inputs.Studio` (Task 2).
- Produces: warnings of the form `kit core restates <fact> (owned by the studio layer)`.

- [ ] **Step 1: Write the failing test**

```go
func TestCompileLintsKitRestatingStudioFacts(t *testing.T) {
	in := Inputs{
		Session: SessionFacts{Kind: KindStanding, Name: "n", Project: "p", Role: "r", Kit: "web@v1"},
		Kit:     Layer{Core: "You may reach proxy.golang.org. Ask human:alice for help."},
		Studio:  StudioFacts{Egress: []string{"proxy.golang.org"}, EgressKnown: true, Targets: []StudioTarget{{Target: "human:alice", Who: "your owner"}}},
	}
	got := strings.Join(Compile(in).Warnings, "\n")
	for _, want := range []string{"proxy.golang.org", "human:alice"} {
		if !strings.Contains(got, want) {
			t.Errorf("want a lint warning naming %q, got:\n%s", want, got)
		}
	}
	in.Kit.Core = "Work on the web service."
	if w := Compile(in).Warnings; len(w) != 0 {
		t.Errorf("clean kit core must not warn: %v", w)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jam/sessionctx/ -run Lints`
Expected: FAIL — no warnings.

- [ ] **Step 3: Implement** — at the end of `Compile`, before computing the fingerprint:

```go
	b.Warnings = append(b.Warnings, lintAuthored(LayerKit, in.Kit.Core, in.Studio)...)
```

and add:

```go
// lintAuthored flags an authored core restating a fact the Studio layer owns
// (an egress host or a message target). Heuristic and advisory: it only warns.
func lintAuthored(layer, core string, f StudioFacts) []string {
	var out []string
	for _, h := range f.Egress {
		if strings.Contains(core, strings.TrimPrefix(h, ".")) {
			out = append(out, fmt.Sprintf("%s core restates egress host %s (owned by the studio layer)", layer, h))
		}
	}
	for _, t := range f.Targets {
		if strings.Contains(core, t.Target) {
			out = append(out, fmt.Sprintf("%s core restates message target %s (owned by the studio layer)", layer, t.Target))
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/jam/sessionctx/ ./internal/jam/`
Expected: PASS. (Check `TestCompiledBundleNeverTruncatesBoilerplate` and other "no warnings" tests still pass — they have no Studio facts.)

- [ ] **Step 5: Commit**

```bash
git add internal/jam/sessionctx
git commit -m "feat(sessionctx): lint the kit core for restated studio facts"
```

---

### Task 5: Docs

**Files:**
- Modify: `docs/usage/jam/session-context.md` (layer table, budget), `docs/usage/jam/connector.md` (Note: what it is, `--note`), `docs/usage/jam/comms-addressing.md` (one line: targets appear in the Studio layer), `docs/usage/jam/ui-pages.md` (the destination form gains a Note field — add it beside the OAuth beta mention)

- [ ] **Step 1: Edit** (use the docs-author skill)
  - `session-context.md` layer table, new row after Kit: `| Studio | generated at raise: granted destinations (upstream, env keys, git routing, each destination's note), effective egress (role policy, else the kit ceiling), message targets | 1600 B — never truncated: long lists move to studio/destinations.md, egress.md, targets.md |`; remove "Studio" from the "follow later" sentence; add one line under the table: "Raise warns (in Jam's log) when the kit prompt restates an egress host or a message target — those belong to the Studio layer."
  - `connector.md`: a short subsection "Notes for sessions": `Destination.note` (≤ 300 bytes; `at-jam destination add --note`, or the UI) is shown to every session granted the destination in its [session context](session-context.md); write how to use the route, e.g. the `gh -R $GH_HOST/<owner>/<repo>` hint. Never put a secret in it.
  - `comms-addressing.md`: "A session's message targets (what `list_targets` returns at raise) are listed in its [session context](session-context.md)."
  - Bump `updated:` to the commit date on each.

- [ ] **Step 2: Audit and verify**

Run: the docs-audit checker (compare against `main` — no new findings); `just test && just lint`.
Expected: no new audit findings; PASS.

- [ ] **Step 3: Commit**

```bash
git add docs
git commit -m "docs(jam): the Studio layer and destination notes"
```

---

## Out of scope (later slices)

Project/Role/Jam authored layers, `StudioKit.Notes`, `at-jam context` CLI and admin UI panels (slice 3); `GET /context` per-turn refresh and change notices — so grant or destination edits reach a running session only after re-raise until slice 4 (slice 4). The slice-1 deferred minors stay deferred.
