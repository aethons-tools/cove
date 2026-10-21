# intercom structured asks — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An agent posts a one-of-N question (an *ask*) into a channel; a person answers it in `/me`; the answer reaches the agent through `read`.

**Architecture:** Ask events are squawks in the channel log carrying the ask as a typed inline **part** (`Squawk.Parts`, the envelope attachments will reuse); the log store folds them into an `asks` read-model in the same serialized append (so "open only", "current version" and "first answer wins" are atomic). `jam.Intercom` adds the policy (who may ask/answer, addressing, session end), `/squawks/ask*` and four MCP tools expose it to agents, and `/me` renders cards and takes answers.

**Tech Stack:** Go; Postgres via pgx (`internal/intercom/intercompg`); html/template + htmx (`internal/jam/meui`); MCP go-sdk (`cmd/cove-master`).

**Spec:** [`docs/superpowers/specs/2026-10-21-intercom-asks-design.md`](../specs/2026-10-21-intercom-asks-design.md). Two corrections found while planning: (1) `at-jam export` carries no intercom state (backup.md), so §6's export line is moot — nothing to do; (2) appends are already serialized (pg advisory lock / Mem mutex), so the ask checks run under that lock, no row `FOR UPDATE`.

## Global Constraints

- Relays are paused: Discord/Linear egress must **skip** squawks with any part (`len(m.Parts) > 0`); no relay rendering.
- Options: 2–8, ids `^[a-z0-9_-]{1,32}$`, unique, never `other`; question ≤ 2048 B, label ≤ 200 B, detail ≤ 1024 B, comment ≤ 4096 B, reason ≤ 1024 B.
- `choice: "other"` requires a non-empty comment.
- Only the asking session amends/withdraws; only a person (`usr_…`) answers, a current member of a live channel, and in `Of` when set. Sessions never answer.
- An answer names the version it saw; a stale version is refused (`ErrAskChanged`), a closed ask is refused (`ErrAskClosed`).
- Hermetic tests; Postgres conformance behind `-tags integration` with `JAM_TEST_POSTGRES_DSN`.
- Docs updated in the same PR as the behavior.

## Review Focus

1. Two people answer the same open ask at once → exactly one wins, the other gets 409 "already answered by …" (store conformance, concurrent goroutines).
2. A person answers after the agent amended → 409 "this ask changed", nothing recorded (store + `/me` handler test).
3. The asking session ends with asks open → they close as withdrawn "session ended" and the session leaves "Needs you" (Intercom test).
4. A person who left the channel, or isn't in `Of`, posts an answer by hand → 403 (Intercom + handler test).
5. An older agent's `read` (no `parts` support) still sees a readable one-line body for every event (fold test asserts bodies).

## PR split

- **PR A (Tasks 1–5):** model, store, Intercom, `/squawks/ask*`, MCP tools, "Needs you". Ships usable by agents; `/me` shows the plain one-line bodies meanwhile.
- **PR B (Tasks 6–7):** `/me` cards, answering, open-asks panel and rail count, Discuss.

---

### Task 1: The ask model and its fold (`internal/intercom/ask.go`)

**Files:**
- Create: `internal/intercom/ask.go`, `internal/intercom/ask_test.go`
- Modify: `internal/intercom/channel_log.go` (add `Parts []Part` to `Squawk`; `Prepare` validates parts)

