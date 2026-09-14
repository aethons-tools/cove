# harbor comms message-log substrate — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A focused, stdlib-only `internal/msglog` package: the unified message envelope + a durable append-only Log + internal/external classification + an in-process read/append API. Consumed by nothing this slice.

**Architecture:** `Message{From, To[], Body, At, Project, ReplyTo}` with `Target{Kind, Ref}` (`actor`/`human`/`channel`). A `Log` mirrors an append-only JSONL file in memory (load-all-on-open, append to memory+disk), guarded by a mutex. `Classify(Target) Reach` is a pure structural rule. Reads scan the in-memory mirror and return copies.

**Tech Stack:** Go stdlib only (`encoding/json`, `bufio`, `os`, `sync`, `time`, `crypto/rand`, `encoding/hex`, `fmt`, `log/slog`).

## Global Constraints

- **`internal/msglog` imports ONLY the stdlib** — no `internal/harbor`, grpc, kit, dispatch. `internal/harbor` will consume it in a later slice, never the reverse. Verify: `go list -deps ./internal/msglog | grep -E 'aethons-tools/cove'` is empty.
- **Storage is an append-only JSONL file + in-memory mirror.** Append = one `json.Marshal(msg)+"\n"` line under a mutex; reads scan the in-memory `msgs` and return copies (callers can never mutate the mirror). No `fsync` in v1 (matches FileStore).
- **IDs are time-sortable + unique:** `%020d-<hex>` of `At.UnixNano()` + crypto/rand, so append order == lexical ID order.
- **`Classify` is pure/structural:** `actor`→Internal, `human`→External, `channel`→External. Correct for every target that exists today (no internal channels yet); a code comment records the future per-channel refinement.
- **Wired to nothing:** `internal/harbor`, `/messages`, wake-on, escalation, CLI, and config are all untouched. No operator surface, so no `docs/usage` leaf this slice — the spec + package godoc are the docs (a `docs/usage` entry lands with the first consumer/adapter slice).
- **TDD, DRY, YAGNI, frequent commits.** Every task ends green (`GOPROXY=off go build ./... && GOPROXY=off go test ./...`), gofmt-clean, `.at-cove/` untouched. Prefix go commands with `GOPROXY=off`.

## File Structure

- Create `internal/msglog/message.go` — `Target`, `Message`, `Reach`, `Classify`, `newID`, validation (Task 1).
- Create `internal/msglog/log.go` — `Log`, `Open`, `Close`, `Append`, JSONL persistence (Task 2).
- Create `internal/msglog/read.go` — `ReadInbox`, `ReadThread`, `List`, `Filter` (Task 3).
- Tests: `internal/msglog/message_test.go`, `log_test.go`, `read_test.go`.

---

### Task 1: Envelope types + `Classify` + IDs + validation (pure)

**Files:**
- Create: `internal/msglog/message.go`
- Test: `internal/msglog/message_test.go`

**Interfaces:**
- Produces: `Target{Kind, Ref string}` + `Target.String()` + `Target.valid()`; `Message{ID, From, To, Body, At, Project, ReplyTo}`; `Reach` (`Internal`/`External`) + `Classify(Target) Reach`; `newID(time.Time) string`; `(Message).validate() error`.

- [ ] **Step 1: Write failing tests**

