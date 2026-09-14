# harbor msgport Slice 1b — ingress-shadow — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the msgport ingress engine runs live for Linear (egress off) — polling the team-scoped comments feed and appending inbound human replies to the Log, idempotently — with zero live-behavior change.

**Architecture:** a new Linear `CommentFeed`/`Viewer` query pair; a `msgport.Config.EgressEnabled` toggle (ingress-only); concrete `linearSurface`/`directory`/file-`Cursors`/no-op-`Markers` at cmd; wired into the tracker-gated block only when a message-log is configured.

**Tech Stack:** Go; `internal/dispatch/linear` (GraphQL over the injected transport), `internal/msgport`, `internal/msglog`, `harbor.Store`.

## Global Constraints

- **Egress stays OFF this slice** (`EgressEnabled: false`) — running egress would double-post against the live 1a dual-write. `linearSurface.Deliver`/`directory.Resolve` are stubs that must never be reached.
- **No live-behavior change:** the existing count-based `linear.Comments`, wake-on, escalation, and `/messages` send/read are untouched. This slice only *appends* inbound to the Log; nothing reads it yet.
- **Idempotent + over-report-safe:** the msgport engine dedups on `in:linear:<commentID>` + the `seen` set rebuilt from the Log; the timestamp cursor may over-report (use `createdAt > since`, re-poll a window on restart) but must never under-report.
- **Self-post filter:** `directory.Route` drops any comment authored by harbor's own Linear `viewer.displayName` (a cove's brokered outbound), returning `ok=false`.
- **Boundary:** `internal/msgport` gains only `Config.EgressEnabled` (still msglog+stdlib only). Concrete adapters live at `cmd/at-harbor`. `internal/dispatch/linear` gains two query methods; no other consumer changes.
- **No secret/body in logs:** structural warns only (unrouted event, poll error) — never a comment body or token.
- **TDD, DRY, YAGNI, frequent commits.** Every task ends green (`GOPROXY=off go build ./... && GOPROXY=off go test ./...`), gofmt-clean, `.at-cove/` untouched. Prefix go commands with `GOPROXY=off`.

## File Structure

- `internal/dispatch/linear/linear.go` — `FeedComment`, `CommentFeed`, `Viewer` (Task 1).
- `internal/msgport/msgport.go` + `engine.go` — `Config.EgressEnabled` + `Run` gate (Task 2).
- `cmd/at-harbor/msgport_linear.go` — `linearSurface`, `directory`, file `Cursors`, no-op `Markers` (Task 3).
- `cmd/at-harbor/main.go` — wiring (Task 4).
- Tests alongside each.

---

### Task 1: Linear comments feed + viewer

**Files:**
- Modify: `internal/dispatch/linear/linear.go`
- Test: `internal/dispatch/linear/linear_test.go`

**Interfaces:**
- Produces: `FeedComment{ID, Body, CreatedAt time.Time, Author, IssueIdentifier, ParentID}`; `(*Client).CommentFeed(ctx, since time.Time, limit int) ([]FeedComment, error)`; `(*Client).Viewer(ctx) (string, error)`.

- [ ] **Step 1: Write failing tests**

Read `internal/dispatch/linear/linear_test.go` for the `rtFunc` RoundTripper + the client-construction helper (`New(testCfg(), "tok", &http.Client{Transport: rt})`) + how existing tests assert the query/variables and return canned JSON. Add:

