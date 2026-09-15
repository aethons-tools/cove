# message-log append-Seq ordering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a monotonic append-`Seq` the single ordering/cursor key in the message log (ids stay deterministic dedup identifiers), fixing wake-on-reply (COV-184) + read-paging + pg-ordering.

**Architecture:** Add `Message.Seq int64` (assigned at append: file counter / pg BIGSERIAL). Convert every cursor/ordering path from lexical id to numeric Seq (`ReadInboxSince`/`ReadInboxBefore`/`ListSince`/`TailSeq` + `SeqOf` resolver; pg `ORDER BY seq`; `Instance.WaitSeq`/`CommitSeq`; `EgressMark.LastSeq`; wakeon). The cove-facing wire stays message ids, resolved id↔Seq at the harbor boundary via `SeqOf`.

**Tech Stack:** Go; `internal/msglog` (+ `msglogpg`), `internal/msgport`, `internal/harbor`, `internal/wakeon`, `cmd/at-harbor`.

## Global Constraints

- One atomic cursor-type change. **`go build ./...` goes red after Task 2 and returns green at Task 6.** Each task keeps its OWN converted package(s) compiling+testing; the final PR head is fully green (CI gates there). Verify per-task with the package-scoped commands each task names; only Task 6 requires whole-module green.
- The cove-facing wire is UNCHANGED: `/messages` cursors stay **message ids** (`committed_cursor`/`page_first`/`page_last`/`up_to`/`?id=`). `cove-master` + the MCP tool schema must not change. Harbor resolves id↔Seq internally via `SeqOf`.
- `SeenIDs(prefix)` stays id-based (dedup membership, not ordering) — do NOT change it.
- `internal/msgport` stays msglog+stdlib (an `int64` field adds no import). `internal/harbor` core import set unchanged.
- No secret/message-body in any log line, cursor, receipt, or marker.
- Tests hermetic. The pg backend runs behind its existing integration gate/DSN-skip — do NOT newly require a live DB in unit runs. `-race` unavailable (no cgo) — reason manually.
- Build offline `GOPROXY=off`. The pre-existing uncommitted `.at-cove/config.yml` / `.at-cove/example-kitconfig.yml` / `.claude/` stay OUT of every commit — stage files explicitly by path.

## File Structure

- `internal/msglog/message.go` — `Message.Seq`.
- `internal/msglog/log.go` — file Append assigns Seq; Open resumes the counter.
- `internal/msglog/read.go` — file Seq-based reads + `SeqOf` + `TailSeq`.
- `internal/msglog/store.go` — interface (cursors int64, `TailSeq`, `SeqOf`; `TailID` removed).
- `internal/msglog/msglogpg/migrations/0002_seq.sql` (new) + `msglogpg.go` — seq column, RETURNING, scan, ORDER BY seq, `SeqOf`/`TailSeq`.
- `internal/msglog/msglogtest/conformance.go` — seq cursors + mixed-namespace ordering test + `SeqOf`.
- `internal/msgport/msgport.go` + `egress.go` — `EgressMark.LastSeq`; `cmd` `fileMarkers` persists it; `logTailSeq`.
- `internal/harbor/instance.go` / `memstate.go` / `supervisor.go` / `filestore.go` / `pgstore.go` / `messages.go` / `storetest/conformance.go` — Instance seqs, AdvanceCommitCursor, tailSeq, SetWaitSeq, messages SeqOf boundary.
- `internal/wakeon/wakeon.go` — `Inbox.ReadInboxSince(int64)` + `WaitSeq`.
- `cmd/at-harbor/main.go` — `logTailSeq`, egress seeds, SetTailReader.
- `docs/usage/harbor/messaging.md` — the append-seq ordering invariant.

---

## Task 1: `Message.Seq` + assignment (additive, module stays green)

**Files:** `internal/msglog/message.go`, `log.go`, `read.go` (add `SeqOf`/`TailSeq`), `store.go` (add `SeqOf`/`TailSeq` to the interface — alongside the existing `TailID`, NOT removing it yet), `msglogpg/migrations/0002_seq.sql` (new), `msglogpg/msglogpg.go`, `msglogtest/conformance.go`.

