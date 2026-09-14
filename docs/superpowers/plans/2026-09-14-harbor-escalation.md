# harbor comms C2 v1 — escalation engine — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** While a managed cove is Waiting, a separate resident engine pings ordered human tiers on per-tier timers, escalating to the next tier on timeout; wake-on keeps owning reply→wake and max-wait→teardown.

**Architecture:** A Project-owned `Escalation []EscalationTier` policy (additive store). Per-Waiting-cove escalation state (`EscalationTier`/`TierPingedAt`) persisted on the `Instance`, reset on Waiting-entry by `Report`, written only by a new `Supervisor.SetEscalation`. A new `internal/escalate` engine (mirroring `internal/wakeon`) gates on `Activity==Waiting` + timers, pings tiers by @-mentioning humans on the cove's own ticket (so replies flow through existing wake-on), reads no comments, and does no teardown.

**Tech Stack:** Go; narrow interfaces (harbor-core-free engine, like wakeon); `time.ParseDuration`; hermetic tests with fakes + injected clock.

## Global Constraints

- **`internal/escalate` stays free of grpc/kit/dispatch/backend/connect imports** — narrow interfaces + a `Pinger`; the `linearCommenter` adapter lives at the `cmd/at-harbor` wiring layer, exactly like `internal/wakeon`.
- **`internal/harbor` core adds no new heavy imports** (types + store + supervisor only).
- **"Escalation open" is defined by `TierPingedAt` being non-zero**, NOT by `EscalationTier` (whose Go zero-value 0 would read as "tier 0 pinged"). The engine opens tier 0 iff `inst.TierPingedAt.IsZero()`. This avoids any store migration and is the load-bearing invariant — tests must pin it.
- **Escalation is additive/opt-in:** a project with no `Escalation` policy → no escalation (today's passive wait, unchanged). A pre-C2 store loads with nil policy + zero escalation state.
- **Harbor's pings are operator-policy-driven** and do NOT consult the comms access-graph (`DecideSend` gates a *cove's* outbound send, not harbor's own pings).
- **wake-on stays the sole owner** of reply-detection, waking, and max-wait teardown. The escalation engine never reads comments, wakes, or tears down. The two engines share only the `Instance.Activity==Waiting` gate.
- **Secrets and message bodies never hit logs/argv;** @-handles + tier indices are non-secret and may be logged.
- **A mis-configured tier never strands a blocked cove:** a tier with only non-human/unknown targets posts nothing but still advances the timer; a `channel:`/malformed target is skipped with a logged warning.
- **TDD, DRY, YAGNI, frequent commits.** Every task ends green (`GOPROXY=off go build ./... && GOPROXY=off go test ./...`), gofmt-clean, `.at-cove/` untouched. Prefix go commands with `GOPROXY=off`.

---

### Task 1: Policy + runtime-state types + store + supervisor state

**Files:**
- Modify: `internal/harbor/identity.go` (`EscalationTier` type; `Project.Escalation`)
- Modify: `internal/harbor/instance.go` (`Instance.EscalationTier`, `TierPingedAt`)
- Modify: `internal/harbor/filestore.go` (`SetEscalationPolicy`; `Store` interface; `GetProject` escalation-slice copy)
- Modify: `internal/harbor/supervisor.go` (`SetEscalation`; `Report` reset)
- Test: `internal/harbor/filestore_test.go`, `internal/harbor/supervisor_test.go`

**Interfaces:**
- Produces: `EscalationTier{Targets []string, Timeout time.Duration}`; `Project.Escalation []EscalationTier`; `Instance.EscalationTier int` + `Instance.TierPingedAt time.Time`; `Store.SetEscalationPolicy(project string, tiers []EscalationTier) error`; `Supervisor.SetEscalation(actorID string, tier int, at time.Time) error`.

- [ ] **Step 1: Write failing tests**

In `internal/harbor/filestore_test.go`:

```go
func TestEscalationPolicyRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	fs, _ := NewFileStore(path)
	tiers := []EscalationTier{
		{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute},
		{Targets: []string{"human:bob"}, Timeout: time.Hour},
	}
	if err := fs.SetEscalationPolicy("acme", tiers); err != nil {
		t.Fatal(err)
	}
	fs2, _ := NewFileStore(path) // reload from disk
	p, ok := fs2.GetProject("acme")
	if !ok || len(p.Escalation) != 2 || p.Escalation[0].Timeout != 15*time.Minute || p.Escalation[1].Targets[0] != "human:bob" {
		t.Fatalf("escalation not persisted: %+v ok=%v", p.Escalation, ok)
	}
}

func TestSetEscalationPolicyReplaces(t *testing.T) {
	fs, _ := NewFileStore(filepath.Join(t.TempDir(), "s.json"))
	_ = fs.SetEscalationPolicy("p", []EscalationTier{{Targets: []string{"human:a"}, Timeout: time.Minute}})
	_ = fs.SetEscalationPolicy("p", []EscalationTier{{Targets: []string{"human:b"}, Timeout: 2 * time.Minute}})
	p, _ := fs.GetProject("p")
	if len(p.Escalation) != 1 || p.Escalation[0].Targets[0] != "human:b" {
		t.Fatalf("expected replace, got %+v", p.Escalation)
	}
}

func TestGetProjectCopiesEscalation(t *testing.T) {
	fs, _ := NewFileStore(filepath.Join(t.TempDir(), "s.json"))
	_ = fs.SetEscalationPolicy("p", []EscalationTier{{Targets: []string{"human:a"}, Timeout: time.Minute}})
	p, _ := fs.GetProject("p")
	p.Escalation[0].Targets[0] = "mutated" // must not corrupt the store
	p2, _ := fs.GetProject("p")
	if p2.Escalation[0].Targets[0] != "human:a" {
		t.Fatalf("GetProject leaked a live slice: %v", p2.Escalation[0].Targets)
	}
}
```

In `internal/harbor/supervisor_test.go` (mirror the existing `Report`/`SetWaitCursor` tests + fake store):

```go
func TestSetEscalationPersists(t *testing.T) {
	sup, st := newTestSupervisor(t) // use the file's existing helper/fake
	st.putInstance(Instance{ActorID: "cove-1", Phase: PhaseLive, Activity: ActivityWaiting})
	at := time.Unix(1000, 0)
	if err := sup.SetEscalation("cove-1", 2, at); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetInstance("cove-1")
	if got.EscalationTier != 2 || !got.TierPingedAt.Equal(at) {
		t.Fatalf("escalation state not persisted: tier=%d at=%v", got.EscalationTier, got.TierPingedAt)
	}
}

func TestReportResetsEscalationOnEnteringWaiting(t *testing.T) {
	sup, st := newTestSupervisor(t)
	// a cove that was mid-escalation, currently Running
	st.putInstance(Instance{ActorID: "cove-1", Phase: PhaseLive, Activity: ActivityRunning, EscalationTier: 3, TierPingedAt: time.Unix(500, 0)})
	if err := sup.Report(context.Background(), "cove-1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetInstance("cove-1")
	if !got.TierPingedAt.IsZero() || got.EscalationTier != 0 {
		t.Fatalf("entering Waiting must reset escalation: tier=%d at=%v", got.EscalationTier, got.TierPingedAt)
	}
}
```

> Adapt `newTestSupervisor`/`st.putInstance`/`ActivityRunning` to the file's actual helper and constant names (read `supervisor_test.go` + `instance.go` first).

- [ ] **Step 2: Run tests, verify they fail**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'Escalation|SetEscalation|ReportResetsEscalation|GetProjectCopies' -v` → FAIL (undefined types/methods/fields).

- [ ] **Step 3: Add types (identity.go)**

```go
// EscalationTier is one rung of a Project's escalation policy: the targets to
// ping and how long to wait for an answer before advancing to the next tier.
type EscalationTier struct {
	Targets []string      `json:"targets"` // kind-prefixed human names, e.g. "human:alice"
	Timeout time.Duration `json:"timeout"` // wait after pinging this tier before advancing
}
```

Add to the `Project` struct (after `Roster`):

```go
	Escalation []EscalationTier `json:"escalation,omitempty"`
```

- [ ] **Step 4: Add Instance fields (instance.go)**

Add to the `Instance` struct (after `WaitCursor`):

```go
	EscalationTier int       `json:"escalation_tier,omitempty"` // last-pinged tier index; meaningful only when TierPingedAt is non-zero
	TierPingedAt   time.Time `json:"tier_pinged_at,omitempty"`  // when EscalationTier was pinged; zero = no escalation open
```

- [ ] **Step 5: Store method + GetProject copy (filestore.go)**

Add `SetEscalationPolicy` (mirror `AddHuman`'s lock/auto-create/save shape):

```go
func (fs *FileStore) SetEscalationPolicy(project string, tiers []EscalationTier) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p := fs.projects[project]
	p.Name = project
	p.Escalation = tiers
	fs.projects[project] = p
	return fs.save()
}
```

In `GetProject`, return a copy of the `Escalation` slice (it currently returns the stored `Project` — the C1 fix copied roster slices; do the same for Escalation so a caller can't mutate store state):

```go
func (fs *FileStore) GetProject(name string) (Project, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p, ok := fs.projects[name]
	if !ok {
		return Project{}, false
	}
	p.Roster = Roster{
		Humans:   append([]Human(nil), p.Roster.Humans...),
		Channels: append([]Channel(nil), p.Roster.Channels...),
	}
	p.Escalation = append([]EscalationTier(nil), p.Escalation...)
	return p, true
}
```

> If the current `GetProject` already copies the roster (from the C1 final-review fix), just add the `p.Escalation = append(...)` line. Read the current body first.

Add `SetEscalationPolicy(project string, tiers []EscalationTier) error` to the `Store` interface. Then find every fake `Store` implementer with `GOPROXY=off go test ./...` (NOT `go build`) and add the method to any that break.

- [ ] **Step 6: Supervisor `SetEscalation` + `Report` reset (supervisor.go)**

In `Report`, inside the existing `if enteringWaiting {` block (which sets `WaitingSince`/clears `WaitCursor`), also reset escalation:

```go
		inst.EscalationTier = 0
		inst.TierPingedAt = time.Time{}
```

Add the method (mirror `SetWaitCursor`):

```go
// SetEscalation persists the escalation engine's per-instance tier state (which
// tier was last pinged, and when). The zero TierPingedAt means "no escalation
// open" — see the escalation engine. No-op semantics if the actor is gone.
func (s *Supervisor) SetEscalation(actorID string, tier int, at time.Time) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.EscalationTier = tier
	inst.TierPingedAt = at
	return s.store.PutInstance(inst)
}
```

- [ ] **Step 7: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'Escalation|SetEscalation|ReportResetsEscalation|GetProjectCopies' -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → all pass (fix any fake `Store`).

- [ ] **Step 8: gofmt + commit**

```bash
gofmt -w internal/harbor/*.go
git add internal/harbor/identity.go internal/harbor/instance.go internal/harbor/filestore.go internal/harbor/supervisor.go internal/harbor/filestore_test.go internal/harbor/supervisor_test.go
git commit -m "harbor: escalation policy + per-instance tier state (COV-165)" # + trailers
```

---

### Task 2: The escalation engine (`internal/escalate`)

**Files:**
- Create: `internal/escalate/escalate.go`
- Test: `internal/escalate/escalate_test.go`

**Interfaces:**
- Consumes: `harbor.Instance`, `harbor.Project`, `harbor.Roster`, `harbor.EscalationTier`, `harbor.ActivityWaiting` (Task 1).
- Produces: `escalate.New(reg Registry, projects Projects, state State, pinger Pinger, cfg Config, log *slog.Logger) *Engine`; `(*Engine).Run(ctx)`; interfaces `Registry`/`Projects`/`State`/`Pinger`; `Config{PollInterval time.Duration}`.

- [ ] **Step 1: Write failing tests**

Create `internal/escalate/escalate_test.go`. Fakes: a registry returning a fixed instance slice; a projects fake mapping name→Project (with Escalation + Roster); a state fake recording `SetEscalation` calls; a pinger fake recording `PostComment(issueID, body)` and mapping identifier→issueID. Inject the clock via an exported test hook (`e.now = func() time.Time { return clock }`) or a settable field — mirror how `internal/wakeon/wakeon_test.go` injects its clock (read it first).

```go
func TestOpensTierZeroImmediately(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: harbor.ActivityWaiting}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation: []harbor.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
		Roster:     harbor.Roster{Humans: []harbor.Human{{Name: "alice", Handle: "alice.h"}}}}}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }

	e.tick(context.Background())

	if st.lastTier != 0 || !st.lastAt.Equal(clock) {
		t.Fatalf("expected SetEscalation(0, now); got tier=%d at=%v", st.lastTier, st.lastAt)
	}
	if pg.lastIssue != "iss-42" || !strings.Contains(pg.lastBody, "@alice.h") || !strings.Contains(pg.lastBody, "ACME-42") {
		t.Fatalf("expected tier-0 ping on own ticket with @alice.h; issue=%q body=%q", pg.lastIssue, pg.lastBody)
	}
}

func TestAdvancesTierOnTimeout(t *testing.T) {
	clock := time.Unix(2000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42",
		Activity: harbor.ActivityWaiting, EscalationTier: 0, TierPingedAt: clock.Add(-16 * time.Minute)}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation: []harbor.EscalationTier{
			{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute},
			{Targets: []string{"human:bob"}, Timeout: time.Hour}},
		Roster: harbor.Roster{Humans: []harbor.Human{{Name: "alice", Handle: "a"}, {Name: "bob", Handle: "b"}}}}}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }

	e.tick(context.Background())

	if st.lastTier != 1 {
		t.Fatalf("expected advance to tier 1, got %d", st.lastTier)
	}
	if !strings.Contains(pg.lastBody, "@b") {
		t.Fatalf("expected tier-1 ping to @b, got %q", pg.lastBody)
	}
}

func TestNoAdvanceBeforeTimeout(t *testing.T) {
	clock := time.Unix(2000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42",
		Activity: harbor.ActivityWaiting, EscalationTier: 0, TierPingedAt: clock.Add(-1 * time.Minute)}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation: []harbor.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}, {Targets: []string{"human:bob"}, Timeout: time.Hour}},
		Roster:     harbor.Roster{Humans: []harbor.Human{{Name: "alice", Handle: "a"}, {Name: "bob", Handle: "b"}}}}}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.called || pg.called {
		t.Fatal("must not ping/advance before timeout")
	}
}

func TestLastTierNoFurtherAdvanceNoTeardown(t *testing.T) {
	clock := time.Unix(3000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42",
		Activity: harbor.ActivityWaiting, EscalationTier: 0, TierPingedAt: clock.Add(-time.Hour)}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation: []harbor.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
		Roster:     harbor.Roster{Humans: []harbor.Human{{Name: "alice", Handle: "a"}}}}}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.called || pg.called {
		t.Fatal("last tier already pinged: no further ping/advance (teardown is wake-on's job)")
	}
}

func TestIgnoresNonWaitingAndNoPolicy(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "running", Project: "acme", Unit: "ACME-1", Activity: harbor.ActivityRunning},
		{ActorID: "nopolicy", Project: "beta", Unit: "BETA-1", Activity: harbor.ActivityWaiting},
	}}
	proj := &fakeProjects{projects: map[string]harbor.Project{
		"acme": {Name: "acme", Escalation: []harbor.EscalationTier{{Targets: []string{"human:a"}, Timeout: time.Minute}}},
		"beta": {Name: "beta"}, // no policy
	}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.called || pg.called {
		t.Fatal("non-Waiting and no-policy instances must be ignored")
	}
}

func TestEmptyTierAdvancesWithoutPosting(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: harbor.ActivityWaiting}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation: []harbor.EscalationTier{{Targets: []string{"channel:x", "human:ghost"}, Timeout: time.Minute}},
		Roster:     harbor.Roster{}}}} // no matching humans
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.lastTier != 0 || !st.lastAt.Equal(clock) {
		t.Fatalf("empty tier must still advance the timer; tier=%d", st.lastTier)
	}
	if pg.called {
		t.Fatal("empty tier must post nothing")
	}
}
```

Write the fakes (`fakeReg`, `fakeProjects`, `fakeState`, `fakePinger`) with the recorded fields the tests reference (`lastTier`, `lastAt`, `called`, `lastIssue`, `lastBody`, `ids`).

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/escalate/ -v` → FAIL (package doesn't exist).

- [ ] **Step 3: Implement `internal/escalate/escalate.go`**

```go
// Package escalate is harbor's resident escalation engine: while a managed cove
// is Waiting, it pings ordered human tiers of the cove's Project escalation
// policy on per-tier timers, advancing to the next tier on timeout. It reads no
// comments, wakes no coves, and tears nothing down — reply-detection, waking, and
// max-wait teardown stay in internal/wakeon. The two engines share only the
// Instance.Activity==Waiting gate. Wired from cmd/at-harbor; not imported by
// internal/harbor core.
package escalate

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

type Registry interface{ ListInstances() []harbor.Instance }
type Projects interface {
	GetProject(name string) (harbor.Project, bool)
	GetRoster(project string) (harbor.Roster, bool)
}
type State interface {
	SetEscalation(actorID string, tier int, at time.Time) error
}
type Pinger interface {
	IssueByIdentifier(ctx context.Context, identifier string) (string, error)
	PostComment(ctx context.Context, issueID, body string) error
}

type Config struct{ PollInterval time.Duration }

const defaultPollInterval = 30 * time.Second

type Engine struct {
	reg   Registry
	proj  Projects
	state State
	ping  Pinger
	cfg   Config
	now   func() time.Time
	log   *slog.Logger
}

func New(reg Registry, proj Projects, state State, ping Pinger, cfg Config, log *slog.Logger) *Engine {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Engine{reg, proj, state, ping, cfg, time.Now, log}
}

func (e *Engine) Run(ctx context.Context) {
	e.tick(ctx)
	tk := time.NewTicker(e.cfg.PollInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			e.tick(ctx)
		}
	}
}

func (e *Engine) tick(ctx context.Context) {
	for _, inst := range e.reg.ListInstances() {
		if inst.Activity != harbor.ActivityWaiting {
			continue
		}
		proj, ok := e.proj.GetProject(inst.Project)
		if !ok || len(proj.Escalation) == 0 {
			continue
		}
		// Open tier 0 iff no escalation is open (TierPingedAt zero — NOT the int,
		// whose zero value would read as "tier 0 pinged").
		if inst.TierPingedAt.IsZero() {
			e.pingTier(ctx, inst, proj, 0)
			continue
		}
		cur := inst.EscalationTier
		if cur+1 < len(proj.Escalation) && e.now().Sub(inst.TierPingedAt) > proj.Escalation[cur].Timeout {
			e.pingTier(ctx, inst, proj, cur+1)
		}
	}
}

// pingTier resolves the tier's human handles from the roster, posts an @-mention
// nudge on the cove's OWN ticket, and records the advance. A tier with no
// resolvable human handles posts nothing but still advances the timer (so a
// mis-configured tier can't wedge a blocked cove).
func (e *Engine) pingTier(ctx context.Context, inst harbor.Instance, proj harbor.Project, tier int) {
	handles := e.resolveHandles(proj, tier)
	if len(handles) > 0 {
		issueID, err := e.ping.IssueByIdentifier(ctx, inst.Unit)
		if err != nil {
			e.log.Warn("escalate: resolve ticket failed", "actor", inst.ActorID, "error", err.Error())
			return // retry next tick; do NOT advance (ticket transiently unavailable)
		}
		body := strings.Join(handles, " ") + " — cove " + inst.ActorID + " needs input on " + inst.Unit + " (escalation tier " + strconv.Itoa(tier) + ")"
		if err := e.ping.PostComment(ctx, issueID, body); err != nil {
			e.log.Warn("escalate: ping failed", "actor", inst.ActorID, "tier", tier, "error", err.Error())
			return // retry next tick; do NOT advance
		}
		e.log.Info("escalate: pinged tier", "actor", inst.ActorID, "tier", tier, "targets", len(handles))
	} else {
		e.log.Warn("escalate: tier has no resolvable human targets, advancing", "actor", inst.ActorID, "tier", tier)
	}
	if err := e.state.SetEscalation(inst.ActorID, tier, e.now()); err != nil {
		e.log.Warn("escalate: set state failed", "actor", inst.ActorID, "tier", tier, "error", err.Error())
	}
}

func (e *Engine) resolveHandles(proj harbor.Project, tier int) []string {
	roster := proj.Roster
	var handles []string
	for _, target := range proj.Escalation[tier].Targets {
		kind, name, ok := strings.Cut(target, ":")
		if !ok || kind != "human" {
			e.log.Warn("escalate: skipping non-human tier target", "target", target)
			continue
		}
		for _, h := range roster.Humans {
			if h.Name == name {
				handles = append(handles, "@"+h.Handle)
				break
			}
		}
	}
	return handles
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
```

> Note the subtle failure semantics: if a tier HAS resolvable handles but the ticket/PostComment call fails, `pingTier` returns early WITHOUT advancing (`SetEscalation` not called) so it retries next tick — a transient tracker error must not silently skip a tier. Only the empty-tier path advances-without-posting. The tests must cover the happy paths; the transient-error path is covered by inspection (optional extra test if the fake pinger can be made to error).

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/escalate/ -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/escalate/escalate.go internal/escalate/escalate_test.go
git add internal/escalate/
git commit -m "harbor: resident escalation engine — tier pinging on per-tier timers (COV-165)" # + trailers
```

---

### Task 3: Config + wiring (`cmd/at-harbor`)

**Files:**
- Modify: `cmd/at-harbor/config.go` (`EscalationPollInterval`)
- Modify: `cmd/at-harbor/main.go` (build + run the engine in `cmdServe`)
- Test: `cmd/at-harbor/config_test.go`

**Interfaces:**
- Consumes: `escalate.New`, `escalate.Config` (Task 2); `st` (Registry+Projects), `sup` (State), `linearCommenter` (Pinger).

- [ ] **Step 1: Write failing test**

Extend the existing dispatcher-config parse test (`TestRuntimeDispatcherParsed`) in `cmd/at-harbor/config_test.go` with an `escalation-poll-interval: 45s` fixture line and an assertion `dc.EscalationPollInterval == "45s"` (match the file's existing table/assertion style).

- [ ] **Step 2: Run test, verify fail**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run Dispatcher -v` → FAIL (`EscalationPollInterval` undefined).

- [ ] **Step 3: Add config field (config.go)**

In `dispatcherConfig`, after `WarmTimeout`:

```go
	EscalationPollInterval string `yaml:"escalation-poll-interval"`
```

Update the doc comment above the wake-on fields to mention the escalation poll interval too.

- [ ] **Step 4: Wire the engine (main.go)**

In `cmdServe`, in the tracker-gated dispatcher block right after the wake-on engine is built + started (`eng := wakeon.New(...); go eng.Run(...)`), add:

```go
		epoll, _ := time.ParseDuration(dc.EscalationPollInterval) // "" or invalid → 0 → engine default
		eeng := escalate.New(st /*Registry*/, st /*Projects*/, sup /*State*/, linearCommenter{tracker} /*Pinger*/, escalate.Config{PollInterval: epoll}, log)
		go eeng.Run(context.Background())
		log.Info("harbor escalation engine: resident", "poll-interval", epoll)
