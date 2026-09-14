# harbor message-log admin UI — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Present harbor's `internal/msglog` Log as a read-only, filterable, newest-first table at `/ui/messages` in the admin UI.

**Architecture:** `at-harbor serve` opens the Log (a new optional `message-log:` config path) and passes a read-only `MessageReader` (just `List`) into `adminui.Handler`. A new server-rendered `messages.html` page renders the envelopes; `project`/`since`/`until` filter through `msglog.Filter`, `participant`/`q` filter in the handler. No writers, no adapters, no mutation route — those stay deferred to COV-171.

**Tech Stack:** Go stdlib `net/http` + `html/template`, htmx (already vendored, unused by this page), `gopkg.in/yaml.v3` for config. Hermetic `httptest` tests against a real `*msglog.Log` in a temp dir.

**Spec:** `docs/superpowers/specs/2026-09-14-harbor-message-log-ui.md`

## Global Constraints

- **`internal/msglog` stays stdlib-only.** Do not add imports to it or modify it. The dependency direction is `harbor → msglog`, never the reverse.
- **Read-only.** No `POST`/mutation route for messages; the page is pure `GET`. No new auth code — it rides the existing `/ui/` gate.
- **No secret exposure.** Render only `From`/`To`/`Body`/`At`/`Project`. Bodies are auto-escaped by `html/template` (never use `template.HTML` on a body).
- **TDD.** Write the failing test first, watch it fail, implement minimally, watch it pass, commit.
- **Hermetic tests** — drive a real `msglog.Log` under `t.TempDir()`; no network, Docker, or live VM. Run with `just test`.
- **Commit trailer — use verbatim on every commit** (do not substitute your own model name):

  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

- Branch is already `feat/harbor-message-log-ui` (the spec is committed there).

## File Structure

- `cmd/at-harbor/config.go` — add `MessageLog` field to `serveConfig` (Task 1).
- `cmd/at-harbor/config_test.go` — parse test + known-key guard (Task 1).
- `internal/harbor/adminui/messages.go` — **new**: `MessageReader` interface, `/ui/messages` handler, filter/row logic (Tasks 2–3).
- `internal/harbor/adminui/templates/messages.html` — **new**: the page (Task 2).
- `internal/harbor/adminui/templates/layout.html` — add the Messages nav link (Task 2).
- `internal/harbor/adminui/adminui.go` — add `msgs MessageReader` param to `Handler`, register the route + page template (Task 2).
- `internal/harbor/adminui/messages_test.go` — **new**: render, states, filters (Tasks 2–3).
- `internal/harbor/adminui/*_test.go` (existing) — pass trailing `nil` to `Handler` (Task 2).
- `cmd/at-harbor/main.go` — open the Log when configured, pass the reader in (Task 4).
- `docs/usage/harbor/ui.md`, `docs/usage/harbor/serve.md` — document the view + config field (Task 5).

---

### Task 1: `message-log:` serve config field