```go
func TestCommentFeedParsesAndSendsVars(t *testing.T) {
	var gotVars map[string]any
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		gotVars = body.Variables
		resp := `{"data":{"comments":{"nodes":[
			{"id":"c1","body":"please hold","createdAt":"2026-09-14T10:00:00.000Z","user":{"displayName":"Brent"},"issue":{"identifier":"ACME-42"},"parent":null},
			{"id":"c2","body":"why?","createdAt":"2026-09-14T11:00:00.000Z","user":{"displayName":"Brent"},"issue":{"identifier":"ACME-42"},"parent":{"id":"c1"}}
		]}}}`
		return jsonResp(resp), nil // use the file's canned-response helper (or httpResp)
	})
	c := newTestClient(t, rt) // adapt to the file's helper; team key from testCfg()
	since := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	got, err := c.CommentFeed(context.Background(), since, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 ||
		got[0].ID != "c1" || got[0].Author != "Brent" || got[0].IssueIdentifier != "ACME-42" || got[0].ParentID != "" ||
		got[1].ParentID != "c1" || got[1].Body != "why?" {
		t.Fatalf("parse: %+v", got)
	}
	if got[0].CreatedAt.IsZero() {
		t.Fatal("CreatedAt must parse")
	}
	// team key + since + first are sent as variables
	if gotVars["key"] == nil || gotVars["since"] == nil || gotVars["first"] == nil {
		t.Fatalf("missing vars: %v", gotVars)
	}
}

func TestViewerParsesDisplayName(t *testing.T) {
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(`{"data":{"viewer":{"displayName":"harbor-bot"}}}`), nil
	})
	c := newTestClient(t, rt)
	name, err := c.Viewer(context.Background())
	if err != nil || name != "harbor-bot" {
		t.Fatalf("viewer=%q err=%v", name, err)
	}
}
```
> Adapt `newTestClient`/`jsonResp`/`rtFunc` to the file's actual helper names (from the existing tests). `CommentFeed`'s `since` → a `DateTime` string variable; assert its presence, not its exact format.

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/dispatch/linear/ -run 'CommentFeed|Viewer' -v` → FAIL (undefined).

- [ ] **Step 3: Implement (linear.go)**

```go
// FeedComment is one comment from the team-scoped comments feed.
type FeedComment struct {
	ID              string
	Body            string
	CreatedAt       time.Time
	Author          string
	IssueIdentifier string
	ParentID        string
}

// CommentFeed returns comments in the Client's team created after `since`,
// oldest-first, capped at `limit`. Backs the msgport Linear ingress adapter.
func (c *Client) CommentFeed(ctx context.Context, since time.Time, limit int) ([]FeedComment, error) {
	const q = `query($key:String!,$since:DateTimeOrDuration!,$first:Int!){comments(filter:{issue:{team:{key:{eq:$key}}},createdAt:{gt:$since}},orderBy:createdAt,first:$first){nodes{id body createdAt user{displayName} issue{identifier} parent{id}}}}`
	var out struct {
		Comments struct {
			Nodes []struct {
				ID        string `json:"id"`
				Body      string `json:"body"`
				CreatedAt string `json:"createdAt"`
				User      struct{ DisplayName string `json:"displayName"` } `json:"user"`
				Issue     struct{ Identifier string `json:"identifier"` } `json:"issue"`
				Parent    *struct{ ID string `json:"id"` } `json:"parent"`
			} `json:"nodes"`
		} `json:"comments"`
	}
	if err := c.do(ctx, q, map[string]any{"key": c.team, "since": since.UTC().Format(time.RFC3339Nano), "first": limit}, &out); err != nil {
		return nil, err
	}
	fs := make([]FeedComment, 0, len(out.Comments.Nodes))
	for _, n := range out.Comments.Nodes {
		at, _ := time.Parse(time.RFC3339, n.CreatedAt) // Linear returns RFC3339; tolerate parse failure as zero
		fc := FeedComment{ID: n.ID, Body: n.Body, CreatedAt: at, Author: n.User.DisplayName, IssueIdentifier: n.Issue.Identifier}
		if n.Parent != nil {
			fc.ParentID = n.Parent.ID
		}
		fs = append(fs, fc)
	}
	return fs, nil
}

// Viewer returns the display name of the identity the Client's token
// authenticates as (harbor's own Linear user) — for the ingress self-post filter.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	const q = `query{viewer{displayName}}`
	var out struct{ Viewer struct{ DisplayName string `json:"displayName"` } `json:"viewer"` }
	if err := c.do(ctx, q, nil, &out); err != nil {
		return "", err
	}
	return out.Viewer.DisplayName, nil
}
```
> Note the `createdAt` scalar type: Linear's filter uses a date comparator; the variable type `DateTimeOrDuration` (or `DateTime`) — if the API rejects the declared type at implementation, adjust to what introspection reports (this is the one field to verify live). Parsing tolerates either `time.RFC3339` or `RFC3339Nano`.

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/dispatch/linear/ -run 'CommentFeed|Viewer' -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/dispatch/linear/linear.go internal/dispatch/linear/linear_test.go
git add internal/dispatch/linear/linear.go internal/dispatch/linear/linear_test.go
git commit -m "linear: team-scoped CommentFeed + Viewer for msgport ingress (COV-174)" # + trailers
```