```go
package msglog

import (
	"sort"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := map[string]Reach{"actor": Internal, "human": External, "channel": External}
	for kind, want := range cases {
		if got := Classify(Target{Kind: kind, Ref: "x"}); got != want {
			t.Errorf("Classify(%s)=%v want %v", kind, got, want)
		}
	}
}

func TestTargetString(t *testing.T) {
	if got := (Target{Kind: "actor", Ref: "cove-1"}).String(); got != "actor:cove-1" {
		t.Fatalf("String()=%q", got)
	}
}

func TestNewIDTimeSortable(t *testing.T) {
	a := newID(time.Unix(0, 100))
	b := newID(time.Unix(0, 200))
	ids := []string{b, a}
	sort.Strings(ids)
	if ids[0] != a || ids[1] != b {
		t.Fatalf("IDs not time-sortable: %v", ids)
	}
	if a == newID(time.Unix(0, 100)) {
		t.Fatal("IDs at the same instant must still differ (random suffix)")
	}
}

func TestMessageValidate(t *testing.T) {
	good := Message{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "hi"}
	if err := good.validate(); err != nil {
		t.Fatalf("good message rejected: %v", err)
	}
	bad := []Message{
		{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}}, // empty body
		{From: Target{Kind: "actor", Ref: "a"}, Body: "hi"},                                // empty To
		{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "bogus", Ref: "b"}}, Body: "hi"}, // bad To kind
		{From: Target{Kind: "", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "hi"},      // bad From
		{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: ""}}, Body: "hi"},  // empty To ref
	}
	for i, m := range bad {
		if err := m.validate(); err == nil {
			t.Errorf("bad message %d accepted", i)
		}
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `GOPROXY=off go test ./internal/msglog/ -run 'Classify|TargetString|NewID|MessageValidate' -v`
Expected: FAIL (package/symbols undefined).

- [ ] **Step 3: Implement `message.go`**

```go
// Package msglog is harbor's durable, append-only message Log: one envelope
// (Message{From, To[], Body, …}) for all comms, over a JSONL file mirrored in
// memory. A Target's Reach (Internal/External) decides whether it's delivered
// in-band (a cove reads its inbox) or later rendered onto a human surface by an
// adapter. This package is stdlib-only and imports nothing from internal/harbor;
// harbor consumes it. Single-node (the serve process is the sole writer).
package msglog

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Target addresses a participant or conduit.
//
//	actor:<coveID>  — an internal harbor cove (Reach Internal; delivered in-band)
//	human:<name>    — an external human (Reach External; rendered by an adapter)
//	channel:<name>  — a shared conduit (a ticket thread, a chat channel)
type Target struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

func (t Target) String() string { return t.Kind + ":" + t.Ref }

func validKind(k string) bool { return k == "actor" || k == "human" || k == "channel" }

func (t Target) valid() bool { return validKind(t.Kind) && t.Ref != "" }

// Message is one immutable Log entry.
type Message struct {
	ID      string    `json:"id"`
	From    Target    `json:"from"`
	To      []Target  `json:"to"`
	Body    string    `json:"body"`
	At      time.Time `json:"at"`
	Project string    `json:"project,omitempty"`
	ReplyTo string    `json:"reply_to,omitempty"`
}

func (m Message) validate() error {
	if m.Body == "" {
		return fmt.Errorf("msglog: empty body")
	}
	if !m.From.valid() {
		return fmt.Errorf("msglog: invalid from %q", m.From.String())
	}
	if len(m.To) == 0 {
		return fmt.Errorf("msglog: empty to")
	}
	for _, t := range m.To {
		if !t.valid() {
			return fmt.Errorf("msglog: invalid to %q", t.String())
		}
	}
	return nil
}

// Reach classifies a Target for delivery.
type Reach int

const (
	Internal Reach = iota // delivered in-band (a cove reads its inbox); never touches an adapter
	External              // rendered onto a surface by an adapter
)

// Classify is the structural reach rule, correct for every target kind that
// exists today: actor → Internal, human → External, channel → External (every
// current channel is a Linear ticket, an external surface). Per-channel
// internal/external resolution — for a future agent-only channel — is deferred
// to when such a channel type exists (it will take a directory lookup then).
func Classify(t Target) Reach {
	if t.Kind == "actor" {
		return Internal
	}
	return External
}

// newID returns a time-sortable, unique id: zero-padded UnixNano + a random
// suffix, so append order equals lexical id order.
func newID(at time.Time) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%020d-%s", at.UnixNano(), hex.EncodeToString(b))
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/msglog/ -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/msglog/message.go internal/msglog/message_test.go
git add internal/msglog/message.go internal/msglog/message_test.go
git commit -m "harbor: msglog envelope types + Classify + ids (COV-171)" # + trailers
```

---

### Task 2: The `Log` store — `Open`/`Close`/`Append` + JSONL persistence

**Files:**
- Create: `internal/msglog/log.go`
- Test: `internal/msglog/log_test.go`

**Interfaces:**
- Consumes: `Message`, `newID`, `(Message).validate()` (Task 1).
- Produces: `Log`; `Open(path string, log *slog.Logger) (*Log, error)`; `(*Log).Close() error`; `(*Log).Append(m Message) (Message, error)`; unexported `(*Log).snapshot() []Message` for Task 3 to build on (optional helper).

- [ ] **Step 1: Write failing tests**

```go
package msglog

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAppendAssignsIDAndAt(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "m.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got, err := l.Append(Message{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.At.IsZero() {
		t.Fatalf("Append must assign ID+At: %+v", got)
	}
	// caller-supplied ID/At preserved
	at := time.Unix(5, 0)
	got2, _ := l.Append(Message{ID: "fixed", At: at, From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "yo"})
	if got2.ID != "fixed" || !got2.At.Equal(at) {
		t.Fatalf("Append must preserve supplied ID/At: %+v", got2)
	}
}

func TestAppendRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.jsonl")
	l, _ := Open(path, nil)
	defer l.Close()
	if _, err := l.Append(Message{From: Target{Kind: "actor", Ref: "a"}, Body: ""}); err == nil {
		t.Fatal("expected validation error")
	}
	// nothing written to disk
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Fatalf("invalid append must not write: %q", b)
	}
}

func TestPersistenceReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.jsonl")
	l, _ := Open(path, nil)
	for i := 0; i < 3; i++ {
		_, _ = l.Append(Message{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "m"})
	}
	l.Close()
	l2, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if n := len(l2.List(Filter{})); n != 3 {
		t.Fatalf("reload: got %d messages want 3", n)
	}
}

func TestOpenToleratesTornLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.jsonl")
	l, _ := Open(path, nil)
	_, _ = l.Append(Message{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "good"})
	l.Close()
	// simulate a torn final append
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"id":"x","body":"trunca`)
	f.Close()
	l2, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open must tolerate a torn line: %v", err)
	}
	defer l2.Close()
	if n := len(l2.List(Filter{})); n != 1 {
		t.Fatalf("torn line: want the 1 good message, got %d", n)
	}
	// a subsequent append still yields valid lines
	if _, err := l2.Append(Message{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "after"}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentAppend(t *testing.T) {
	l, _ := Open(filepath.Join(t.TempDir(), "m.jsonl"), nil)
	defer l.Close()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = l.Append(Message{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "c"})
		}()
	}
	wg.Wait()
	if n := len(l.List(Filter{})); n != 50 {
		t.Fatalf("concurrent append: got %d want 50", n)
	}
}
```

(`List` is implemented in Task 3; add a minimal `List` stub now if needed to compile, OR write Task 2 to include a temporary count accessor. Cleanest: implement `List(Filter{})` as part of Task 2's snapshot so these tests compile — but per the plan, `List`'s full filter logic is Task 3. To keep tasks green: in Task 2 add `List(f Filter) []Message` returning all messages ignoring the filter fields is wrong. Instead, expose `snapshot() []Message` in Task 2 and have these Task-2 tests call `l.snapshot()`; move the `List(Filter{})` assertions to use `len(l.snapshot())`. Adjust the test bodies above to use `l.snapshot()` accordingly.)

> **Implementer note:** rewrite the four `len(l.List(Filter{}))` calls above to `len(l.snapshot())` so Task 2 needs no `List`. `snapshot()` returns a copy of the in-memory mirror.

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/msglog/ -run 'Append|Persistence|TornLine|Concurrent' -v` → FAIL.

- [ ] **Step 3: Implement `log.go`**

