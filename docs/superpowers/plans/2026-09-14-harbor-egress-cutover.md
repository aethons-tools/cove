# msgport Slice 3 — egress cutover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cut the `/messages` POST send path over from direct Linear `PostComment` to Log→egress delivery, reproducing today's rendering byte-for-byte.

**Architecture:** `handlePost` becomes append-only (authoritative); the resident msgport engine (already ingesting) turns egress ON and delivers each outbound Log message via a concrete Linear adapter (`directory.Resolve` pure mapping → `linearSurface.Deliver` posts). A one-time startup seed of the per-Service `EgressMark` to the Log's tail prevents re-delivering the 1a shadow history. Clean switch — no flag.

**Tech Stack:** Go; `internal/msgport` (msglog+stdlib only), `internal/harbor` (core), `cmd/at-harbor` (concrete adapter), hermetic tests via fakes.

## Global Constraints

- `internal/msgport` imports **msglog + stdlib ONLY** (no harbor). Adding a struct field must not add an import.
- The concrete `linearSurface`/`directory`/`fileMarkers` live at `cmd/at-harbor`; they may use `harbor.Store` + `internal/dispatch/linear`.
- `internal/harbor` core must not gain kit/grpc/dispatch/backend/connect imports.
- Secrets/tokens/message bodies NEVER reach a log line, a `Delivery`, a marker, or a cursor. Structural warns only (service / msg-id / target / error).
- Byte-parity target — the egress path must post exactly these `(issueID, body)` triples: own-ticket `(iss_7,"hi")`; `human:alice` `(iss_7,"@alice.h ping")`; `channel:eng-help` `(iss_9,"heads up")`.
- Tests hermetic (fakes; no network, no live Linear). `-race` is not runnable in-sandbox (no cgo) — reason about concurrency manually.
- Build offline with `GOPROXY=off` (deps cached); `just build` / `go test ./...`.

---

## File Structure

- `internal/msgport/msgport.go` — add `Delivery.BodyPrefix`.
- `internal/msgport/egress_test.go` — assert `BodyPrefix` passes through to `Deliver`.
- `cmd/at-harbor/msgport_linear.go` — real `directory.Resolve`; real `linearSurface.Deliver` + `commentPoster` iface + `poster` field; new `fileMarkers`; delete `noopMarkers`.
- `cmd/at-harbor/msgport_linear_test.go` — Resolve cases; Deliver cases; the golden parity test; fileMarkers tests; remove Deliver-stub + noopMarkers tests.
- `cmd/at-harbor/main.go` — wire real markers + seed + `EgressEnabled:true` + `poster`; add `logTailID` helper.
- `internal/harbor/messages.go` — `handlePost` append-only.
- `internal/harbor/messages_test.go` — rewrite the three rendering tests to assert Append; add 502/503 tests.
- `docs/usage/harbor/messaging.md` — send path is Log→egress; message-log required; operator schema-verification note.

---

## Task 1: egress rendering — `Delivery.BodyPrefix` + real `Resolve`/`Deliver` + golden parity gate

**Files:**
- Modify: `internal/msgport/msgport.go` (add `BodyPrefix` to `Delivery`)
- Modify: `internal/msgport/egress_test.go` (passthrough assertion)
- Modify: `cmd/at-harbor/msgport_linear.go` (`directory.Resolve`, `linearSurface.Deliver`, `commentPoster`, `poster` field)
- Test: `cmd/at-harbor/msgport_linear_test.go` (Resolve + Deliver + golden)

**Interfaces:**
- Consumes: `msgport.Delivery`, `msglog.Target{Kind,Ref}`, `msglog.Classify`, `harbor.Store` (`ListInstances`, `GetRoster`), `harbor.Instance{ActorID,Unit}`, `harbor.Roster{Humans []Human{Name,Handle}, Channels []Channel{Name,Ref}}`, `linear.Client.IssueByIdentifier/PostComment`.
- Produces: `directory.Resolve(service, project, to, from) (msgport.Delivery, bool)`; `linearSurface.Deliver(ctx, d, m) (string, error)`; `commentPoster` interface; `linearSurface.poster` field. Task 4 supplies the concrete `poster` (`*linear.Client`).

- [ ] **Step 1: Write the failing passthrough test (msgport)**

Add to `internal/msgport/egress_test.go` — assert the engine hands `Delivery.BodyPrefix` to `Deliver` unchanged (the field exists and is opaque to the engine):