```

Add the `escalate` import. (`st` already satisfies `Registry`+`Projects` via `ListInstances`/`GetProject`/`GetRoster`; `sup` gains `SetEscalation` from Task 1; `linearCommenter` already provides `IssueByIdentifier`/`PostComment`.)

- [ ] **Step 5: Run tests, verify pass**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run Dispatcher -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass. Remove any stray binary.

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -w cmd/at-harbor/config.go cmd/at-harbor/main.go cmd/at-harbor/config_test.go
git add cmd/at-harbor/config.go cmd/at-harbor/main.go cmd/at-harbor/config_test.go
git commit -m "harbor: wire escalation engine + escalation-poll-interval config (COV-165)" # + trailers
```

---

### Task 4: Operator surface — admin route + adminclient + CLI

**Files:**
- Modify: `internal/harbor/admin.go` (escalation routes + `EscalationBody`)
- Modify: `internal/harbor/adminclient/adminclient.go` (`SetEscalationPolicy`/`GetEscalationPolicy`)
- Modify: `cmd/at-harbor/main.go` (`cmdProject` gains `escalation set|list|clear`)
- Test: `internal/harbor/admin_test.go`, `internal/harbor/adminclient/adminclient_test.go`, `cmd/at-harbor/main_test.go`

**Interfaces:**
- Consumes: store `SetEscalationPolicy` + `GetProject` (Task 1).
- Produces: `PUT/GET /admin/projects/{project}/escalation`; adminclient `SetEscalationPolicy(project, tiers)`/`GetEscalationPolicy(project)`; CLI `project escalation set|list|clear`.