```go
package msglog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

// maxLineBytes bounds a single JSONL line read at open (a message body is far
// smaller in practice; this is a safety cap well above any real message).
const maxLineBytes = 4 << 20 // 4 MiB

// Log is a durable, append-only message log: a JSONL file mirrored in memory.
// The serve process is the sole writer (single-node MVP).
type Log struct {
	mu   sync.Mutex
	path string
	f    *os.File
	msgs []Message
	log  *slog.Logger
}

// Open loads (or creates) the log at path — reading every well-formed line into
// the in-memory mirror, tolerating a torn trailing line (logged + skipped) — and
// opens the file for append. A nil logger discards.
func Open(path string, log *slog.Logger) (*Log, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	l := &Log{path: path, log: log}
	if data, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(data)
		sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var m Message
			if err := json.Unmarshal(line, &m); err != nil {
				log.Warn("msglog: skipping malformed line", "error", err.Error())
				continue
			}
			l.msgs = append(l.msgs, m)
		}
		data.Close()
		// A Scanner error (e.g. an over-long final torn line) is tolerated: the
		// prior good messages are kept.
		if err := sc.Err(); err != nil {
			log.Warn("msglog: scan ended early", "error", err.Error())
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("msglog: open %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("msglog: open-append %s: %w", path, err)
	}
	l.f = f
	return l, nil
}

// Close closes the append handle.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Append validates m, assigns an ID/At when unset, writes one JSONL line, and
// mirrors it in memory. Returns the stored message.
func (l *Log) Append(m Message) (Message, error) {
	if err := m.validate(); err != nil {
		return Message{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if m.At.IsZero() {
		m.At = time.Now()
	}
	if m.ID == "" {
		m.ID = newID(m.At)
	}
	line, err := json.Marshal(m)
	if err != nil {
		return Message{}, fmt.Errorf("msglog: marshal: %w", err)
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return Message{}, fmt.Errorf("msglog: write: %w", err)
	}
	l.msgs = append(l.msgs, m)
	return m, nil
}

// snapshot returns a copy of the in-memory mirror (deep enough that callers
// can't mutate stored To slices). Reads in read.go build on this.
func (l *Log) snapshot() []Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Message, len(l.msgs))
	for i, m := range l.msgs {
		m.To = append([]Target(nil), m.To...)
		out[i] = m
	}
	return out
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/msglog/ -v` → PASS (Task 1 + Task 2 tests).
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/msglog/log.go internal/msglog/log_test.go
git add internal/msglog/log.go internal/msglog/log_test.go
git commit -m "harbor: msglog append-only Log store + JSONL persistence (COV-171)" # + trailers
```

---

### Task 3: Read API — `ReadInbox` / `ReadThread` / `List`

**Files:**
- Create: `internal/msglog/read.go`
- Test: `internal/msglog/read_test.go`

**Interfaces:**
- Consumes: `Log.snapshot()`, `Message`, `Target` (Task 2/1).
- Produces: `Filter{Project string, Since, Until time.Time}`; `(*Log).ReadInbox(t Target) []Message`; `(*Log).ReadThread(rootID string) []Message`; `(*Log).List(f Filter) []Message`.

- [ ] **Step 1: Write failing tests**

```go
package msglog

import (
	"path/filepath"
	"testing"
	"time"
)

func seed(t *testing.T) *Log {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "m.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestReadInboxMultiRecipient(t *testing.T) {
	l := seed(t)
	defer l.Close()
	_, _ = l.Append(Message{From: Target{Kind: "actor", Ref: "s"}, To: []Target{{Kind: "actor", Ref: "a"}, {Kind: "human", Ref: "b"}}, Body: "x"})
	if n := len(l.ReadInbox(Target{Kind: "actor", Ref: "a"})); n != 1 {
		t.Fatalf("actor:a inbox=%d want 1", n)
	}
	if n := len(l.ReadInbox(Target{Kind: "human", Ref: "b"})); n != 1 {
		t.Fatalf("human:b inbox=%d want 1", n)
	}
	if n := len(l.ReadInbox(Target{Kind: "actor", Ref: "c"})); n != 0 {
		t.Fatalf("actor:c inbox=%d want 0", n)
	}
}

func TestReadInboxReturnsCopy(t *testing.T) {
	l := seed(t)
	defer l.Close()
	_, _ = l.Append(Message{From: Target{Kind: "actor", Ref: "s"}, To: []Target{{Kind: "actor", Ref: "a"}}, Body: "x"})
	got := l.ReadInbox(Target{Kind: "actor", Ref: "a"})
	got[0].To[0].Ref = "mutated" // must not corrupt the store
	if l.ReadInbox(Target{Kind: "actor", Ref: "a"})[0].To[0].Ref != "a" {
		t.Fatal("ReadInbox leaked a mutable reference into the store")
	}
}