```go
func TestEgressPassesBodyPrefixToDeliver(t *testing.T) {
	lg := openLog(t)
	appendInternal(t, lg, msglog.Target{Kind: "channel", Ref: "ops"}) // internal-authored, external target
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{
		"channel:ops": {Service: "linear", Address: "ACME-9", BodyPrefix: "@bob "},
	}}
	e, _ := newEgressEngine(t, surf, dir)
	e.egressTick(context.Background())
	if len(surf.delivers) != 1 || surf.delivers[0].d.BodyPrefix != "@bob " {
		t.Fatalf("BodyPrefix not passed through: %+v", surf.delivers)
	}
}
```

Check the existing `fakeSurface` records the `Delivery` it received (field `delivers []struct{ d Delivery; m msglog.Message }` or similar). If it only records counts, extend it minimally to capture `d`. Check `appendInternal`/`newEgressEngine` helper names against the existing test file and reuse them; if absent, append an internal-authored message with `lg.Append(msglog.Message{From: msglog.Target{Kind:"actor",Ref:"cove-1"}, To: []msglog.Target{{Kind:"channel",Ref:"ops"}}, Project:"acme"})`.

- [ ] **Step 2: Run it — verify it fails to compile (no `BodyPrefix`)**

Run: `GOPROXY=off go test ./internal/msgport/ -run TestEgressPassesBodyPrefixToDeliver`
Expected: compile error `unknown field BodyPrefix in struct literal of type msgport.Delivery`.

- [ ] **Step 3: Add `BodyPrefix` to `Delivery`**

In `internal/msgport/msgport.go`, add the field (between `Address` and `SenderName`):

```go
type Delivery struct {
	Service      string // "linear" | "discord" — the owning Service
	Address      string // the surface to post onto (a Linear ticket identifier, a Discord channel id)
	BodyPrefix   string // literal string prepended to m.Body at delivery ("" = none); Resolve sets it, Deliver renders it
	SenderName   string // the From actor's display identity (Discord webhook username override; a "<name>:" prefix on Linear)
	SenderAvatar string
}
```

- [ ] **Step 4: Run it — verify the passthrough test passes**

Run: `GOPROXY=off go test ./internal/msgport/`
Expected: PASS (all msgport tests, including the new one).

- [ ] **Step 5: Write the failing `directory.Resolve` + golden `Deliver` tests (cmd)**

Add to `cmd/at-harbor/msgport_linear_test.go`. First a fake poster + store helpers (reuse the existing `fakeStore` in this test file if present; otherwise a minimal one exposing `ListInstances`/`GetRoster`):

```go
type fakePoster struct {
	posts  []struct{ issueID, body string }
	idByID map[string]string // identifier -> internal id
	postErr, resolveErr error
}

func (f *fakePoster) IssueByIdentifier(_ context.Context, identifier string) (string, error) {
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	id, ok := f.idByID[identifier]
	if !ok {
		return "", fmt.Errorf("no such identifier %q", identifier)
	}
	return id, nil
}
func (f *fakePoster) PostComment(_ context.Context, issueID, body string) error {
	if f.postErr != nil {
		return f.postErr
	}
	f.posts = append(f.posts, struct{ issueID, body string }{issueID, body})
	return nil
}
```

Golden parity test — the heart of the slice. Build a store with one Instance (`ActorID:"cove-1"`, `Unit:"ACME-7"`) and a roster (`Human{Name:"alice",Handle:"alice.h"}`, `Channel{Name:"eng-help",Ref:"ACME-9"}`), a `fakePoster{idByID: {"ACME-7":"iss_7","ACME-9":"iss_9"}}`, then drive each logical message through `Resolve`+`Deliver`:

```go
func TestEgressGoldenParity(t *testing.T) {
	st := newRosterStore(t, "acme",
		harbor.Instance{ActorID: "cove-1", Unit: "ACME-7", Project: "acme"},
		harbor.Roster{
			Humans:   []harbor.Human{{Name: "alice", Handle: "alice.h"}},
			Channels: []harbor.Channel{{Name: "eng-help", Service: "linear", Ref: "ACME-9"}},
		})
	poster := &fakePoster{idByID: map[string]string{"ACME-7": "iss_7", "ACME-9": "iss_9"}}
	dir := &directory{store: st, project: "acme", selfIdentity: "harbor-bot"}
	surf := &linearSurface{poster: poster}
	from := msglog.Target{Kind: "actor", Ref: "cove-1"}

	cases := []struct {
		name    string
		to      msglog.Target
		body    string
		wantID  string
		wantBod string
	}{
		{"own", msglog.Target{Kind: "channel", Ref: "ACME-7"}, "hi", "iss_7", "hi"},
		{"human", msglog.Target{Kind: "human", Ref: "alice"}, "ping", "iss_7", "@alice.h ping"},
		{"channel", msglog.Target{Kind: "channel", Ref: "eng-help"}, "heads up", "iss_9", "heads up"},
	}
	for _, c := range cases {
		d, ok := dir.Resolve("linear", "acme", c.to, from)
		if !ok {
			t.Fatalf("%s: Resolve ok=false", c.name)
		}
		if _, err := surf.Deliver(context.Background(), d, msglog.Message{From: from, To: []msglog.Target{c.to}, Body: c.body, Project: "acme"}); err != nil {
			t.Fatalf("%s: Deliver: %v", c.name, err)
		}
	}
	want := []struct{ issueID, body string }{
		{"iss_7", "hi"}, {"iss_7", "@alice.h ping"}, {"iss_9", "heads up"},
	}
	if len(poster.posts) != len(want) {
		t.Fatalf("posts = %+v, want %+v", poster.posts, want)
	}
	for i, w := range want {
		if poster.posts[i] != w {
			t.Fatalf("post %d = %+v, want %+v", i, poster.posts[i], w)
		}
	}
}
```