---

### Task 2: `msgport.Config.EgressEnabled` toggle

**Files:**
- Modify: `internal/msgport/msgport.go` (Config field), `internal/msgport/engine.go` (Run gate)
- Test: `internal/msgport/engine_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestRunEgressDisabledOnlyIngress(t *testing.T) {
	lg := openLog(t)
	// an outbound (internal-authored, external target) message that egress WOULD deliver if enabled
	_, _ = lg.Append(msglog.Message{From: msglog.Target{Kind: "actor", Ref: "cove-1"}, To: []msglog.Target{{Kind: "human", Ref: "a"}}, Body: "x", Project: "acme"})
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{"human:a": {Service: "linear", Address: "ACME-1"}}}
	e := New(surf, lg, &fakeMarkers{}, &fakeCursors{}, dir, Config{EgressEnabled: false, EgressPoll: time.Millisecond, IngressPoll: time.Millisecond}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	time.Sleep(30 * time.Millisecond) // a few ticks would fire if egress ran
	cancel()
	if surf.deliverCount() != 0 {
		t.Fatalf("EgressEnabled=false must not deliver; got %d", surf.deliverCount())
	}
}
```
(This test uses real tickers + a short sleep. Keep it modest; it asserts the egress loop never ran.)

- [ ] **Step 2: Run test, verify fail**

Run: `GOPROXY=off go test ./internal/msgport/ -run 'EgressDisabled' -v` → FAIL (egress runs today).

- [ ] **Step 3: Implement**

`msgport.go`: add to `Config`:
```go
	EgressEnabled bool
```
`engine.go`: gate the egress loop in `Run`:
```go
func (e *Engine) Run(ctx context.Context) {
	if e.cfg.EgressEnabled {
		go e.loop(ctx, e.cfg.EgressPoll, e.egressTick)
	}
	e.loop(ctx, e.cfg.IngressPoll, e.ingressTick)
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/msgport/ -v` → PASS (new + existing). Then full build+test.

> **Note:** existing spine tests call `egressTick`/`ingressTick` directly (not via `Run`), so they're unaffected by the `Run` gate. If any test relied on `Run` starting egress, update it to set `EgressEnabled: true`.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/msgport/msgport.go internal/msgport/engine.go internal/msgport/engine_test.go
git add internal/msgport/
git commit -m "harbor: msgport Config.EgressEnabled — run ingress-only when off (COV-174)" # + trailers
```

---

### Task 3: cmd concrete adapters — `linearSurface`, `directory`, `Cursors`, `Markers`

**Files:**
- Create: `cmd/at-harbor/msgport_linear.go`
- Test: `cmd/at-harbor/msgport_linear_test.go`

**Interfaces:**
- Consumes: `linear.FeedComment`/`CommentFeed` (Task 1), `msgport.{Surface,Directory,Markers,Cursors,Event,Delivery,EgressMark}`, `msglog.Target`, `harbor.Store`.

- [ ] **Step 1: Write failing tests**

```go
// fake feeder + a fake harbor.Store (or a real *harbor.FileStore over a temp dir).
func TestLinearSurfacePollMapsFeed(t *testing.T) {
	started := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	ff := &fakeFeeder{cs: []linear.FeedComment{
		{ID: "c1", Body: "hold", CreatedAt: started.Add(time.Hour), Author: "Brent", IssueIdentifier: "ACME-42", ParentID: ""},
		{ID: "c2", Body: "?", CreatedAt: started.Add(2 * time.Hour), Author: "Brent", IssueIdentifier: "ACME-42", ParentID: "c1"},
	}}
	s := &linearSurface{feed: ff, started: started}
	evs, next, err := s.Poll(context.Background(), "acme", "")
	if err != nil || len(evs) != 2 {
		t.Fatalf("poll: %v %+v", err, evs)
	}
	if evs[0].ForeignID != "c1" || evs[0].Surface != "ACME-42" || evs[0].Author != "Brent" || evs[1].ReplyToForeign != "c1" {
		t.Fatalf("event map: %+v", evs)
	}
	if ff.since != started { // empty cursor → started baseline
		t.Fatalf("empty cursor must poll from started, got %v", ff.since)
	}
	if next == "" {
		t.Fatal("next cursor must advance")
	}
	// Deliver must never succeed (egress off)
	if _, err := s.Deliver(context.Background(), msgport.Delivery{}, msglog.Message{}); err == nil {
		t.Fatal("Deliver must error (egress not enabled)")
	}
}

