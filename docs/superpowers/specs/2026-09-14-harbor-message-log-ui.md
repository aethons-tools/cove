# harbor: message-log admin UI — a filterable view of the comms Log

**Status:** design approved, pre-plan
**Issue:** follow-on to COV-171 (the `internal/msglog` substrate). Read-only observability slice: open the Log in `serve` and present it as a filterable table in the admin UI. **No writers, no adapters** — those remain the deferred later slices of COV-171.
**Foundation:** COV-171 (the durable `msglog.Log` + envelope + `List`), the harbor admin UI (`internal/harbor/adminui`, `/ui/`).

## Summary

The `internal/msglog` Log exists but is wired to nothing. This slice adds the
**read side**: `at-harbor serve` opens the Log (a new `message-log:` config
path) and hands a **read-only reader** to the admin UI, which renders a new
**Messages** page at `/ui/messages` — a flat, filterable, newest-first table of
the log's message envelopes. This is an *observability* view, **not a chat UI**:
no send box, no reply, strictly read-only, behind the same `/ui/` gate as every
other page. Until the deferred COV-171 writer slices land, the view correctly
renders "no messages yet".

**Explicitly out of scope (still deferred to COV-171):** making `send` /
escalation *append* to the Log, and any egress/ingress adapters. This slice
touches no write path.

## 1. Serve wiring (read-only)

- Add `MessageLog string \`yaml:"message-log"\`` to `serveConfig`
  (`cmd/at-harbor/config.go`) — an optional filesystem path to the JSONL log.
- In `serve` (`cmd/at-harbor/main.go`), when `cfg.MessageLog != ""`, open it
  once with `msglog.Open(cfg.MessageLog, log)` and `defer Close`. Pass the
  resulting `*msglog.Log` into `adminui.Handler` as a new reader dependency.
  When unset, pass `nil`.
- The `*msglog.Log` is shared as a **read-only view**: the UI only ever calls
  `List`. `adminui` depends on a narrow interface, not the concrete writer:

  ```go
  // in adminui: the only capability the Messages page needs.
  type MessageReader interface {
      List(msglog.Filter) []msglog.Message
  }
  ```

  `*msglog.Log` satisfies it; the UI never holds an append path. Opening the
  Log for append in `serve` is intentional and forward-compatible — the same
  `*Log` becomes the shared writer when the deferred consumer slices land — but
  nothing in *this* slice writes to it.
- `adminui.Handler` signature gains one parameter: `msgs MessageReader` (may be
  `nil`). `nil` means "message log not configured" — the page renders a notice.
  The **Messages** nav link is always present (no cross-page template plumbing
  for an edge state that a configured harbor never hits); the notice lives on the
  page itself.

## 2. The Messages page (`/ui/messages`)

A new `GET /ui/messages` route + `messages.html` template + a `Messages` nav
link in `layout.html`.

**Filters** (all optional, all combinable, all carried in the query string so a
filtered view is a shareable/bookmarkable URL):

| Param | Meaning | Applied by |
|-------|---------|-----------|
| `project` | exact `Message.Project` | `msglog.Filter.Project` |
| `since` / `until` | `date` inputs (`2006-01-02`); `[since, until)` | `msglog.Filter.Since/Until` |
| `participant` | `kind:ref` (e.g. `channel:eng`, `actor:cove-1`, `human:alice`); matches `From` **or** any `To` | handler, post-`List` |
| `q` | case-insensitive substring of `Body` | handler, post-`List` |

`msglog.List` handles `project`/`since`/`until` natively; `participant` and `q`
are applied in the handler over the returned slice (the Log has no by-participant
index — an acknowledged v1 scan, matching the substrate's own "reads scan memory"
choice). Results are **reversed to newest-first** for display.

**Columns:** Time (`At`, `2006-01-02 15:04:05`) · From (`kind:ref`) · To (the
set, each target with an internal/external badge from `msglog.Classify`) ·
Project · Body. Long bodies are shown in full in a wrapping cell (bounded by the
16 KiB message cap upstream); no truncation UI in v1.

**Not live-polling.** Unlike the Coves page's 3 s htmx poll, this is a snapshot
with a manual **Refresh** (a link that re-issues the current filtered GET). It is
a log-review surface, not a tail. Live-poll is a trivial later addition.

**Empty / unconfigured states:** reader present but `List` empty (given the
filters) → "No messages match." / "No messages logged yet." Reader `nil` →
"Message log not configured (set `message-log:` in the serve config)."

## 3. Security & boundaries

- **Read-only.** No `POST`/mutation route; the page is pure `GET`. It rides the
  existing `/ui/` gate (loopback always; off-loopback via browser OIDC) with no
  new auth code.
- **No secret exposure.** Message bodies are agent/human comms, not credentials;
  the view renders `From`/`To`/`Body`/`At`/`Project` only — never a token, hash,
  or credential (there are none in the envelope). Consistent with the UI's
  existing "never render a secret" rule.
- `adminui` importing `msglog` introduces no cycle: `msglog` is stdlib-only and
  imports nothing from `internal/harbor`. The dependency direction stays
  `harbor → msglog`.
- The write path (Append) is untouched; the serve process remains the sole
  holder of the append handle, unused this slice.

## 4. Tests (hermetic)

- **config:** `serveConfig` parses `message-log:` to `MessageLog`; absent → "".
- **adminui, real Log in `t.TempDir()`:** open a Log, `Append` a fixture set
  spanning projects, participants, times, and bodies, build the Handler with it,
  then via `httptest`:
  - unfiltered `/ui/messages` renders all fixtures, **newest-first**;
  - `?project=` narrows to that project;
  - `?participant=channel:eng` matches messages with that target in `From` or
    `To`, excludes others;
  - `?q=` case-insensitively substring-filters bodies;
  - `?since=&until=` windows by date;
  - combined filters intersect;
  - internal/external badge reflects `Classify` per `To` target;
  - empty result → "No messages match"; empty log → "No messages logged yet".
- **unconfigured:** Handler built with `nil` reader → `/ui/messages` renders the
  "not configured" notice (and does not panic).
- Existing `adminui`/`config` tests updated for the new `Handler` parameter and
  config field.

## 5. Docs (same change)

- **`docs/usage/harbor/ui.md`** — add the **Messages** view to the rendered-pages
  list and a short subsection: what it shows, the filters, read-only, manual
  refresh, and that it is empty until log writers exist.
- **`docs/usage/harbor/serve.md`** — document the `message-log:` config field
  (optional path; enables the Messages view; the Log is created on first open).
- No new leaf doc; both facts are owned by existing docs. `INDEX.md` already
  lists `ui.md`/`serve.md`, so no INDEX change is required (verify with
  docs-audit).

## Deferred (unchanged from COV-171)

Writers (retrofit `send`/escalation to append), wake-on-via-log, the Linear /
Discord adapters, per-participant index, thread grouping in the UI (view (B)/(C)),
and live-polling.
