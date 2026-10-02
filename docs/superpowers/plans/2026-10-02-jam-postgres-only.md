# Postgres-only Jam Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove Jam's file-backed storage options that Postgres already provides (`store:`, `intercom-log`, `session-events-dir`), make `store-postgres` required, give relay state its own `state-dir:`, and replace the file stores in hermetic tests with in-memory stores.

**Architecture:** `cmd/at-jam` serve config rejects the removed keys and requires `store-postgres`; the store/log/session-events selection collapses to the Postgres branch. Each subsystem's file store becomes an in-memory, test-only implementation of the same interface (`jam.MemStore`, `intercom.Log` via `NewMemLog`, `sessionevents.MemStore`) that runs that subsystem's existing conformance suite hermetically.

**Tech Stack:** Go 1.27, pgx v5, existing `storetest` / `intercomtest` / `sessioneventstest` conformance suites.

**Spec:** `docs/superpowers/specs/2026-10-02-jam-postgres-only-design.md`

## Global Constraints

- Removed serve-config keys: `store`, `intercom-log`, `session-events-dir` — a set value is a hard startup error naming the key, with the hint to `at-jam export` from the old Jam and `at-jam import` into a Postgres Jam (see `docs/usage/jam/backup.md`), and stating that squawk history / session events in the old files are not migrated.
- `store-postgres` is required for `at-jam serve`.
- New key `state-dir:`; default `$XDG_STATE_HOME/at-jam`, else `~/.local/state/at-jam`; created `0700` when a relay needs it; holds `relay-cursors.json`, `relay-markers.json`, `relay-receipts.json`.
- Kept file stores: pool store (`pool.store`, `at-jam pool --store`), relay cursors/markers/receipts, credentials file.
- In-memory stores are test-only: doc comments say "not for production"; `serve` never constructs them.
- No change to Postgres schemas, the three store interfaces' behaviour, or `at-jam export`/`import`.
- Do not touch the allocator nil-ledger fallback (follow-up).
- Tests hermetic by default; Postgres tests stay behind `//go:build integration` + `JAM_TEST_POSTGRES_DSN`.
- Go env here: use `GOPROXY=https://proxy.golang.org`; `-race` unavailable (no cgo). Local Postgres for integration runs: `JAM_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54329/sess_utf8?sslmode=disable'`.
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **An operator upgrades with `store:` still in their config** → serve exits non-zero before touching anything, with a message naming `store` and the export/import path (not a YAML unknown-key warning, not a panic). Test: Task 1 `TestValidateRemovedStorageKeys`.
2. **Upgraded Jam with Discord/Linear relays enabled and no `state-dir`** → relay files go to the XDG state dir (created), never to the process cwd. Test: Task 1 `TestStateDirDefault` + `TestRelayStatePaths`.
3. **A test that relied on reopening a FileStore to prove persistence** → must be deleted or rewritten, not silently turned into a tautology against MemStore. Test: Task 2 reviewer checks every removed/rewritten persistence test is named in the report.
4. **MemStore aliasing** → callers mutating a returned slice/struct must not mutate the store (FileStore had the same in-memory core, so behaviour must match). Test: Task 2 runs `storetest` conformance on MemStore.
5. **intercom MemLog Seq/cursor semantics** → identical to the old Log (Seq from 1, ListSince/ReadInboxSince/TailSeq) so wake-on and relays behave the same in tests. Test: Task 3 runs `intercomtest.RunConformance` on MemLog.

## File Structure