Resolve unit cases:

```go
func TestResolveUnroutableAndNonLinear(t *testing.T) {
	st := newRosterStore(t, "acme",
		harbor.Instance{ActorID: "cove-1", Unit: "ACME-7", Project: "acme"},
		harbor.Roster{Humans: []harbor.Human{{Name: "alice", Handle: "alice.h"}}})
	dir := &directory{store: st, project: "acme", selfIdentity: "harbor-bot"}
	from := msglog.Target{Kind: "actor", Ref: "cove-1"}
	// unknown human
	if _, ok := dir.Resolve("linear", "acme", msglog.Target{Kind: "human", Ref: "nobody"}, from); ok {
		t.Fatal("unknown human should be unresolved")
	}
	// unknown channel (not own Unit, not a roster channel)
	if _, ok := dir.Resolve("linear", "acme", msglog.Target{Kind: "channel", Ref: "ACME-999"}, from); ok {
		t.Fatal("unknown channel should be unresolved")
	}
	// non-linear service
	if _, ok := dir.Resolve("discord", "acme", msglog.Target{Kind: "human", Ref: "alice"}, from); ok {
		t.Fatal("non-linear service should be unresolved")
	}
	// sender with no instance → human unresolved
	if _, ok := dir.Resolve("linear", "acme", msglog.Target{Kind: "human", Ref: "alice"}, msglog.Target{Kind: "actor", Ref: "ghost"}); ok {
		t.Fatal("human target with no sender instance should be unresolved")
	}
}
```

Deliver error propagation:

```go
func TestDeliverPropagatesErrors(t *testing.T) {
	m := msglog.Message{Body: "x"}
	d := msgport.Delivery{Service: "linear", Address: "ACME-7"}
	// resolve error
	s1 := &linearSurface{poster: &fakePoster{resolveErr: fmt.Errorf("boom")}}
	if _, err := s1.Deliver(context.Background(), d, m); err == nil {
		t.Fatal("expected resolve error")
	}
	// post error
	s2 := &linearSurface{poster: &fakePoster{idByID: map[string]string{"ACME-7": "iss_7"}, postErr: fmt.Errorf("nope")}}
	if _, err := s2.Deliver(context.Background(), d, m); err == nil {
		t.Fatal("expected post error")
	}
}
```

`newRosterStore(t, project, inst, roster)` — a small test helper building a real `harbor.NewFileStore` in a temp dir, calling its instance-put + `SetRoster` methods (check the exact Store mutators: e.g. `PutInstance`/`raise` path and `SetRoster`/`PutRoster`; grep `func.*Roster` and `func.*Instance` on the store). If a direct roster setter isn't exported, use the admin/enroll path already used by other cmd tests, or a hand-rolled `fakeStore` implementing just `ListInstances()`/`GetRoster()` (the two methods `directory.Resolve` calls). A minimal `fakeStore` is simplest and hermetic — prefer it:

```go
type fakeStore struct {
	insts  []harbor.Instance
	roster map[string]harbor.Roster
}
func (f *fakeStore) ListInstances() []harbor.Instance { return f.insts }
func (f *fakeStore) GetRoster(p string) (harbor.Roster, bool) { r, ok := f.roster[p]; return r, ok }
```

But `directory.store` is typed `harbor.Store` (the full interface). Option A: change `directory.store` to a narrow local interface `type instanceRoster interface { ListInstances() []harbor.Instance; GetRoster(string) (harbor.Roster, bool) }` — cleaner and lets the fake satisfy it. Do this: it also documents exactly what Resolve/Route need. `Route` uses the same two methods, so the narrow interface covers both. Keep the concrete `*FileStore` (passed at cmd) satisfying it.