**Interfaces — Produces:**
```go
// The message parts envelope (shared with attachments later).
type Part struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"` // inline
	Ref  string          `json:"ref,omitempty"`  // reference (attachments; refused for now)
	Name string          `json:"name,omitempty"`
	Size int64           `json:"size,omitempty"`
}
const AskPartType = "application/vnd.jam.ask+json"
func AskPart(ev AskEvent) Part              // marshals ev inline
func AskOf(m Squawk) (AskEvent, bool)      // the squawk's ask event, if it has one
type AskOption struct{ ID, Label, Detail string } // json: id,label,detail
type AskEventKind string // "ask" | "amend" | "withdraw" | "answer"
type AskEvent struct {
	Event    AskEventKind `json:"event"`
	AskID    string       `json:"ask_id"`            // "" on the "ask" event: it is the squawk's own id
	Version  int          `json:"version,omitempty"` // answer: the version answered; set by Apply on ask/amend
	Question string       `json:"question,omitempty"`
	Options  []AskOption  `json:"options,omitempty"`
	Of       []ident.ID   `json:"of,omitempty"`      // ask only
	Choice   string       `json:"choice,omitempty"`  // answer: option id or "other"
	Comment  string       `json:"comment,omitempty"` // answer
	Reason   string       `json:"reason,omitempty"`  // withdraw
}
type AskState string // "open" | "answered" | "withdrawn"
type AskAnswer struct{ Version int; Choice, Comment string; By ident.ID; At time.Time }
type Ask struct {
	ID string; Channel, Asker ident.ID; Of []ident.ID
	Question string; Options []AskOption; Version int
	State AskState; Answer *AskAnswer; Reason string
	History []AskVersion // earlier {Version, Question, Options}, oldest first
	LastSeq int64
}
var ErrAskNotFound, ErrAskClosed, ErrAskChanged, ErrAskForbidden error
// ApplyAsk folds squawk m (AskOf(m) ok, m.ID/From/Channel/At/Seq set) onto prev
// (nil for the "ask" event) and returns the new state, or an error and no change.
func ApplyAsk(prev *Ask, m Squawk) (Ask, error)
// AskBody is the one-line plain rendering stored as the squawk's Body.
func AskBody(ev AskEvent, a Ask, byLabel string) string
```

Rules in `ApplyAsk`: `ask` needs prev==nil and valid question/options (limits above; wrap `ErrInvalid`), sets `ID=m.ID, Asker=m.From, Version=1, State=open`. `amend` needs prev open and `m.From==prev.Asker` (else `ErrAskForbidden`), pushes the old version onto History, applies non-empty question/options (validated), `Version++`. `withdraw`: open, asker-only, `State=withdrawn, Reason`. `answer`: open (else `ErrAskClosed`), `m.From.Kind()==ident.User` and in `Of` if set (else `ErrAskForbidden`), `ev.Version==prev.Version` (else `ErrAskChanged`), choice is an option id or `other` with comment. A prev in another channel than m.Channel → `ErrAskNotFound`.

`AskBody`: ask → `asked: <question> — (a) Yes, (b) No`; amend → `edited ask: <question> — …`; withdraw → `withdrew ask: <question>` + ` (<reason>)`; answer → `<byLabel> chose <label>: <comment>` (label `other` when other). Never empty.

- [ ] **Step 1: Failing tests** in `ask_test.go`: table test over `ApplyAsk` covering each rule above (valid ask; too few/many options; duplicate id; option id `other`; amend by non-asker → ErrAskForbidden; amend bumps version and records History; withdraw then answer → ErrAskClosed; answer by `ses_…` → ErrAskForbidden; answer outside Of → ErrAskForbidden; stale version → ErrAskChanged; `other` without comment → ErrInvalid; answer sets Answer{By,At,Choice,Comment}); and `AskBody` for each event is non-empty and contains the question/choice label. (Review Focus 5.)
- [ ] **Step 2:** `go test ./internal/intercom/ -run Ask` → FAIL (undefined).
- [ ] **Step 3:** Implement `ask.go`; add `Parts []Part \`json:"parts,omitempty"\`` to `Squawk`. `Prepare` refuses (ErrInvalid) a reference part (`Ref != ""`: attachments not yet), an unknown part type, more than one ask part, or an ask part whose data doesn't decode. Tests for each refusal.
- [ ] **Step 4:** Tests pass.
- [ ] **Step 5:** Commit `intercom: ask model and fold`.

### Task 2: Store support (Mem + Postgres + conformance)

**Files:**
- Modify: `internal/intercom/channel_log.go` (Store interface), `internal/intercom/mem.go`, `internal/intercom/intercompg/store.go`
- Create: `internal/intercom/intercompg/migrations/0005_asks.sql`
- Modify: `internal/intercom/intercomtest/conformance.go` (new `asks` cases)

**Interfaces — Produces:**
```go
// Store additions:
GetAsk(id string) (Ask, bool)
ListAsks(f AskFilter) []Ask // ordered by LastSeq ascending
type AskFilter struct{ Channel, Asker ident.ID; State AskState } // zero fields match all
```
`Append` semantics: when `AskOf(m)` is ok, inside the existing serialized section, load the current ask (`GetAsk(ev.AskID)` unless event is `ask`), call `ApplyAsk`, then insert the squawk and upsert the ask; any `ApplyAsk` error aborts the append (nothing stored). The returned squawk's ask part carries the new version (ask/amend); the store rewrites the part's data with `Version` set before inserting.

Migration `0005_asks.sql`:
```sql
ALTER TABLE squawks ADD COLUMN parts jsonb;
CREATE TABLE asks (
    id        text PRIMARY KEY,
    channel_id text NOT NULL,
    asker_id  text NOT NULL,
    state     text NOT NULL,
    last_seq  bigint NOT NULL,
    doc       jsonb NOT NULL
);
CREATE INDEX asks_channel_open ON asks (channel_id) WHERE state = 'open';
CREATE INDEX asks_asker ON asks (asker_id, state);
```
`squawkCols` gains `s.parts`; scan into `[]byte` and unmarshal when non-null. Mem keeps `asks map[string]Ask` beside `msgs`.