| File | Change |
|---|---|
| `cmd/at-jam/config.go` | `StateDir` field, `atJamStateDir()`, `stateDir()`, `validateStorage()`; removed-key detection fields keep their yaml tags |
| `cmd/at-jam/main.go` | call `validateStorage`; drop file branches for store / intercom log / session events; relay paths from `cfg.stateDir()` |
| `cmd/at-jam/config_test.go` | tests for the above; update YAML literals that used `store:` |
| `internal/jam/store.go` (new) | the `Store` interface, moved out of filestore.go |
| `internal/jam/memstore.go` (from filestore.go) | `MemStore`, `NewMemStore()` |
| `internal/jam/config_snapshot.go` | `ImportConfig` receiver FileStore → MemStore |
| `internal/jam/filestore_test.go` | delete (file-format tests); keep any pure-behaviour cases by moving them to `memstore_test.go` |
| ~34 test files | `jam.NewFileStore(...)` → `jam.NewMemStore()` |
| `internal/intercom/log.go` | in-memory only: `NewMemLog()`; delete `Open`, file handle, JSONL load |
| intercom-using test files (11) | `intercom.Open(...)` → `intercom.NewMemLog()` |
| `internal/jam/sessionevents/memstore.go` (new), `filestore.go` (delete) | `sessionevents.NewMemStore()` |
| sessionevents-using test files (7) | `OpenFileStore(...)` → `NewMemStore()` |
| docs (see Task 5) | updated |

---
### Task 1: Serve config — require Postgres, reject removed keys, `state-dir`

**Files:**
- Modify: `cmd/at-jam/config.go` (fields at ~60-70; helpers near `atJamConfigDir` ~663)
- Modify: `cmd/at-jam/main.go` (validators ~1555-1590; store selection ~1598-1628; message Log ~1714-1748; session events ~1750-1772; relay paths ~1883-1905)
- Test: `cmd/at-jam/config_test.go`

**Interfaces:**
- Produces: `func (c serveConfig) validateStorage() error`; `func atJamStateDir() string`; `func (c serveConfig) stateDir() string`; serve-config field `StateDir string \`yaml:"state-dir"\``. After this task `serve` never calls `jam.NewFileStore`, `intercom.Open` or `sessionevents.OpenFileStore` (later tasks delete them).

- [ ] **Step 1: Write the failing tests** (append to `cmd/at-jam/config_test.go`)

```go
func TestValidateRemovedStorageKeys(t *testing.T) {
	pg := "store-postgres: {host: h, port: 5432, database: d, user: u, password-cred: p, sslmode: disable}\n"
	for _, key := range []string{"store: /var/lib/jam/store.json", "intercom-log: /var/lib/jam/log.jsonl", "session-events-dir: /var/lib/jam/events"} {
		c, err := parseServeConfig([]byte(pg + key + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		err = c.validateStorage()
		name := strings.SplitN(key, ":", 2)[0]
		if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "at-jam export") || !strings.Contains(err.Error(), "not migrated") {
			t.Errorf("%s: want removed-key error naming it with the export/import hint, got %v", name, err)
		}
	}
}

func TestValidateStorageRequiresPostgres(t *testing.T) {
	c, _ := parseServeConfig([]byte("listen: \":443\"\n"))
	if err := c.validateStorage(); err == nil || !strings.Contains(err.Error(), "store-postgres") {
		t.Fatalf("want store-postgres required, got %v", err)
	}
	ok, _ := parseServeConfig([]byte("store-postgres: {host: h, port: 5432, database: d, user: u, password-cred: p, sslmode: disable}\n"))
	if err := ok.validateStorage(); err != nil {
		t.Fatalf("valid postgres config: %v", err)
	}
}

func TestStateDirDefault(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if got := (serveConfig{}).stateDir(); got != "/xdg/state/at-jam" {
		t.Fatalf("XDG: got %q", got)
	}
	t.Setenv("XDG_STATE_HOME", "")
	home, _ := os.UserHomeDir()
	if got := (serveConfig{}).stateDir(); got != filepath.Join(home, ".local", "state", "at-jam") {
		t.Fatalf("home default: got %q", got)
	}
	c, _ := parseServeConfig([]byte("state-dir: /srv/jam-state\n"))
	if got := c.stateDir(); got != "/srv/jam-state" {
		t.Fatalf("explicit: got %q", got)
	}
}
```

(Match `store-postgres` field names to `storePostgresConfig` in config.go; if `password-cred` must name a configured credential at validation time, that is `validateStorePostgres`'s job, not `validateStorage`'s — `validateStorage` only checks presence.)