- [ ] **Step 1: Write failing tests**

Mirror the C1 roster-route + adminclient + CLI tests (`TestAdminRosterRoutes`, `TestClientRosterAndAddressing`, `TestProjectRosterCommands`). Add:

```go
// admin_test.go — PUT then GET the policy, assert round-trip through a real FileStore.
func TestAdminEscalationRoutes(t *testing.T) { /* PUT [{targets:[human:alice],timeout:15m}] → GET returns it */ }
// adminclient_test.go
func TestClientEscalationPolicy(t *testing.T) { /* SetEscalationPolicy then GetEscalationPolicy round-trips */ }
// main_test.go — end-to-end through httptest + FileStore:
func TestProjectEscalationCommands(t *testing.T) { /* `project escalation set p --tier 'human:alice,human:bob@15m' --tier 'human:carol@1h'` → `escalation list p` shows 2 tiers */ }
```

Fill in real assertions matching each file's existing harness style.

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/harbor/ ./internal/harbor/adminclient/ ./cmd/at-harbor/ -run 'Escalation' -v` → FAIL.

- [ ] **Step 3: Admin routes (admin.go)**

Add the body type near the other `*Body` types:

```go
// EscalationBody is the PUT /admin/projects/{project}/escalation request.
type EscalationBody struct {
	Tiers []EscalationTier `json:"tiers"`
}
```

Register inside `NewAdminHandler` (mirror the roster routes; behind the operator authenticator):

```go
	mux.HandleFunc("GET /admin/projects/{project}/escalation", func(w http.ResponseWriter, r *http.Request) {
		p, _ := store.GetProject(r.PathValue("project"))
		writeJSON(w, http.StatusOK, EscalationBody{Tiers: p.Escalation})
	})
	mux.HandleFunc("PUT /admin/projects/{project}/escalation", func(w http.ResponseWriter, r *http.Request) {
		var b EscalationBody
		if !decode(w, r, &b) {
			return
		}
		if err := store.SetEscalationPolicy(r.PathValue("project"), b.Tiers); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin escalation policy", "operator", operatorID(r), "project", r.PathValue("project"), "tiers", len(b.Tiers))
		w.WriteHeader(http.StatusNoContent)
	})