- [ ] **Step 1: Failing conformance cases** (run by both Mem and pg suites): ask round-trip (`GetAsk`, `ListAsks` by channel/asker/state); reads of the squawk return its `Parts` intact; amend→answer; stale answer refused and not stored (`TailSeq` unchanged); answer on closed refused; **concurrent answers**: 8 goroutines answer the same ask, exactly one succeeds, the rest `ErrAskClosed` (Review Focus 1).
- [ ] **Step 2:** `go test ./internal/intercom/...` → FAIL.
- [ ] **Step 3:** Implement Mem, then pg (migration, Append branch, `GetAsk`/`ListAsks`, scan).
- [ ] **Step 4:** Unit tests pass; reset local pg and run `JAM_TEST_POSTGRES_DSN=… go test -count=1 -p 1 -tags integration ./internal/intercom/...` → pass.
- [ ] **Step 5:** Commit `intercom: asks in the channel log (mem + pg)`.

### Task 3: Intercom policy (`internal/jam/intercom_asks.go`)

**Files:**
- Create: `internal/jam/intercom_asks.go`, `internal/jam/intercom_asks_test.go`
- Modify: `internal/jam/intercom_post.go` (`Ended` closes asks), `internal/jam/channels.go` (`AskedForPerson`), `internal/jam/instance.go` (`OpenAsks int \`json:"open_asks,omitempty"\``), `internal/jam/supervisor.go` (`SetOpenAsks`)

**Interfaces — Consumes:** Task 2's Store. **Produces:**
```go
type AskInput struct{ Question string; Options []intercom.AskOption; Of []string } // Of: "user:<name|id>"
func (ic *Intercom) Ask(p Poster, to string, in AskInput, now time.Time) (intercom.Ask, Channel, error)
func (ic *Intercom) AmendAsk(p Poster, askID, question string, options []intercom.AskOption) (intercom.Ask, error)
func (ic *Intercom) WithdrawAsk(p Poster, askID, reason string) (intercom.Ask, error)
func (ic *Intercom) AnswerAsk(user ident.ID, askID string, version int, choice, comment string) (intercom.Ask, error)
func (ic *Intercom) AsksOf(session ident.ID, state intercom.AskState) []intercom.Ask
func (ic *Intercom) OpenAsksIn(ch ident.ID) []intercom.Ask
func (ic *Intercom) MayAnswer(user ident.ID, a intercom.Ask) bool
func (ic *Intercom) SetOnAsksChanged(f func(session ident.ID, open int))
func (s *Supervisor) SetOpenAsks(actorID string, n int) error
```
- `Ask` plans exactly like a send (`ic.Plan(p, to, now)`, same 403/404 errors), resolves `Of` to project members (`memberUser`; unknown → `ErrSendUnresolved`), builds the squawk (`Ask` event, `Body` = `AskBody`), posts via `Post`.
- `AmendAsk`/`WithdrawAsk`: post into the ask's channel (`PostTrusted`-style append with `ic.audience`); store enforces asker-only.
- `AnswerAsk`: ask exists and its channel is live and the user is a current member (else `ErrSendDenied`); then append with `From=user`. Map store errors through unchanged.
- After any successful event, recount `len(AsksOf(asker, open))` restricted to live channels and call the hook (main wires it to `Supervisor.SetOpenAsks`).
- `Ended(inst)`: before archiving, withdraw each open ask of the session with reason `session ended` (trusted, as the session), then fire the hook with 0.
- `AskedForPerson(i)` becomes `i.EscalationAsked || i.OpenAsks > 0 || needs-input`.

- [ ] **Step 1: Failing tests** (use `newICFixture` from `intercom_session_test.go`): ask to home channel → answer by a member reaches the session's inbox with `Ask` set and wakes nothing extra; answer by non-member and by user outside `Of` → `ErrSendDenied`/`ErrAskForbidden` (Review Focus 4); ask with `to` beyond addressing → `ErrSendDenied`; amend by another session → `ErrAskForbidden`; hook receives 1 then 0 on answer; `Ended` withdraws open asks with reason `session ended` and hook gets 0 (Review Focus 3); `AskedForPerson` with `OpenAsks>0` (in `channels_test.go` `TestNeedsPerson` add a case).
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** `go test ./internal/jam/` passes.
- [ ] **Step 5:** Commit `jam: asks — intercom policy and Needs you`.

### Task 4: Agent API (`/squawks/ask*`) and read wire

**Files:** Modify `internal/jam/squawks.go`, `internal/jam/squawks_test.go`; Modify `cmd/at-jam/main.go` (wire `SetOnAsksChanged` → `sup.SetOpenAsks`); Modify `cmd/at-jam/relay_*.go` egress loops to `continue` on `len(m.Parts) > 0` (Global Constraint), with a test in the existing relay egress test file.

