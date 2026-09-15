# harbor Slice 5c — Discord ingress + reply-routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A human's Discord reply to a cove's message lands in the Log addressed to that cove (waking it), via reply-to-message-id + a receipt store.

**Architecture:** Minimal switchboard extension (`Message.ReferencedID`, `RESTClient.PostID`) → a file-backed receipt store (`discord-msg-id → cove`) → a real `discordSurface` (Deliver records a receipt; Poll maps replies to Events) → a service-aware `directory.Route` (discord replies → the cove via receipts). Wake-on (Slice 2) wakes the cove for free.

**Tech Stack:** Go; `internal/switchboard` (additive), `cmd/at-harbor` (receipts, surface, route, wiring); hermetic tests. `internal/msgport`/`internal/harbor` core unchanged.

## Global Constraints

- `internal/msgport` and `internal/harbor` core gain NO new import. The receipt store, discord surface, and discord routing live at `cmd/at-harbor`. cmd extends+imports `internal/switchboard` (wiring-layer reuse).
- `internal/switchboard` changes are ADDITIVE ONLY — `Post`'s signature and the `Discord` interface stay unchanged (switchboard's own loop untouched). `Message` gains a field; `RESTClient` gains `PostID`.
- **Linear ingress byte-parity:** the existing `directory.Route` behavior for linear is preserved verbatim (moved into `routeLinear`); the existing `TestDirectoryRoute` must pass UNCHANGED.
- **No double-post:** a receipt-write failure in `Deliver` is swallowed (warn), never returned — else the engine retries and double-posts to Discord.
- **Self-post / no-echo:** only an inbound that REPLIES to a known receipt routes; harbor's own non-reply posts are dropped by construction (no bot-identity filter needed).
- No secret/token/message-body in any log line, receipt, cursor, or `Delivery`. The discord bot token stays host-resolved, never logged/injected.
- Concurrency: the receipt store is written by the egress goroutine (`Deliver`) and read by the ingress goroutine (`Route`) of the ONE discord engine — mutex-guarded. Values are immutable strings (no nested-map aliasing like 5b's EgressMark). `-race` unavailable in-sandbox (no cgo) — reason manually.
- Tests hermetic (fakes / injected transport; no live Discord). Build offline `GOPROXY=off`.
- The pre-existing uncommitted `.at-cove/config.yml` / `.at-cove/example-kitconfig.yml` / `.claude/` stay OUT of every commit — stage files explicitly by path.

## File Structure

- `internal/switchboard/message.go` — `Message.ReferencedID`.
- `internal/switchboard/discord.go` — decode `message_reference`; `RESTClient.PostID`; `Post` delegates.
- `cmd/at-harbor/msgport_discord.go` — `fileReceipts`, `discordClient`, `discordSurface` (rewritten), `decodeCursors`/`encodeCursors`, `discordInboxChannels`.
- `cmd/at-harbor/msgport_linear.go` — `directory.Route` → `routeLinear`+`routeDiscord`; `directory.receipts` field.
- `cmd/at-harbor/main.go` — receipts + new discordSurface wiring.
- test files alongside; `docs/usage/harbor/comms-addressing.md` / `messaging.md`.

---

## Task 1: `internal/switchboard` — `PostID` + `Message.ReferencedID`

**Files:**
- Modify: `internal/switchboard/message.go` (`ReferencedID`), `internal/switchboard/discord.go` (`discordMessage.MessageReference`, `Poll` mapping, `PostID`, `Post` delegates)
- Test: `internal/switchboard/discord_test.go`

**Interfaces:**
- Produces: `Message.ReferencedID string`; `(*RESTClient).PostID(ctx, channel, content string) (string, error)`. Task 3 consumes both.

- [ ] **Step 1: Write failing tests**

In `internal/switchboard/discord_test.go` (mirror the existing Post/Poll tests — they use `WithBaseURL`+`WithHTTPClient` with a canned RoundTripper):
```go
func TestPostIDReturnsCreatedID(t *testing.T) {
	c := NewRESTClient("tok", nil, WithHTTPClient(cannedJSON(t, 200, `{"id":"D1"}`)), WithBaseURL("http://x"))
	id, err := c.PostID(context.Background(), "chan-1", "hello")
	if err != nil || id != "D1" {
		t.Fatalf("PostID = %q,%v; want D1,nil", id, err)
	}
}

func TestPollDecodesReferencedID(t *testing.T) {
	// one channel; canned newest-first page with a reply carrying message_reference
	body := `[{"id":"m2","content":"re","author":{"username":"alice"},"message_reference":{"message_id":"D1"}}]`
	c := NewRESTClient("tok", []string{"chan-1"}, WithHTTPClient(cannedJSON(t, 200, body)), WithBaseURL("http://x"))
	msgs, _, err := c.Poll(context.Background(), map[string]string{})
	if err != nil || len(msgs) != 1 || msgs[0].ReferencedID != "D1" {
		t.Fatalf("Poll ReferencedID = %+v,%v", msgs, err)
	}
}
```
Reuse the existing test's canned-transport helper (find how `TestRESTClientPost`/`TestRESTClientPoll` build their `*http.Client` — mirror it; `cannedJSON` above is a placeholder for that helper).

- [ ] **Step 2: Run — verify failing**

Run: `GOPROXY=off go test ./internal/switchboard/ -run 'TestPostID|TestPollDecodesReferenced'`
Expected: compile errors (`PostID`, `ReferencedID` undefined).

- [ ] **Step 3: Implement**

- `message.go`: add `ReferencedID string` to `Message` (doc: "id of the message this replies to; empty if not a reply").
- `discord.go`: add to `discordMessage`:
  ```go
  MessageReference struct {
  	MessageID string `json:"message_id"`
  } `json:"message_reference"`
  ```
  In `Poll`'s mapping loop, set `ReferencedID: m.MessageReference.MessageID` on the appended `Message`.
- Add `PostID` (extract the current `Post` body, but capture the response id):
  ```go
  func (c *RESTClient) PostID(ctx context.Context, channel, content string) (string, error) {
  	body, err := json.Marshal(map[string]string{"content": content})
  	if err != nil {
  		return "", err
  	}
  	u := fmt.Sprintf("%s/channels/%s/messages", c.baseURL, channel)
  	resp, err := c.doWithRetry(ctx, false, func() (*http.Request, error) {
  		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
  		if err != nil {
  			return nil, err
  		}
  		req.Header.Set("Authorization", "Bot "+c.token)
  		req.Header.Set("Content-Type", "application/json")
  		return req, nil
  	})
  	if err != nil {
  		return "", fmt.Errorf("switchboard: post %s: %w", channel, err)
  	}
  	defer resp.Body.Close()
  	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
  		return "", fmt.Errorf("switchboard: post %s: status %d", channel, resp.StatusCode)
  	}
  	var created struct{ ID string `json:"id"` }
  	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
  		return "", fmt.Errorf("switchboard: post %s decode: %w", channel, err)
  	}
  	return created.ID, nil
  }
  ```
  Then make `Post` delegate: `func (c *RESTClient) Post(ctx context.Context, channel, content string) error { _, err := c.PostID(ctx, channel, content); return err }`.

- [ ] **Step 4: Run — verify pass (incl. switchboard's existing tests)**

Run: `GOPROXY=off go test ./internal/switchboard/`
Expected: PASS (new tests + all existing Post/Poll/Seed/loop tests — Post delegating must not regress them).

- [ ] **Step 5: Commit**

```bash
git add internal/switchboard/message.go internal/switchboard/discord.go internal/switchboard/discord_test.go
git commit -m "switchboard: PostID (returns created id) + Message.ReferencedID (COV-183)"
```

---

## Task 2: `fileReceipts` receipt store

**Files:**
- Modify: `cmd/at-harbor/msgport_discord.go` (add `fileReceipts`)
- Test: `cmd/at-harbor/msgport_discord_test.go`

**Interfaces:**
- Produces: `newFileReceipts(path) (*fileReceipts, error)`, `(*fileReceipts).Record(discordMsgID, actorID string) error`, `(*fileReceipts).Lookup(discordMsgID string) (string, bool)`. Tasks 3/4/5 consume it.

- [ ] **Step 1: Write failing tests** (mirror `fileCursors`/`fileMarkers` tests):
```go
func TestFileReceiptsRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.json")
	r, err := newFileReceipts(p)
	if err != nil { t.Fatal(err) }
	if _, ok := r.Lookup("D1"); ok { t.Fatal("empty lookup should miss") }
	if err := r.Record("D1", "cove-1"); err != nil { t.Fatal(err) }
	if a, ok := r.Lookup("D1"); !ok || a != "cove-1" { t.Fatalf("lookup = %q,%v", a, ok) }
	r2, err := newFileReceipts(p) // reload
	if err != nil { t.Fatal(err) }
	if a, ok := r2.Lookup("D1"); !ok || a != "cove-1" { t.Fatalf("reload = %q,%v", a, ok) }
}
func TestFileReceiptsMissingFile(t *testing.T) {
	r, err := newFileReceipts(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil { t.Fatal(err) }
	if _, ok := r.Lookup("x"); ok { t.Fatal("missing file → empty") }
}
```

- [ ] **Step 2: Run — verify failing.** `GOPROXY=off go test ./cmd/at-harbor/ -run TestFileReceipts` → compile error.

- [ ] **Step 3: Implement `fileReceipts`** in `msgport_discord.go` (mirror `fileCursors`: mutex, load-at-open tolerating missing/torn → empty, save-on-Record):
```go
type fileReceipts struct {
	path string
	mu   sync.Mutex
	m    map[string]string // discord-msg-id → actorID
}
func newFileReceipts(path string) (*fileReceipts, error) { /* ReadFile; IsNotExist→empty; Unmarshal; torn→empty */ }
func (r *fileReceipts) Record(discordMsgID, actorID string) error { /* lock; m[id]=actor; MarshalIndent; WriteFile 0o600 */ }
func (r *fileReceipts) Lookup(discordMsgID string) (string, bool) { /* lock; a,ok:=m[id]; return */ }
```

- [ ] **Step 4: Run — verify pass.** `GOPROXY=off go test ./cmd/at-harbor/ -run TestFileReceipts` → PASS.

- [ ] **Step 5: Commit**
```bash
git add cmd/at-harbor/msgport_discord.go cmd/at-harbor/msgport_discord_test.go
git commit -m "harbor: file-backed discord receipt store (COV-183)"
```

---

## Task 3: `discordSurface` real ingress + receipt-writing egress

**Files:**
- Modify: `cmd/at-harbor/msgport_discord.go` (`discordClient`, rewrite `discordSurface`, `decodeCursors`/`encodeCursors`, `discordInboxChannels`; remove the 5b `discordPoster`)
- Test: `cmd/at-harbor/msgport_discord_test.go` (replace the 5b `fakeDiscordPoster` surface tests)

**Interfaces:**
- Consumes: `switchboard.Message`/`PostID`/`NewRESTClient`, `fileReceipts` (Task 2), `harbor.Roster`/`Human.DeliveryFor` (via the store closure), `msgport.Surface`/`Event`/`Delivery`.
- Produces: `discordClient interface { PostID(...); Poll(...) }`; the rewritten `discordSurface{dial, channelsFor, receipts}`; `decodeCursors`/`encodeCursors`; `discordInboxChannels(store, project) []string`.

- [ ] **Step 1: Write failing surface tests** (fake `discordClient`):
```go
type fakeDiscordClient struct {
	posts   []struct{ channel, content string }
	postID  string
	postErr error
	pollMsgs []switchboard.Message
	pollNext map[string]string
	pollErr  error
	gotCursors map[string]string
}
func (f *fakeDiscordClient) PostID(_ context.Context, ch, c string) (string, error) {
	if f.postErr != nil { return "", f.postErr }
	f.posts = append(f.posts, struct{ channel, content string }{ch, c}); return f.postID, nil
}
func (f *fakeDiscordClient) Poll(_ context.Context, cur map[string]string) ([]switchboard.Message, map[string]string, error) {
	f.gotCursors = cur; return f.pollMsgs, f.pollNext, f.pollErr
}

func TestDiscordDeliverRecordsReceipt(t *testing.T) {
	fc := &fakeDiscordClient{postID: "D1"}
	rec := mustReceipts(t)
	s := &discordSurface{dial: func([]string) discordClient { return fc }, receipts: rec}
	id, err := s.Deliver(context.Background(), msgport.Delivery{Address: "inbox-A", BodyPrefix: "cove-1: "}, msglog.Message{From: msglog.Target{Kind: "actor", Ref: "cove-1"}, Body: "hi"})
	if err != nil || id != "D1" { t.Fatalf("Deliver = %q,%v", id, err) }
	if fc.posts[0].channel != "inbox-A" || fc.posts[0].content != "cove-1: hi" { t.Fatalf("post = %+v", fc.posts) }
	if a, ok := rec.Lookup("D1"); !ok || a != "cove-1" { t.Fatalf("receipt = %q,%v", a, ok) }
}

func TestDiscordDeliverSwallowsReceiptError(t *testing.T) {
	// a receipts impl whose Record errors → Deliver STILL returns nil (post happened; no double-post)
	// use a small fake receipts or point fileReceipts at an unwritable path; assert err==nil
}

func TestDiscordPollMapsReplies(t *testing.T) {
	fc := &fakeDiscordClient{pollMsgs: []switchboard.Message{{ID: "m2", Channel: "inbox-A", Author: "alice", Content: "re", ReferencedID: "D1"}}, pollNext: map[string]string{"inbox-A": "m2"}}
	s := &discordSurface{dial: func([]string) discordClient { return fc }, channelsFor: func(string) []string { return []string{"inbox-A"} }}
	ev, next, err := s.Poll(context.Background(), "acme", "")
	if err != nil || len(ev) != 1 || ev[0].ReplyToForeign != "D1" || ev[0].ForeignID != "m2" || ev[0].Author != "alice" { t.Fatalf("poll = %+v,%v", ev, err) }
	// next round-trips: decode(next) == pollNext
	if got := decodeCursors(next); got["inbox-A"] != "m2" { t.Fatalf("next = %q", next) }
}

func TestDiscordPollEmptyChannels(t *testing.T) {
	s := &discordSurface{channelsFor: func(string) []string { return nil }}
	ev, next, err := s.Poll(context.Background(), "acme", "cur")
	if err != nil || len(ev) != 0 || next != "cur" { t.Fatalf("empty = %+v,%q,%v", ev, next, err) }
}

func TestCursorCodecRoundTrip(t *testing.T) {
	if got := decodeCursors(""); len(got) != 0 { t.Fatal("empty → empty map") }
	m := map[string]string{"a": "1", "b": "2"}
	if got := decodeCursors(encodeCursors(m)); got["a"] != "1" || got["b"] != "2" { t.Fatalf("round-trip = %+v", got) }
	if got := decodeCursors("not json"); len(got) != 0 { t.Fatal("torn → empty") }
}
```

- [ ] **Step 2: Run — verify failing.** `GOPROXY=off go test ./cmd/at-harbor/ -run 'TestDiscord|TestCursor'` → compile errors.

- [ ] **Step 3: Rewrite `discordSurface`** per spec §3 (fields `dial func([]string) discordClient`, `channelsFor func(string) []string`, `receipts *fileReceipts`; `Service`/`Deliver`/`Poll`/`Close`). Add `decodeCursors(string) map[string]string` / `encodeCursors(map[string]string) string` (JSON; ""→empty; torn→empty; empty map→"" or "{}" — pick one and make the codec round-trip). Add `discordInboxChannels(store instanceRoster, project string) []string` (distinct non-empty `h.DeliveryFor("discord").Address` across `GetRoster(project).Humans`). REMOVE the 5b `discordPoster` interface + `fakeDiscordPoster` test.

- [ ] **Step 4: Run — verify pass.** `GOPROXY=off go test ./cmd/at-harbor/` → PASS (new discord tests; the 5b `TestDiscordDeliver`/`TestDiscordPollIsInert` are replaced; `TestEgressGoldenParity` + `TestResolveDiscordRouting` unaffected).

- [ ] **Step 5: Commit**
```bash
git add cmd/at-harbor/msgport_discord.go cmd/at-harbor/msgport_discord_test.go
git commit -m "harbor: real discord surface — Poll ingress + Deliver records receipts (COV-183)"
```

---

## Task 4: service-aware `directory.Route`

**Files:**
- Modify: `cmd/at-harbor/msgport_linear.go` (`Route` → `routeLinear`+`routeDiscord`; `directory.receipts` field)
- Test: `cmd/at-harbor/msgport_linear_test.go`

**Interfaces:**
- Consumes: `fileReceipts.Lookup`, `msgport.Event`.
- Produces: service-aware `Route`; `routeDiscord`; `directory.receipts *fileReceipts`.

- [ ] **Step 1: Write failing tests:**
```go
func TestRouteDiscord(t *testing.T) {
	rec := mustReceipts(t); rec.Record("D1", "cove-1")
	dir := &directory{store: st, receipts: rec}
	// reply to a known receipt → cove
	from, to, replyTo, ok := dir.Route("discord", "acme", msgport.Event{Author: "alice", ReplyToForeign: "D1", ForeignID: "m2"})
	if !ok || from.Ref != "alice" || len(to) != 1 || to[0] != (msglog.Target{Kind: "actor", Ref: "cove-1"}) || replyTo != "in:discord:D1" {
		t.Fatalf("routeDiscord reply: %+v %+v %q %v", from, to, replyTo, ok)
	}
	// not a reply → drop
	if _, _, _, ok := dir.Route("discord", "acme", msgport.Event{Author: "alice"}); ok { t.Fatal("non-reply must drop") }
	// reply to unknown id → drop
	if _, _, _, ok := dir.Route("discord", "acme", msgport.Event{Author: "alice", ReplyToForeign: "D9"}); ok { t.Fatal("unknown-ref must drop") }
}
```
Keep the existing `TestDirectoryRoute` (linear) — it must still pass unchanged (now exercising `routeLinear` via the service switch).

- [ ] **Step 2: Run — verify failing.** `GOPROXY=off go test ./cmd/at-harbor/ -run 'TestRoute|TestDirectoryRoute'` → compile/fail.

- [ ] **Step 3: Implement** per spec §4: add `receipts *fileReceipts` to `directory`; `Route` switches `service=="discord"`→`routeDiscord` else `routeLinear`; move the current `Route` body verbatim into `routeLinear(project, e)`; add `routeDiscord(e)`.

- [ ] **Step 4: Run — verify pass.** `GOPROXY=off go test ./cmd/at-harbor/` → PASS (incl. unchanged `TestDirectoryRoute`).

- [ ] **Step 5: Commit**
```bash
git add cmd/at-harbor/msgport_linear.go cmd/at-harbor/msgport_linear_test.go
git commit -m "harbor: service-aware directory.Route — discord replies route via receipts (COV-183)"
```

---

## Task 5: wiring + docs

**Files:**
- Modify: `cmd/at-harbor/main.go` (the 5b discord block)
- Modify: `docs/usage/harbor/comms-addressing.md` / `messaging.md`

- [ ] **Step 1: Rewire the discord block** per spec §5: `newFileReceipts(filepath.Join(filepath.Dir(cfg.Store), "discord-receipts.json"))` (error → `fmt.Fprintln(stderr, …); return 1`); `token := tokEnv["AT_DISCORD_BOT_TOKEN"]`; `dir.receipts = receipts`; build `dsurf := &discordSurface{dial: func(ch []string) discordClient { return switchboard.NewRESTClient(token, ch) }, channelsFor: func(p string) []string { return discordInboxChannels(st, p) }, receipts: receipts}`. Keep the existing seed + `msgport.New(dsurf, …, EgressEnabled:true)` + `go deng.Run`. (`st` satisfies `instanceRoster` incl. `GetProject`/`GetRoster` from prior slices.)

- [ ] **Step 2: Build + full test.** `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → PASS.

- [ ] **Step 3: Boundaries.**
  `GOPROXY=off go list -deps ./internal/msgport | grep -iE 'harbor|switchboard|dispatch'` → empty.
  `GOPROXY=off go list -deps ./internal/harbor | grep -iE 'grpc|dispatch|/kit|backend|connect'` → empty.

- [ ] **Step 4: Docs** (`comms-addressing.md` / `messaging.md`): the Discord reply loop — a human **replies** (Discord's reply feature) to a cove's message and the cove wakes with that reply; the constraint (only a reply routes; a bare message in a shared inbox is dropped); receipts are unpruned for now. Bump `updated:` to 2026-09-15. Then `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md` (no NEW findings).

- [ ] **Step 5: Commit**
```bash
git add cmd/at-harbor/main.go docs/usage/harbor/comms-addressing.md docs/usage/harbor/messaging.md
git commit -m "harbor: wire discord ingress (receipts + real surface) + docs (COV-183)"
```

---

## Self-Review

**Spec coverage:** §1 switchboard → Task 1. §2 receipts → Task 2. §3 surface → Task 3. §4 route → Task 4. §5 wiring → Task 5. Docs → Task 5. All covered.

**Placeholder scan:** the switchboard canned-transport helper (`cannedJSON`) and `mustReceipts` are "reuse the existing helper / add a tiny one" — concrete. `fileReceipts` bodies are described by mirroring `fileCursors` (a fully-worked sibling in the same file).

**Type consistency:** `PostID(ctx, channel, content string) (string, error)` + `Message.ReferencedID` (Task 1) match `discordClient` (Task 3). `fileReceipts.Record/Lookup` (Task 2) match `discordSurface`/`directory` usage (Tasks 3/4). `directory.receipts` set in wiring (Task 5). `decodeCursors`/`encodeCursors` round-trip. `discordInboxChannels(store, project)` uses `GetRoster` + `Human.DeliveryFor("discord")` (5a).

**Risks flagged for review:** (1) receipt-error swallow in Deliver — the reviewer confirms a Record failure never makes Deliver return an error (no double-post). (2) `routeLinear` must be byte-identical to today's `Route` (linear ingress unchanged; `TestDirectoryRoute` passes). (3) the "reply-required" rule genuinely drops harbor's own posts (no echo) and non-reply human messages — confirm no path routes a non-reply. (4) concurrency: `fileReceipts` mutex-guarded, string values (no aliasing); the two discord-engine goroutines (Deliver writes, Route reads) are safe. (5) cursor codec: empty/torn → empty map (never a Poll with a garbage cursor).