```

- [ ] **Step 4: adminclient methods**

```go
func (c *Client) SetEscalationPolicy(project string, tiers []harbor.EscalationTier) error {
	return c.do("PUT", "/admin/projects/"+url.PathEscape(project)+"/escalation", harbor.EscalationBody{Tiers: tiers}, nil)
}
func (c *Client) GetEscalationPolicy(project string) ([]harbor.EscalationTier, error) {
	var b harbor.EscalationBody
	err := c.do("GET", "/admin/projects/"+url.PathEscape(project)+"/escalation", nil, &b)
	return b.Tiers, err
}
```

- [ ] **Step 5: CLI — `project escalation` (main.go)**

Extend `cmdProject` so `args[0]` may be `roster` (existing) OR `escalation`. Update the top guard and brief. Add an `escalation` branch:

```go
	if args[0] == "escalation" {
		return cmdProjectEscalation(args[1:], stdout, stderr)
	}
```

Implement `cmdProjectEscalation` (own flag set with `app`/`admin-url`/`token` like `cmdProject`, plus a repeatable `--tier` flag):

```go
func cmdProjectEscalation(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "at-harbor project escalation: expected set|list|clear")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("project escalation "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	var tiers tierFlags
	fs.Var(&tiers, "tier", "a tier as 'target,target@timeout' (repeatable, ordered); set only")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor project escalation:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "set":
		if len(pos) != 1 || len(tiers) == 0 {
			fmt.Fprintln(stderr, "at-harbor project escalation set: expected <project> and at least one --tier 'targets@timeout'")
			return 2
		}
		if err := c.SetEscalationPolicy(pos[0], tiers); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "set escalation policy for", pos[0], "-", len(tiers), "tier(s)")
	case "list":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor project escalation list: expected one project name")
			return 2
		}
		got, err := c.GetEscalationPolicy(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for i, tr := range got {
			fmt.Fprintf(stdout, "tier %d\t%s\ttimeout=%s\n", i, strings.Join(tr.Targets, ","), tr.Timeout)
		}
	case "clear":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor project escalation clear: expected one project name")
			return 2
		}
		if err := c.SetEscalationPolicy(pos[0], nil); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "cleared escalation policy for", pos[0])
	default:
		fmt.Fprintln(stderr, "at-harbor project escalation: unknown subcommand", sub)
		return 2
	}
	return 0
}