- [ ] **Step 6: Run the tests — verify they fail**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run 'TestEgressGoldenParity|TestResolve|TestDeliver'`
Expected: FAIL/compile-error — `Resolve` still returns the stub `{}, false`; `Deliver` still returns the "not enabled" error; `linearSurface` has no `poster` field; `directory.store` type mismatch.

- [ ] **Step 7: Implement `commentPoster`, `linearSurface.Deliver`, `poster` field, narrow store iface, and `directory.Resolve`**

In `cmd/at-harbor/msgport_linear.go`:

Add the poster interface + field, replace the Deliver stub:

```go
// commentPoster is the narrow slice of *linear.Client that linearSurface
// delivers through: identifier→id resolution plus the comment post.
type commentPoster interface {
	IssueByIdentifier(ctx context.Context, identifier string) (string, error)
	PostComment(ctx context.Context, issueID, body string) error
}

type linearSurface struct {
	feed    commentFeeder
	poster  commentPoster
	started time.Time
}

// Deliver resolves d.Address (a ticket identifier) to an internal id and posts
// d.BodyPrefix+m.Body. Linear returns no comment id, so foreignID is ""; the
// engine's EgressMark provides exactly-once (no idempotency footer — it would
// break byte-parity with the pre-cutover direct-post path).
func (s *linearSurface) Deliver(ctx context.Context, d msgport.Delivery, m msglog.Message) (string, error) {
	issueID, err := s.poster.IssueByIdentifier(ctx, d.Address)
	if err != nil {
		return "", fmt.Errorf("linear deliver: resolve %q: %w", d.Address, err)
	}
	if err := s.poster.PostComment(ctx, issueID, d.BodyPrefix+m.Body); err != nil {
		return "", fmt.Errorf("linear deliver: post to %q: %w", d.Address, err)
	}
	return "", nil
}
```

Narrow the directory store type + replace the Resolve stub:

```go
// instanceRoster is the slice of harbor.Store that the Linear directory reads:
// live instances (own-ticket / sender resolution) and the project roster
// (human handles, channel refs). *harbor.FileStore satisfies it.
type instanceRoster interface {
	ListInstances() []harbor.Instance
	GetRoster(project string) (harbor.Roster, bool)
}

type directory struct {
	store        instanceRoster
	project      string
	selfIdentity string
}

// Resolve maps an External Log target (+ sender) to a concrete Linear
// Delivery, reproducing the pre-cutover handlePost rendering exactly. Pure:
// roster/instance lookups only, no network (Deliver does the API calls).
func (d *directory) Resolve(service, project string, to, from msglog.Target) (msgport.Delivery, bool) {
	if service != "linear" {
		return msgport.Delivery{}, false
	}
	var self harbor.Instance
	var haveSelf bool
	if from.Kind == "actor" {
		for _, inst := range d.store.ListInstances() {
			if inst.ActorID == from.Ref {
				self, haveSelf = inst, true
				break
			}
		}
	}
	switch to.Kind {
	case "human":
		if !haveSelf {
			return msgport.Delivery{}, false
		}
		r, ok := d.store.GetRoster(project)
		if !ok {
			return msgport.Delivery{}, false
		}
		for _, h := range r.Humans {
			if h.Name == to.Ref {
				return msgport.Delivery{Service: "linear", Address: self.Unit, BodyPrefix: "@" + h.Handle + " "}, true
			}
		}
		return msgport.Delivery{}, false
	case "channel":
		if haveSelf && to.Ref == self.Unit {
			return msgport.Delivery{Service: "linear", Address: self.Unit}, true
		}
		if r, ok := d.store.GetRoster(project); ok {
			for _, ch := range r.Channels {
				if ch.Name == to.Ref {
					return msgport.Delivery{Service: "linear", Address: ch.Ref}, true
				}
			}
		}
		return msgport.Delivery{}, false
	default:
		return msgport.Delivery{}, false
	}
}
```

Note: `directory.store` was `harbor.Store`; the cmd construction site (Task 4) passes the same `st` — a `*FileStore` satisfies the narrower `instanceRoster`, so no cmd change needed beyond what Task 4 already does. Confirm `Route` still compiles against `instanceRoster` (it calls only `ListInstances`/`GetRoster`).

- [ ] **Step 8: Run the tests — verify they pass**

Run: `GOPROXY=off go test ./cmd/at-harbor/ ./internal/msgport/`
Expected: PASS. (Existing `msgport_linear_test.go` tests that construct `&linearSurface{feed: ff, started: started}` still compile — `poster` defaults nil and Poll doesn't touch it.)

- [ ] **Step 9: Commit**

```bash
git add internal/msgport/msgport.go internal/msgport/egress_test.go cmd/at-harbor/msgport_linear.go cmd/at-harbor/msgport_linear_test.go
git commit -m "harbor: msgport egress rendering — Delivery.BodyPrefix + Linear Resolve/Deliver + golden parity (COV-176)"
```

---

## Task 2: `fileMarkers` + `logTailID` + delete `noopMarkers`

**Files:**
- Modify: `cmd/at-harbor/msgport_linear.go` (add `fileMarkers`, delete `noopMarkers`)
- Modify: `cmd/at-harbor/main.go` (add `logTailID` helper — small, near the wiring)
- Test: `cmd/at-harbor/msgport_linear_test.go` (fileMarkers round-trip + `has` + reload; remove `noopMarkers` test)

**Interfaces:**
- Consumes: `msgport.Markers`, `msgport.EgressMark{LastMsg string, Pending map[string]map[string]bool}`, `*msglog.Log.List(msglog.Filter{})`, `msglog.Message.ID`.
- Produces: `newFileMarkers(path) (*fileMarkers, error)`; `(*fileMarkers).Egress/SetEgress/has`; `logTailID(*msglog.Log) string`. Task 4 calls these.

- [ ] **Step 1: Write the failing fileMarkers tests**

Add to `cmd/at-harbor/msgport_linear_test.go`:

```go
func TestFileMarkersRoundTripAndReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "markers.json")
	m, err := newFileMarkers(p)
	if err != nil {
		t.Fatalf("newFileMarkers: %v", err)
	}
	if m.has("linear") {
		t.Fatal("fresh markers must not have linear")
	}
	want := msgport.EgressMark{LastMsg: "id-9", Pending: map[string]map[string]bool{"id-10": {"human:a": true}}}
	if err := m.SetEgress("linear", want); err != nil {
		t.Fatalf("SetEgress: %v", err)
	}
	if !m.has("linear") {
		t.Fatal("has(linear) false after SetEgress")
	}
	m2, err := newFileMarkers(p) // reload from disk
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := m2.Egress("linear")
	if got.LastMsg != "id-9" || !got.Pending["id-10"]["human:a"] {
		t.Fatalf("reloaded mark = %+v, want %+v", got, want)
	}
	if got := m2.Egress("discord"); got.LastMsg != "" || got.Pending != nil {
		t.Fatalf("unset service must be zero EgressMark, got %+v", got)
	}
}