- [ ] **Step 2: Run** — `go test ./cmd/at-jam/ -run 'RemovedStorage|RequiresPostgres|StateDir'` → FAIL (undefined).

- [ ] **Step 3: Implement in `config.go`**

Replace the three field comments (keep the fields and their yaml tags — they now exist only to detect a removed key, like `DeprecatedBotToken`):

```go
	// Store, IntercomLog and SessionEventsDir are REMOVED file backends
	// (Postgres-only Jam, docs/usage/jam/serve.md). They are kept only so
	// validateStorage can reject a config that still sets them.
	Store            string `yaml:"store"`
	IntercomLog      string `yaml:"intercom-log"`
	SessionEventsDir string `yaml:"session-events-dir"`
	// StateDir holds Jam's remaining file state (relay cursors, markers,
	// receipts — no Postgres equivalent yet). Empty → atJamStateDir().
	StateDir string `yaml:"state-dir"`
```

Add:

```go
const removedStorageHint = "Jam is Postgres-only: set store-postgres instead. To keep an existing file Jam's config, run `at-jam export` against it (on the old version) and `at-jam import` into the Postgres Jam (docs/usage/jam/backup.md); squawk history and session events in the old files are not migrated"

// validateStorage requires store-postgres and rejects the removed file-backend
// keys with a migration hint.
func (c serveConfig) validateStorage() error {
	for _, k := range []struct{ name, val string }{
		{"store", c.Store}, {"intercom-log", c.IntercomLog}, {"session-events-dir", c.SessionEventsDir},
	} {
		if k.val != "" {
			return fmt.Errorf("%s is no longer supported. %s", k.name, removedStorageHint)
		}
	}
	if c.StorePostgres == nil {
		return fmt.Errorf("store-postgres is required (Jam is Postgres-only; see docs/usage/jam/serve.md)")
	}
	return nil
}

// atJamStateDir is $XDG_STATE_HOME/at-jam, else ~/.local/state/at-jam.
func atJamStateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "at-jam")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "at-jam")
}

// stateDir is where Jam keeps its remaining file state (relay cursors,
// markers, receipts).
func (c serveConfig) stateDir() string {
	if c.StateDir != "" {
		return c.StateDir
	}
	return atJamStateDir()
}
```

Delete `validateSessionEvents`' dependence on `SessionEventsDir` if any (it only parses retention — leave it).

- [ ] **Step 4: Update existing config tests** — every YAML literal in `config_test.go` that sets top-level `store:` or `intercom-log:` only to exercise parsing: keep the key-recognition test (`unknownServeKeys` must still treat them as known so the user gets the precise error, not an "unknown key" warning) and add `state-dir` to the known list; change other literals to `store-postgres: {...}`. Delete/replace `TestServeConfigIntercomLog` (it asserted the file path is honoured) with a check that `validateStorage` rejects it (already covered — delete it). Do not touch the pool's `store:` under `pool:`.

- [ ] **Step 5: Wire `main.go`**

1. Next to `cfg.validateStorePostgres()`, add (same stderr + `return 1` pattern), placed **before** it:

```go
	if err := cfg.validateStorage(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
```

2. Store selection: `store-postgres` is now guaranteed. Remove the `else` FileStore branch; keep the Postgres body unconditional (`pc := cfg.StorePostgres`). Update the comment ("Postgres is the only backend…").
3. Message Log: replace the `switch` with the unconditional `intercompg.New(...)` body; delete the `cfg.IntercomLog` case and `defer ml.Close()`. `intercomLog` is now always non-nil: remove `intercomLog != nil` guards **only where the change is mechanical** (the Notifier wrap at ~1743, `runDiscord := cfg.Runtime.Discord != nil`, the relay-cursor gate becomes `if dc != nil || runDiscord`, the admin UI `squawkReader` assignment, and the guards at ~1850, 1865, 1955, 2122, 2131); update comments that mention "unset config → nil / not configured". Keep `intercomLog`'s type as `intercom.Store`.
4. Session events: replace the `switch` with unconditional `sessionpg.New(...)`; drop the file case and the "not stored" default log. Keep `sessionevents.NopStore` in the package (tests may use it).
5. Relay state: before the relay block, compute `stateDir := cfg.stateDir()`; when the relay block runs, `os.MkdirAll(stateDir, 0o700)` (error → `at-jam: state-dir: <err>`, return 1), and replace the three `filepath.Join(filepath.Dir(cfg.Store), "relay-…json")` with `filepath.Join(stateDir, "relay-…json")`.