func TestDirectoryRoute(t *testing.T) {
	st := newTestStore(t) // a *harbor.FileStore over a temp dir, or a fake harbor.Store
	st.PutInstance(harbor.Instance{ActorID: "cove-1", Unit: "ACME-42", Project: "acme"})
	_ = st.AddChannel("acme", harbor.Channel{Name: "eng", Service: "linear", Ref: "ACME-9"})
	d := &directory{store: st, project: "acme", selfIdentity: "harbor-bot"}

	// self-post → dropped
	if _, _, _, ok := d.Route("linear", "acme", msgport.Event{Author: "harbor-bot", Surface: "ACME-42"}); ok {
		t.Fatal("self-authored comment must be dropped")
	}
	// human reply on a cove's ticket → actor
	from, to, _, ok := d.Route("linear", "acme", msgport.Event{Author: "Brent", Surface: "ACME-42", ForeignID: "c1"})
	if !ok || from.Kind != "human" || from.Ref != "Brent" || len(to) != 1 || to[0].Kind != "actor" || to[0].Ref != "cove-1" {
		t.Fatalf("cove route: %v %+v %+v", ok, from, to)
	}
	// reply on a channel's thread → channel
	_, to, replyTo, ok := d.Route("linear", "acme", msgport.Event{Author: "Brent", Surface: "ACME-9", ReplyToForeign: "p1"})
	if !ok || to[0].Kind != "channel" || to[0].Ref != "eng" || replyTo != "in:linear:p1" {
		t.Fatalf("channel route: %v %+v %q", ok, to, replyTo)
	}
	// unknown issue → unrouted
	if _, _, _, ok := d.Route("linear", "acme", msgport.Event{Author: "Brent", Surface: "ACME-999"}); ok {
		t.Fatal("unknown issue must be unrouted")
	}
}

func TestFileCursorsRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cursors.json")
	c, _ := newFileCursors(p)
	_ = c.SetIngress("linear", "acme", "2026-09-14T10:00:00Z")
	c2, _ := newFileCursors(p) // reload
	if c2.Ingress("linear", "acme") != "2026-09-14T10:00:00Z" {
		t.Fatal("cursor must persist across reload")
	}
}
```
> Adapt `newTestStore` to a real `harbor.NewFileStore(tempfile)` or a minimal fake implementing the `harbor.Store` methods `directory` uses (`ListInstances`, `GetRoster`). Provide `fakeFeeder{cs []linear.FeedComment; since time.Time}` recording the `since` it was called with.

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run 'LinearSurface|DirectoryRoute|FileCursors' -v` → FAIL.

- [ ] **Step 3: Implement `msgport_linear.go`**

Implement per the spec §3–5:
- `commentFeeder` interface (`CommentFeed(ctx, since, limit)`); `*linear.Client` satisfies it.
- `linearSurface{feed commentFeeder; started time.Time}` — `Service`, `Poll` (empty since → started; map `FeedComment`→`msgport.Event`; `next` = max `CreatedAt` formatted RFC3339Nano, else the incoming `since`), `Deliver` (returns the not-enabled error), `Close`.
- `directory{store harbor.Store; project, selfIdentity string}` — `Projects`, `Route` (self-post drop; `human:<Author>`; issue→`ListInstances` Unit match → actor; else `GetRoster` Channel.Ref match → channel; else unrouted; `ReplyTo = "in:"+service+":"+ParentForeign`), `Resolve` (stub).
- `fileCursors{path string; mu sync.Mutex; m map[string]string}` — `newFileCursors(path)` loads (tolerate missing/torn), `Ingress(service,project)`=`m[service+"/"+project]`, `SetIngress` sets + saves (marshal + write). `noopMarkers{}` — `Egress` returns zero, `SetEgress` returns nil.

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run 'LinearSurface|DirectoryRoute|FileCursors' -v` → PASS. Then full build+test.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w cmd/at-harbor/msgport_linear.go cmd/at-harbor/msgport_linear_test.go
git add cmd/at-harbor/msgport_linear.go cmd/at-harbor/msgport_linear_test.go
git commit -m "harbor: msgport linear ingress adapters (surface/directory/cursors) (COV-174)" # + trailers
```