func TestFileMarkersMissingFileStartsEmpty(t *testing.T) {
	m, err := newFileMarkers(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("newFileMarkers missing: %v", err)
	}
	if m.has("linear") {
		t.Fatal("missing file must start empty")
	}
}
```

- [ ] **Step 2: Run — verify it fails**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run TestFileMarkers`
Expected: compile error — `newFileMarkers` undefined.

- [ ] **Step 3: Implement `fileMarkers`; delete `noopMarkers`**

In `cmd/at-harbor/msgport_linear.go`, remove the `noopMarkers` type + its two methods, and add (mirrors `fileCursors`):

```go
// fileMarkers is a file-backed msgport.Markers: a JSON map[service]EgressMark,
// mutex-guarded, loaded at open, saved on every SetEgress. Nested Pending maps
// round-trip through encoding/json.
type fileMarkers struct {
	path string
	mu   sync.Mutex
	m    map[string]msgport.EgressMark
}

// newFileMarkers loads path, tolerating a missing or corrupt/torn file (either
// starts empty rather than failing).
func newFileMarkers(path string) (*fileMarkers, error) {
	fm := &fileMarkers{path: path, m: map[string]msgport.EgressMark{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fm, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return fm, nil
	}
	var m map[string]msgport.EgressMark
	if err := json.Unmarshal(data, &m); err != nil {
		return fm, nil // torn/corrupt: tolerate, start empty
	}
	fm.m = m
	return fm, nil
}

func (fm *fileMarkers) Egress(service string) msgport.EgressMark {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	return fm.m[service]
}

func (fm *fileMarkers) SetEgress(service string, mk msgport.EgressMark) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.m[service] = mk
	data, err := json.MarshalIndent(fm.m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fm.path, data, 0o600)
}

// has reports whether service has a persisted mark (the one-time seed guard).
func (fm *fileMarkers) has(service string) bool {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	_, ok := fm.m[service]
	return ok
}
```

Remove the now-obsolete `TestNoopMarkers` (or equivalent) from the test file.

- [ ] **Step 4: Write the failing `logTailID` test**

Add to `cmd/at-harbor/main_test.go` (or wherever cmd helpers are tested; create a small `msgport_seed_test.go` if cleaner):

```go
func TestLogTailID(t *testing.T) {
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := logTailID(lg); got != "" {
		t.Fatalf("empty log tail = %q, want \"\"", got)
	}
	var lastID string
	for i := 0; i < 3; i++ {
		m, err := lg.Append(msglog.Message{From: msglog.Target{Kind: "actor", Ref: "c"}, To: []msglog.Target{{Kind: "channel", Ref: "x"}}, Project: "p"})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		lastID = m.ID
	}
	if got := logTailID(lg); got != lastID {
		t.Fatalf("tail = %q, want %q", got, lastID)
	}
}
```