**Interfaces:**
- Produces: `Message.Seq int64`; `Store.SeqOf(id string) (int64, bool)`; `Store.TailSeq() (int64, bool)`. Existing id-based methods (`ReadInboxSince(afterID string,…)`, `TailID`, etc.) are UNCHANGED this task — so all callers still compile (module green).

- [ ] **Step 1: Write failing tests** in `msglogtest/conformance.go` (runs both backends):
```go
func testSeqAndResolution(t *testing.T, newStore func(t *testing.T) msglog.Store) {
	s := newStore(t)
	a, _ := s.Append(msglog.Message{From: actor("c"), To: []msglog.Target{actor("x")}, Body: "a"})
	b, _ := s.Append(msglog.Message{From: actor("c"), To: []msglog.Target{actor("x")}, Body: "b"})
	if a.Seq <= 0 || b.Seq <= a.Seq {
		t.Fatalf("Seq not monotonic: a=%d b=%d", a.Seq, b.Seq)
	}
	if seq, ok := s.SeqOf(a.ID); !ok || seq != a.Seq {
		t.Fatalf("SeqOf(a) = %d,%v want %d", seq, ok, a.Seq)
	}
	if _, ok := s.SeqOf("nope"); ok {
		t.Fatal("SeqOf(unknown) should miss")
	}
	if tail, ok := s.TailSeq(); !ok || tail != b.Seq {
		t.Fatalf("TailSeq = %d,%v want %d", tail, ok, b.Seq)
	}
}
```
Call it from the shared conformance entrypoint for both backends. (The file backend must also persist+resume Seq — add a file-specific test in `internal/msglog` that appends, reopens via `Open`, appends again, and asserts the new Seq == prior max + 1.)

- [ ] **Step 2: Run — verify failing.** `GOPROXY=off go test ./internal/msglog/...` → compile errors (`Seq`/`SeqOf`/`TailSeq` undefined).

- [ ] **Step 3: Implement.**
- `message.go`: add `Seq int64 \`json:"seq"\`` (place it first, before `ID`).
- `log.go`: `*Log` gains `nextSeq int64`. In `Open`, after loading messages, set `l.nextSeq = maxSeq + 1` (0 → 1 when empty; compute `maxSeq` from loaded `m.Seq`). In `Append`, under the mutex, `m.Seq = l.nextSeq; l.nextSeq++` BEFORE marshal/write (so the persisted line carries Seq).
- `read.go`: `SeqOf(id)` scans `l.snapshot()` for `m.ID==id` → `(m.Seq, true)`; `TailSeq()` = last message's `Seq` (`len==0 → 0,false`).
- `store.go`: add `SeqOf(id string) (int64, bool)` and `TailSeq() (int64, bool)` to the interface (keep `TailID` for now).
- `msglogpg/migrations/0002_seq.sql`:
  ```sql
  ALTER TABLE messages ADD COLUMN seq BIGSERIAL;
  CREATE INDEX IF NOT EXISTS idx_messages_seq ON messages (seq);
  CREATE INDEX IF NOT EXISTS idx_recipients_target_seq ON message_recipients (kind, ref);
  ```
  (The recipient index already covers (kind,ref,message_id); inbox-by-seq joins messages and orders by m.seq — the existing index plus idx_messages_seq suffice. Keep the migration minimal; do not drop existing indexes.)
- `msglogpg.go`: `Append`'s INSERT gains `RETURNING seq` → scan into `m.Seq` (switch the `tx.Exec` to `tx.QueryRow(...).Scan(&m.Seq)`); `scanMessages` scans `seq` as the FIRST column (update every SELECT column list to include `seq` and the Scan to `&m.Seq, &m.ID, …`); add `SeqOf` (`SELECT seq FROM messages WHERE id=$1`) and `TailSeq` (`SELECT seq FROM messages ORDER BY seq DESC LIMIT 1`). Leave the existing id-based WHERE/ORDER BY for now (Task 2 converts them).

