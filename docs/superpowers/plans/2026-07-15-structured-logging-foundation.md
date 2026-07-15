# Structured Logging — Foundation (increments 1 & 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish `slog`-based structured logging (`internal/logging`) with an attended/unattended output model, wire it through the `at-cove` entry point and the dispatch scheduler with run-id/step correlation, and make the work path fail closed with attribution when the agent bearer is unresolved.

**Architecture:** A self-contained `internal/logging` package owns all `slog` handler/mode wiring behind a tee handler (text→stderr, JSON→file in attended; JSON→stderr in unattended). The logger flows via `context.Context`. The scheduler mints a per-dispatch run ID and tags records with `run`/`issue`/`class`/`step`. User-facing errors use a dual-output helper.

**Tech Stack:** Go stdlib only — `log/slog`, `context`, `os` (TTY detection via `os.ModeCharDevice`). No third-party dependency.

**Scope note:** This plan covers spec increments **#1** and **#3** only. It writes a **process-level** log file (`<kitDir>/.state/logs/at-cove-<timestamp>.jsonl`) with per-run records correlated by the `run` attribute; the **per-run file split** and **VM-side capture/merge** (spec §5, §7 / increments #2, #4–#6) are a deliberate follow-on plan. Design spec: [`docs/superpowers/specs/2026-07-15-structured-logging-design.md`](../specs/2026-07-15-structured-logging-design.md).

## Global Constraints

- **Go stdlib only** — no new dependency, no new `allowed-domains` entry. TTY detection uses `os.File.Stat()` + `os.ModeCharDevice`, not `golang.org/x/term`.
- **Secrets never hit logs, at any level** — no code path logs resolved secret values or the VM env map. Enforced by a dedicated test.
- **stdout stays clean** — all logs go to stderr (or the file); stdout carries only CLI data output.
- **Hermetic tests, TDD** — drive buffers/fakes; failing test first. Run via `just test`. No new test needs Docker/network/live VM.
- **Frequent commits** — one commit per task (after its tests pass).
- **Module:** `github.com/aethons-tools/cove`. Build/test: `just build`, `just test`.

---

### Task 1: Tee + user-shown-filter `slog.Handler` primitives

**Files:**
- Create: `internal/logging/handler.go`
- Test: `internal/logging/handler_test.go`