func TestReadThread(t *testing.T) {
	l := seed(t)
	defer l.Close()
	root, _ := l.Append(Message{From: Target{Kind: "actor", Ref: "s"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "root"})
	_, _ = l.Append(Message{From: Target{Kind: "human", Ref: "b"}, To: []Target{{Kind: "actor", Ref: "s"}}, Body: "reply", ReplyTo: root.ID})
	_, _ = l.Append(Message{From: Target{Kind: "actor", Ref: "s"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "unrelated"})
	th := l.ReadThread(root.ID)
	if len(th) != 2 || th[0].Body != "root" || th[1].Body != "reply" {
		t.Fatalf("thread=%+v want [root, reply]", th)
	}
}

func TestListFilter(t *testing.T) {
	l := seed(t)
	defer l.Close()
	_, _ = l.Append(Message{From: Target{Kind: "actor", Ref: "s"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "acme", Project: "acme", At: time.Unix(10, 0)})
	_, _ = l.Append(Message{From: Target{Kind: "actor", Ref: "s"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "beta", Project: "beta", At: time.Unix(20, 0)})
	if n := len(l.List(Filter{Project: "acme"})); n != 1 {
		t.Fatalf("project filter=%d want 1", n)
	}
	if n := len(l.List(Filter{Since: time.Unix(15, 0)})); n != 1 {
		t.Fatalf("since filter=%d want 1", n)
	}
	if n := len(l.List(Filter{})); n != 2 {
		t.Fatalf("empty filter=%d want 2", n)
	}
}
```

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/msglog/ -run 'ReadInbox|ReadThread|ListFilter' -v` → FAIL.

- [ ] **Step 3: Implement `read.go`**

```go
package msglog

import "time"

// Filter selects messages for List. Zero fields are unbounded.
type Filter struct {
	Project string
	Since   time.Time // inclusive lower bound; zero = unbounded
	Until   time.Time // exclusive upper bound; zero = unbounded
}

// ReadInbox returns, in append order, the messages addressed to t (t ∈ To).
func (l *Log) ReadInbox(t Target) []Message {
	var out []Message
	for _, m := range l.snapshot() {
		for _, r := range m.To {
			if r == t {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// ReadThread returns the root message (ID == rootID) followed by its direct
// replies (ReplyTo == rootID), in append order. Deep (multi-level) threads are
// deferred.
func (l *Log) ReadThread(rootID string) []Message {
	var out []Message
	for _, m := range l.snapshot() {
		if m.ID == rootID || m.ReplyTo == rootID {
			out = append(out, m)
		}
	}
	return out
}

// List returns messages matching f, in append order.
func (l *Log) List(f Filter) []Message {
	var out []Message
	for _, m := range l.snapshot() {
		if f.Project != "" && m.Project != f.Project {
			continue
		}
		if !f.Since.IsZero() && m.At.Before(f.Since) {
			continue
		}
		if !f.Until.IsZero() && !m.At.Before(f.Until) {
			continue
		}
		out = append(out, m)
	}
	return out
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/msglog/ -v` → PASS (all three files).
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass. Confirm the boundary: `go list -deps ./internal/msglog | grep -E 'aethons-tools/cove'` is empty.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/msglog/read.go internal/msglog/read_test.go
git add internal/msglog/read.go internal/msglog/read_test.go
git commit -m "harbor: msglog read API — ReadInbox/ReadThread/List (COV-171)" # + trailers
```

---

## Self-Review

- **Spec coverage:** §1 package+storage → Tasks 1–2; §2 envelope → Task 1; §3 classification → Task 1; §4 API → Tasks 2 (Append) + 3 (reads); §5 wired-to-nothing → all (no harbor/CLI/config touched). Tests § list → distributed across the three test files.
- **Type consistency:** `Target`/`Message`/`Reach`/`Filter` defined once (Task 1/3); `snapshot()` (Task 2) is the single copy-out the reads (Task 3) build on, so no reader mutates the mirror.
- **Boundary:** the package imports only stdlib — verified by the `go list -deps` grep in Task 3 Step 4.
- **Green between tasks:** Task 2's tests use `snapshot()` (not `List`) so they don't depend on Task 3 (the implementer note fixes the sketched `List` calls). Each task's package compiles and tests pass on its own.
- **Docs:** intentionally no `docs/usage` leaf (no operator surface this slice) — the spec + the package godoc are the documentation; a usage doc lands with the first consumer/adapter slice. Flag this to the final reviewer so it isn't mistaken for a missed step.
- **Placeholder scan:** none — all code is concrete. The only "adapt" note is the Task 2 test's `List`→`snapshot()` rewrite, which is explicit.