(Check `msglog.Open`'s exact signature — it may return only `*Log` + error; adjust. The append call must match `Append`'s signature: `(msglog.Message) (msglog.Message, error)`.)

- [ ] **Step 5: Run — verify it fails**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run TestLogTailID`
Expected: compile error — `logTailID` undefined.

- [ ] **Step 6: Implement `logTailID`**

In `cmd/at-harbor/main.go` (near the msgport wiring), add:

```go
// logTailID returns the id of the last (newest) message in lg, or "" when the
// Log is empty. Used to seed the egress low-water at cutover so already-
// delivered shadow history is skipped. List is time-sorted; the tail is last.
func logTailID(lg *msglog.Log) string {
	all := lg.List(msglog.Filter{})
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1].ID
}
```

- [ ] **Step 7: Run — verify pass**

Run: `GOPROXY=off go test ./cmd/at-harbor/`
Expected: PASS (fileMarkers + logTailID; noopMarkers test gone).

- [ ] **Step 8: Commit**

```bash
git add cmd/at-harbor/msgport_linear.go cmd/at-harbor/main.go cmd/at-harbor/*_test.go
git commit -m "harbor: file-backed egress Markers + log-tail seed helper (COV-176)"
```

---

## Task 3: `handlePost` → append-only

**Files:**
- Modify: `internal/harbor/messages.go` (`handlePost`)
- Test: `internal/harbor/messages_test.go` (rewrite 3 rendering tests; add 502/503)

**Interfaces:**
- Consumes: `DecideSend`, `msglog.Target`, `appender.Append`, `Instance{Unit,Project}`, `Actor{ID}`.
- Produces: `handlePost` no longer calls `h.cmt.PostComment`/`IssueByIdentifier`; appends and returns 204/502/503.

- [ ] **Step 1: Rewrite the failing handler tests**

In `internal/harbor/messages_test.go`, the fake `Commenter` records `PostComment` in `posted`; add an appender fake that records appends and can error. Reuse or add:

```go
type fakeAppender struct {
	appended []msglog.Message
	err      error
}
func (f *fakeAppender) Append(m msglog.Message) (msglog.Message, error) {
	if f.err != nil {
		return msglog.Message{}, f.err
	}
	f.appended = append(f.appended, m)
	return m, nil
}
```

Rewrite the three rendering tests to assert the Append (no PostComment). Example for own-ticket (adapt the existing `TestMessagesPostIsSelfScoped` setup — same store/token/handler, but pass a `fakeAppender` as `lg`):

```go
func TestMessagesPostIsSelfScoped(t *testing.T) {
	// ... existing setup: store with actor+instance (Unit resolves to iss_7), token ...
	lg := &fakeAppender{}
	h := NewMessagesHandler(store, cmt, lg, testLogger(&logbuf))
	// POST {"body":"hi"} with the actor's bearer token
	// ...
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(cmt.posted) != 0 {
		t.Fatalf("handlePost must not PostComment after cutover; got %d", len(cmt.posted))
	}
	if len(lg.appended) != 1 {
		t.Fatalf("append calls = %d, want 1", len(lg.appended))
	}
	m := lg.appended[0]
	if m.From.Kind != "actor" || m.From.Ref == "" {
		t.Fatalf("From = %+v, want actor:<self>", m.From)
	}
	if len(m.To) != 1 || m.To[0].Kind != "channel" || m.To[0].Ref == "" {
		t.Fatalf("To = %+v, want [channel:<Unit>]", m.To)
	}
	if m.Body != "hi" {
		t.Fatalf("Body = %q, want raw \"hi\"", m.Body)
	}
	if m.Project == "" {
		t.Fatal("Project unset")
	}
}
```

Human target — assert `To == human:alice`, `Body == "ping"` (RAW, no `@` prefix in the Log), and body never logged:

```go
func TestSendToHumanAppendsRawToHumanTarget(t *testing.T) {
	// ... setup with roster Human{Name:"alice",Handle:"alice.h"} + a grant authorizing human:alice ...
	lg := &fakeAppender{}
	// POST {"body":"ping","to":"human:alice"}
	// assert 204, no PostComment, one append: To==[{human,alice}], Body=="ping"
	// assert !strings.Contains(logbuf.String(), "ping")
}
```

Channel target — `To == channel:eng-help`, `Body == "heads up"`.

New failure-path tests:

```go
func TestMessagesPostAppendFailureIs502(t *testing.T) {
	lg := &fakeAppender{err: fmt.Errorf("disk full")}
	// ... POST {"body":"hi"} ...
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestMessagesPostNilLogIs503(t *testing.T) {
	h := NewMessagesHandler(store, cmt, nil, log) // nil appender
	// ... POST {"body":"hi"} ...
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
```

Keep unchanged the 401/403(no-instance)/413/400/405/never-logs-token tests and the `DecideSend` denied(403)/unresolved(404) tests (those still reject before Append — add a `fakeAppender` and assert `len(appended)==0` on those denied paths).

- [ ] **Step 2: Run — verify failures**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'TestMessages|TestSendTo'`
Expected: FAIL — current `handlePost` still calls PostComment (so `cmt.posted` is 1, not 0) and doesn't emit 502/503.

- [ ] **Step 3: Rewrite `handlePost` to append-only**

Replace the body of `handlePost` (from the `deliverIssue, body := ...` block through the shadow-write block) in `internal/harbor/messages.go`. Keep everything up to and including the `req.Body == ""` check. New tail:

```go
	// Resolve the logical target (authz via the comms access-graph). No `to` →
	// the cove's own ticket, modeled as channel:<Unit>. A human/channel target
	// is authorized here; rendering + ticket resolution happen at egress.
	logicalTo := msglog.Target{Kind: "channel", Ref: inst.Unit}
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
		logicalTo = msglog.Target{Kind: st.Kind, Ref: st.Name}
	}

	// The Log is the authoritative delivery path (egress delivers it to Linear).
	// A send requires a configured Log; an append failure fails the send.
	if h.lg == nil {
		http.Error(w, "messaging not configured", http.StatusServiceUnavailable)
		return
	}
	if _, err := h.lg.Append(msglog.Message{
		From:    msglog.Target{Kind: "actor", Ref: actor.ID},
		To:      []msglog.Target{logicalTo},
		Body:    req.Body, // raw — @handle rendering is the adapter's job at egress
		Project: inst.Project,
	}); err != nil {
		h.log.Error("messages: append failed", "actor", actor.ID, "ticket", inst.Unit, "error", err.Error())
		http.Error(w, "send failed", http.StatusBadGateway)
		return
	}
	h.log.Info("messages", "actor", actor.ID, "ticket", inst.Unit, "op", "send", "to", req.To, "bytes", len(req.Body))
	w.WriteHeader(http.StatusNoContent)
}
```

- Remove the now-unused `deliverIssue`/`body` locals and the `issueID` parameter *use* inside handlePost (the `issueID` arg stays in the signature — `ServeHTTP` still passes it, and `handleGet` uses its own resolution; verify `issueID` isn't now an "unused parameter" problem — Go allows unused params, so leaving the signature is fine, but if a linter flags it, rename to `_`). Prefer keeping the signature stable and letting `handleGet` be the `issueID` consumer; if `handlePost` is the only consumer of the passed `issueID`, change `ServeHTTP` to not resolve/pass it for POST — check the call site at `messages.go:126` and keep the smallest change. (Most likely `issueID` is resolved once in `ServeHTTP` and used by both handlePost and handleGet; after this change only handleGet uses it — that's fine, no signature change needed.)
- Confirm `errors`, `time`, `msglog` imports remain used; `h.cmt` is still referenced by `handleGet` so the field stays.

- [ ] **Step 4: Run — verify pass**

Run: `GOPROXY=off go test ./internal/harbor/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/messages.go internal/harbor/messages_test.go
git commit -m "harbor: /messages POST is append-only — Log is the delivery path (COV-176)"
```

---

## Task 4: wire egress ON + seed + docs

**Files:**
- Modify: `cmd/at-harbor/main.go` (tracker-gated block: real markers, seed, `EgressEnabled:true`, `poster`)
- Modify: `docs/usage/harbor/messaging.md`

**Interfaces:**
- Consumes: `newFileMarkers`, `logTailID`, `(*fileMarkers).has/SetEgress`, `msgport.New(...EgressEnabled:true)`, `linearSurface{feed,poster,started}`.

- [ ] **Step 1: Update the wiring**

In `cmd/at-harbor/main.go`, inside the `if messageLog != nil { ... }` ingress block (around lines 1127–1140), change the surface/markers/config and add the seed:

```go
		surf := &linearSurface{feed: tracker, poster: tracker, started: time.Now()}
		dir := &directory{store: st, project: firstNonEmpty(dc.Project, harbor.DefaultProject), selfIdentity: self}
		cur, err := newFileCursors(filepath.Join(filepath.Dir(cfg.Store), "msgport-cursors.json"))
		if err != nil {
			return fmt.Errorf("msgport cursors: %w", err)
		}
		markers, err := newFileMarkers(filepath.Join(filepath.Dir(cfg.Store), "msgport-markers.json"))
		if err != nil {
			return fmt.Errorf("msgport markers: %w", err)
		}
		// Seed once: skip everything the 1a dual-write already delivered live, so
		// turning egress on doesn't re-post the Log's shadow history. Persisted →
		// never re-seeds (a re-seed to a newer tail would drop undelivered messages).
		if !markers.has("linear") {
			if err := markers.SetEgress("linear", msgport.EgressMark{LastMsg: logTailID(messageLog)}); err != nil {
				return fmt.Errorf("msgport egress seed: %w", err)
			}
		}
		eng := msgport.New(surf, messageLog, markers, cur, dir, msgport.Config{EgressEnabled: true}, log)
		go eng.Run(context.Background())
		log.Info("harbor msgport (linear): resident, egress ON")
```

(Match the existing variable names — the block currently builds `surf`, `dir`, `cur`, then `ingest := msgport.New(...EgressEnabled:false...)`. Rename `ingest`→`eng` consistently, or keep `ingest`; just ensure it's the engine that runs. `tracker` must satisfy `commentPoster` — `*linear.Client` has `IssueByIdentifier` + `PostComment`, so `poster: tracker` compiles. Confirm `self` is the viewer displayName already fetched above.)

- [ ] **Step 2: Build + full test run**

Run: `GOPROXY=off go build ./... && GOPROXY=off go test ./...`
Expected: PASS across the module.

- [ ] **Step 3: Update docs**

In `docs/usage/harbor/messaging.md`, revise the send-path description and add the operator note. Find the section describing `send`/outbound and replace the "direct PostComment" framing with:

> **Outbound is Log→egress (asynchronous, at-least-once).** A `send` appends the message to harbor's durable message-log and returns `204`; a resident egress loop then delivers it to Linear (≈ the egress poll interval later). A **message-log is required** for sends — without one, `POST /messages` returns `503`. An append failure returns `502` (the append *is* the delivery).

Add an operator-verification callout (near the ingress/feed description):

> **Before relying on inbound (reply) delivery, confirm the Linear `comments` feed schema against your live Linear workspace** — specifically the `$since` scalar (`DateTimeOrDuration` vs `DateTime`) and the `issue → team → key` filter path. harbor targets the schema captured during development; if it differs, the ingress `Poll` errors and its cursor holds (no data loss, inbound stalls) while **egress is unaffected**. This can't be exercised in an egress-locked build environment.

Bump the doc's `updated:` frontmatter date to 2026-09-14.

- [ ] **Step 4: docs-audit (delta only)**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`
Expected: no NEW orphans/dangling links vs the pre-existing baseline (this task only edits an existing doc; the spec/plan under `docs/superpowers/` are design history, already outside the audited index). If the delta is clean, proceed.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-harbor/main.go docs/usage/harbor/messaging.md
git commit -m "harbor: turn linear egress ON + seed + docs — cutover complete (COV-176)"
```

---

## Self-Review

**Spec coverage:** §1 BodyPrefix → Task 1. §2 Resolve → Task 1. §3 Deliver → Task 1. §4 handlePost → Task 3. §5 fileMarkers+seed → Task 2 (impl) + Task 4 (seed call). §6 wiring → Task 4. §7 golden test → Task 1. §8 handler test rewrites → Task 3. §9 docs → Task 4. All covered.

**Placeholder scan:** every code step carries complete code. The `newRosterStore` vs `fakeStore` decision is resolved (use the narrow `instanceRoster` interface + a `fakeStore`, spelled out in Task 1 Step 5/7).

**Type consistency:** `directory.store` becomes `instanceRoster` (Task 1) — the cmd construction (`&directory{store: st, ...}`, Task 4) passes `st` (`*FileStore`), which satisfies it; `Route` uses only `ListInstances`/`GetRoster`, so it still compiles. `linearSurface` gains `poster commentPoster` (Task 1); Task 4 sets `poster: tracker` (`*linear.Client` satisfies `commentPoster`). `EgressMark`/`Delivery` field names match `internal/msgport/msgport.go`. `Append` signature `(msglog.Message)(msglog.Message,error)` matches the `appender` fake and `handlePost`. `logTailID(*msglog.Log) string` consistent across Task 2 and Task 4.

**Risks flagged for review:** (1) confirm `directory.store` narrowing doesn't break any other cmd caller of `directory` (grep `directory{`); (2) confirm `ServeHTTP`'s `issueID` resolution is still needed by `handleGet` and not left dead after handlePost stops using it; (3) the byte-parity golden test is the gate — the reviewer should verify the three triples match the pre-cutover `handlePost` test assertions exactly.