// tierFlags collects repeatable --tier values, parsing 'targets@timeout'.
type tierFlags []harbor.EscalationTier

func (t *tierFlags) String() string { return fmt.Sprintf("%d tiers", len(*t)) }
func (t *tierFlags) Set(v string) error {
	at := strings.LastIndex(v, "@")
	if at < 0 {
		return fmt.Errorf("tier %q missing '@timeout'", v)
	}
	d, err := time.ParseDuration(v[at+1:])
	if err != nil {
		return fmt.Errorf("tier %q: bad timeout: %w", v, err)
	}
	targets := strings.Split(v[:at], ",")
	*t = append(*t, harbor.EscalationTier{Targets: targets, Timeout: d})
	return nil
}
```

Update the `project` command `Brief` in the command table to mention `escalation set|list|clear`, and the top-level `cmdProject` usage line. Ensure `time`/`strings` are imported in main.go (they are, given existing usage — confirm).

- [ ] **Step 6: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ ./internal/harbor/adminclient/ ./cmd/at-harbor/ -run 'Escalation|Project' -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass.

- [ ] **Step 7: gofmt + commit**

```bash
gofmt -w internal/harbor/admin.go internal/harbor/adminclient/adminclient.go cmd/at-harbor/main.go internal/harbor/admin_test.go internal/harbor/adminclient/adminclient_test.go cmd/at-harbor/main_test.go
git add internal/harbor/admin.go internal/harbor/adminclient/ cmd/at-harbor/main.go internal/harbor/admin_test.go cmd/at-harbor/main_test.go
git commit -m "harbor: admin API + adminclient + CLI for escalation policy (COV-165)" # + trailers
```

---

### Task 5: Docs

**Files:**
- Create: `docs/usage/harbor/escalation.md`
- Modify: `docs/usage/harbor/messaging.md`, `docs/usage/harbor/comms-addressing.md`, `docs/usage/harbor/INDEX.md`

**Interfaces:** none (docs only).

- [ ] **Step 1: Write `escalation.md`**

New leaf, proper frontmatter (`summary`, `read_when` as the reader's situation, `owns`, `prereqs`, `tier: leaf`, `updated: 2026-09-14`, ≤200 lines). OWNS: the per-project ordered **human tiers** + per-tier **timeout**; **auto-on-Waiting** with an **immediate tier-0 ping**; the **two independent clocks** (escalation tier-timer vs wake-on `wait-max` — link [messaging.md](messaging.md#waiting-for-a-reply-wake-on)); **delivery** = an @-mention nudge on the cove's **own ticket**, so a human reply is answered via the existing wake-on (link [comms-addressing.md](comms-addressing.md) for the human/handle model); **advance-on-empty-tier** (a mis-configured tier advances without posting); config `runtime.dispatcher.escalation-poll-interval`; and the `at-harbor project escalation set|list|clear` commands (with the `--tier 'targets@timeout'` syntax). List the deferred items (categories + agent-declared escalate + auto-detection; channel tiers; the reserved Attach `TierChanged`; CODEOWNERS/assignee ingest).

- [ ] **Step 2: Cross-links + INDEX**

- `messaging.md` "Waiting for a reply (wake-on)" section: one line noting that a Project may configure an **escalation policy** that actively pings human tiers while a cove waits — link to `escalation.md` (single source; don't duplicate). Bump `updated`.
- `comms-addressing.md`: one line under the roster/humans material noting escalation tiers ping roster humans — link to `escalation.md`. Bump `updated`.
- `INDEX.md`: add the `escalation.md` row with a `read_when` one-liner. Bump `updated`.

- [ ] **Step 3: Verify docs health**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/usage` → 0 new errors (delta vs baseline). Confirm every new link resolves.