---

### Task 4: Wire the ingress engine + docs

**Files:**
- Modify: `cmd/at-harbor/main.go`
- Modify: `docs/usage/harbor/messaging.md`

- [ ] **Step 1: Wire in `cmdServe`**

In the tracker-gated dispatcher block (after the tracker + `messageLog` are available, near the wake-on/escalate engines), add — ONLY when `messageLog != nil`:
```go
		self, err := tracker.Viewer(context.Background())
		if err != nil {
			log.Warn("harbor msgport: viewer lookup failed; self-post filter disabled", "error", err.Error())
		}
		surf := &linearSurface{feed: tracker, started: time.Now()}
		dir := &directory{store: st, project: dc.Project, selfIdentity: self}
		cur, err := newFileCursors(filepath.Join(filepath.Dir(cfg.Store), "msgport-cursors.json"))
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: msgport cursors:", err)
			return 1
		}
		ingest := msgport.New(surf, messageLog, noopMarkers{}, cur, dir, msgport.Config{EgressEnabled: false}, log)
		go ingest.Run(context.Background())
		log.Info("harbor msgport (linear ingress): resident", "egress", false, "self", self != "")
```
- Use the SAME `messageLog` opened in Slice 1a (shared writer/reader/ingress).
- `dc.Project` is the dispatcher's configured project (falls back to `harbor.DefaultProject` if empty — mirror how the dispatcher handles it).

- [ ] **Step 2: Build + test**

Run: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → green. Reason about: with a tracker + message-log configured, the ingress engine is now resident (egress off); with no message-log, no ingress engine (nil guard). No change to send/read/wake-on/escalation.

- [ ] **Step 3: Docs**

In `docs/usage/harbor/messaging.md`, extend the shadow note from 1a: when `message-log` + a tracker are configured, harbor also **ingests** human ticket replies into the Log (the msgport linear ingress engine, egress off) — visible in the admin message view alongside the shadow-written sends; wake-on still detects replies the old way until a later slice. Bump `updated`.

- [ ] **Step 4: Commit**

```bash
gofmt -w cmd/at-harbor/main.go docs/usage/harbor/messaging.md
git add cmd/at-harbor/main.go docs/usage/harbor/messaging.md
git commit -m "harbor: run the msgport linear ingress engine (egress off) (COV-174)" # + trailers
```

---

## Self-Review

- **Spec coverage:** §1 feed/viewer → Task 1; §2 toggle → Task 2; §3–4 adapters → Task 3; §5 wiring → Task 4; docs → Task 4.
- **Egress-off is enforced** by `EgressEnabled:false` (Task 4 wiring) + the `Run` gate (Task 2, pinned by `TestRunEgressDisabledOnlyIngress`) + `linearSurface.Deliver` erroring (Task 3).
- **Self-post filter + routing** pinned by `TestDirectoryRoute` (self-drop, cove, channel, unrouted, replyTo).
- **Idempotency** rides on the spine (already tested); this slice adds the deterministic `in:linear:<id>` via `Event.ForeignID = comment.ID` (Task 3 Poll) — the engine builds the id.
- **The one live-API risk** (the `comments` filter/scalar shape) is isolated to Task 1 and flagged for schema-introspection confirmation; the query mirrors the existing `issues(filter:{team:{key}})` grammar already in the file.
- **Green between tasks:** Task 1 (linear) + Task 2 (msgport) + Task 3 (cmd adapters, unused) each compile+test independently; Task 4 wires them live.
- **Placeholder scan:** only "adapt to the file's real test helper names" (linear `rtFunc`/`newTestClient`; a fake `harbor.Store`) — real, discoverable.