**Interfaces:**
- Produces:
  - `func newMulti(hs ...slog.Handler) slog.Handler` — fan-out handler: `Enabled` is true if any child is; `Handle` forwards to every child whose `Enabled` is true; `WithAttrs`/`WithGroup` map over children.
  - `const userShownKey = "user_shown"` — attribute key marking a record already shown to the human as a friendly line.
  - `func skipUserShown(h slog.Handler) slog.Handler` — wraps a handler so its `Handle` drops any record carrying `userShown=true` (used for the attended **stderr text** handler, so a `UserError` twin isn't printed twice). Other handlers keep the record.

- [ ] **Step 1: Write the failing test**

```go
package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestMultiFansOutToAllChildren(t *testing.T) {
	var a, b bytes.Buffer
	h := newMulti(slog.NewTextHandler(&a, nil), slog.NewJSONHandler(&b, nil))
	slog.New(h).Info("hello", "k", "v")
	if !strings.Contains(a.String(), "hello") || !strings.Contains(b.String(), `"msg":"hello"`) {
		t.Fatalf("both children should receive the record; a=%q b=%q", a.String(), b.String())
	}
}

func TestSkipUserShownDropsMarkedRecords(t *testing.T) {
	var text, json bytes.Buffer
	stderr := skipUserShown(slog.NewTextHandler(&text, nil))
	file := slog.NewJSONHandler(&json, nil)
	log := slog.New(newMulti(stderr, file))

	log.LogAttrs(context.Background(), slog.LevelError, "boom", slog.Bool(userShownKey, true))
	if text.Len() != 0 {
		t.Fatalf("stderr text handler must skip user-shown records; got %q", text.String())
	}
	if !strings.Contains(json.String(), `"msg":"boom"`) {
		t.Fatalf("file handler must keep user-shown records; got %q", json.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logging/ -run 'TestMulti|TestSkipUserShown' -v`
Expected: FAIL — `undefined: newMulti` / `undefined: skipUserShown`.

- [ ] **Step 3: Write minimal implementation**

```go
package logging

import (
	"context"
	"log/slog"
)

const userShownKey = "user_shown"

type multiHandler struct{ hs []slog.Handler }

func newMulti(hs ...slog.Handler) slog.Handler { return multiHandler{hs: hs} }

func (m multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m.hs {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range m.hs {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m multiHandler) WithAttrs(as []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(m.hs))
	for i, h := range m.hs {
		out[i] = h.WithAttrs(as)
	}
	return multiHandler{hs: out}
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(m.hs))
	for i, h := range m.hs {
		out[i] = h.WithGroup(name)
	}
	return multiHandler{hs: out}
}

type skipHandler struct{ slog.Handler }

func skipUserShown(h slog.Handler) slog.Handler { return skipHandler{Handler: h} }

func (s skipHandler) Handle(ctx context.Context, r slog.Record) error {
	shown := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == userShownKey && a.Value.Bool() {
			shown = true
			return false
		}
		return true
	})
	if shown {
		return nil
	}
	return s.Handler.Handle(ctx, r)
}

func (s skipHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return skipHandler{Handler: s.Handler.WithAttrs(as)}
}
func (s skipHandler) WithGroup(name string) slog.Handler {
	return skipHandler{Handler: s.Handler.WithGroup(name)}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/logging/ -run 'TestMulti|TestSkipUserShown' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logging/handler.go internal/logging/handler_test.go
git commit -m "feat(logging): tee + user-shown-filter slog handler primitives"
```

---

### Task 2: `logging.New` — mode resolution + logger construction

**Files:**
- Create: `internal/logging/logging.go`
- Test: `internal/logging/logging_test.go`

**Interfaces:**
- Consumes: `newMulti`, `skipUserShown` (Task 1).
- Produces:
  - `type Mode int` with `const ( Auto Mode = iota; Attended; Unattended )`.
  - `type Options struct { Mode Mode; Stderr io.Writer; FilePath string; Level slog.Level }` — `Level` is the stderr level (default `slog.LevelInfo` when zero-value is used, mapped explicitly by the caller); `FilePath` empty disables the file.
  - `func New(o Options) (*Logger, error)` where `type Logger struct { *slog.Logger; sink *slog.Logger; human io.Writer; mode Mode; closer io.Closer }`. `New` resolves `Auto` via `isTerminal(o.Stderr)`; builds handlers per §4 of the spec; returns a `*Logger` whose embedded `*slog.Logger` is the tee (normal logging) and whose `sink` is the structured-only logger (file in attended, stderr in unattended) used by `UserError` (Task 4).
  - `func (l *Logger) Close() error` — closes the file if open (no-op otherwise).
  - `func isTerminal(w io.Writer) bool` — true iff `w` is an `*os.File` whose `Stat().Mode()&os.ModeCharDevice != 0`.

- [ ] **Step 1: Write the failing test**

```go
package logging

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnattendedWritesJSONToStderrNoFile(t *testing.T) {
	var errb bytes.Buffer
	dir := t.TempDir()
	fp := filepath.Join(dir, "x.jsonl")
	lg, err := New(Options{Mode: Unattended, Stderr: &errb, FilePath: fp, Level: slog.LevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()
	lg.Info("hi", "k", 1)
	if !strings.Contains(errb.String(), `"msg":"hi"`) {
		t.Fatalf("unattended stderr should be JSON; got %q", errb.String())
	}
	if _, err := os.Stat(fp); !os.IsNotExist(err) {
		t.Fatalf("unattended must not create the log file")
	}
}

func TestAttendedTextToStderrJSONToFile(t *testing.T) {
	var errb bytes.Buffer
	dir := t.TempDir()
	fp := filepath.Join(dir, "x.jsonl")
	lg, err := New(Options{Mode: Attended, Stderr: &errb, FilePath: fp, Level: slog.LevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	lg.Info("hi", "k", 1)
	lg.Close()
	if strings.Contains(errb.String(), "{") {
		t.Fatalf("attended stderr should be human text, not JSON; got %q", errb.String())
	}
	b, _ := os.ReadFile(fp)
	if !strings.Contains(string(b), `"msg":"hi"`) {
		t.Fatalf("attended must write JSON to the file; got %q", string(b))
	}
}

func TestAttendedFileCapturesDebugWhileStderrDoesNot(t *testing.T) {
	var errb bytes.Buffer
	dir := t.TempDir()
	fp := filepath.Join(dir, "x.jsonl")
	lg, _ := New(Options{Mode: Attended, Stderr: &errb, FilePath: fp, Level: slog.LevelInfo})
	lg.Debug("verbose")
	lg.Close()
	if strings.Contains(errb.String(), "verbose") {
		t.Fatalf("stderr at info+ must not show debug; got %q", errb.String())
	}
	b, _ := os.ReadFile(fp)
	if !strings.Contains(string(b), "verbose") {
		t.Fatalf("file at debug+ must capture debug; got %q", string(b))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logging/ -run TestAttended -v`
Expected: FAIL — `undefined: New` / `undefined: Options`.

- [ ] **Step 3: Write minimal implementation**

```go
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

type Mode int

const (
	Auto Mode = iota
	Attended
	Unattended
)

type Options struct {
	Mode     Mode
	Stderr   io.Writer
	FilePath string
	Level    slog.Level
}

type Logger struct {
	*slog.Logger
	sink   *slog.Logger
	human  io.Writer
	mode   Mode
	closer io.Closer
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func New(o Options) (*Logger, error) {
	mode := o.Mode
	if mode == Auto {
		if isTerminal(o.Stderr) {
			mode = Attended
		} else {
			mode = Unattended
		}
	}

	if mode == Unattended {
		h := slog.NewJSONHandler(o.Stderr, &slog.HandlerOptions{Level: o.Level})
		lg := slog.New(h)
		return &Logger{Logger: lg, sink: lg, human: o.Stderr, mode: mode}, nil
	}

	// Attended: text→stderr @ Level (skipping user-shown twins), JSON→file @ Debug.
	stderrH := skipUserShown(slog.NewTextHandler(o.Stderr, &slog.HandlerOptions{Level: o.Level}))
	handlers := []slog.Handler{stderrH}
	var closer io.Closer
	var sink *slog.Logger
	if o.FilePath != "" {
		if err := os.MkdirAll(filepath.Dir(o.FilePath), 0o755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(o.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		fileH := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
		handlers = append(handlers, fileH)
		closer = f
		sink = slog.New(fileH) // structured-only sink: file (never re-hits stderr)
	}
	lg := slog.New(newMulti(handlers...))
	if sink == nil {
		sink = lg // no file: sink falls back to the tee
	}
	return &Logger{Logger: lg, sink: sink, human: o.Stderr, mode: mode, closer: closer}, nil
}

func (l *Logger) Close() error {
	if l.closer != nil {
		return l.closer.Close()
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/logging/ -run 'TestUnattended|TestAttended' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logging/logging.go internal/logging/logging_test.go
git commit -m "feat(logging): New() with attended/unattended mode resolution"
```

---

### Task 3: Context propagation — `Into` / `From`

**Files:**
- Create: `internal/logging/context.go`
- Test: `internal/logging/context_test.go`

**Interfaces:**
- Consumes: `*Logger` (Task 2).
- Produces:
  - `func Into(ctx context.Context, l *Logger) context.Context`.
  - `func From(ctx context.Context) *Logger` — returns the stored `*Logger`, or a discard logger (`New(Options{Mode: Unattended, Stderr: io.Discard, Level: slog.LevelInfo})`) if none is set, so callers never nil-panic.

- [ ] **Step 1: Write the failing test**

```go
package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestIntoFromRoundTrips(t *testing.T) {
	var b bytes.Buffer
	lg, _ := New(Options{Mode: Unattended, Stderr: &b, Level: slog.LevelInfo})
	ctx := Into(context.Background(), lg)
	From(ctx).Info("via-context")
	if !strings.Contains(b.String(), "via-context") {
		t.Fatalf("From(ctx) should return the stored logger; got %q", b.String())
	}
}

func TestFromWithoutLoggerDoesNotPanic(t *testing.T) {
	From(context.Background()).Info("no-panic") // discard logger; must not panic
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logging/ -run 'TestInto|TestFrom' -v`
Expected: FAIL — `undefined: Into` / `undefined: From`.

- [ ] **Step 3: Write minimal implementation**

```go
package logging

import (
	"context"
	"io"
	"log/slog"
)

type ctxKey struct{}

func Into(ctx context.Context, l *Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

func From(ctx context.Context) *Logger {
	if l, ok := ctx.Value(ctxKey{}).(*Logger); ok && l != nil {
		return l
	}
	l, _ := New(Options{Mode: Unattended, Stderr: io.Discard, Level: slog.LevelInfo})
	return l
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/logging/ -run 'TestInto|TestFrom' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logging/context.go internal/logging/context_test.go
git commit -m "feat(logging): context propagation Into/From with discard fallback"
```

---

### Task 4: Dual-output `UserError` + secret scrubber

**Files:**
- Create: `internal/logging/usererror.go`, `internal/logging/scrub.go`
- Test: `internal/logging/usererror_test.go`, `internal/logging/scrub_test.go`

**Interfaces:**
- Consumes: `*Logger` (Task 2), `userShownKey` (Task 1).
- Produces:
  - `func (l *Logger) UserError(ctx context.Context, err error, attrs ...slog.Attr)` — **attended:** writes `at-cove: <err>\n` to `l.human`, then logs an `Error` record to the tee with the given attrs **plus** `slog.Bool(userShownKey, true)` (so the file keeps it, the stderr text handler drops the twin). **unattended:** logs an `Error` record to `l.sink` (JSON→stderr) with the attrs; no human line.
  - `func Scrub(s string, secrets ...string) string` — replaces every non-empty secret value in `s` with `«redacted»`. Used as the debug-raw-text backstop (consumed by the follow-on VM-capture plan; introduced here so the invariant test can exercise it).

- [ ] **Step 1: Write the failing tests**

```go
package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserErrorAttendedHumanLinePlusFileRecordNoStderrTwin(t *testing.T) {
	var errb bytes.Buffer
	dir := t.TempDir()
	fp := filepath.Join(dir, "x.jsonl")
	lg, _ := New(Options{Mode: Attended, Stderr: &errb, FilePath: fp, Level: slog.LevelInfo})
	lg.UserError(context.Background(), errors.New("bad token"), slog.String("step", "secrets"))
	lg.Close()

	if !strings.Contains(errb.String(), "at-cove: bad token") {
		t.Fatalf("human line missing from stderr; got %q", errb.String())
	}
	if strings.Count(errb.String(), "bad token") != 1 {
		t.Fatalf("error must appear on stderr exactly once (no structured twin); got %q", errb.String())
	}
	b, _ := os.ReadFile(fp)
	if !strings.Contains(string(b), `"step":"secrets"`) || !strings.Contains(string(b), `"level":"ERROR"`) {
		t.Fatalf("structured error record missing from file; got %q", string(b))
	}
}

func TestUserErrorUnattendedStructuredOnlyNoHumanLine(t *testing.T) {
	var errb bytes.Buffer
	lg, _ := New(Options{Mode: Unattended, Stderr: &errb, Level: slog.LevelInfo})
	lg.UserError(context.Background(), errors.New("bad token"), slog.String("step", "secrets"))
	if strings.Contains(errb.String(), "at-cove: bad token") {
		t.Fatalf("unattended must not print a human line; got %q", errb.String())
	}
	if !strings.Contains(errb.String(), `"step":"secrets"`) {
		t.Fatalf("unattended must emit a structured record to stderr; got %q", errb.String())
	}
}

func TestScrubMasksSecretValues(t *testing.T) {
	got := Scrub("Authorization: Bearer sk-ant-oat01-XYZ done", "sk-ant-oat01-XYZ", "")
	if strings.Contains(got, "sk-ant-oat01-XYZ") {
		t.Fatalf("secret value leaked: %q", got)
	}
	if !strings.Contains(got, "«redacted»") {
		t.Fatalf("expected redaction marker; got %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/logging/ -run 'TestUserError|TestScrub' -v`
Expected: FAIL — `undefined: (*Logger).UserError` / `undefined: Scrub`.

- [ ] **Step 3: Write minimal implementation**

`internal/logging/usererror.go`:

```go
package logging

import (
	"context"
	"fmt"
	"log/slog"
)

func (l *Logger) UserError(ctx context.Context, err error, attrs ...slog.Attr) {
	if l.mode == Unattended {
		l.sink.LogAttrs(ctx, slog.LevelError, err.Error(), attrs...)
		return
	}
	fmt.Fprintf(l.human, "at-cove: %s\n", err.Error())
	l.Logger.LogAttrs(ctx, slog.LevelError, err.Error(), append(attrs, slog.Bool(userShownKey, true))...)
}
```

`internal/logging/scrub.go`:

```go
package logging

import "strings"

func Scrub(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		s = strings.ReplaceAll(s, sec, "«redacted»")
	}
	return s
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logging/ -run 'TestUserError|TestScrub' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logging/usererror.go internal/logging/scrub.go internal/logging/usererror_test.go internal/logging/scrub_test.go
git commit -m "feat(logging): dual-output UserError + secret scrubber"
```

---

### Task 5: Wire `logging.New` into the `at-cove` entry point

**Files:**
- Modify: `internal/cli/cli.go:14` (extend `Globals`)
- Modify: `cmd/at-cove/main.go` (parse global log flags; construct logger; put into a base context)
- Test: `internal/cli/cli_test.go` (global flag parsing)

**Interfaces:**
- Consumes: `logging.New`, `logging.Into`, `logging.Mode` (Tasks 2–3).
- Produces:
  - `cli.Globals` gains: `LogMode string` (`""|"attended"|"unattended"`, default `""` → `logging.Auto`), `LogLevel string` (default `"info"`), `NoLogFile bool`.
  - A helper `func logModeFrom(s string) logging.Mode` and `func logLevelFrom(s string) slog.Level` in `cmd/at-cove/main.go`.
  - The dispatch and work commands obtain their logger from context via `logging.From(ctx)` (used in Tasks 6–7).

**Note on `cli.Globals` parsing:** follow the existing global-flag wiring in `internal/cli/cli.go` (where `DryRun` is parsed). Add `--log-mode`, `--log-level`, `--no-log-file` alongside it, and env fallbacks `AT_LOG_MODE` / `AT_LOG_LEVEL` resolved in `cmd/at-cove/main.go` before constructing the logger.

- [ ] **Step 1: Write the failing test** (global flag parsing — mirror the existing `DryRun` test in `internal/cli/cli_test.go`)

```go
func TestGlobalsParseLogFlags(t *testing.T) {
	g, rest := ParseGlobals([]string{"--log-mode", "unattended", "--log-level", "debug", "--no-log-file", "dispatch"})
	if g.LogMode != "unattended" || g.LogLevel != "debug" || !g.NoLogFile {
		t.Fatalf("globals not parsed: %+v", g)
	}
	if len(rest) != 1 || rest[0] != "dispatch" {
		t.Fatalf("remaining args wrong: %v", rest)
	}
}
```

(If the existing globals parser has a different name/signature than `ParseGlobals`, match it — read `internal/cli/cli.go` first and adapt the test to the real entry point. The assertion content stays the same.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestGlobalsParseLogFlags -v`
Expected: FAIL — unknown flags / missing `Globals` fields.

- [ ] **Step 3: Implement**

In `internal/cli/cli.go`, extend `Globals` and its parser:

```go
type Globals struct {
	DryRun    bool
	LogMode   string // "", "attended", "unattended"
	LogLevel  string // "debug","info","warn","error"; default "info"
	NoLogFile bool
}
```

Register `--log-mode` (string), `--log-level` (string, default `"info"`), `--no-log-file` (bool) in the same place `--dry-run` is registered.

In `cmd/at-cove/main.go`, add the mappers and env fallback, and construct the logger once near the top of the dispatch/work command bodies (exact file path in Tasks 6–7):

```go
func logModeFrom(s string) logging.Mode {
	switch s {
	case "attended":
		return logging.Attended
	case "unattended":
		return logging.Unattended
	default:
		return logging.Auto
	}
}

func logLevelFrom(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// envOr returns os.Getenv(key) when g-flag is empty.
func envOr(flag, key string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv(key)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run TestGlobalsParseLogFlags -v`
Expected: PASS. Then `just build` to confirm the binary still compiles.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go cmd/at-cove/main.go
git commit -m "feat(logging): global --log-mode/--log-level/--no-log-file flags + mappers"
```

---

### Task 6: Migrate the dispatch scheduler to the context logger with run-id/step correlation

**Files:**
- Modify: `internal/dispatch/scheduler/engine.go` (replace `*log.Logger` field with `*logging.Logger`; add run-id + `step` attrs)
- Modify: `cmd/at-cove/main.go:783-791` (construct `logging.New`, `logging.Into(ctx, …)`, pass to `scheduler.New`)
- Modify: `internal/dispatch/scheduler/engine_test.go` (construct engine with a buffer-backed `*logging.Logger`)
- Modify: `internal/dispatch/scheduler/fakes_test.go` if it constructs the logger

**Interfaces:**
- Consumes: `logging.New`, `logging.From`, `logging.Into`, `*logging.Logger` (Tasks 2–5).
- Produces:
  - `func New(cfg kit.Config, kitDir string, t Tracker, e Executor, lg *logging.Logger) *Engine` — signature changes from `*log.Logger` to `*logging.Logger`.
  - Per-dispatch correlation: in `handle`, derive a run id `run_<identifier>_<rand4>` and a per-dispatch child logger carrying `slog.String("run", …), slog.String("issue", iss.Identifier), slog.String("class", iss.Class)`; each phase logs with `slog.String("step", "<phase>")`. Phases: `claim`, `brief`, `secrets`, `dispatch`, `broker` (host-side subset; the VM phases arrive in the follow-on plan).

- [ ] **Step 1: Write the failing test**

Add to `internal/dispatch/scheduler/engine_test.go`:

```go
func TestHandleLogsRunAndStepAttrs(t *testing.T) {
	var buf bytes.Buffer
	lg, _ := logging.New(logging.Options{Mode: logging.Unattended, Stderr: &buf, Level: slog.LevelInfo})
	tr := newFakeTracker()
	ex := &fakeExecutor{OutJSON: `{"status":{"ok":{}}}`}
	eng := New(testConfig(), "/kits/implement", tr, ex, lg)
	eng.handle(context.Background(), Issue{ID: "id1", Identifier: "AET-9", Title: "X", Class: "implement"})

	s := buf.String()
	if !strings.Contains(s, `"issue":"AET-9"`) || !strings.Contains(s, `"class":"implement"`) {
		t.Fatalf("expected issue/class attrs on dispatch logs; got %q", s)
	}
	if !strings.Contains(s, `"run":"run_AET-9`) {
		t.Fatalf("expected a run id attr; got %q", s)
	}
	if !strings.Contains(s, `"step":"dispatch"`) {
		t.Fatalf("expected a step attr on the exec log; got %q", s)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dispatch/scheduler/ -run TestHandleLogsRunAndStepAttrs -v`
Expected: FAIL — `New` still wants `*log.Logger`; no run/step attrs.

- [ ] **Step 3: Implement**

In `engine.go`: change the field `log *log.Logger` → `log *logging.Logger`; update `New`'s parameter type. Add a helper to mint the run id and derive the per-dispatch logger:

```go
func runID(identifier string) string {
	var b [2]byte
	_, _ = rand.Read(b[:]) // crypto/rand
	return "run_" + identifier + "_" + hex.EncodeToString(b[:])
}
```

At the top of `handle`, build the dispatch-scoped logger and use it (replacing the `e.log.Printf(...)` sites with structured calls carrying `step`):

```go
func (e *Engine) handle(ctx context.Context, iss Issue) {
	dl := e.log.With(
		slog.String("run", runID(iss.Identifier)),
		slog.String("issue", iss.Identifier),
		slog.String("class", iss.Class),
	)
	// ... on claim failure:
	//   dl.Error("claim failed", slog.String("step", "claim"), slog.Any("err", err))
	// ... comments:
	//   dl.Warn("no comments; continuing", slog.String("step", "brief"), slog.Any("err", err))
	// ... exec line:
	dl.Info("dispatching work", slog.String("step", "dispatch"), slog.String("argv", strings.Join(argv, " ")))
	// ... etc.
}
```

Add `func (l *Logger) With(attrs ...slog.Attr) *Logger` to `internal/logging/logging.go` so the dispatch-scoped child preserves mode/human/sink:

```go
func (l *Logger) With(attrs ...slog.Attr) *Logger {
	as := make([]any, 0, len(attrs))
	for _, a := range attrs {
		as = append(as, a)
	}
	child := *l
	child.Logger = l.Logger.With(as...)
	child.sink = l.sink.With(as...)
	return &child
}
```

(Write the failing `With` sub-test first if implementing TDD-strict: assert a `With`-added attr appears on a subsequent record.)

In `cmd/at-cove/main.go` dispatch body, replace the `log.New(...)` construction:

```go
logFile := ""
if !g.NoLogFile {
	logFile = filepath.Join(state.Dir(kitDir), "logs", "at-cove-dispatch.jsonl")
}
lg, err := logging.New(logging.Options{
	Mode:     logModeFrom(envOr(g.LogMode, "AT_LOG_MODE")),
	Stderr:   stderr,
	FilePath: logFile,
	Level:    logLevelFrom(envOr(g.LogLevel, "AT_LOG_LEVEL")),
})
if err != nil {
	fmt.Fprintf(stderr, "at-cove: %v\n", err)
	return 1
}
defer lg.Close()
ctx = logging.Into(ctx, lg)
engine := scheduler.New(cfg, kitDir, tracker, dexec.New(), lg)
lg.Info("scheduler started", slog.String("poll", cfg.Tracker.Linear.PollInterval))
```

Update `engine_test.go` / `fakes_test.go` construction sites to pass a `*logging.Logger` (buffer-backed) instead of `log.New(...)`. Update the existing `TestHandleLogsExecArgv` to assert on the structured field (`"argv":"at-cove work --kit-dir /kits/implement`) rather than the old prefix text.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dispatch/scheduler/ -v`
Expected: PASS (including the updated argv-log test). Then `just build`.

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/scheduler/ cmd/at-cove/main.go internal/logging/logging.go
git commit -m "feat(logging): scheduler emits run-id/step-correlated structured logs"
```

---

### Task 7: Fail closed with attribution when the agent bearer is unresolved

**Files:**
- Modify: `cmd/at-cove/main.go:664-683` (the work-path secret resolution block)
- Test: `cmd/at-cove/main_test.go` (or the nearest existing `doWork` test file)

**Interfaces:**
- Consumes: `logging.From` / the work-path logger (Task 5–6), `cfg.Secrets`, the resolver `unresolved` slice already computed at `main.go:676`.
- Produces:
  - A constant `const agentBearerSecret = "ANTHROPIC_AUTH_TOKEN"`.
  - Work-path behavior: if `agentBearerSecret` is among `unresolved` (or absent from `cfg.Secrets` entirely), the run **aborts before building the VM** with an attributed error, instead of warn-and-continue.

- [ ] **Step 1: Write the failing test**

Model it on the existing `doWork` tests (which drive `runner.Fake` and a temp kit dir). Assert that a kit declaring `ANTHROPIC_AUTH_TOKEN` with no supply causes `doWork` to return non-zero **without** invoking the backend/VM, and that stderr names the secret and kit:

```go
func TestWorkFailsClosedWhenAgentBearerUnresolved(t *testing.T) {
	kitDir := writeMinimalWorkerKit(t) // declares secrets: ANTHROPIC_AUTH_TOKEN: {}, no supply wired
	var stderr bytes.Buffer
	fake := runner.NewFake() // records calls; must see NO ssh/VM step
	code := doWork([]string{"--kit-dir", kitDir, "--in", writeTaskJSON(t), "--out", tmpOut(t)}, fake, false, io.Discard, &stderr)

	if code == 0 {
		t.Fatalf("expected non-zero exit when the agent bearer is unresolved")
	}
	if !strings.Contains(stderr.String(), "ANTHROPIC_AUTH_TOKEN") || !strings.Contains(stderr.String(), "fail") {
		t.Fatalf("error must name the unresolved bearer and be a fail-closed message; got %q", stderr.String())
	}
	if fake.Ran("ssh") {
		t.Fatalf("must abort before any VM/SSH step")
	}
}
```

(Adapt `writeMinimalWorkerKit`, `writeTaskJSON`, `tmpOut`, and `runner.Fake`'s call-inspection to the real helpers in the existing `cmd/at-cove` tests — read them first. The behavioral assertions stay as written.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-cove/ -run TestWorkFailsClosedWhenAgentBearerUnresolved -v`
Expected: FAIL — today the work path warns and continues, so it proceeds toward the VM (or fails later for a different reason).

- [ ] **Step 3: Implement**

In `cmd/at-cove/main.go`, after the `unresolved` loop at `:681-683`, before the git-token `planRequired` call, add the fail-closed gate:

```go
const agentBearerSecret = "ANTHROPIC_AUTH_TOKEN"

// The dispatched agent cannot authenticate without its bearer; a keyless
// worker is a guaranteed 401. Fail closed with attribution (like the
// git/tracker well-known secrets) rather than launch a doomed VM.
bearerUnresolved := false
if _, declared := cfg.Secrets[agentBearerSecret]; !declared {
	bearerUnresolved = true
} else {
	for _, name := range unresolved {
		if name == agentBearerSecret {
			bearerUnresolved = true
			break
		}
	}
}
if bearerUnresolved {
	err := fmt.Errorf("agent bearer %s is unresolved for kit %q — the worker would fail closed with a 401; wire it under kits: %q in %s (or secrets.local.yml)",
		agentBearerSecret, cfg.Name, cfg.Name, secretsPath)
	logging.From(ctx).UserError(ctx, err, slog.String("step", "secrets"), slog.String("secret", agentBearerSecret), slog.String("kit", cfg.Name))
	return 1
}
```

Ensure the work-path body has a `ctx` in scope carrying the logger (construct it in `doWork` the same way Task 6 does for dispatch; if `doWork` has no `ctx`, add `ctx := logging.Into(context.Background(), lg)` after building `lg`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/at-cove/ -run TestWorkFailsClosedWhenAgentBearerUnresolved -v`
Expected: PASS. Then full suite: `just test`.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-cove/main.go cmd/at-cove/main_test.go
git commit -m "feat(dispatch): fail closed with attribution when ANTHROPIC_AUTH_TOKEN is unresolved"
```

---

## Final verification (after Task 7)

- [ ] `just test` — all hermetic tests green.
- [ ] `just build` — `at-cove` / `at-task` / `at-mint` build.
- [ ] `just lint` — `go vet` clean; changed files `gofmt`-clean (ignore pre-existing module-cache `gofmt` noise).
- [ ] Manual smoke: `at-cove dispatch --kit-dir <kit>` in a terminal shows human text on stderr and writes `<kit>/.state/logs/at-cove-dispatch.jsonl`; piping it (`… | cat`) auto-switches to JSON on stderr.
- [ ] Secret-safety spot check: `grep -r ANTHROPIC_AUTH_TOKEN <kit>/.state/logs/*.jsonl` shows the *name* in the fail-closed record but never a token *value*.

## Spec Coverage Note

This plan implements spec increments **#1** (Task 1–5: `internal/logging` core, modes, tee, context, UserError, scrubber, CLI wiring) and **#3** (Task 6: scheduler adoption + correlation; Task 7: fail-closed-with-attribution — the motivating-bug fix). Spec increment **#2** (runner writer injection) and **#4–#6** (VM capture/demux/merge, `at-task` `slog` adoption, remaining-site migration + docs) are the deliberate follow-on plan noted in the scope header.