- [ ] **Step 6: Relay path test** — extract the three file names into a helper so it is testable without booting serve:

```go
// relayStatePaths returns the relay cursor, marker and receipt files under dir.
func relayStatePaths(dir string) (cursors, markers, receipts string) {
	return filepath.Join(dir, "relay-cursors.json"), filepath.Join(dir, "relay-markers.json"), filepath.Join(dir, "relay-receipts.json")
}
```

use it in main.go, and test:

```go
func TestRelayStatePaths(t *testing.T) {
	c, m, r := relayStatePaths("/srv/state")
	if c != "/srv/state/relay-cursors.json" || m != "/srv/state/relay-markers.json" || r != "/srv/state/relay-receipts.json" {
		t.Fatalf("%s %s %s", c, m, r)
	}
}
```

- [ ] **Step 7: Verify** — `go build ./... && go vet ./cmd/at-jam/ && go test ./cmd/at-jam/` → PASS. Also `grep -n "NewFileStore\|intercom.Open\|OpenFileStore" cmd/at-jam/*.go | grep -v _test` → no output.

- [ ] **Step 8: Commit**

```bash
git add cmd/at-jam/
git commit -m "feat(at-jam): Postgres-only serve — reject removed file-backend keys, add state-dir

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `jam.MemStore` replaces `jam.FileStore`

**Files:**
- Create: `internal/jam/store.go` (the `Store` interface, moved verbatim)
- Rename: `internal/jam/filestore.go` → `internal/jam/memstore.go`
- Modify: `internal/jam/config_snapshot.go` (`ImportConfig` receiver)
- Delete: `internal/jam/filestore_test.go` (after moving pure-behaviour tests, see Step 4)
- Modify: every test file calling `jam.NewFileStore` / `NewFileStore` (list with `grep -rln "NewFileStore" --include=*_test.go .`, ~34 files)
- Modify: stale comments naming the file store (`memstate.go:12,16,625`, `sessions.go:46`, `supervisor.go:105,724`, `cmd/at-jam/nag.go:39`, `cmd/at-jam/relay_linear.go:114`, `storetest/conformance.go:2`) — reword to "the in-memory test store" / "Postgres"; do NOT change the allocator nil-ledger code paths' behaviour (only comment wording if it says "file store").

**Interfaces:**
- Consumes: Task 1 (no prod caller of `NewFileStore` remains).
- Produces: `type MemStore struct{ *memState }`; `func NewMemStore() *MemStore` (never errors); `var _ Store = (*MemStore)(nil)`. Doc comment: "MemStore is an in-memory Store for tests and tooling. Not for production: nothing is persisted."

- [ ] **Step 1: Move the interface** — cut the `Store` interface (and its doc comment) from `filestore.go` into a new `internal/jam/store.go` (`package jam`, no imports unless the interface needs `time`). `go build ./internal/jam/` → still builds.

- [ ] **Step 2: Rename and strip persistence**

```bash
git mv internal/jam/filestore.go internal/jam/memstore.go
```

In `memstore.go`:
- delete `storeFile`, `legacyIdentity`, `NewFileStore`, `migrateIdentities`, `nonNilStrings`, `save()`; keep `sameStrings` only if non-test code still uses it (it is used by `decide_test.go` — if it is defined only here, move it into `memstore.go` unchanged or into the test file that uses it).
- rename the type and receivers: `FileStore` → `MemStore` (receiver var `fs` → `s` optional; keep diff small).
- add:

```go
// MemStore is an in-memory Store for tests and tooling. Not for production:
// nothing is persisted. It shares the in-memory core (memState) with
// PostgresStore, so read and mutation semantics are identical.
type MemStore struct {
	*memState
}