- [ ] **Step 4: Run — verify pass.** `GOPROXY=off go test ./internal/msglog/...`; `GOPROXY=off go build ./...` (must be GREEN — this task is additive).

- [ ] **Step 5: Commit.**
```bash
git add internal/msglog/message.go internal/msglog/log.go internal/msglog/read.go internal/msglog/store.go internal/msglog/msglogpg/ internal/msglog/msglogtest/conformance.go internal/msglog/*_test.go
git commit -m "msglog: Message.Seq (append order) + SeqOf + TailSeq (COV-184)"
```

---

## Task 2: msglog cursor API → Seq (module goes red until Task 6)

**Files:** `internal/msglog/read.go`, `store.go`, `msglogpg/msglogpg.go`, `msglogtest/conformance.go`.

**Interfaces:**
- Produces: `ReadInboxSince(t, afterSeq int64, limit)`, `ReadInboxBefore(t, beforeSeq int64, limit)`, `ListSince(afterSeq int64, limit)` (cursor is now int64 Seq); ordering by Seq; `TailID` removed. Consumers (msgport/harbor/wakeon/cmd) now fail to compile — fixed in Tasks 3-6.

- [ ] **Step 1: Update conformance** (`msglogtest`) to drive the new int64 cursors AND add the regression guard:
```go
func testMixedNamespaceOrdering(t *testing.T, newStore func(t *testing.T) msglog.Store) {
	s := newStore(t)
	x := actor("x")
	// append in a deliberate order whose lexical id order differs from append order
	m1, _ := s.Append(msglog.Message{ID: "in:linear:zzz", From: human("h"), To: []msglog.Target{x}, Body: "1"})
	m2, _ := s.Append(msglog.Message{ID: "in:discord:aaa", From: human("h"), To: []msglog.Target{x}, Body: "2"}) // lexically < m1
	m3, _ := s.Append(msglog.Message{From: human("h"), To: []msglog.Target{x}, Body: "3"})                        // digit-prefixed internal id
	got := s.ReadInbox(x)
	if len(got) != 3 || got[0].Body != "1" || got[1].Body != "2" || got[2].Body != "3" {
		t.Fatalf("ReadInbox not in APPEND order: %+v", bodies(got))
	}
	// ReadInboxSince(after m1.Seq) must include m2 even though m2.ID < m1.ID lexically
	since := s.ReadInboxSince(x, m1.Seq, 0)
	if len(since) != 2 || since[0].Body != "2" || since[1].Body != "3" {
		t.Fatalf("ReadInboxSince(m1.Seq) = %+v, want [2,3] (lexical-id would wrongly drop 2)", bodies(since))
	}
	_ = m2; _ = m3
}
```
(Add `human`/`bodies` helpers if absent.) Update the existing `ListSince`/`ReadInboxSince`/`ReadInboxBefore`/`TailID` conformance cases to pass seqs (from the appended messages' `.Seq`) and call `TailSeq`.

- [ ] **Step 2: Run — verify failing** (conformance won't compile against old signatures): `GOPROXY=off go test ./internal/msglog/...`.

- [ ] **Step 3: Convert the file backend (`read.go`)** — `ReadInboxSince(t, afterSeq int64, limit)`: filter `m.Seq <= afterSeq` skip; `ReadInboxBefore(t, beforeSeq int64, limit)`: `beforeSeq<=0` = from end, else `m.Seq < beforeSeq`, nearest-below; `ListSince(afterSeq int64, limit)`: `m.Seq <= afterSeq` skip. All iterate append-order (already correct). Remove `TailID` (keep `TailSeq`).

- [ ] **Step 4: Convert the interface (`store.go`)** — change the three cursor signatures to `int64`; remove `TailID`; update the doc comments (ids are identifiers; Seq is the order key).

- [ ] **Step 5: Convert the pg backend (`msglogpg.go`)** — every `ORDER BY m.id`/`m.id DESC` → `m.seq`/`m.seq DESC`; `WHERE m.id > $` → `m.seq > $`; `ReadInboxBefore` `WHERE m.seq < $` (and `beforeSeq<=0` → from end: `ORDER BY m.seq DESC LIMIT`); `ListSince` `WHERE seq > $`. Remove `TailID`. (Keep `ReadInbox`/`ReadThread`/`List` but switch their `ORDER BY id` → `ORDER BY seq`.)

- [ ] **Step 6: Run — verify the msglog package is GREEN** (its own tests pass): `GOPROXY=off go test ./internal/msglog/...`. NOTE: `GOPROXY=off go build ./...` is now RED at msgport/harbor/wakeon/cmd (expected — Tasks 3-6). Confirm the ONLY build failures are those downstream cursor-type mismatches.

- [ ] **Step 7: Commit.**
```bash
git add internal/msglog/read.go internal/msglog/store.go internal/msglog/msglogpg/msglogpg.go internal/msglog/msglogtest/conformance.go
git commit -m "msglog: cursors + ordering are append-Seq, not lexical id (COV-184)"
```

---

## Task 3: `internal/msgport` egress → `LastSeq`

**Files:** `internal/msgport/msgport.go` (`EgressMark`), `egress.go`; `cmd/at-harbor/msgport_linear.go` (`fileMarkers` JSON), `cmd/at-harbor/main.go` (`logTailSeq` + seeds).

- [ ] **Step 1:** Update `internal/msgport` tests/fakes for `LastSeq` + the `ListSince(int64)` / `TailSeq` fake surface (the msgport `fakeLog`/`Markers` fakes). Assert exactly-once egress still holds with a mixed-id Log (append three, deliver, advance `LastSeq` by `m.Seq`).

- [ ] **Step 2:** `EgressMark.LastMsg string` → `LastSeq int64`. `egress.go`: `e.lg.ListSince(mark.LastSeq, egressBatch)`; the done-prefix advance sets `mark.LastSeq = m.Seq`; replace any `m.ID == mark.LastMsg` / lexical logic with Seq. The `Pending map[string]...` dedup stays id-keyed (uses `m.ID`). `cmd fileMarkers`: the persisted JSON field becomes `LastSeq` (int64) — the map value type changes; a torn/old file tolerates as before (old string `LastMsg` ignored → LastSeq 0 = re-seed-needed, but the seed guard `has(service)` still works on the key presence; if an old mark had LastMsg set, after upgrade LastSeq=0 → egress would re-evaluate from seq 0 → the echo-guard + Pending dedup prevent double-delivery of already-delivered-and-marked... NOTE: LastSeq=0 means "deliver from the start" → could re-deliver. MITIGATION: on load, if a `"discord"`/`"linear"` marker key exists but has no `LastSeq` (old format), RE-SEED it to the current tail on startup — i.e. the cmd seed step should seed when `!has(service) || markers.Egress(service).LastSeq == 0`. Document this in the seed code comment.) `cmd main.go`: `logTailID`→`logTailSeq` (calls `lg.TailSeq()`); both egress seeds set `EgressMark{LastSeq: logTailSeq(messageLog)}`; the seed guard re-seeds a zero/absent LastSeq (per the mitigation).

- [ ] **Step 3:** Run `GOPROXY=off go test ./internal/msgport/ ./cmd/at-harbor/` — msgport package green; cmd still red on harbor/wakeon downstream (expected). Confirm msgport is green.

- [ ] **Step 4: Commit.**
```bash
git add internal/msgport/msgport.go internal/msgport/egress.go internal/msgport/*_test.go cmd/at-harbor/msgport_linear.go cmd/at-harbor/main.go
git commit -m "msgport: egress low-water is append-Seq (EgressMark.LastSeq) (COV-184)"
```

---

## Task 4: `internal/harbor` Instance/supervisor/memstate/stores/storetest → Seq

**Files:** `internal/harbor/instance.go`, `memstate.go`, `supervisor.go`, `filestore.go`, `pgstore.go`, `storetest/conformance.go`.

- [ ] **Step 1:** Update `storetest/conformance.go` `AdvanceCommitCursor` cases to `(actorID, upToID string, upToSeq int64)` — forward advance (higher seq) sets both `CommitCursor`(id)+`CommitSeq`; a lower-seq call no-ops (both unchanged).

- [ ] **Step 2:** `instance.go`: `WaitCursor`→`WaitSeq int64 \`json:"wait_seq,omitempty"\``; add `CommitSeq int64 \`json:"commit_seq,omitempty"\``; KEEP `CommitCursor string` (the committed id, for display). `memstate.applyAdvanceCommitCursor(actorID, upToID string, upToSeq int64)`: forward iff `upToSeq > i.CommitSeq` → set `i.CommitSeq=upToSeq; i.CommitCursor=upToID`. `filestore.go`/`pgstore.go` `AdvanceCommitCursor(actorID, upToID string, upToSeq int64)` signatures + the `Store` interface. `supervisor.go`: `tailReader.TailID()`→`TailSeq() (int64,bool)`, `tailID()`→`tailSeq()`; `Report` baselines `inst.WaitSeq = s.tailSeq()` and `inst.CommitSeq = s.tailSeq()` (leave CommitCursor ""); `SetWaitCursor`→`SetWaitSeq(actorID string, seq int64)`.

- [ ] **Step 3:** Run `GOPROXY=off go test ./internal/harbor/ ./internal/harbor/storetest/` — harbor package compiles EXCEPT messages.go (Task 5) and the wakeon/cmd wiring of tailReader. If messages.go blocks the harbor package build, this task may need to stub messages.go minimally OR be merged with Task 5. PREFERRED: do Task 4 + Task 5 together if the harbor package won't compile split (messages.go uses the changed Instance fields). Decide at implementation: if `internal/harbor` can't compile without messages.go converted, fold Task 5 into this commit. Report which.

- [ ] **Step 4: Commit** (stage the harbor files changed).
```bash
git commit -m "harbor: Instance WaitSeq/CommitSeq + AdvanceCommitCursor numeric (COV-184)"
```

---

## Task 5: `internal/harbor/messages` (SeqOf boundary) + `internal/wakeon` → Seq

**Files:** `internal/harbor/messages.go`, `internal/wakeon/wakeon.go`.

- [ ] **Step 1:** Tests — wakeon `fakeInbox.ReadInboxSince(t, afterSeq int64, limit)`; the **COV-184 regression test**: an external reply with `Seq > WaitSeq` wakes even when the tail at baseline is an ingress-id message with a lexically-smaller id (seqs prove it); no-wake when `Seq <= WaitSeq`; internal-origin no-wake; nil inbox. messages tests: `commit {up_to:"<id>"}` resolves via `SeqOf` → `AdvanceCommitCursor(id, seq)`; `read anchor=id` resolves; `read anchor=cursor` uses `CommitSeq`; `committed_cursor`/`page_*` are message ids; an unknown `up_to`/`id` → 400.

- [ ] **Step 2:** `wakeon.go`: `Inbox.ReadInboxSince(t, afterSeq int64, limit)`; `replied()` uses `inst.WaitSeq` + `Classify(From)==External`. `messages.go`: the reader interface gains `SeqOf(id string) (int64, bool)` (the `inboxReader`); `handleCommit` resolves `SeqOf(req.UpTo)` (unknown→400) → `AdvanceCommitCursor(actor.ID, req.UpTo, seq)`, response echoes `inst.CommitCursor`; `handleGet` `anchor=cursor`→`inst.CommitSeq`, `anchor=id`→`SeqOf(id)` (unknown→400), `anchor=start`→`ReadInboxSince(target,0,limit)`, `anchor=end`→`ReadInboxBefore(target,0,limit)`; `committed_cursor`=`inst.CommitCursor`, `page_first/last`=`msgs[0/last].ID`.

- [ ] **Step 3:** Run `GOPROXY=off go test ./internal/harbor/ ./internal/wakeon/` → green.

- [ ] **Step 4: Commit.**
```bash
git commit -m "harbor+wakeon: resolve wire ids to Seq at the boundary; wakeon waits on Seq (COV-184)"
```

---

## Task 6: cmd wiring final green + docs

**Files:** `cmd/at-harbor/main.go` (`SetTailReader`/any `TailID`/`logTailID` leftovers → Seq; wakeon `Inbox` = messageLog still satisfies the new `ReadInboxSince(int64)`), `docs/usage/harbor/messaging.md`.

- [ ] **Step 1:** Fix any remaining cmd references (`logTailSeq` done in Task 3; confirm `SetTailReader(messageLog)` — `messageLog` (an `msglog.Store`) satisfies the supervisor's `tailReader` now that it wants `TailSeq`; confirm `wakeon.New(..., messageLog, ...)` — `messageLog` satisfies `wakeon.Inbox`'s `ReadInboxSince(int64)`).
- [ ] **Step 2:** `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → ALL GREEN (the whole module). This is the task that closes the red window.
- [ ] **Step 3:** Boundaries: `GOPROXY=off go list -deps ./internal/msgport | grep -iE 'harbor|switchboard|dispatch'` empty; `GOPROXY=off go list -deps ./internal/harbor | grep -iE 'grpc|dispatch|/kit|backend|connect'` empty.
- [ ] **Step 4:** Docs: `docs/usage/harbor/messaging.md` (or the message-log/observability doc) — one paragraph: the message log orders by a monotonic append sequence; ids are identifiers (deterministic for ingress dedup), NOT ordering keys; cursors compare by sequence. Bump `updated:` 2026-09-15. `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md` (no NEW findings).
- [ ] **Step 5: Commit.**
```bash
git add cmd/at-harbor/main.go docs/usage/harbor/messaging.md
git commit -m "harbor: wire append-Seq end to end + docs (COV-184)"
```

---

## Self-Review

**Spec coverage:** §1 Seq → Task 1. §2 API → Task 2. §3 egress → Task 3. §4 Instance → Task 4. §5 wakeon → Task 5. §6 messages boundary → Task 5. cmd+docs → Task 6. All covered.

**Placeholder scan:** conformance helper names (`human`/`bodies`/`actor`) flagged reuse-or-add. The Task 4/5 split caveat (harbor package may not compile without messages.go) is called out with a concrete fallback (fold 5 into 4).

**Type consistency:** `ReadInboxSince(t, int64, int)`/`ReadInboxBefore(t,int64,int)`/`ListSince(int64,int)`/`TailSeq()(int64,bool)`/`SeqOf(string)(int64,bool)` consistent across store.go, both backends, wakeon.Inbox, messages reader. `AdvanceCommitCursor(actorID, upToID string, upToSeq int64)` consistent (memstate, filestore, pgstore, storetest, messages handler). `EgressMark.LastSeq int64` (msgport + fileMarkers + cmd seeds). `Instance.WaitSeq`/`CommitSeq int64` + kept `CommitCursor string`.

**Risks flagged for review:** (1) the mid-slice red build is intentional — the reviewer verifies Task 6's head is fully green, and that each intermediate task's own package is green. (2) the EgressMark old-format (`LastMsg` string → `LastSeq` 0) re-seed mitigation — confirm a cove/harbor upgrading with an existing marker file doesn't re-deliver the backlog (the seed re-seeds a zero LastSeq to the tail). (3) the mixed-namespace ordering conformance test is the core regression guard — it must assert append order holds where lexical id order would differ. (4) Instance field rename/migration — old `wait_cursor`/`commit_cursor` strings are ignored; `WaitSeq`/`CommitSeq` default 0 (safe); `CommitCursor` id is kept and still loads. (5) the pg `seq BIGSERIAL` migration orders pre-existing rows arbitrarily (noted; acceptable pre-prod).
