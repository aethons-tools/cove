# harbor comms C1 — roster + addressing + comms access-graph — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generalize a managed cove's messaging from "own ticket only" to `send(text, to=target)` over a symbolic target space (humans + channels), with harbor authorizing every recipient via a comms access-graph that mirrors the broker's scope model.

**Architecture:** New first-class `Project`/`Roster`/`Human`/`Channel` types in `internal/harbor`, persisted additively in the FileStore. Addressing lives in `Scope`/`Override` (so `EffectiveScope` layers it exactly like `Destinations`/`Repos`). A pure `DecideSend` policy in `decide.go` authorizes a target per-grant-existentially and resolves how to deliver it. The `/messages` handler gains an optional `to`, delivering a human via an @-mention on the cove's own ticket (two-way via existing wake-on) and a channel via a comment on its thread (post-only). Delivery reuses the existing `Commenter` — no new tracker methods. CLI/admin gain a `project` group and `role --addressing`.

**Tech Stack:** Go; `path.Match` globbing (as the broker's `repoAllowed`); official `github.com/modelcontextprotocol/go-sdk` for the cove-side MCP; hermetic tests with fakes.

## Global Constraints

- **`internal/harbor` core stays free of grpc/kit/dispatch/backend/connect imports.** Keep the local-types + `Commenter` interface pattern from COV-145; the Linear adapter stays at the `cmd/at-harbor` wiring layer.
- **Addressing lives in `Scope` and `Override`** (not a separate `Role` field), so `EffectiveScope` handles it uniformly with `Destinations`/`Repos`. Override REPLACES, never merges (existing semantics).
- **Comms authz is fail-closed and mirrors `Decide`:** resolved live, additive across grants, **per-grant existential** (a target must be authorized by a single grant), deny on unknown actor / expired / no grant / malformed target. The cove's **own ticket** (empty `to`) never consults the access-graph and is always allowed.
- **Authz is checked before existence:** a target whose form no grant authorizes returns 403 (never revealing whether it exists); a target authorized-in-form but absent from the roster returns 404.
- **Target syntax:** kind-prefixed names `human:<name>` / `channel:<name>`; addressing globs match within a kind via `path.Match` (`human:*`, `channel:eng-*`, `*`).
- **Secrets and message bodies never hit logs/argv.** Target strings and human handles are non-secret and may be logged; message bodies and tokens must not.
- **Store migration is additive:** a store written before this change (no `projects`, no `addressing`) loads to identical prior behavior (own-ticket only; every non-own-ticket target denied).
- **at-cove / connect / dispatchrun stay go-oidc-free and grpc-free** (unchanged; `cove-master` is a separate binary).
- **TDD, DRY, YAGNI, frequent commits.** Every task ends green (`GOPROXY=off go build ./... && GOPROXY=off go test ./...`), gofmt-clean, `.at-cove/` untouched. Reuse the existing `path.Match` glob helper pattern (`repoAllowed`) — do not add a second matcher style.

---

### Task 1: Roster/Project types + `Scope.Addressing` + store persistence & migration

**Files:**
- Modify: `internal/harbor/identity.go` (add types + `Scope.Addressing`/`Override.Addressing`)
- Modify: `internal/harbor/decide.go` (extend `EffectiveScope` for Addressing)
- Modify: `internal/harbor/filestore.go` (`storeFile.Projects`, `fs.projects`, load/save, roster methods, `Store` interface, `ListProjects` union)
- Test: `internal/harbor/filestore_test.go`, `internal/harbor/decide_test.go`

**Interfaces:**
- Produces: `Human{Name,Handle}`, `Channel{Name,Service,Ref}`, `Roster{Humans,Channels}`, `Project{Name,Roster}`; `Scope.Addressing []string`, `Override.Addressing []string`; store methods `AddHuman(project string, h Human) error`, `AddChannel(project string, c Channel) error`, `RemoveHuman(project, name string) error`, `RemoveChannel(project, name string) error`, `GetProject(name string) (Project, bool)`, `GetRoster(project string) (Roster, bool)`.

- [ ] **Step 1: Write failing tests**

In `internal/harbor/decide_test.go` add:

```go
func TestEffectiveScopeAddressingReplaces(t *testing.T) {
	role := Role{Name: "r", Scope: Scope{Addressing: []string{"human:*"}}}
	// nil override addressing inherits the role's
	if got := EffectiveScope(Grant{Role: "r"}, role).Addressing; !sameStrings(got, []string{"human:*"}) {
		t.Fatalf("inherit: got %v", got)
	}
	// set override addressing REPLACES (no merge)
	g := Grant{Role: "r", Overrides: &Override{Addressing: []string{"channel:eng-help"}}}
	if got := EffectiveScope(g, role).Addressing; !sameStrings(got, []string{"channel:eng-help"}) {
		t.Fatalf("replace: got %v", got)
	}
}
```

In `internal/harbor/filestore_test.go` add:

```go
func TestRosterRoundTripAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.AddHuman("acme", Human{Name: "alice", Handle: "alice.h"}); err != nil {
		t.Fatal(err)
	}
	if err := fs.AddChannel("acme", Channel{Name: "eng-help", Service: "linear", Ref: "ACME-1"}); err != nil {
		t.Fatal(err)
	}
	// reload from disk
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	rr, ok := fs2.GetRoster("acme")
	if !ok || len(rr.Humans) != 1 || rr.Humans[0].Handle != "alice.h" || len(rr.Channels) != 1 || rr.Channels[0].Ref != "ACME-1" {
		t.Fatalf("roster not persisted: %+v ok=%v", rr, ok)
	}
	// a project with a roster shows up in ListProjects even without roles
	found := false
	for _, p := range fs2.ListProjects() {
		if p == "acme" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListProjects missing acme: %v", fs2.ListProjects())
	}
}

func TestAddHumanUpsertsByName(t *testing.T) {
	fs, _ := NewFileStore(filepath.Join(t.TempDir(), "s.json"))
	_ = fs.AddHuman("p", Human{Name: "a", Handle: "old"})
	_ = fs.AddHuman("p", Human{Name: "a", Handle: "new"})
	rr, _ := fs.GetRoster("p")
	if len(rr.Humans) != 1 || rr.Humans[0].Handle != "new" {
		t.Fatalf("expected upsert to new handle, got %+v", rr.Humans)
	}
}

func TestMigrationLeavesEmptyRoster(t *testing.T) {
	// A pre-existing v4 store with no "projects" key loads with empty rosters.
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	if err := os.WriteFile(path, []byte(`{"roles":{},"actors":{"h":{"id":"a","token_hash":"h"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.GetRoster("acme"); ok {
		t.Fatal("expected no roster for absent project")
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'EffectiveScopeAddressing|Roster|AddHuman|MigrationLeaves' -v`
Expected: FAIL (undefined `Human`/`Channel`/`Roster`/`AddHuman`/`GetRoster`/`Scope.Addressing`).

- [ ] **Step 3: Add types + Scope.Addressing (identity.go)**

In `internal/harbor/identity.go`, add `Addressing []string` to `Scope` and `Override`, and the roster types:

```go
// (in Scope struct) — add after Repos:
	Addressing []string `json:"addressing,omitempty"` // allowed comms targets (globs, kind-prefixed)

// (in Override struct) — add after Repos:
	Addressing []string `json:"addressing,omitempty"` // REPLACES Scope.Addressing when non-nil

// Human is a roster member reachable by @-mention on a tracker thread.
type Human struct {
	Name   string `json:"name"`   // roster-local name, e.g. "alice"
	Handle string `json:"handle"` // tracker @-mention handle
}

// Channel is a named conduit on a Service. C1: Service == "linear", Ref is a
// tracker issue identifier (e.g. "ACME-1") the channel posts to.
type Channel struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	Ref     string `json:"ref"`
}

// Roster is a Project's addressable membership.
type Roster struct {
	Humans   []Human   `json:"humans,omitempty"`
	Channels []Channel `json:"channels,omitempty"`
}

// Project is the top of the config tree: it owns its Roster (and, in a later
// slice, its escalation policy). Roles remain keyed by (project, name).
type Project struct {
	Name   string `json:"name"`
	Roster Roster `json:"roster"`
}
```

- [ ] **Step 4: Extend `EffectiveScope` (decide.go)**

Add the Addressing branch inside `EffectiveScope`'s `if g.Overrides != nil {` block:

```go
		if g.Overrides.Addressing != nil {
			s.Addressing = g.Overrides.Addressing
		}
```

- [ ] **Step 5: Store persistence + methods (filestore.go)**

Add `Projects map[string]Project` to `storeFile` (json:`"projects,omitempty"`); add `projects map[string]Project` to `FileStore`; init it in `NewFileStore`; load it in the v3/v4 branch (`if v3.Projects != nil { fs.projects = v3.Projects }`); include it in `save()` (`Projects: fs.projects`). Then add methods and extend `ListProjects`:

```go
func (fs *FileStore) AddHuman(project string, h Human) error {
	if h.Name == "" {
		return fmt.Errorf("human name required")
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p := fs.projects[project]
	p.Name = project
	replaced := false
	for i := range p.Roster.Humans {
		if p.Roster.Humans[i].Name == h.Name {
			p.Roster.Humans[i] = h
			replaced = true
			break
		}
	}
	if !replaced {
		p.Roster.Humans = append(p.Roster.Humans, h)
	}
	fs.projects[project] = p
	return fs.save()
}

func (fs *FileStore) AddChannel(project string, c Channel) error {
	if c.Name == "" {
		return fmt.Errorf("channel name required")
	}
	if c.Service == "" {
		c.Service = "linear"
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p := fs.projects[project]
	p.Name = project
	replaced := false
	for i := range p.Roster.Channels {
		if p.Roster.Channels[i].Name == c.Name {
			p.Roster.Channels[i] = c
			replaced = true
			break
		}
	}
	if !replaced {
		p.Roster.Channels = append(p.Roster.Channels, c)
	}
	fs.projects[project] = p
	return fs.save()
}

func (fs *FileStore) RemoveHuman(project, name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p, ok := fs.projects[project]
	if !ok {
		return fmt.Errorf("project %q not found", project)
	}
	out := p.Roster.Humans[:0]
	for _, h := range p.Roster.Humans {
		if h.Name != name {
			out = append(out, h)
		}
	}
	p.Roster.Humans = out
	fs.projects[project] = p
	return fs.save()
}

func (fs *FileStore) RemoveChannel(project, name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p, ok := fs.projects[project]
	if !ok {
		return fmt.Errorf("project %q not found", project)
	}
	out := p.Roster.Channels[:0]
	for _, c := range p.Roster.Channels {
		if c.Name != name {
			out = append(out, c)
		}
	}
	p.Roster.Channels = out
	fs.projects[project] = p
	return fs.save()
}

func (fs *FileStore) GetProject(name string) (Project, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p, ok := fs.projects[name]
	return p, ok
}

func (fs *FileStore) GetRoster(project string) (Roster, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p, ok := fs.projects[project]
	if !ok {
		return Roster{}, false
	}
	// copy so callers can't mutate the store's slices
	r := Roster{
		Humans:   append([]Human(nil), p.Roster.Humans...),
		Channels: append([]Channel(nil), p.Roster.Channels...),
	}
	return r, true
}
```

Extend `ListProjects` to union role-projects with roster-projects (dedup):

```go
func (fs *FileStore) ListProjects() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for p := range fs.roles {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for p := range fs.projects {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
```

(If `ListProjects` already sorts / has a different body, preserve its existing return contract and just add the `fs.projects` keys to the union. Add the `sort` import if not present.)

Add the six new methods to the `Store` interface block.

- [ ] **Step 6: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'EffectiveScopeAddressing|Roster|AddHuman|MigrationLeaves' -v` → PASS
Then full: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → all pass (any fake `Store` in tests may need the six new methods — find with `GOPROXY=off go test ./...`, not `go build`).

- [ ] **Step 7: gofmt + commit**

```bash
gofmt -w internal/harbor/identity.go internal/harbor/decide.go internal/harbor/filestore.go internal/harbor/*_test.go
git add internal/harbor/identity.go internal/harbor/decide.go internal/harbor/filestore.go internal/harbor/filestore_test.go internal/harbor/decide_test.go
git commit -m "harbor: Project/Roster types + Scope.Addressing + store persistence (COV-161)" # + trailers
```

---

### Task 2: `DecideSend` + `ListTargets` comms-policy (pure)

**Files:**
- Modify: `internal/harbor/decide.go`
- Test: `internal/harbor/decide_test.go`

**Interfaces:**
- Consumes: `Actor`, `Grant`, `Role`, `Roster`, `Human`, `Channel`, `EffectiveScope` (Task 1).
- Produces:
  - `type SendTarget struct { Kind, Name, Handle, Ref, Project string }`
  - `var ErrSendDenied error`, `var ErrSendUnresolved error`
  - `func DecideSend(a Actor, getRole func(project, role string) (Role, bool), getRoster func(project string) (Roster, bool), target string, now time.Time) (SendTarget, error)`
  - `func ListTargets(a Actor, getRole func(project, role string) (Role, bool), getRoster func(project string) (Roster, bool), now time.Time) []SendTarget`

- [ ] **Step 1: Write failing tests**

```go
func TestDecideSend(t *testing.T) {
	roles := map[string]map[string]Role{
		"acme": {
			"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*", "channel:eng-help"}}},
			"noaddr": {Name: "noaddr"},
		},
	}
	rosters := map[string]Roster{
		"acme": {
			Humans:   []Human{{Name: "alice", Handle: "alice.h"}},
			Channels: []Channel{{Name: "eng-help", Service: "linear", Ref: "ACME-1"}},
		},
	}
	getRole := func(p, r string) (Role, bool) { rr, ok := roles[p][r]; return rr, ok }
	getRoster := func(p string) (Roster, bool) { rr, ok := rosters[p]; return rr, ok }
	now := time.Unix(1_000, 0)

	actor := Actor{ID: "a", Grants: []Grant{{Project: "acme", Role: "impl"}}}

	// authorized human → resolves handle
	st, err := DecideSend(actor, getRole, getRoster, "human:alice", now)
	if err != nil || st.Kind != "human" || st.Handle != "alice.h" || st.Project != "acme" {
		t.Fatalf("human: %+v err=%v", st, err)
	}
	// authorized channel → resolves ref
	st, err = DecideSend(actor, getRole, getRoster, "channel:eng-help", now)
	if err != nil || st.Kind != "channel" || st.Ref != "ACME-1" {
		t.Fatalf("channel: %+v err=%v", st, err)
	}
	// glob does not authorize channel:other → denied (403), and never leaks existence
	if _, err := DecideSend(actor, getRole, getRoster, "channel:other", now); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("expected denied, got %v", err)
	}
	// authorized-in-form (human:*) but not in roster → unresolved (404)
	if _, err := DecideSend(actor, getRole, getRoster, "human:bob", now); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("expected unresolved, got %v", err)
	}
	// malformed target → denied
	if _, err := DecideSend(actor, getRole, getRoster, "alice", now); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("expected denied for malformed, got %v", err)
	}
	// no-addressing role → denied
	na := Actor{ID: "n", Grants: []Grant{{Project: "acme", Role: "noaddr"}}}
	if _, err := DecideSend(na, getRole, getRoster, "human:alice", now); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("expected denied for no addressing, got %v", err)
	}
	// expired actor → denied
	exp := Actor{ID: "e", Expiry: now.Add(-time.Hour), Grants: []Grant{{Project: "acme", Role: "impl"}}}
	if _, err := DecideSend(exp, getRole, getRoster, "human:alice", now); err == nil {
		t.Fatal("expected expired actor denied")
	}
}

func TestDecideSendPerGrantExistential(t *testing.T) {
	// grant A authorizes humans in acme; grant B authorizes channels in beta.
	roles := map[string]map[string]Role{
		"acme": {"a": {Name: "a", Scope: Scope{Addressing: []string{"human:*"}}}},
		"beta": {"b": {Name: "b", Scope: Scope{Addressing: []string{"channel:*"}}}},
	}
	rosters := map[string]Roster{
		"acme": {Humans: []Human{{Name: "alice", Handle: "h"}}},
		"beta": {Channels: []Channel{{Name: "ops", Ref: "BETA-9"}}},
	}
	getRole := func(p, r string) (Role, bool) { rr, ok := roles[p][r]; return rr, ok }
	getRoster := func(p string) (Roster, bool) { rr, ok := rosters[p]; return rr, ok }
	a := Actor{ID: "x", Grants: []Grant{{Project: "acme", Role: "a"}, {Project: "beta", Role: "b"}}}
	now := time.Unix(1, 0)
	if st, err := DecideSend(a, getRole, getRoster, "channel:ops", now); err != nil || st.Ref != "BETA-9" {
		t.Fatalf("beta channel via grant B: %+v %v", st, err)
	}
	// a human that only exists in beta's project is not addressable (acme grant authorizes humans but acme has no bob; beta grant doesn't authorize humans)
	if _, err := DecideSend(a, getRole, getRoster, "human:ops", now); err == nil {
		t.Fatal("expected cross-project recombination to fail")
	}
}

func TestListTargets(t *testing.T) {
	roles := map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}}
	rosters := map[string]Roster{"acme": {Humans: []Human{{Name: "alice", Handle: "h"}, {Name: "bob", Handle: "h2"}}, Channels: []Channel{{Name: "eng", Ref: "R"}}}}
	getRole := func(p, r string) (Role, bool) { rr, ok := roles[p][r]; return rr, ok }
	getRoster := func(p string) (Roster, bool) { rr, ok := rosters[p]; return rr, ok }
	a := Actor{ID: "a", Grants: []Grant{{Project: "acme", Role: "impl"}}}
	got := ListTargets(a, getRole, getRoster, time.Unix(1, 0))
	// only humans are addressable (channel not in addressing)
	names := map[string]bool{}
	for _, tg := range got {
		names[tg.Kind+":"+tg.Name] = true
	}
	if !names["human:alice"] || !names["human:bob"] || names["channel:eng"] {
		t.Fatalf("unexpected targets: %+v", got)
	}
}
```

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'DecideSend|ListTargets' -v` → FAIL (undefined).

- [ ] **Step 3: Implement in decide.go**

```go
// SendTarget is a resolved comms recipient: how harbor should deliver a send.
type SendTarget struct {
	Kind    string // "human" | "channel"
	Name    string // roster-local name
	Handle  string // human @-mention handle (Kind=="human")
	Ref     string // channel thread identifier (Kind=="channel")
	Project string // the project whose grant authorized+resolved this target
}

// ErrSendDenied means no grant's addressing authorizes the target's form (403);
// it never reveals whether the target exists. ErrSendUnresolved means the target
// was authorized-in-form but is absent from the roster of every authorizing
// grant's project (404).
var (
	ErrSendDenied     = errors.New("comms: send target not authorized")
	ErrSendUnresolved = errors.New("comms: send target not found")
)

func parseTarget(target string) (kind, name string, ok bool) {
	k, n, found := strings.Cut(target, ":")
	if !found || n == "" || (k != "human" && k != "channel") {
		return "", "", false
	}
	return k, n, true
}

func targetAllowed(target string, globs []string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, target); ok {
			return true
		}
	}
	return false
}

func resolveInRoster(kind, name string, r Roster) (SendTarget, bool) {
	switch kind {
	case "human":
		for _, h := range r.Humans {
			if h.Name == name {
				return SendTarget{Kind: "human", Name: name, Handle: h.Handle}, true
			}
		}
	case "channel":
		for _, c := range r.Channels {
			if c.Name == name {
				return SendTarget{Kind: "channel", Name: name, Ref: c.Ref}, true
			}
		}
	}
	return SendTarget{}, false
}

// DecideSend authorizes actor a to send to target and resolves delivery. Live,
// additive across grants, per-grant existential, fail-closed. See ErrSendDenied
// / ErrSendUnresolved for the 403/404 split (authz checked before existence).
func DecideSend(a Actor, getRole func(project, role string) (Role, bool), getRoster func(project string) (Roster, bool), target string, now time.Time) (SendTarget, error) {
	if !a.Expiry.IsZero() && now.After(a.Expiry) {
		return SendTarget{}, fmt.Errorf("actor %q expired", a.ID)
	}
	kind, name, ok := parseTarget(target)
	if !ok {
		return SendTarget{}, ErrSendDenied
	}
	authorized := false
	for _, g := range a.Grants {
		role, ok := getRole(g.Project, g.Role)
		if !ok {
			continue
		}
		if !targetAllowed(target, EffectiveScope(g, role).Addressing) {
			continue
		}
		authorized = true
		roster, ok := getRoster(g.Project)
		if !ok {
			continue
		}
		if st, ok := resolveInRoster(kind, name, roster); ok {
			st.Project = g.Project
			return st, nil
		}
	}
	if authorized {
		return SendTarget{}, ErrSendUnresolved
	}
	return SendTarget{}, ErrSendDenied
}

// ListTargets returns the actor's authorized-and-resolvable targets (dedup by
// kind:name). Order is grant-then-roster order.
func ListTargets(a Actor, getRole func(project, role string) (Role, bool), getRoster func(project string) (Roster, bool), now time.Time) []SendTarget {
	if !a.Expiry.IsZero() && now.After(a.Expiry) {
		return nil
	}
	seen := map[string]bool{}
	var out []SendTarget
	for _, g := range a.Grants {
		role, ok := getRole(g.Project, g.Role)
		if !ok {
			continue
		}
		globs := EffectiveScope(g, role).Addressing
		roster, ok := getRoster(g.Project)
		if !ok {
			continue
		}
		for _, h := range roster.Humans {
			key := "human:" + h.Name
			if !seen[key] && targetAllowed(key, globs) {
				seen[key] = true
				out = append(out, SendTarget{Kind: "human", Name: h.Name, Handle: h.Handle, Project: g.Project})
			}
		}
		for _, c := range roster.Channels {
			key := "channel:" + c.Name
			if !seen[key] && targetAllowed(key, globs) {
				seen[key] = true
				out = append(out, SendTarget{Kind: "channel", Name: c.Name, Ref: c.Ref, Project: g.Project})
			}
		}
	}
	return out
}
```

Add imports as needed: `errors`, `path`, `strings` (keep `fmt`, `slices`, `time`).

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'DecideSend|ListTargets' -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/harbor/decide.go internal/harbor/decide_test.go
git add internal/harbor/decide.go internal/harbor/decide_test.go
git commit -m "harbor: DecideSend + ListTargets comms access-graph policy (COV-161)" # + trailers
```

---

### Task 3: `/messages` send with `to` target (authz + delivery)

**Files:**
- Modify: `internal/harbor/messages.go`
- Test: `internal/harbor/messages_test.go`

**Interfaces:**
- Consumes: `DecideSend`, `ErrSendDenied`, `ErrSendUnresolved`, `SendTarget` (Task 2); `Commenter`, `Instance` (existing).
- Produces: `messagesStore` gains `GetRole(project, name string) (Role, bool)` and `GetRoster(project string) (Roster, bool)`; the POST body gains `to`.

- [ ] **Step 1: Write failing tests**

In `internal/harbor/messages_test.go`, extend the existing fake store to satisfy the two new methods (add `roles map[string]map[string]Role` and `rosters map[string]Roster` fields + `GetRole`/`GetRoster`), and add:

```go
func TestSendToHumanMentionsOnOwnTicket(t *testing.T) {
	fc := &fakeCommenter{issueIDs: map[string]string{"ACME-42": "iss-42"}}
	st := newFakeMsgStore()
	st.actors["h"] = Actor{ID: "cove-1", TokenHash: "h", Grants: []Grant{{Project: "acme", Role: "impl"}}}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Unit: "ACME-42"}
	st.roles["acme"] = map[string]Role{"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}
	st.rosters["acme"] = Roster{Humans: []Human{{Name: "alice", Handle: "alice.h"}}}
	h := NewMessagesHandler(st, fc, testLogger())

	rec := postMessage(t, h, "tok-for-h", `{"body":"ping","to":"human:alice"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rec.Code)
	}
	// delivered on OWN ticket (iss-42) with an @mention prefix
	if got := fc.lastIssue; got != "iss-42" {
		t.Fatalf("delivered to %q, want own ticket iss-42", got)
	}
	if !strings.HasPrefix(fc.lastBody, "@alice.h ") || !strings.Contains(fc.lastBody, "ping") {
		t.Fatalf("body=%q, want @mention prefix", fc.lastBody)
	}
}

func TestSendToChannelPostsOnChannelThread(t *testing.T) {
	fc := &fakeCommenter{issueIDs: map[string]string{"ACME-42": "iss-42", "ACME-1": "iss-1"}}
	st := newFakeMsgStore()
	st.actors["h"] = Actor{ID: "cove-1", TokenHash: "h", Grants: []Grant{{Project: "acme", Role: "impl"}}}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Unit: "ACME-42"}
	st.roles["acme"] = map[string]Role{"impl": {Name: "impl", Scope: Scope{Addressing: []string{"channel:*"}}}}
	st.rosters["acme"] = Roster{Channels: []Channel{{Name: "eng-help", Service: "linear", Ref: "ACME-1"}}}
	h := NewMessagesHandler(st, fc, testLogger())

	rec := postMessage(t, h, "tok-for-h", `{"body":"heads up","to":"channel:eng-help"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rec.Code)
	}
	if fc.lastIssue != "iss-1" || fc.lastBody != "heads up" {
		t.Fatalf("delivered to %q body %q, want channel iss-1 verbatim", fc.lastIssue, fc.lastBody)
	}
}

func TestSendToDeniedIs403(t *testing.T) {
	fc := &fakeCommenter{issueIDs: map[string]string{"ACME-42": "iss-42"}}
	st := newFakeMsgStore()
	st.actors["h"] = Actor{ID: "cove-1", TokenHash: "h", Grants: []Grant{{Project: "acme", Role: "impl"}}}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Unit: "ACME-42"}
	st.roles["acme"] = map[string]Role{"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}
	st.rosters["acme"] = Roster{Channels: []Channel{{Name: "secret", Ref: "X"}}}
	h := NewMessagesHandler(st, fc, testLogger())

	rec := postMessage(t, h, "tok-for-h", `{"body":"x","to":"channel:secret"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	if fc.lastIssue != "" {
		t.Fatal("must not deliver on denied")
	}
}

func TestSendToUnresolvedIs404(t *testing.T) {
	fc := &fakeCommenter{issueIDs: map[string]string{"ACME-42": "iss-42"}}
	st := newFakeMsgStore()
	st.actors["h"] = Actor{ID: "cove-1", TokenHash: "h", Grants: []Grant{{Project: "acme", Role: "impl"}}}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Unit: "ACME-42"}
	st.roles["acme"] = map[string]Role{"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}
	st.rosters["acme"] = Roster{Humans: []Human{{Name: "alice", Handle: "a"}}}
	h := NewMessagesHandler(st, fc, testLogger())

	rec := postMessage(t, h, "tok-for-h", `{"body":"x","to":"human:bob"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

func TestSendNoTargetStillOwnTicket(t *testing.T) {
	fc := &fakeCommenter{issueIDs: map[string]string{"ACME-42": "iss-42"}}
	st := newFakeMsgStore()
	st.actors["h"] = Actor{ID: "cove-1", TokenHash: "h"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Unit: "ACME-42"}
	h := NewMessagesHandler(st, fc, testLogger())

	rec := postMessage(t, h, "tok-for-h", `{"body":"status"}`)
	if rec.Code != http.StatusNoContent || fc.lastIssue != "iss-42" || fc.lastBody != "status" {
		t.Fatalf("own-ticket path regressed: code=%d issue=%q body=%q", rec.Code, fc.lastIssue, fc.lastBody)
	}
}
```

> Adapt the exact fake/helper names (`fakeCommenter`, `postMessage`, `testLogger`, token→hash mapping) to whatever `messages_test.go` already defines from COV-145. The token used in `postMessage` must hash to the actor's `TokenHash` via the same helper the existing tests use. `fakeCommenter` must record `lastIssue`/`lastBody` and map identifier→issueID via `IssueByIdentifier`.

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'SendTo|SendNoTarget' -v` → FAIL (no `to` field; missing store methods).

- [ ] **Step 3: Widen `messagesStore` + implement delivery**

In `internal/harbor/messages.go`, add to the `messagesStore` interface:

```go
	GetRole(project, name string) (Role, bool)
	GetRoster(project string) (Roster, bool)
```

Replace `handlePost` with the targeted version (own-ticket path unchanged; `time` already imported):

```go
func (h *MessagesHandler) handlePost(w http.ResponseWriter, r *http.Request, actor Actor, inst Instance, issueID string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMessageBodyBytes)
	var req struct {
		Body string `json:"body"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Body == "" {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	// deliverIssue defaults to the cove's own ticket; a channel target overrides
	// it. body may be prefixed with an @-mention for a human target.
	deliverIssue, body := issueID, req.Body
	if req.To != "" {
		st, err := DecideSend(actor, h.store.GetRole, h.store.GetRoster, req.To, time.Now())
		switch {
		case errors.Is(err, ErrSendDenied):
			http.Error(w, "target not authorized", http.StatusForbidden)
			return
		case errors.Is(err, ErrSendUnresolved):
			http.Error(w, "target not found", http.StatusNotFound)
			return
		case err != nil:
			http.Error(w, "target error", http.StatusForbidden)
			return
		}
		switch st.Kind {
		case "human":
			body = "@" + st.Handle + " " + req.Body // reply lands on own ticket → existing wake-on
		case "channel":
			chID, err := h.cmt.IssueByIdentifier(r.Context(), st.Ref)
			if err != nil {
				h.log.Error("messages: resolve channel failed", "actor", actor.ID, "target", req.To, "error", err.Error())
				http.Error(w, "channel unavailable", http.StatusBadGateway)
				return
			}
			deliverIssue = chID
		}
	}

	if err := h.cmt.PostComment(r.Context(), deliverIssue, body); err != nil {
		h.log.Error("messages: post comment failed", "actor", actor.ID, "ticket", inst.Unit, "error", err.Error())
		http.Error(w, "send failed", http.StatusBadGateway)
		return
	}
	h.log.Info("messages", "actor", actor.ID, "ticket", inst.Unit, "op", "send", "to", req.To, "bytes", len(req.Body))
	w.WriteHeader(http.StatusNoContent)
}
```

(Note: the default-error case for a non-sentinel `DecideSend` error — only expiry today — is treated as 403; log-free to avoid leaking. `req.To` is a non-secret target string, safe to log.)

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'SendTo|SendNoTarget|Messages' -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass (a real `*FileStore` already satisfies the widened `messagesStore` via Task 1's `GetRole`/`GetRoster`).

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/harbor/messages.go internal/harbor/messages_test.go
git add internal/harbor/messages.go internal/harbor/messages_test.go
git commit -m "harbor: /messages send(to) — targeted brokered send with access-graph authz (COV-161)" # + trailers
```

---

### Task 4: `GET /messages/targets` endpoint + mount

**Files:**
- Modify: `internal/harbor/messages.go` (targets branch in `ServeHTTP` + response type)
- Modify: `cmd/at-harbor/mux.go` (route `/messages/targets`)
- Test: `internal/harbor/messages_test.go`, `cmd/at-harbor/mux_test.go` (if present)

**Interfaces:**
- Consumes: `ListTargets` (Task 2), `messagesStore` (widened in Task 3).
- Produces: `GET /messages/targets` → `{"targets":[{"target":"human:alice","kind":"human","name":"alice"}, …]}`.

- [ ] **Step 1: Write failing test**

```go
func TestTargetsListsAllowedTargets(t *testing.T) {
	fc := &fakeCommenter{issueIDs: map[string]string{"ACME-42": "iss-42"}}
	st := newFakeMsgStore()
	st.actors["h"] = Actor{ID: "cove-1", TokenHash: "h", Grants: []Grant{{Project: "acme", Role: "impl"}}}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Unit: "ACME-42"}
	st.roles["acme"] = map[string]Role{"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}
	st.rosters["acme"] = Roster{Humans: []Human{{Name: "alice", Handle: "a"}}, Channels: []Channel{{Name: "eng", Ref: "R"}}}
	h := NewMessagesHandler(st, fc, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/messages/targets", nil)
	req.Header.Set("Authorization", "Bearer "+tokenFor("h")) // same helper the other tests use
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var out struct {
		Targets []struct {
			Target, Kind, Name string
		} `json:"targets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Targets) != 1 || out.Targets[0].Target != "human:alice" {
		t.Fatalf("targets=%+v (channel must be excluded — not in addressing)", out.Targets)
	}
}
```

- [ ] **Step 2: Run test, verify fail**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'TargetsLists' -v` → FAIL (targets path returns read/inbox, not targets).

- [ ] **Step 3: Add the targets branch**

In `ServeHTTP`, after resolving `actor`/`inst` but before the ticket resolution (targets needs no ticket), route the targets path. Restructure so the GET-targets case does not call `IssueByIdentifier`:

```go
	// GET /messages/targets — the actor's addressable targets (no ticket needed).
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/targets") {
		h.handleTargets(w, r, actor)
		return
	}

	ctx := r.Context()
	issueID, err := h.cmt.IssueByIdentifier(ctx, inst.Unit)
	// … unchanged …
```

Add the handler + response type:

```go
type targetOut struct {
	Target string `json:"target"` // "human:alice"
	Kind   string `json:"kind"`
	Name   string `json:"name"`
}

func (h *MessagesHandler) handleTargets(w http.ResponseWriter, r *http.Request, actor Actor) {
	targets := ListTargets(actor, h.store.GetRole, h.store.GetRoster, time.Now())
	out := make([]targetOut, 0, len(targets))
	for _, t := range targets {
		out = append(out, targetOut{Target: t.Kind + ":" + t.Name, Kind: t.Kind, Name: t.Name})
	}
	h.log.Info("messages", "actor", actor.ID, "op", "targets", "count", len(out))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Targets []targetOut `json:"targets"`
	}{Targets: out}); err != nil {
		h.log.Error("messages: encode targets failed", "actor", actor.ID, "error", err.Error())
	}
}
```

> Note: the `Allow` set at the top of `ServeHTTP` already permits GET; the `/messages/targets` path is only reached via GET here. Do not deliver handles in the targets list (a handle is roster config, not needed by the agent to address — the agent uses `human:alice`).

- [ ] **Step 4: Route `/messages/targets` in the mux**

In `cmd/at-harbor/mux.go`, extend `messagesMux` so the targets subpath also reaches `msgH`:

```go
func messagesMux(msgH, broker http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/messages" || r.URL.Path == "/messages/targets" {
			msgH.ServeHTTP(w, r)
			return
		}
		broker.ServeHTTP(w, r)
	})
}
```

If `mux_test.go` exists, add a case asserting `/messages/targets` routes to `msgH` (a sentinel handler), not the broker.

- [ ] **Step 5: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'Targets' ./cmd/at-harbor/ -run 'Mux|Messages' -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass.

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -w internal/harbor/messages.go internal/harbor/messages_test.go cmd/at-harbor/mux.go
git add internal/harbor/messages.go internal/harbor/messages_test.go cmd/at-harbor/mux.go cmd/at-harbor/mux_test.go
git commit -m "harbor: GET /messages/targets — addressable-target discovery (COV-161)" # + trailers
```

---

### Task 5: cove-master MCP — `send(to)` + `list_targets`

**Files:**
- Modify: `cmd/cove-master/mcp.go`
- Test: `cmd/cove-master/mcp_test.go`

**Interfaces:**
- Consumes: harbor `POST /messages` (`{body,to}`) and `GET /messages/targets` (Tasks 3–4).
- Produces: MCP `send` tool gains an optional `to`; new `list_targets` tool.

- [ ] **Step 1: Write failing tests**

In `cmd/cove-master/mcp_test.go`, extend the existing in-memory/httptest harness (mirror COV-145's `TestMCPSend`/`TestMCPRead`):

```go
func TestMCPSendForwardsTo(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_HARBOR_RUNTIME_ADDR":
			return srv.URL
		case "AT_HARBOR_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.send(context.Background(), "hi", "human:alice"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/messages" || !strings.Contains(gotBody, `"to":"human:alice"`) || !strings.Contains(gotBody, `"body":"hi"`) {
		t.Fatalf("path=%q body=%q", gotPath, gotBody)
	}
}

func TestMCPListTargets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages/targets" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"targets":[{"target":"human:alice","kind":"human","name":"alice"}]}`))
	}))
	defer srv.Close()
	c, _ := newMessagingClient(func(k string) string {
		switch k {
		case "AT_HARBOR_RUNTIME_ADDR":
			return srv.URL
		case "AT_HARBOR_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	out, err := c.listTargets(context.Background())
	if err != nil || len(out.Targets) != 1 || out.Targets[0].Target != "human:alice" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}
```

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./cmd/cove-master/ -run 'MCPSendForwardsTo|MCPListTargets' -v` → FAIL.

- [ ] **Step 3: Implement**

Extend `sendIn`, add `targetsOut`/`listTargetsIn`, update the client `send`, add `listTargets`, add a path-parameterized `do`, and register the tools.

Change `do` to accept a path (so GET can hit `/messages/targets`):

```go
func (c *messagingClient) do(ctx context.Context, method, pathSuffix string, body []byte) ([]byte, error) {
	// … build req against c.baseURL + pathSuffix (was hardcoded "/messages") …
```

Update existing callers: `send` → `c.do(ctx, http.MethodPost, "/messages", payload)`; `read` → `c.do(ctx, http.MethodGet, "/messages", nil)`.

```go
// sendIn — add the optional target.
type sendIn struct {
	Text string `json:"text" jsonschema:"the message body"`
	To   string `json:"to,omitempty" jsonschema:"optional target: human:<name> or channel:<name>; omit to post to this cove's own ticket"`
}

type targetItem struct {
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
}
type listTargetsIn struct{}
type targetsOut struct {
	Targets []targetItem `json:"targets"`
}

func (c *messagingClient) send(ctx context.Context, text, to string) error {
	payload, err := json.Marshal(struct {
		Body string `json:"body"`
		To   string `json:"to,omitempty"`
	}{Body: text, To: to})
	if err != nil {
		return fmt.Errorf("encoding send payload: %w", err)
	}
	_, err = c.do(ctx, http.MethodPost, "/messages", payload)
	return err
}

func (c *messagingClient) listTargets(ctx context.Context) (targetsOut, error) {
	body, err := c.do(ctx, http.MethodGet, "/messages/targets", nil)
	if err != nil {
		return targetsOut{}, err
	}
	var out targetsOut
	if err := json.Unmarshal(body, &out); err != nil {
		return targetsOut{}, fmt.Errorf("decoding targets response")
	}
	return out, nil
}
```

In `newMessagingServer`, update the `send` tool handler to pass `in.To`, and add the `list_targets` tool:

```go
	mcp.AddTool(s, &mcp.Tool{
		Name:        "send",
		Description: "Post a message. Omit 'to' for this cove's own ticket; set to=human:<name> to @-mention a person (their reply reaches you), or to=channel:<name> to post to a channel.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sendIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		if err := client.send(ctx, in.Text, in.To); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_targets",
		Description: "List the targets this cove may send to (human:<name> / channel:<name>).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listTargetsIn) (*mcp.CallToolResult, targetsOut, error) {
		if cfgErr != nil {
			return nil, targetsOut{}, cfgErr
		}
		out, err := client.listTargets(ctx)
		if err != nil {
			return nil, targetsOut{}, err
		}
		return nil, out, nil
	})
```

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./cmd/cove-master/ -v` → PASS (existing send/read tests still pass with the new `send` signature updated in-test).
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass. Remove any stray built binary.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w cmd/cove-master/mcp.go cmd/cove-master/mcp_test.go
git add cmd/cove-master/mcp.go cmd/cove-master/mcp_test.go
git commit -m "harbor: cove-master MCP send(to) + list_targets (COV-161)" # + trailers
```

---

### Task 6: admin API + adminclient + CLI (`project` group, `role --addressing`)

**Files:**
- Modify: `internal/harbor/admin.go` (RoleBody/RoleSummary `Addressing`; project/roster routes)
- Modify: `internal/harbor/adminclient/adminclient.go` (project/roster methods; role addressing)
- Modify: `cmd/at-harbor/main.go` (`project` command group; `role add --addressing`)
- Test: `internal/harbor/admin_test.go`, `internal/harbor/adminclient/adminclient_test.go` (mirror existing role/kit tests)

**Interfaces:**
- Consumes: store roster methods (Task 1); `Scope.Addressing`.
- Produces: `POST /admin/projects/{project}/humans`, `POST /admin/projects/{project}/channels`, `GET /admin/projects/{project}/roster`, `DELETE …/humans/{name}`, `DELETE …/channels/{name}`; adminclient `AddHuman`/`AddChannel`/`GetRoster`/`RemoveHuman`/`RemoveChannel`; `at-harbor project` subcommands; `role add --addressing`.

- [ ] **Step 1: Write failing tests**

Mirror the existing `admin_test.go` role/kit route tests and `adminclient_test.go` round-trips. Add:

```go
// admin_test.go
func TestAdminRosterRoutes(t *testing.T) {
	// POST a human + channel, GET the roster back. (Use the test's existing
	// authenticated admin handler + client helpers.)
	// assert GET /admin/projects/acme/roster returns the human and channel.
}

func TestAdminRoleAddressingRoundTrips(t *testing.T) {
	// POST /admin/roles with Addressing:["human:*"] then GET /admin/roles and
	// assert the addressing came back.
}
```

```go
// adminclient_test.go — mirror TestPutRole etc.:
func TestClientRosterAndAddressing(t *testing.T) {
	// against a fake admin server: AddHuman / AddChannel / GetRoster round-trip;
	// PutRole with Scope.Addressing round-trips via ListRoles.
}
```

Fill these in with the concrete assertions matching the file's existing helper style (the reviewer requires real assertions, not stubs).

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/harbor/ ./internal/harbor/adminclient/ -run 'Roster|Addressing' -v` → FAIL.

- [ ] **Step 3: admin.go — body/summary fields + routes**

Add `Addressing []string \`json:"addressing,omitempty"\`` to `RoleBody` and `RoleSummary`. In the `POST /admin/roles` handler, set `Scope{…, Addressing: b.Addressing}`; in the `GET /admin/roles` list, populate `Addressing` from the role's scope. Add roster routes (mirror the kit/grant handler style, using `decode`, `operatorID`, `orDefaultProject`):

```go
	mux.HandleFunc("GET /admin/projects/{project}/roster", func(w http.ResponseWriter, r *http.Request) {
		rr, _ := store.GetRoster(r.PathValue("project"))
		writeJSON(w, rr) // use the file's existing JSON writer
	})
	mux.HandleFunc("POST /admin/projects/{project}/humans", func(w http.ResponseWriter, r *http.Request) {
		var b Human
		if !decode(w, r, &b) {
			return
		}
		if err := store.AddHuman(r.PathValue("project"), b); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin roster human", "operator", operatorID(r), "project", r.PathValue("project"), "name", b.Name)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /admin/projects/{project}/channels", func(w http.ResponseWriter, r *http.Request) {
		var b Channel
		if !decode(w, r, &b) {
			return
		}
		if err := store.AddChannel(r.PathValue("project"), b); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin roster channel", "operator", operatorID(r), "project", r.PathValue("project"), "name", b.Name)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("DELETE /admin/projects/{project}/humans/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := store.RemoveHuman(r.PathValue("project"), r.PathValue("name")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/projects/{project}/channels/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := store.RemoveChannel(r.PathValue("project"), r.PathValue("name")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
```

> Use whatever JSON-write helper `admin.go` already uses for GET responses (e.g. an existing `writeJSON`/inline `json.NewEncoder`); match the surrounding style. All routes sit inside `NewAdminHandler`, so they inherit the `/admin/*` authenticator.

- [ ] **Step 4: adminclient.go — methods**

Mirror `PutRole`/`AddGrant`:

```go
func (c *Client) AddHuman(project string, h harbor.Human) error {
	return c.do(http.MethodPost, "/admin/projects/"+url.PathEscape(project)+"/humans", h, nil)
}
func (c *Client) AddChannel(project string, ch harbor.Channel) error {
	return c.do(http.MethodPost, "/admin/projects/"+url.PathEscape(project)+"/channels", ch, nil)
}
func (c *Client) GetRoster(project string) (harbor.Roster, error) {
	var rr harbor.Roster
	err := c.do(http.MethodGet, "/admin/projects/"+url.PathEscape(project)+"/roster", nil, &rr)
	return rr, err
}
func (c *Client) RemoveHuman(project, name string) error {
	return c.do(http.MethodDelete, "/admin/projects/"+url.PathEscape(project)+"/humans/"+url.PathEscape(name), nil, nil)
}
func (c *Client) RemoveChannel(project, name string) error {
	return c.do(http.MethodDelete, "/admin/projects/"+url.PathEscape(project)+"/channels/"+url.PathEscape(name), nil, nil)
}
```

(Match `do`'s actual signature/param order in this file.)

- [ ] **Step 5: CLI — `project` group + `role --addressing`**

Register a `project` command in the command table (near `role`/`grant`):

```go
			{Name: "project", Brief: "manage a project's roster (roster add-human|add-channel|list|rm-human|rm-channel)", Run: cmdProject},
```

Implement `cmdProject` following `cmdRole`/`cmdKit` structure (subcommand switch on `args[0]`, flag sets, admin client from the login helper the other commands use). Subcommands:
- `project roster add-human <project> --name <n> --handle <h>` → `client.AddHuman(project, harbor.Human{Name,Handle})`
- `project roster add-channel <project> --name <n> --ref <identifier> [--service linear]` → `client.AddChannel(...)`
- `project roster list <project>` → `client.GetRoster(project)`, print humans + channels
- `project roster rm-human <project> <name>` / `rm-channel <project> <name>`

In `cmdRole`'s `add` subcommand, add a `--addressing` flag (comma-separated globs) and pass it into the `RoleBody.Addressing` (via the adminclient `PutRole` → set `Scope.Addressing`). Mirror how `--destinations`/`--repos` are parsed today.

- [ ] **Step 6: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ ./internal/harbor/adminclient/ ./cmd/at-harbor/ -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass.

- [ ] **Step 7: gofmt + commit**

```bash
gofmt -w internal/harbor/admin.go internal/harbor/adminclient/adminclient.go cmd/at-harbor/main.go internal/harbor/admin_test.go internal/harbor/adminclient/adminclient_test.go
git add -A internal/harbor/admin.go internal/harbor/adminclient/ cmd/at-harbor/main.go internal/harbor/admin_test.go
git commit -m "harbor: admin API + adminclient + CLI for roster and role addressing (COV-161)" # + trailers
```

---

### Task 7: Docs

**Files:**
- Create: `docs/usage/harbor/comms-addressing.md`
- Modify: `docs/usage/harbor/messaging.md`, `docs/usage/harbor/roster.md`, `docs/usage/harbor/INDEX.md`

**Interfaces:** none (docs only).

- [ ] **Step 1: Write `comms-addressing.md`**

New leaf with correct frontmatter (`summary`, `read_when`, `owns`, `prereqs`, `tier: leaf`, `updated: 2026-09-14`). It OWNS: the target space (kind-prefixed `human:`/`channel:` names + `path.Match` globs), the Project roster (`Human{Name,Handle}`, `Channel{Name,Service,Ref}`), the comms access-graph (`Scope.Addressing` + `Override`, resolved per-grant existential, fail-closed; own ticket always allowed; authz-before-existence → 403 vs 404), `send(text, to=…)` delivery/reply semantics (human two-way via own-ticket @mention; channel post-only), the `GET /messages/targets` / `list_targets` discovery tool, and the `at-harbor project` / `role --addressing` operator commands. Deferred: escalation (C2), cross-thread reply-routing + merged inbox, Discord (C3), actor/role-to-actor addressing.

- [ ] **Step 2: Update `messaging.md`**

In "What the tools do", note `send` now takes an optional `to` (own ticket when omitted) and there's a `list_targets` tool; link to `comms-addressing.md` for the target space + access-graph (single source — do not duplicate the rules). Keep the "self-scoped `read`" wording (read is still own-ticket by construction). Bump `updated`.

- [ ] **Step 3: Update `roster.md` + `INDEX.md`**

`roster.md`: add a one-line note that a `Role`'s `Scope` now includes `addressing` (comms plane) and that the Project *roster of humans/channels* lives in `comms-addressing.md` (link). `INDEX.md`: add the `comms-addressing.md` row with a `read_when` one-liner.

- [ ] **Step 4: Verify docs health**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/usage` → 0 new errors (compare delta to baseline). Confirm every new link resolves.

- [ ] **Step 5: Commit**

```bash
git add docs/usage/harbor/
git commit -m "docs: harbor comms C1 — addressing, roster, access-graph (COV-161)" # + trailers
```

---

## Self-Review

- **Spec coverage:** §1 data model → Task 1; §2 `DecideSend` → Task 2; §3 delivery → Task 3; §4 endpoint+MCP → Tasks 3/4/5; §5 CLI/admin → Task 6; §6 docs → Task 7. All covered.
- **Type consistency:** `SendTarget{Kind,Name,Handle,Ref,Project}` used identically in Tasks 2–4; `Scope.Addressing`/`Override.Addressing` (Task 1) consumed by `EffectiveScope`/`DecideSend`; `Human{Name,Handle}`/`Channel{Name,Service,Ref}`/`Roster` consistent across store, policy, admin, MCP.
- **Fail-closed & authz-before-existence:** `DecideSend` returns `ErrSendDenied` (403) before consulting the roster and `ErrSendUnresolved` (404) only after a glob match — encoded in Task 2 tests and Task 3 handler.
- **Boundaries:** no grpc/kit/dispatch imports added to `internal/harbor` (policy is pure; delivery reuses `Commenter`); `cove-master` unchanged re: grpc/oidc.
- **Placeholder scan:** the only intentionally-open items are "match the file's existing helper names" in test/handler steps (fakes from COV-145, `writeJSON`/`decode`/`do` signatures) — these are real, discoverable in the current code, not TBDs.