var _ Store = (*MemStore)(nil)

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore { return &MemStore{memState: newMemState()} }
```

- every `return fs.save()` → `return nil`; in `CommitUnread` keep the early-return on no change and return nil otherwise; drop the now-unused `encoding/json`, `os`, `fmt`, `time` imports as the compiler reports.
- the mutator section comment becomes `// ---- mutators: Lock; validate/compute via memState; apply ----`.

In `config_snapshot.go`, `func (fs *FileStore) ImportConfig` → `func (fs *MemStore) ImportConfig`, removing its trailing `save()` (return nil after `applyImport`).

- [ ] **Step 3: Swap test call sites**

`NewFileStore` returned `(*FileStore, error)`; `NewMemStore` returns `*MemStore`. Update each call site by hand (the shapes vary). Typical rewrites:

```go
// before
st, err := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
if err != nil {
	t.Fatal(err)
}
// after
st := jam.NewMemStore()
```

```go
// before (helper)
func newStore(t *testing.T) jam.Store {
	t.Helper()
	st, err := jam.NewFileStore(t.TempDir() + "/store.json")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return st
}
// after
func newStore(t *testing.T) jam.Store {
	t.Helper()
	return jam.NewMemStore()
}
```

Inside package `jam` tests use `NewMemStore()`. Remove imports (`path/filepath`, `os`) that become unused.

**Persistence tests:** any test that writes, re-opens the same path, and asserts the data survived (or asserts file mode/contents) tested FileStore persistence, which no longer exists — delete it. List every deleted or rewritten test by name in the task report with one line of why. Do not convert such a test into a same-instance check that trivially passes.

- [ ] **Step 4: Conformance and `filestore_test.go`**