- [ ] **Step 4: Commit**

```bash
git add docs/usage/harbor/
git commit -m "docs: harbor comms C2 v1 — escalation engine (COV-165)" # + trailers
```

---

## Self-Review

- **Spec coverage:** §1 policy data → Task 1; §2 runtime state → Task 1; §3 engine → Task 2; §4 config+wiring → Task 3; §5 operator surface → Task 4; §6 docs → Task 5. All covered.
- **Type consistency:** `EscalationTier{Targets,Timeout}` identical across store/engine/admin/CLI; `Instance.EscalationTier`/`TierPingedAt` written by `SetEscalation` (Task 1) and read by the engine (Task 2); `SetEscalationPolicy` (store/adminclient) vs `SetEscalation` (supervisor/engine `State`) are deliberately distinct names.
- **The load-bearing invariant** (`TierPingedAt.IsZero()` ⇒ not open) is stated in Global Constraints, encoded in the engine's `tick`, and pinned by `TestOpensTierZeroImmediately` + `TestReportResetsEscalationOnEnteringWaiting`.
- **Boundaries:** `internal/escalate` imports only `harbor` + stdlib; no comment-reading/waking/teardown; pings don't consult the access-graph.
- **Placeholder scan:** the only intentionally-open items are "adapt to the file's existing fake/helper names" (supervisor/admin/CLI test harnesses from prior slices) — real, discoverable, not TBDs. Admin/adminclient/CLI test bodies in Task 4 Step 1 are sketched; the implementer fills real assertions mirroring the named C1 tests.