**Files:**
- Modify: `cmd/at-harbor/config.go` (add field to `serveConfig`, ~line 44 near `Store`)
- Test: `cmd/at-harbor/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `serveConfig.MessageLog string` (yaml key `message-log`) — read by Task 4.

- [ ] **Step 1: Write the failing test**

Add to `cmd/at-harbor/config_test.go` (add `"gopkg.in/yaml.v3"` to its imports):

```go
func TestServeConfigMessageLog(t *testing.T) {
	var c serveConfig
	if err := yaml.Unmarshal([]byte("message-log: /var/lib/harbor/messages.jsonl\n"), &c); err != nil {
		t.Fatal(err)
	}
	if c.MessageLog != "/var/lib/harbor/messages.jsonl" {
		t.Fatalf("MessageLog = %q, want the configured path", c.MessageLog)
	}
	var empty serveConfig
	if err := yaml.Unmarshal([]byte("listen: \":443\"\n"), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.MessageLog != "" {
		t.Fatalf("MessageLog default = %q, want empty", empty.MessageLog)
	}
}
```

Also extend the "every known key is accepted" line in `TestUnknownServeKeys` to include the new key, so the reflect-derived key set is guarded against drift:

```go
	known := "listen: a\nadmin-listen: b\ntls: {}\nadmin-tls: {}\nstore: s\ncredentials: {}\noperator-auth: {}\nmessage-log: m\n"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `just test 2>&1 | head -40` (or `go test ./cmd/at-harbor/ -run 'MessageLog|UnknownServeKeys' -v`)
Expected: FAIL — `serveConfig` has no field `MessageLog` (compile error), and/or the known-key line reports `message-log` as unknown.

- [ ] **Step 3: Add the field**

In `cmd/at-harbor/config.go`, inside `serveConfig`, right after the `Store` field (~line 44):

```go
	Store string `yaml:"store"`
	// MessageLog is an optional path to the durable msglog JSONL file. When set,
	// `serve` opens it and the admin UI serves the read-only Messages view
	// (/ui/messages). Created on first open. Empty disables the view.
	MessageLog  string              `yaml:"message-log"`
	Credentials map[string]credSpec `yaml:"credentials"`
```

(Move the existing `Credentials` line down under it; keep the rest of the struct unchanged. `serveConfigKeys()` derives known keys via reflection, so no other change is needed for unknown-key detection.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/at-harbor/ -run 'MessageLog|UnknownServeKeys' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-harbor/config.go cmd/at-harbor/config_test.go
git commit -m "harbor: add message-log serve config field

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 2: The `/ui/messages` page — surface, render, states

Adds the `MessageReader` interface, the handler + template + nav link, the `Handler` parameter, and updates existing call sites. Renders all messages newest-first with per-target internal/external badges, plus the empty and "not configured" states.

**Files:**
- Create: `internal/harbor/adminui/messages.go`
- Create: `internal/harbor/adminui/templates/messages.html`
- Create: `internal/harbor/adminui/messages_test.go`
- Modify: `internal/harbor/adminui/adminui.go` (Handler signature, `pages` map, route)
- Modify: `internal/harbor/adminui/templates/layout.html` (nav link)
- Modify: `internal/harbor/adminui/adminui_test.go`, `writes_test.go`, `coves_write_test.go`, `destinations_write_test.go`, `kits_write_test.go` (trailing `nil` arg)

**Interfaces:**
- Consumes: `serveConfig.MessageLog` (Task 1) is unrelated here; this task's reader is injected as an interface.
- Produces:
  - `adminui.MessageReader interface { List(msglog.Filter) []msglog.Message }`
  - `adminui.Handler(store harbor.Store, log *slog.Logger, sup *harbor.Supervisor, credExists func(string) bool, msgs MessageReader) http.Handler` — the new trailing `msgs` param (Task 4 passes the opened Log or `nil`).

- [ ] **Step 1: Write the failing test**

Create `internal/harbor/adminui/messages_test.go`:

```go
package adminui_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor/adminui"
	"github.com/aethons-tools/cove/internal/msglog"
)

// newMsgLog opens a hermetic Log in a temp dir and appends the given messages.
func newMsgLog(t *testing.T, msgs ...msglog.Message) *msglog.Log {
	t.Helper()
	l, err := msglog.Open(t.TempDir()+"/messages.jsonl", testLogger())
	if err != nil {
		t.Fatalf("msglog.Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	for _, m := range msgs {
		if _, err := l.Append(m); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	return l
}

func msgHandler(t *testing.T, l adminui.MessageReader) http.Handler {
	t.Helper()
	return adminui.Handler(newStore(t), testLogger(), nil, anyCred, l)
}

func actor(ref string) msglog.Target   { return msglog.Target{Kind: "actor", Ref: ref} }
func human(ref string) msglog.Target   { return msglog.Target{Kind: "human", Ref: ref} }
func channel(ref string) msglog.Target { return msglog.Target{Kind: "channel", Ref: ref} }

func TestMessagesRendersNewestFirst(t *testing.T) {
	t0 := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	l := newMsgLog(t,
		msglog.Message{From: actor("cove-1"), To: []msglog.Target{human("alice")}, Body: "older ping", At: t0, Project: "acme"},
		msglog.Message{From: human("alice"), To: []msglog.Target{actor("cove-1")}, Body: "newer reply", At: t0.Add(time.Hour), Project: "acme"},
	)
	body := get(t, msgHandler(t, l), "/ui/messages").Body.String()
	for _, want := range []string{"<nav", "Messages", "older ping", "newer reply", "actor:cove-1", "human:alice", "acme"} {
		if !strings.Contains(body, want) {
			t.Errorf("messages page missing %q; got:\n%s", want, body)
		}
	}
	if i, j := strings.Index(body, "newer reply"), strings.Index(body, "older ping"); i > j {
		t.Errorf("newest message must render first: newer@%d older@%d", i, j)
	}
}

func TestMessagesReachBadges(t *testing.T) {
	l := newMsgLog(t,
		msglog.Message{From: actor("cove-1"), To: []msglog.Target{actor("cove-2"), human("alice")}, Body: "hi"},
	)
	body := get(t, msgHandler(t, l), "/ui/messages").Body.String()
	// actor:cove-2 → internal, human:alice → external (msglog.Classify).
	if !strings.Contains(body, "internal") || !strings.Contains(body, "external") {
		t.Errorf("expected internal+external reach badges; got:\n%s", body)
	}
}

func TestMessagesEmptyLog(t *testing.T) {
	body := get(t, msgHandler(t, newMsgLog(t)), "/ui/messages").Body.String()
	if !strings.Contains(body, "No messages logged yet") {
		t.Errorf("empty log should say so; got:\n%s", body)
	}
}

func TestMessagesNotConfigured(t *testing.T) {
	rec := get(t, msgHandler(t, nil), "/ui/messages")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/messages (nil reader) = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Errorf("nil reader should render a not-configured notice; got:\n%s", rec.Body.String())
	}
}

func TestMessagesNavLinkPresentOnOtherPages(t *testing.T) {
	body := get(t, msgHandler(t, newMsgLog(t)), "/ui/coves").Body.String()
	if !strings.Contains(body, `href="/ui/messages"`) {
		t.Errorf("nav should link to /ui/messages; got:\n%s", body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/harbor/adminui/ -run TestMessages -v 2>&1 | head -30`
Expected: FAIL — compile error (`adminui.MessageReader` undefined; `Handler` takes 4 args, not 5). This confirms the surface does not exist yet.

- [ ] **Step 3a: Create the handler**

Create `internal/harbor/adminui/messages.go`:

```go
package adminui

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

// MessageReader is the read-only slice of the msglog.Log the Messages page
// needs. *msglog.Log satisfies it; the UI never holds an append path.
type MessageReader interface {
	List(msglog.Filter) []msglog.Message
}

// dateLayout is the format of the since/until date inputs.
const dateLayout = "2006-01-02"

// toCell is one recipient target rendered with its reach classification.
type toCell struct {
	Target   string // "kind:ref"
	External bool
}

// msgRow is one message flattened for the template.
type msgRow struct {
	At      string
	From    string
	To      []toCell
	Project string
	Body    string
}

// messagesData is the Messages page payload. The filter fields are echoed back
// into the form so a filtered view round-trips; RawQuery drives the Refresh
// link; Filtered distinguishes the two empty states.
type messagesData struct {
	Title       string
	Configured  bool
	Filtered    bool
	Rows        []msgRow
	Project     string
	Participant string
	Q           string
	Since       string
	Until       string
	RawQuery    string
}

// handleMessages renders the read-only, newest-first message view. A nil reader
// means the log is not configured.
func handleMessages(w http.ResponseWriter, r *http.Request, msgs MessageReader) {
	data := messagesData{Title: "Messages", Configured: msgs != nil}
	if msgs == nil {
		render(w, "messages", data)
		return
	}

	q := r.URL.Query()
	data.Project = strings.TrimSpace(q.Get("project"))
	data.Participant = strings.TrimSpace(q.Get("participant"))
	data.Q = q.Get("q")
	data.Since = strings.TrimSpace(q.Get("since"))
	data.Until = strings.TrimSpace(q.Get("until"))
	data.RawQuery = r.URL.RawQuery
	data.Filtered = data.Project != "" || data.Participant != "" || data.Q != "" || data.Since != "" || data.Until != ""

	f := msglog.Filter{Project: data.Project}
	if t, err := time.Parse(dateLayout, data.Since); err == nil {
		f.Since = t
	}
	if t, err := time.Parse(dateLayout, data.Until); err == nil {
		f.Until = t.AddDate(0, 0, 1) // until is an inclusive day → exclusive next-midnight bound
	}

	msgList := msgs.List(f)

	needle := strings.ToLower(data.Q)
	filtered := msgList[:0:0]
	for _, m := range msgList {
		if data.Participant != "" && !matchesParticipant(m, data.Participant) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(m.Body), needle) {
			continue
		}
		filtered = append(filtered, m)
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].At.After(filtered[j].At) })

	data.Rows = make([]msgRow, 0, len(filtered))
	for _, m := range filtered {
		data.Rows = append(data.Rows, toRow(m))
	}
	render(w, "messages", data)
}

// matchesParticipant reports whether p ("kind:ref") is the sender or one of the
// recipients of m.
func matchesParticipant(m msglog.Message, p string) bool {
	if m.From.String() == p {
		return true
	}
	for _, t := range m.To {
		if t.String() == p {
			return true
		}
	}
	return false
}

func toRow(m msglog.Message) msgRow {
	to := make([]toCell, 0, len(m.To))
	for _, t := range m.To {
		to = append(to, toCell{Target: t.String(), External: msglog.Classify(t) == msglog.External})
	}
	return msgRow{
		At:      m.At.Format("2006-01-02 15:04:05"),
		From:    m.From.String(),
		To:      to,
		Project: m.Project,
		Body:    m.Body,
	}
}
```

- [ ] **Step 3b: Create the template**

Create `internal/harbor/adminui/templates/messages.html`:

```html
{{define "content"}}
<h1>Messages</h1>
{{if not .Configured}}
  <p class="empty">Message log not configured (set <code>message-log:</code> in the serve config).</p>
{{else}}
<form method="get" action="/ui/messages">
  <input name="project" value="{{.Project}}" placeholder="project">
  <input name="participant" value="{{.Participant}}" placeholder="participant (kind:ref)">
  <input name="q" value="{{.Q}}" placeholder="body contains">
  <input name="since" value="{{.Since}}" type="date">
  <input name="until" value="{{.Until}}" type="date">
  <button type="submit">Filter</button>
  <a href="/ui/messages">Reset</a>
  <a href="/ui/messages{{if .RawQuery}}?{{.RawQuery}}{{end}}">Refresh</a>
</form>
<table id="messages">
  <thead><tr><th>Time</th><th>From</th><th>To</th><th>Project</th><th>Body</th></tr></thead>
  <tbody>
    {{range .Rows}}
      <tr>
        <td>{{.At}}</td>
        <td>{{.From}}</td>
        <td>{{range .To}}{{.Target}} <span class="reach">[{{if .External}}external{{else}}internal{{end}}]</span> {{end}}</td>
        <td>{{.Project}}</td>
        <td class="body">{{.Body}}</td>
      </tr>
    {{else}}
      <tr><td colspan="5" class="empty">{{if .Filtered}}No messages match.{{else}}No messages logged yet.{{end}}</td></tr>
    {{end}}
  </tbody>
</table>
{{end}}
{{end}}
```

- [ ] **Step 3c: Wire the route, template, and nav link**

In `internal/harbor/adminui/adminui.go`:

1. Add to the `pages` map:

```go
	"messages":     mustParse("messages.html"),
```

2. Change the `Handler` signature and register the route. Replace the signature line and add the route after the `/ui/destinations` route:

```go
func Handler(store harbor.Store, log *slog.Logger, sup *harbor.Supervisor, credExists func(string) bool, msgs MessageReader) http.Handler {
```

```go
	mux.HandleFunc("GET /ui/messages", func(w http.ResponseWriter, r *http.Request) {
		handleMessages(w, r, msgs)
	})
```

In `internal/harbor/adminui/templates/layout.html`, add the nav link after the Destinations link:

```html
    <a href="/ui/messages">Messages</a>
```

- [ ] **Step 3d: Update existing Handler call sites to pass a trailing `nil`**

Find every call site:

```bash
grep -rn "adminui.Handler(" internal/harbor/adminui/*_test.go
```

Each existing call passes 4 args (e.g. `adminui.Handler(store, testLogger(), nil, anyCred)` or `adminui.Handler(store, testLogger(), nil, credOK)` in the write-test helpers). Append `, nil` to each so it reads `..., anyCred, nil)`. These live in `adminui_test.go` (direct calls) and in the `uiHandler`/`destHandler` helpers in the `*_write_test.go` files. The compiler will flag any you miss.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/harbor/adminui/ -v 2>&1 | tail -30`
Expected: PASS — the new `TestMessages*` tests and all pre-existing adminui tests are green.

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/adminui/
git commit -m "harbor: message-log admin UI — the /ui/messages view

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 3: Filters — project, participant, body, time window

The handler already parses and applies the filters (written in Task 2). This task proves each dimension with tests. If any test fails, fix the handler logic; the code in Task 2 is designed to satisfy them, so expect small or no fixes.

**Files:**
- Modify: `internal/harbor/adminui/messages_test.go`

**Interfaces:**
- Consumes: `adminui.Handler(..., msgs)`, `msgHandler`, `newMsgLog`, `actor`/`human`/`channel` (Task 2).
- Produces: nothing new.

- [ ] **Step 1: Write the failing tests**

Append to `internal/harbor/adminui/messages_test.go`:

```go
func fixtureLog(t *testing.T) *msglog.Log {
	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	return newMsgLog(t,
		msglog.Message{From: actor("cove-1"), To: []msglog.Target{channel("eng")}, Body: "deploy started", At: t0, Project: "acme"},
		msglog.Message{From: human("alice"), To: []msglog.Target{actor("cove-1")}, Body: "please HOLD", At: t0.Add(24 * time.Hour), Project: "acme"},
		msglog.Message{From: actor("cove-9"), To: []msglog.Target{human("bob")}, Body: "beta status", At: t0.Add(48 * time.Hour), Project: "beta"},
	)
}

func TestMessagesFilterProject(t *testing.T) {
	body := get(t, msgHandler(t, fixtureLog(t)), "/ui/messages?project=beta").Body.String()
	if !strings.Contains(body, "beta status") || strings.Contains(body, "deploy started") {
		t.Errorf("project=beta should show only beta rows; got:\n%s", body)
	}
}

func TestMessagesFilterParticipant(t *testing.T) {
	// channel:eng appears only in the first message's To.
	body := get(t, msgHandler(t, fixtureLog(t)), "/ui/messages?participant=channel:eng").Body.String()
	if !strings.Contains(body, "deploy started") || strings.Contains(body, "beta status") || strings.Contains(body, "please HOLD") {
		t.Errorf("participant=channel:eng should match only the eng-channel message; got:\n%s", body)
	}
	// actor:cove-1 is the sender of msg1 and a recipient of msg2 → both match.
	body = get(t, msgHandler(t, fixtureLog(t)), "/ui/messages?participant=actor:cove-1").Body.String()
	if !strings.Contains(body, "deploy started") || !strings.Contains(body, "please HOLD") || strings.Contains(body, "beta status") {
		t.Errorf("participant=actor:cove-1 should match its sent + received messages; got:\n%s", body)
	}
}

func TestMessagesFilterBodySubstringCaseInsensitive(t *testing.T) {
	body := get(t, msgHandler(t, fixtureLog(t)), "/ui/messages?q=hold").Body.String()
	if !strings.Contains(body, "please HOLD") || strings.Contains(body, "deploy started") {
		t.Errorf("q=hold should case-insensitively match 'please HOLD' only; got:\n%s", body)
	}
}

func TestMessagesFilterTimeWindow(t *testing.T) {
	// [2026-09-11, 2026-09-11] inclusive → only the 2026-09-11 message (msg2).
	body := get(t, msgHandler(t, fixtureLog(t)), "/ui/messages?since=2026-09-11&until=2026-09-11").Body.String()
	if !strings.Contains(body, "please HOLD") || strings.Contains(body, "deploy started") || strings.Contains(body, "beta status") {
		t.Errorf("since=until=2026-09-11 should show only that day; got:\n%s", body)
	}
}

func TestMessagesFiltersIntersect(t *testing.T) {
	body := get(t, msgHandler(t, fixtureLog(t)), "/ui/messages?project=acme&q=deploy").Body.String()
	if !strings.Contains(body, "deploy started") || strings.Contains(body, "please HOLD") {
		t.Errorf("project=acme&q=deploy should intersect to one row; got:\n%s", body)
	}
}

func TestMessagesFilterNoMatch(t *testing.T) {
	body := get(t, msgHandler(t, fixtureLog(t)), "/ui/messages?q=nothingmatchesthis").Body.String()
	if !strings.Contains(body, "No messages match") {
		t.Errorf("a no-match filter should say 'No messages match'; got:\n%s", body)
	}
}
```

- [ ] **Step 2: Run to verify (expect PASS, or a targeted fail to fix)**

Run: `go test ./internal/harbor/adminui/ -run TestMessagesFilter -v 2>&1 | tail -30`
Expected: PASS. If `TestMessagesFilterTimeWindow` fails, confirm the `until` bound adds one day (`AddDate(0,0,1)`) so an inclusive end date includes that whole day. If `participant` fails, confirm `matchesParticipant` checks `From` and every `To` via `Target.String()`.

- [ ] **Step 3: Commit**

```bash
git add internal/harbor/adminui/messages_test.go
git commit -m "harbor: cover message-log UI filters (project/participant/body/time)

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 4: Open the Log in `serve` and inject the reader

**Files:**
- Modify: `cmd/at-harbor/main.go` (the `serve` function; the admin block near line 1116–1132)

**Interfaces:**
- Consumes: `serveConfig.MessageLog` (Task 1); `adminui.Handler(..., msgs MessageReader)` (Task 2); `msglog.Open(path string, log *slog.Logger) (*msglog.Log, error)`.
- Produces: a running `/ui/messages` backed by the on-disk log.

- [ ] **Step 1: Add the import**

In `cmd/at-harbor/main.go`, add to the import block:

```go
	"github.com/aethons-tools/cove/internal/msglog"
```

- [ ] **Step 2: Open the log and pass it into the UI**

Inside `if cfg.AdminListen != "" {`, before the `uiMux := http.NewServeMux()` line, open the log:

```go
	// Read-only message-log view: open the durable Log (append handle held for
	// the serve lifetime, unused until the deferred writer slices land) and give
	// the admin UI a read-only reader. Unset config → nil → the view renders a
	// "not configured" notice.
	var msgReader adminui.MessageReader
	if cfg.MessageLog != "" {
		ml, err := msglog.Open(cfg.MessageLog, log)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: message-log:", err)
			return 1
		}
		defer ml.Close()
		msgReader = ml
		log.Info("harbor message log", "path", cfg.MessageLog)
	}
```

Then change the UI mount to pass it:

```go
	uiMux.Handle("/ui/", gate.Wrap(adminui.Handler(st, log, sup, credExists, msgReader)))
```

- [ ] **Step 3: Verify it builds and the suite is green**

Run: `go build ./... && just test 2>&1 | tail -20`
Expected: build succeeds; all tests pass. (The reader's rendering behavior is covered by the adminui tests from Tasks 2–3; this wiring has no separate unit test — `serve` is an integration entry point. `go build` + the green suite is the gate.)

- [ ] **Step 4: Manual smoke check (optional but recommended)**

```bash
go run ./cmd/at-harbor serve --help >/dev/null   # sanity: still builds/runs
```

Then, if you want a live look: write a minimal serve config with `admin-listen: "127.0.0.1:8081"` and `message-log: /tmp/harbor-msgs.jsonl`, run `go run ./cmd/at-harbor serve --config <file>`, and open `http://127.0.0.1:8081/ui/messages` — expect the empty-log view with the filter form. (The `run` skill can drive this.)

- [ ] **Step 5: Commit**

```bash
git add cmd/at-harbor/main.go
git commit -m "harbor: wire the message log into serve → /ui/messages

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 5: Docs

**Files:**
- Modify: `docs/usage/harbor/ui.md`
- Modify: `docs/usage/harbor/serve.md`

**Interfaces:**
- Consumes: the behavior shipped in Tasks 1–4.
- Produces: nothing (docs).

- [ ] **Step 1: Document the Messages view in `ui.md`**

Add **Messages** to the "It renders:" bullet list:

```markdown
- **Messages** (`/ui/messages`) — a read-only, filterable, newest-first table of
  the durable message Log (`message-log:` in the serve config). Filter by
  project, participant (`kind:ref`, e.g. `channel:eng`), a body substring, and a
  date window; filters live in the URL, so a filtered view is shareable. Manual
  refresh (not a live tail); each recipient carries an internal/external reach
  badge. Empty until the log has writers, and absent-config renders a
  "not configured" notice.
```

Also update the leaf's `summary`/`read_when` frontmatter to mention the message-log view, and — since the page shows message bodies — extend the existing "The UI never renders a token hash…" paragraph with one clause noting the Messages view shows comms bodies (agent/human messages), which are not secrets.

- [ ] **Step 2: Document the config field in `serve.md`**

In the serve-config field reference, add a `message-log` row/entry:

```markdown
- **`message-log`** (optional) — filesystem path to harbor's durable message Log
  (JSONL). When set, `serve` opens it (creating it on first open) and the admin
  UI serves the read-only Messages view at `/ui/messages`. Unset disables the
  view. The Log is append-only and single-writer (the serve process); this field
  only enables the read side — see [ui.md](ui.md#messages).
```

(Match the surrounding doc's exact heading/list style; place it near the `store` field.)

- [ ] **Step 3: Audit the docs**

Invoke the **docs-audit** skill (or run its checker) over `docs/`.
Expected: no orphans, no dangling links, no oversize docs, valid frontmatter. Fix anything it flags. No `INDEX.md` change is expected (both `ui.md` and `serve.md` are already listed).

- [ ] **Step 4: Commit**

```bash
git add docs/usage/harbor/ui.md docs/usage/harbor/serve.md
git commit -m "docs(harbor): document the message-log UI view + message-log config

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

## Final verification

- [ ] `just test` — full hermetic suite green.
- [ ] `just lint` — clean.
- [ ] `go build ./...` — builds.
- [ ] Skim the diff: no write path to the Log, no secret rendered, `internal/msglog` untouched, `harbor → msglog` direction preserved.
- [ ] Open a PR against `main` (branch `feat/harbor-message-log-ui`) with the PR-description attribution block.