- `internal/jam/store_conformance_test.go`: `TestFileStoreConformance` → `TestMemStoreConformance` running `storetest.RunConformance` (or the suite's actual entry point) with `func(t *testing.T) jam.Store { return jam.NewMemStore() }`.
- `filestore_test.go`: delete tests about file format, legacy v1/v2/v3 migration, `backfillProjects` on load, reopen/persistence, file mode. Move any test that exercises plain store behaviour not already covered by `storetest` (e.g. validation errors, upsert semantics) to `internal/jam/memstore_test.go` using `NewMemStore()`. Then delete `filestore_test.go`.

- [ ] **Step 5: Verify**

```bash
grep -rn "FileStore\b\|NewFileStore" --include=*.go . | grep -v "sessionevents\|PoolStore\|FilePoolStore"   # expect: no output
go build ./... && go vet ./... && go test ./...
JAM_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54329/sess_utf8?sslmode=disable' go test -tags integration ./internal/jam/...
```

Expected: no stray references; all PASS. (`pgstore_importconfig_integration_test.go` used a FileStore as the *source* of an import — swap to `NewMemStore()`.)

- [ ] **Step 6: Commit**

```bash
git add -A internal/jam cmd/at-jam internal/covemaster
git commit -m "refactor(jam): replace the file-backed Store with an in-memory test store

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: `intercom` — in-memory Log replaces the JSONL file Log

**Files:**
- Modify: `internal/intercom/log.go`
- Modify: `internal/intercom/store_conformance_test.go`, `log_test.go`, `notify_test.go`, `read_test.go`
- Modify: tests calling `intercom.Open` in `cmd/at-jam/{mux,nag,relay_seed}_test.go`, `internal/jam/adminui/intercom_test.go`, `internal/jam/squawks_test.go`, `internal/relay/engine_test.go`, `internal/wakeon/wakeon_test.go` (confirm with `grep -rln "intercom.Open\|intercom\.Log\b" --include=*_test.go .`)

**Interfaces:**
- Produces: `func NewMemLog() *Log` (type name `Log` kept so `read.go`'s methods are unchanged); `Open` and file persistence removed; `Close() error` kept as a no-op only if a non-test caller still needs it (after Task 1 none should — then delete it).

- [ ] **Step 1: Conformance first** — in `store_conformance_test.go` rename `TestFileLogConformance` → `TestMemLogConformance` using `intercom.NewMemLog()`. Run `go test ./internal/intercom/ -run Conformance` → FAIL (undefined `NewMemLog`).

- [ ] **Step 2: Make `Log` in-memory** — in `log.go`:
- delete `maxLineBytes`, the `path`/`f`/`log` fields, `Open` (JSONL load + append handle), and the file write in `Append`;
- new doc + constructor:

```go
// Log is an in-memory, append-only message log implementing Store, for tests
// and tooling. Not for production: nothing is persisted — Jam's message log is
// intercompg (Postgres).
type Log struct {
	mu      sync.Mutex
	msgs    []Squawk
	nextSeq int64 // next Seq to assign, from 1
}

// NewMemLog returns an empty in-memory log.
func NewMemLog() *Log { return &Log{nextSeq: 1} }
```

- `Append` keeps `Prepare`, Seq assignment and the in-memory append; drop the JSON marshal/write.
- remove unused imports (`bufio`, `encoding/json`, `fmt`, `io`, `log/slog`, `os`).

- [ ] **Step 3: Swap tests** — `intercom.Open(path, nil)` (and `Open(path, log)`) returns `(*Log, error)`; replace with `intercom.NewMemLog()` (drop the error handling and any `defer l.Close()`). In `log_test.go` delete file-only tests (torn trailing line, malformed line skipped, reopen resumes Seq, file mode, content-type default on old lines) and list them in the report; keep in-memory behaviour tests (Append assigns ID/At/Seq, validation via Prepare). `read_test.go`'s `seed` helper → `NewMemLog()`.

- [ ] **Step 4: Verify**

```bash
grep -rn "intercom.Open\|intercom\.Open(" --include=*.go .   # expect: no output
go build ./... && go vet ./... && go test ./...
JAM_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54329/sess_utf8?sslmode=disable' go test -tags integration ./internal/intercom/...
```

- [ ] **Step 5: Commit**

```bash
git add -A internal/intercom internal/jam internal/relay internal/wakeon cmd/at-jam
git commit -m "refactor(intercom): in-memory Log for tests; drop the JSONL file log

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: `sessionevents` — in-memory store replaces the file store

**Files:**
- Create: `internal/jam/sessionevents/memstore.go`, `internal/jam/sessionevents/memstore_test.go`
- Delete: `internal/jam/sessionevents/filestore.go`, `filestore_test.go`
- Modify: tests using `OpenFileStore`: `internal/covemaster/client_test.go`, `internal/jam/attach/events_test.go`, `internal/jam/sessionevents/{retention,ingest,export}_test.go`, `internal/jam/adminui/session_test.go`

**Interfaces:**
- Produces: `func NewMemStore() *MemStore`; `*MemStore` implements `Store` (Append no-op on existing seq; HighWater; List seq order; Streams newest-first by FirstAt; DeleteBefore removes **streams** whose last ReceivedAt < t, returning events removed — same semantics the file store had).

- [ ] **Step 1: Failing conformance test** — `memstore_test.go`:

```go
package sessionevents_test

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionevents/sessioneventstest"
)

func TestMemStoreConformance(t *testing.T) {
	sessioneventstest.RunConformance(t, func(t *testing.T) sessionevents.Store { return sessionevents.NewMemStore() })
}

func TestMemStoreCopiesRaw(t *testing.T) {
	s := sessionevents.NewMemStore()
	raw := []byte(`{"a":1}`)
	s.Append(sessionevents.Event{ActorID: "w1", StreamID: sid, Seq: 1, Kind: sessionevents.KindEvent, Raw: raw})
	raw[2] = 'Z'
	got, _ := s.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
	if string(got[0].Raw) != `{"a":1}` {
		t.Fatalf("store aliased caller's slice: %s", got[0].Raw)
	}
}
```

(`sid` is defined in `ingest_test.go`.) Run → FAIL (undefined `NewMemStore`).

- [ ] **Step 2: Implement `memstore.go`**

```go
package sessionevents

import (
	"sort"
	"sync"
	"time"
)

// MemStore is an in-memory Store for tests and tooling. Not for production:
// nothing is persisted — Jam stores session events in Postgres (sessionpg).
type MemStore struct {
	mu      sync.Mutex
	streams map[streamKey][]Event // seq-ascending
}

var _ Store = (*MemStore)(nil)

func NewMemStore() *MemStore { return &MemStore{streams: map[streamKey][]Event{}} }

func clone(e Event) Event {
	e.Raw = append([]byte(nil), e.Raw...)
	return e
}

func (s *MemStore) Append(ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := streamKey{ev.ActorID, ev.StreamID}
	evs := s.streams[k]
	if n := len(evs); n > 0 && ev.Seq <= evs[n-1].Seq {
		return nil // existing (actor, stream, seq): no-op
	}
	s.streams[k] = append(evs, clone(ev))
	return nil
}

func (s *MemStore) HighWater(actorID, streamID string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs := s.streams[streamKey{actorID, streamID}]
	if len(evs) == 0 {
		return 0, nil
	}
	return evs[len(evs)-1].Seq, nil
}

func (s *MemStore) List(f Filter) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, e := range s.streams[streamKey{f.ActorID, f.StreamID}] {
		if e.Seq > f.AfterSeq {
			out = append(out, clone(e))
			if f.Limit > 0 && len(out) == f.Limit {
				break
			}
		}
	}
	return out, nil
}

func (s *MemStore) Streams(actorID string) ([]StreamInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []StreamInfo
	for k, evs := range s.streams {
		if k.actor != actorID || len(evs) == 0 {
			continue
		}
		out = append(out, StreamInfo{StreamID: k.stream, FirstAt: evs[0].ReceivedAt, LastAt: evs[len(evs)-1].ReceivedAt,
			LastSeq: evs[len(evs)-1].Seq, Events: len(evs)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstAt.After(out[j].FirstAt) })
	return out, nil
}

// DeleteBefore removes whole streams whose last event was received before t.
func (s *MemStore) DeleteBefore(t time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, evs := range s.streams {
		if len(evs) > 0 && evs[len(evs)-1].ReceivedAt.Before(t) {
			n += len(evs)
			delete(s.streams, k)
		}
	}
	return n, nil
}
```

(`streamKey{actor, stream}` already exists in `ingest.go`; if its field names differ, use them.) Note the old file store also enforced `ValidStreamID`/safe actor ids for path safety; Ingest validates stream ids before any store call, and MemStore has no paths, so no check is needed here.

- [ ] **Step 3: Swap and delete** — replace `sessionevents.OpenFileStore(dir)` (returns `(*FileStore, error)`) with `sessionevents.NewMemStore()` at every test call site; tests that reopened a store on the same dir to prove high-water survives restart (e.g. `TestIngestRecoversHighWaterAfterRestart`, `TestFileStoreHighWaterSurvivesReopen`) must keep their intent by **sharing one MemStore between two Ingest instances** (a Jam restart re-creates Ingest over the same durable store):

```go
func TestIngestRecoversHighWaterAfterRestart(t *testing.T) {
	st := sessionevents.NewMemStore()
	ing := sessionevents.NewIngest(st, sessionevents.NewHub(), nil)
	ing.Append("w1", sessionevents.Stamp{}, in(1, `{}`))
	ing.Append("w1", sessionevents.Stamp{}, in(2, `{}`))
	ing2 := sessionevents.NewIngest(st, sessionevents.NewHub(), nil) // restart: fresh in-memory hw cache
	for _, s := range []uint64{1, 2, 3} {
		ing2.Append("w1", sessionevents.Stamp{}, in(s, `{}`))
	}
	got, _ := st.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
	if len(got) != 3 {
		t.Fatalf("want 3 rows, no dups, no gap; got %+v", got)
	}
	for _, e := range got {
		if e.Kind == sessionevents.KindGap {
			t.Fatalf("false gap: %+v", e)
		}
	}
}
```

Delete `filestore.go` and `filestore_test.go` (torn-tail, over-long-line, unsafe-id path tests are file-specific — list them in the report). Keep `NopStore`.

- [ ] **Step 4: Verify**

```bash
grep -rn "OpenFileStore\|sessionevents.FileStore" --include=*.go .   # expect: no output
go build ./... && go vet ./... && go test ./...
JAM_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54329/sess_utf8?sslmode=disable' go test -tags integration ./internal/jam/sessionevents/...
```

- [ ] **Step 5: Commit**

```bash
git add -A internal/jam internal/covemaster
git commit -m "refactor(sessionevents): in-memory store for tests; drop the JSONL file store

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Docs and final verification

**Files:** `docs/usage/jam/serve.md`, `intercom.md`, `ui.md`, `intercom-ui.md`, `session-events.md`, `personal-sessions.md`, `standing-sessions.md`, `discord.md`, `backup.md`, `INDEX.md` (only if a row's "read when" mentions file stores), `docs/OVERVIEW.md`, `docs/DEVELOPMENT.md`.

Follow `/agent-data/skills/docs-author/SKILL.md` (single source of truth; serve.md owns the serve-config keys) and finish with `/agent-data/skills/docs-audit/SKILL.md` (run on `docs/usage` with `--index jam/INDEX.md`; fix findings this change introduced).

- [ ] **Step 1: serve.md** — sample config uses `store-postgres` (no `store:`); key table: `store-postgres` **required**; a "Removed keys" note for `store`, `intercom-log`, `session-events-dir` (hard error; migrate via export/import → backup.md; history not migrated); new `state-dir` row (default `$XDG_STATE_HOME/at-jam` else `~/.local/state/at-jam`; holds relay-cursors/markers/receipts; upgraders move those three files from their old store directory or point `state-dir` there; missing files = relays re-seed). Replace the "no data migration / file fallback" text with a pointer to backup.md. Remove file-backend allocator-fallback wording that only applied to the file store (the nil-ledger mode itself is a follow-up — say "the allocator ledger is always available" only if true after Task 1; otherwise leave allocator text untouched).
- [ ] **Step 2: other leaves** — remove/replace every statement about the JSONL message log, `intercom-log`, `session-events-dir`, "on the file store …" (`intercom.md` 17,69-77,139,161; `ui.md` 36,143,149,160,164; `intercom-ui.md` 71,94; `session-events.md` 52,57 + any `session-events-dir` / torn-tail / 8 MiB / OS-page-cache text; `personal-sessions.md` 92,107,149; `standing-sessions.md` 16,114; `discord.md` 73). Intercom and session events are always on with Postgres. `backup.md`: the file→Postgres path is now the only upgrade path for a file Jam — state it plainly.
- [ ] **Step 3: OVERVIEW.md / DEVELOPMENT.md** — Jam is Postgres-only; hermetic tests use the in-memory stores (`jam.NewMemStore`, `intercom.NewMemLog`, `sessionevents.NewMemStore`); integration tests need `JAM_TEST_POSTGRES_DSN`.
- [ ] **Step 4: Verify**

```bash
grep -rn "intercom-log\|session-events-dir\|NewFileStore\|file store" docs/usage docs/OVERVIEW.md docs/DEVELOPMENT.md   # only intentional "removed key" mentions remain
just lint && just test && go vet -tags integration ./...
JAM_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54329/sess_utf8?sslmode=disable' go test -tags integration ./internal/jam/... ./internal/intercom/...
```

- [ ] **Step 5: Commit**

```bash
git add docs/
git commit -m "docs(jam): Postgres-only storage — required store-postgres, removed keys, state-dir

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