Routes (POST, handled like `/call-in`, before the ticket path; non-POST → 405):
- `/squawks/ask` `{question, options[{id,label,detail}], to?, of?[]}` → `200 {ask_id, version, channel}`
- `/squawks/ask/amend` `{ask_id, question?, options?}` → `200 {version}`
- `/squawks/ask/withdraw` `{ask_id, reason?}` → `204`
- `GET /squawks/asks?state=` → `200 {asks:[{ask_id, channel, question, options, version, state, answer?}]}` (handled before `handleGet`)

Errors: `ErrInvalid` 400; `ErrSendDenied`/`ErrAskForbidden` 403; `ErrSendUnresolved`/`ErrAskNotFound` 404; `ErrAskClosed`/`ErrAskChanged` 409.

Read wire: `jam.Squawk` gains `Parts []intercom.Part \`json:"parts,omitempty"\``, copied from the log in `handleGet` (an ask part's data is the `AskEvent` JSON).

- [ ] Step 1: failing handler tests for each route and status, and a read entry carrying its `parts`. Step 2: FAIL. Step 3: implement. Step 4: pass. Step 5: commit `jam: /squawks ask endpoints`.

### Task 5: MCP tools and docs for PR A

**Files:** Modify `cmd/cove-master/mcp.go`, `cmd/cove-master/mcp_test.go`; docs: create `docs/usage/jam/asks.md` (leaf: what an ask is, tools, rules, Needs you/escalation, limits), add its row to `docs/usage/jam/INDEX.md`; update `intercom.md` (tool list → link to asks.md), `escalation.md` (open asks count as asking).

Tools: `ask(question, options, to?, of?)`, `amend(ask_id, question?, options?)`, `withdraw(ask_id, reason?)`, `asks(state?)`; `squawkOut` gains `Parts []partOut` (`{type, data}` with data as raw JSON). Client methods mirror `callIn` (`c.do(ctx, POST, "/squawks/ask", payload)`).

- [ ] Step 1: failing tests: tools listed; each forwards method/path/body to the fake Jam (pattern of `TestMCPEndForwardsReason`); read decodes `parts`. Step 2: FAIL. Step 3: implement + docs. Step 4: `go test ./cmd/cove-master/ ./internal/... ./cmd/...`, `bash scripts/lint.sh`, docs audit, pg integration. Step 5: commit; open **PR A**; fresh review; merge when green.

### Task 6: `/me` cards and answering

**Files:** Modify `internal/jam/meui/conversation.go` (`MessageRow.Ask *AskCard`), `internal/jam/meui/handler.go` (route `POST /me/asks/{id}/answer`), `internal/jam/meui/templates/inbox.html` (card), `layout.html` (styles); tests in `internal/jam/meui/asks_test.go`.

```go
type AskCard struct {
	ID, Question string; Options []intercom.AskOption; Version int
	State string; AnsweredBy, Choice, Comment, Reason string
	Edited, CanAnswer bool; For string // "for brent" when Of excludes the viewer
}
```
- A squawk whose `AskOf` event is `ask` renders the card from `Log.GetAsk(id)` (current state). amend/withdraw/answer squawks render as a one-line system row (their `Body`), not a bubble.
- Answer form: `hx-post="/me/asks/{id}/answer"` with hidden `version`, a button per option (`name=choice value=<id>`), `Other…` (`choice=other`) and a `comment` textarea. Handler calls `Intercom.AnswerAsk(p.UserID, …)`: 204 + `HX-Refresh`; 409 body `this ask changed — review the new version` / `already answered by <label>: <choice>`; 403 `not allowed`; 404.
- [ ] Steps: failing tests (card open/answered/withdrawn/edited/read-only-for; answer 204; stale 409 (Review Focus 2); already-answered 409; non-member 403) → FAIL → implement → pass → commit.

### Task 7: Open-asks panel, rail count, Discuss, docs; PR B

**Files:** `conversation.go` (`Conversation.OpenAsks []AskCard` from `Intercom.OpenAsksIn`), `meui.go` (`ChannelRow.OpenAsks int`; a channel with an open ask the viewer `MayAnswer` sets `NeedsYou`), `inbox.html` (collapsible panel above `#stream`; rail badge; Discuss button sets the composer's hidden `reply_to` and prefills `re: <question>`), `internal/jam/me_send.go` (accept `reply_to` = an ask id in the same channel; store as `ReplyTo`), message row shows a `re:` chip linking `#ask-<id>`. Docs: `intercom-ui.md` (cards, panel, Discuss), `asks.md` (the `/me` side).

- [ ] Steps: failing tests (panel lists open asks; rail count and NeedsYou; `/me/send` with `reply_to` stored and rendered as chip; reply_to to an ask in another channel → 400) → FAIL → implement → pass → full verification → commit; open **PR B**; fresh review; merge when green.
