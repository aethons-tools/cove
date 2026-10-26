# Jam SPA, slice 6b: Studio and "View here" — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the session pane's framed views work in the Jam app, matching the old Inbox (`layout.html`'s `meFrame*`, `meStudio*` and `mePv*` code).

The **Studio** tab frames the session's IDE:
- it mints a one-time frame URL, shows "Starting the IDE…" until the frame loads, and offers Reload and Open in new tab (the plain IDE URL works even when a mint fails);
- it stays alive while you switch to other tabs, so the IDE keeps its state, and is torn down when the pane closes or you leave the session;
- its first open, with no pane size saved for the placement, widens the pane to 60 % of the split.

A framed page reports a refusal with `postMessage({jam:'frame-error', status})`. The app heeds it **only from that frame's own window**:
- **"signin" and "cookies":** each re-mints once per user action. A second "signin" offers "Reopen this view"; a second "cookies" says to use Open in new tab.
- **403, 404 and 502–504:** each shows its message.
- **Mint refusals (403, 404, 429)** and a non-http(s) URL show their messages.
- **A back/forward-cache restore** re-mints, because the old ticket is spent.

In Previews, **View here** frames a preview in place of the list, with "← All previews" to go back. Leaving the tab closes it, and a live refresh keeps it.

The app page's CSP `frame-src` admits exactly the preview origins (`https://*.<previews domain>[:admin port]`) when pane framing is on, and `'none'` otherwise.

**Architecture:**
- **Go.**
  - `POST /app/user/api/sessions/{id}/frame` takes `{name}` and answers `{url, open}`. It shares one `frameOutcome` with the old form route, so authorization, the mint cap and the refusals are identical.
  - `pane/studio` joins the JSON (`studio`, `ideOpen`).
  - `cmd/at-jam` computes `appFrameSrc(cfg)` and passes it to `userui.Next`.
- **App.**
  - `src/session/frames/useFrame.ts` is the shared state machine (mint, retry rules, message filter, bfcache), and `FrameView.tsx` its UI.
  - `src/session/StudioTab.tsx` is a lazy chunk; `SessionView` keeps it mounted, hidden, while another tab shows.
  - `PaneTabs`' Previews gains View here.

**Tech Stack:** the slice 1–6a stack. No new dependencies.

**Spec:** [`docs/superpowers/specs/2026-10-26-jam-spa-design.md`](../specs/2026-10-26-jam-spa-design.md):
- §3 slice 6, the Studio half: "the Studio tab (framed IDE, ticket mint and fallback, Previews 'View here')";
- §4: "The Studio frame boundary is unchanged: tickets, `/__jam/frame`, the partitioned frame cookie, and the `referrerpolicy="origin"` iframe", and the CSP's `frame-src <preview origins>`;
- §1 and §5: Studio is a lazy chunk.

Helpers already on main:
- Go: `frameEnv`, `fakeFrames`, `postFrame`, `sessAPI`, `decodeAs`, `checkJSONHeaders`, `asPrincipal`;
- browser tests: `studioFrames`, `framePage`, `pageProblems`, `browserCtx`, `into`, `call`, `thinkingListJS`-style helpers;
- TS: `apiPost`, `ApiError`, `usePaneTab`, `usePaneLayout`, `renderApp(b, path, client)`, `boot`, `server`, and `head`/`pane`/`serve` in `SessionView.test.tsx`.

## Global Constraints

- **Parity:** the old form route `POST /app/user/session/{id}/frame` keeps its exact codes and bodies. `frame_test.go` and `studio_browser_test.go` pass unchanged.
- **The frame boundary is unchanged:**
  - URLs come only from the mint, for the name the user chose;
  - the iframe carries `referrerpolicy="origin"`, `allow="clipboard-read; clipboard-write"` and a `title`;
  - only an `http:` or `https:` URL from the mint may become an iframe `src` or a link `href`.
- **Messages:** a `frame-error` is heeded only when `event.source` is the current iframe's `contentWindow`. Status text goes in as text.
- **Retries:** "signin" and "cookies" re-mint **at most once each per user action** (open, Reload, View here, Reopen, a bfcache restore). Nothing loops.
- **Authorization:**
  - the mint re-checks `sessionFor` and the right: ssh for the IDE, preview otherwise;
  - an unseen session, framing off, or a name the session doesn't expose answers exactly like a missing session (404 `not_found`, "no such session");
  - over the per-user cap: 429 `too_many`.
- **CSP:** `frame-src` is the preview origin wildcard only when framing is on, else `'none'`; `frame-ancestors 'none'` stays.
- **Copy, verbatim from the old code:**
  - **status lines:**
    - "Starting the IDE…" / "Loading…";
    - "No longer shared with you.";
    - "Nothing to show.";
    - "Too many views opened; wait a minute.";
    - "Couldn't open this view.";
    - "Nothing to show: the studio isn't running.";
    - "The IDE didn't start. This studio's image may predate it: upgrade the studio, or expose an IDE as ide.";
    - "The preview isn't answering.";
    - "This browser can't sign in inside the pane — use Open in new tab.";
    - "Couldn't show this view.";
  - **buttons and links:** "Reopen this view", "Reload", "Open in new tab", "← All previews", "View here";
  - **empty Studio pane:** "The studio isn't available to you here."
- **Lazy chunk:** the Studio code isn't in the initial bundle (≤ 350 KB gzipped; it's 208 KB now).
- **Docs:** they ship in the same change.
- **gofmt:** use positional struct literals in table tests.

## Review Focus

1. **A `frame-error` from anywhere but the current frame** is ignored and mints nothing. That covers:
   - the page itself (`window.postMessage`);
   - another iframe;
   - the frame a Reload just replaced, posting late.
   (Task 3 test.)
2. **The retry rules can't loop:**
   - "signin" twice: one re-mint, then the Reopen button;
   - "cookies" twice: one re-mint, then the message;
   - Reload, Reopen and View here re-arm both.
   (Task 3 test.)
3. **Only http(s) URLs** from the mint become an iframe `src`. Anything else shows "Couldn't open this view.", and only http(s) becomes Open in new tab. (Task 3 test.)
4. **Tab switches keep the live IDE:** the same iframe element, no re-mint. Closing the pane or leaving the session removes it, and the next open mints afresh. (Task 4 test.)
5. **`frame-src`:** with framing on it is exactly `https://*.<domain>` plus the admin port when that isn't 443. With framing off (no previews, or no ui-origins) it's `'none'`. (Task 2 Go test; Task 6 browser test.)

---

## File structure

| File | Responsibility |
|---|---|
| `internal/jam/userui/frame.go` (modify) | `frameOutcome`, shared; `frameRoute` uses it. |
| `internal/jam/userui/api_sessions.go`, `api_sessions_test.go` (modify) | `POST api/sessions/{id}/frame`; `studio` in `apiPaneTabs`. |
| `internal/jam/userui/apitypes.go` (modify) | `FrameRequest`, `FrameURL`; `SessionPane.Studio` (`studio`) and `IDEOpen` (`ideOpen`). |
| `cmd/at-jam/previewhost.go`, `previewhost_test.go`, `main.go` (modify) | `appFrameSrc`, passed to `userui.Next`. |
| `web/app/src/session/frames/useFrame.ts`, `FrameView.tsx`, `FrameView.test.tsx` (new) | The framed view. |
| `web/app/src/session/StudioTab.tsx` (new) | The Studio tab (default export, lazy). |
| `web/app/src/session/layout.ts`, `usePaneLayout.ts` (modify) | `toggle(t, size?)` widens on a first open. |
| `web/app/src/session/SessionView.tsx`, `SessionView.test.tsx` (modify) | Keeps Studio mounted; the 60 % widening. |
| `web/app/src/session/PaneTabs.tsx` (modify) | View here; drop the Studio placeholder. |
| `internal/jam/userui/next_browser_test.go` (modify) | End-to-end under the CSP. |
| `docs/web-app.md`, `docs/usage/jam/intercom-ui.md`, `docs/usage/jam/previews.md`, the spec's status line (modify) | Docs. |

---

### Task 1: `POST api/sessions/{id}/frame` and `pane/studio`

**Files:**
- Modify: `internal/jam/userui/frame.go`, `apitypes.go`, `api_sessions.go`, `api_sessions_test.go`
- Modify: `web/app/src/api/types.ts` (generated)

**Interfaces:**
- Produces (Go):
  - `type FrameRequest struct { Name string "name" }`;
  - `type FrameURL struct { URL string "url"; Open string "open" }`;
  - `SessionPane.Studio bool "studio"` and `SessionPane.IDEOpen string "ideOpen"`;
  - `func (h *handler) frameOutcome(r *http.Request, p jam.Participant, inst jam.Instance, name string) (FrameURL, int, string, string)`, returning (url, status, code, msg) with status 200 on success.
- Produces (route): `POST /app/user/api/sessions/{id}/frame` with body `{"name": "ide" | <an exposed name>}`. It answers 200 `FrameURL`, or:
  - 403 `forbidden` "you may not open this here";
  - 404 `not_found` "no such session" (unseen, framing off, unexposed or empty name);
  - 429 `too_many` "too many frames opened; wait a minute";
  - 500 `internal` "couldn't open the frame";
  - 415 or 400 for a bad body.

- [ ] **Step 1: Write the failing tests** (append to `api_sessions_test.go`):

```go
func frameAPI(t *testing.T, e *env, p jam.Participant, id, name string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(FrameRequest{Name: name})
	return sessAPI(t, e, p, "POST", id+"/frame", string(b), false)
}

func TestAPIFrame(t *testing.T) {
	e, p, personal, standing, ff := frameEnv(t)
	got := decodeAs[FrameURL](t, frameAPI(t, e, p, personal.ActorID, "ide"))
	if got.URL != "https://ide--"+personal.ActorID+".preview.test/__jam/frame?t=tk" || got.Open != "https://ide--"+personal.ActorID+".preview.test/" {
		t.Errorf("ide = %+v", got)
	}
	if len(ff.calls) != 1 || ff.calls[0].right != collab.SSH || ff.calls[0].name != "ide" || ff.calls[0].display != "alice" {
		t.Errorf("minted %+v", ff.calls)
	}
	if decodeAs[FrameURL](t, frameAPI(t, e, p, personal.ActorID, "app")); ff.calls[len(ff.calls)-1].right != collab.Preview {
		t.Errorf("app minted %+v", ff.calls[len(ff.calls)-1])
	}
	n := len(ff.calls)
	bob := jam.Participant{UserID: ident.New(ident.User), Name: "bob", Projects: []string{"proj"}}
	for _, tc := range []struct {
		who  jam.Participant
		id   string
		name string
		code int
		want string
	}{
		{p, personal.ActorID, "nope", 404, "no such session"},
		{p, personal.ActorID, "", 404, "no such session"},
		{p, standing.ActorID, "ide", 403, `"code":"forbidden"`},
		{bob, personal.ActorID, "app", 404, "no such session"},
	} {
		if rec := frameAPI(t, e, tc.who, tc.id, tc.name); rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s %q = %d %s, want %d %s", tc.who.Name, tc.name, rec.Code, rec.Body, tc.code, tc.want)
		}
	}
	if len(ff.calls) != n {
		t.Errorf("minted on a refusal: %+v", ff.calls[n:])
	}
	ff.err = errors.New("boom")
	if rec := frameAPI(t, e, p, personal.ActorID, "app"); rec.Code != 500 || !strings.Contains(rec.Body.String(), `"code":"internal"`) {
		t.Errorf("mint failure = %d %s", rec.Code, rec.Body)
	}
	e.Deps.Frames = nil
	if rec := frameAPI(t, e, p, personal.ActorID, "ide"); rec.Code != 404 {
		t.Errorf("frames off = %d", rec.Code)
	}
}

func TestAPIFrameRateLimited(t *testing.T) {
	e, p, personal, _, _ := frameEnv(t)
	h := Handler(e.Deps, nil)
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/app/user/api/sessions/"+personal.ActorID+"/frame", strings.NewReader(`{"name":"app"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, asPrincipal(req, p, false))
		return rec
	}
	for i := 0; i < maxFrameMints; i++ {
		if rec := call(); rec.Code != 200 {
			t.Fatalf("mint %d = %d", i, rec.Code)
		}
	}
	if rec := call(); rec.Code != 429 || !strings.Contains(rec.Body.String(), `"code":"too_many"`) {
		t.Errorf("over the cap = %d %s", rec.Code, rec.Body)
	}
}

func TestAPIPaneStudio(t *testing.T) {
	e, p, personal, standing, _ := frameEnv(t)
	st := decodeAs[SessionPane](t, sessAPI(t, e, p, "GET", personal.ActorID+"/pane/studio", "", false))
	if !st.Studio || st.IDEOpen != "https://ide--"+personal.ActorID+".preview.test/" {
		t.Errorf("studio pane = %+v", st)
	}
	if no := decodeAs[SessionPane](t, sessAPI(t, e, p, "GET", standing.ActorID+"/pane/studio", "", false)); no.Studio || no.IDEOpen != "" {
		t.Errorf("studio without ssh = %+v", no)
	}
}
```

Also make these changes to existing tests:
- `TestAPISessionHiddenIsUniform`'s table gains `{"POST", "/frame", `{"name":"ide"}`}`.
- In `TestAPISessionPane`, `studio` leaves the "unknown tab" list, which becomes `[]string{"bogus"}`.
- The template-only key check becomes `[]string{"Streams", "IDEOpen"}`.
- Add `"errors"` and `"github.com/aethons-tools/cove/internal/jam/collab"` to the imports as needed.

`TestAPIFrameRateLimited` is a separate test because the limiter is per handler (`Handler` makes a new one), so the 30 mints must all go through one handler.

- [ ] **Step 2: Run them to see them fail.**

Run: `GOPROXY=https://proxy.golang.org go test -run 'TestAPIFrame|TestAPIPaneStudio|TestAPISession' ./internal/jam/userui/`

Expected: a compile error, `undefined: FrameRequest`.

- [ ] **Step 3: Implement.**

`apitypes.go`:

```go
// FrameRequest is POST api/sessions/{id}/frame: the framed view to open, the
// IDE ("ide") or an exposed preview's name.
type FrameRequest struct {
	Name string `json:"name"`
}

// FrameURL is a minted frame URL (one-time: never cached) and the view's
// plain URL, for Open in new tab ("" = none).
type FrameURL struct {
	URL  string `json:"url"`
	Open string `json:"open"`
}
```

In `SessionPane`, change `Studio bool `json:"-"`` to `json:"studio"` and `IDEOpen string `json:"-"`` to `json:"ideOpen"`, and update their comment so it no longer says "(slice 6)".

`frame.go`: factor out the checks.

```go
// frameOutcome mints a frame URL for one of inst's framed views: the IDE
// (preview.IDEName) under the ssh right, else an exposed preview under the
// preview right. It answers 200 with the URL, or a refusal's status, API
// code and text (the HTML route writes the text, the API the envelope).
func (h *handler) frameOutcome(r *http.Request, p jam.Participant, inst jam.Instance, name string) (FrameURL, int, string, string) {
	right := collab.SSH
	if name != preview.IDEName {
		if !slices.ContainsFunc(inst.Exposures, func(e jam.Exposure) bool { return e.Name == name }) {
			return FrameURL{}, http.StatusNotFound, "not_found", "no such session"
		}
		right = collab.Preview
	}
	cp := collabPrincipal(r, p)
	if _, ok := collab.Decide(h.d.Store, time.Now(), cp, inst, right); !ok {
		return FrameURL{}, http.StatusForbidden, "forbidden", "you may not open this here"
	}
	if !h.mints.allow(p.UserID) {
		return FrameURL{}, http.StatusTooManyRequests, "too_many", "too many frames opened; wait a minute"
	}
	u, err := h.d.Frames.FrameURL(cp, p.Name, inst.ActorID, name, right)
	if err != nil {
		h.lg.Error("frame mint failed", "session", inst.ActorID, "name", name, "error", err.Error())
		return FrameURL{}, http.StatusInternalServerError, "internal", "couldn't open the frame"
	}
	out := FrameURL{URL: u}
	if h.d.PreviewURL != nil {
		out.Open, _ = h.d.PreviewURL(inst.ActorID, name)
	}
	return out, http.StatusOK, "", ""
}
```

`frameRoute` keeps its participant, `sessionFor` and `Frames == nil` checks, then:

```go
	fu, status, _, msg := h.frameOutcome(r, p, inst, r.PostFormValue("name"))
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		http.NotFound(w, r)
		return
	default:
		http.Error(w, msg, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // the URL carries a one-time ticket
	_ = json.NewEncoder(w).Encode(map[string]string{"url": fu.URL, "open": fu.Open})
```

The old texts are kept exactly: "you may not open this here", "too many frames opened; wait a minute" and "couldn't open the frame".

`api_sessions.go`:
- `apiPaneTabs` becomes all five tabs (`paneTabs`). Replace the variable's uses with `paneTabs` and delete `apiPaneTabs`.
- Register `mux.HandleFunc("POST /app/user/api/sessions/{id}/frame", h.apiFrame)`. It is more specific than `{action}`, so ServeMux prefers it.

```go
// apiFrame mints a frame URL for the Studio tab or a preview's View here
// (see frameOutcome); every refusal of a view the viewer can't have answers
// like a missing session.
func (h *handler) apiFrame(w http.ResponseWriter, r *http.Request) {
	var req FrameRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, inst, _, ok := h.openSession(w, r)
	if !ok {
		return
	}
	if h.d.Frames == nil {
		noSession(w)
		return
	}
	fu, status, code, msg := h.frameOutcome(r, p, inst, req.Name)
	if status != http.StatusOK {
		writeAPIError(w, status, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, fu)
}
```

`writeJSON` already sets `Cache-Control: no-store`. Regenerate the types.

- [ ] **Step 4: Run them to see them pass.**

Run: `GOPROXY=https://proxy.golang.org go test ./internal/jam/userui/ && ./scripts/app-types-check.sh` (after `git add`).

Expected: PASS, with `TestFrameRoute`, `TestFrameRouteOff`, `TestFrameRouteRateLimited`, `TestStudioTab` and `TestPreviewsViewHere` unchanged.

- [ ] **Step 5: Commit.** Message: `jam app API: frame mints and the Studio pane`.

---

### Task 2: The app's `frame-src`

**Files:**
- Modify: `cmd/at-jam/previewhost.go`, `cmd/at-jam/previewhost_test.go`, `cmd/at-jam/main.go`

**Interfaces:**
- Produces: `func appFrameSrc(c *serveConfig) []string`. It returns nil unless `paneFramingOn(c)`; otherwise `[]string{previewOrigin("*."+strings.ToLower(c.Previews.Domain), c.AdminListen)}`.
- `main.go` changes to `userui.Next(userDeps, log, appFrameSrc(&cfg), userOpts...)`, and its comment drops "no framing yet".

- [ ] **Step 1: Write the failing test** (append to `previewhost_test.go`):

```go
// The app page may frame exactly the preview hosts (any name and label
// under the domain, on the admin port), and nothing when pane framing is off.
func TestAppFrameSrc(t *testing.T) {
	on := func(listen string) *serveConfig {
		return &serveConfig{AdminListen: listen, Previews: &previewsConfig{Domain: "P.Example"}, UIOrigins: []string{"https://jam.example"}}
	}
	for _, tc := range []struct {
		c    *serveConfig
		want []string
	}{
		{on(":8443"), []string{"https://*.p.example:8443"}},
		{on("0.0.0.0:443"), []string{"https://*.p.example"}},
		{&serveConfig{AdminListen: ":8443", Previews: &previewsConfig{Domain: "p.example"}}, nil}, // no ui-origins
		{&serveConfig{AdminListen: ":8443", UIOrigins: []string{"https://jam.example"}}, nil},       // no previews
	} {
		if got := appFrameSrc(tc.c); !slices.Equal(got, tc.want) {
			t.Errorf("appFrameSrc(listen %q, previews %v) = %q, want %q", tc.c.AdminListen, tc.c.Previews != nil, got, tc.want)
		}
	}
}
```

Add `"slices"` to the imports if it's missing.

- [ ] **Step 2: Run it to see it fail.**

Run: `GOPROXY=https://proxy.golang.org go test -run TestAppFrameSrc ./cmd/at-jam/`

Expected: a compile error, `undefined: appFrameSrc`.

- [ ] **Step 3: Implement** (in `previewhost.go`, after `paneFramingOn`):

```go
// appFrameSrc is the Jam app page's CSP frame-src (userui.Next): the preview
// hosts, any name under the domain on the admin port, when the pane may
// frame them; none otherwise.
func appFrameSrc(c *serveConfig) []string {
	if !paneFramingOn(c) {
		return nil
	}
	return []string{previewOrigin("*."+strings.ToLower(c.Previews.Domain), c.AdminListen)}
}
```

`main.go`:

```go
		// The Jam app (web/app) at /app/next/ while it is ported to parity
		// (docs/superpowers/specs/2026-10-26-jam-spa-design.md); its CSP
		// frames the preview hosts when the pane may.
		nextApp := userui.Next(userDeps, log, appFrameSrc(&cfg), userOpts...)
```

- [ ] **Step 4: Run it to see it pass.**

Run: `GOPROXY=https://proxy.golang.org go test ./cmd/at-jam/`

Expected: PASS.

- [ ] **Step 5: Commit.** Message: `jam: the app page frames the preview hosts`.

---

### Task 3: `useFrame` and `FrameView`

**Files:**
- Create: `web/app/src/session/frames/useFrame.ts`, `web/app/src/session/frames/FrameView.tsx`, `web/app/src/session/frames/FrameView.test.tsx`

**Interfaces:**
- Consumes: `apiPost`, `ApiError`, and `FrameURL` (types).
- Produces:
  - `type FrameKind = "ide" | "preview"`;
  - `useFrame(sid: string, name: string, kind: FrameKind): FrameState`, where `interface FrameState { src: string; seq: number; status: string; reopen: boolean; open: string; frame: React.RefObject<HTMLIFrameElement | null>; start(): void; loaded(): void }`;
  - `<FrameView sid name kind open?={plain URL} lead?={ReactNode} reload?={boolean} />`.
- DOM contract (old class names, for the browser tests):
  - the bar is `.st-bar`;
  - status is `.st-status`;
  - the buttons are `.st-reload` ("Reload") and `.st-reopen` ("Reopen this view");
  - the link is `a.st-open` ("Open in new tab", `target="_blank" rel="noopener"`);
  - the frame is `iframe.st-iframe`, keyed by `seq` so every mint is a fresh element.

- [ ] **Step 1: Write the failing tests** (`FrameView.test.tsx`):

```tsx
import { afterEach, describe, expect, it } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { theme } from "../../theme";
import { FrameView } from "./FrameView";

// mint answers the frame route: urls in turn (then the last), or a status.
function mint(...answers: (string | number)[]) {
  const names: string[] = [];
  let i = 0;
  server.use(http.post("/app/user/api/sessions/:id/frame", async ({ request }) => {
    names.push(((await request.json()) as { name: string }).name);
    const a = answers[Math.min(i++, answers.length - 1)]!;
    return typeof a === "number"
      ? HttpResponse.json({ error: "no", code: "x" }, { status: a })
      : HttpResponse.json({ url: a, open: "https://ide--s.p.example/" });
  }));
  return names;
}
const show = (kind: "ide" | "preview" = "ide", open?: string) =>
  render(<MantineProvider theme={theme} env="test"><FrameView sid="ses_1" name={kind === "ide" ? "ide" : "app"} kind={kind} open={open} reload /></MantineProvider>);
const frame = () => document.querySelector<HTMLIFrameElement>("iframe.st-iframe");
const status = () => document.querySelector(".st-status")!.textContent;
// post sends a frame-error from source (default: the current frame's window).
const post = (st: unknown, source: MessageEventSource | null = frame()!.contentWindow) =>
  act(() => { window.dispatchEvent(new MessageEvent("message", { data: { jam: "frame-error", status: st }, source })); });

afterEach(() => server.resetHandlers());

describe("a framed view", () => {
  it("frames the minted URL, as the boundary wants it, and clears the status on load", async () => {
    const names = mint("https://ide--s.p.example/__jam/frame?t=1");
    show();
    expect(status()).toBe("Starting the IDE…");
    await waitFor(() => expect(frame()).not.toBeNull());
    const f = frame()!;
    expect(f.getAttribute("src")).toBe("https://ide--s.p.example/__jam/frame?t=1");
    expect(f.getAttribute("referrerpolicy")).toBe("origin");
    expect(f.getAttribute("allow")).toBe("clipboard-read; clipboard-write");
    expect(f.getAttribute("title")).toBe("IDE");
    expect(screen.getByRole("link", { name: "Open in new tab" })).toHaveAttribute("href", "https://ide--s.p.example/");
    fireEvent.load(f);
    expect(status()).toBe("");
    expect(names).toEqual(["ide"]);
  });

  it("Reload mints a fresh frame; a late message from the replaced frame is ignored", async () => {
    const names = mint("https://a.p.example/1", "https://a.p.example/2");
    show();
    await waitFor(() => expect(frame()).not.toBeNull());
    const old = frame()!.contentWindow;
    await userEvent.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(frame()!.getAttribute("src")).toBe("https://a.p.example/2"));
    fireEvent.load(frame()!);
    post(403, old);
    expect(status()).toBe("");
    expect(names).toHaveLength(2);
  });

  it("ignores frame-errors from anywhere but its frame", async () => {
    const names = mint("https://a.p.example/1");
    show();
    await waitFor(() => expect(frame()).not.toBeNull());
    fireEvent.load(frame()!);
    post(403, window);
    post("signin", null);
    const other = document.createElement("iframe");
    document.body.append(other);
    post("signin", other.contentWindow);
    other.remove();
    expect(status()).toBe("");
    expect(names).toHaveLength(1);
  });

  it("shows a frame's refusal, and the load doesn't clear it", async () => {
    mint("https://a.p.example/1");
    show();
    await waitFor(() => expect(frame()).not.toBeNull());
    post(403);
    expect(status()).toBe("No longer shared with you.");
    fireEvent.load(frame()!);
    expect(status()).toBe("No longer shared with you.");
    post(404);
    expect(status()).toBe("Nothing to show: the studio isn't running.");
    post(502);
    expect(status()).toBe("The IDE didn't start. This studio's image may predate it: upgrade the studio, or expose an IDE as ide.");
    post("weird");
    expect(status()).toBe("Couldn't show this view.");
  });

  it("re-mints once on signin, then offers Reopen, which re-arms it", async () => {
    const names = mint("https://a.p.example/1", "https://a.p.example/2", "https://a.p.example/3", "https://a.p.example/4");
    show();
    await waitFor(() => expect(frame()).not.toBeNull());
    post("signin");
    await waitFor(() => expect(frame()!.getAttribute("src")).toBe("https://a.p.example/2"));
    post("signin");
    expect(await screen.findByRole("button", { name: "Reopen this view" })).toBeInTheDocument();
    expect(names).toHaveLength(2);
    await userEvent.click(screen.getByRole("button", { name: "Reopen this view" }));
    await waitFor(() => expect(frame()!.getAttribute("src")).toBe("https://a.p.example/3"));
    post("signin");
    await waitFor(() => expect(names).toHaveLength(4)); // re-armed: one more re-mint
  });

  it("re-mints once on cookies, then says to use a new tab", async () => {
    const names = mint("https://a.p.example/1", "https://a.p.example/2");
    show();
    await waitFor(() => expect(frame()).not.toBeNull());
    post("cookies");
    await waitFor(() => expect(names).toHaveLength(2));
    await waitFor(() => expect(frame()!.getAttribute("src")).toBe("https://a.p.example/2"));
    post("cookies");
    expect(status()).toBe("This browser can't sign in inside the pane — use Open in new tab.");
    expect(names).toHaveLength(2);
  });

  it("says why a mint was refused, keeps the plain URL, and frames only http(s)", async () => {
    mint(429);
    show("ide", "https://ide--s.p.example/");
    await waitFor(() => expect(status()).toBe("Too many views opened; wait a minute."));
    expect(frame()).toBeNull();
    expect(screen.getByRole("link", { name: "Open in new tab" })).toHaveAttribute("href", "https://ide--s.p.example/");
    mint(403);
    await userEvent.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(status()).toBe("No longer shared with you."));
    mint("javascript:alert(1)");
    await userEvent.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(status()).toBe("Couldn't open this view."));
    expect(frame()).toBeNull();
  });

  it("a preview loads as one, and its 502 says it isn't answering", async () => {
    mint("https://app--s.p.example/__jam/frame?t=1");
    show("preview");
    expect(status()).toBe("Loading…");
    await waitFor(() => expect(frame()).not.toBeNull());
    expect(frame()!.getAttribute("title")).toBe("app");
    post(502);
    expect(status()).toBe("The preview isn't answering.");
  });

  it("re-mints when restored from the back/forward cache (its ticket is spent)", async () => {
    const names = mint("https://a.p.example/1", "https://a.p.example/2");
    show();
    await waitFor(() => expect(frame()).not.toBeNull());
    act(() => { window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true })); });
    await waitFor(() => expect(frame()!.getAttribute("src")).toBe("https://a.p.example/2"));
    act(() => { window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: false })); });
    expect(names).toHaveLength(2);
  });
});
```

`PageTransitionEvent` may be missing from jsdom. If it is, construct `Object.assign(new Event("pageshow"), { persisted: true })` instead, and ledger it.

- [ ] **Step 2: Run them to see them fail.**

Run: `cd web/app && npx vitest run src/session/frames`

Expected: FAIL, "Failed to resolve import ./FrameView".

- [ ] **Step 3: Implement.**

`useFrame.ts`:

```ts
import { useCallback, useEffect, useRef, useState } from "react";
import { apiPost, ApiError } from "../../api/client";
import type { FrameURL } from "../../api/types";

export type FrameKind = "ide" | "preview";

export interface FrameState {
  src: string; // the iframe's URL ("" = no frame)
  seq: number; // bumps on every mint: the iframe is keyed by it
  status: string;
  reopen: boolean; // show Reopen this view (a second signin)
  open: string; // Open in new tab ("" = the caller's plain URL)
  frame: React.RefObject<HTMLIFrameElement | null>;
  start(): void; // a user action: mint afresh, the retries re-armed
  loaded(): void;
}

const http = (u: unknown): u is string => typeof u === "string" && /^https?:\/\//i.test(u);
const MINT_REFUSED: Record<number, string> = { 403: "No longer shared with you.", 404: "Nothing to show.", 429: "Too many views opened; wait a minute." };

// useFrame is one framed view (the Studio's IDE, a preview's View here), as
// layout.html's meFrame* did: a URL comes only from the frame route, for this
// view's name; a frame-error is heeded only from this view's own current
// frame, and re-mints at most once per signin/cookies per user action.
export function useFrame(sid: string, name: string, kind: FrameKind): FrameState {
  const [s, set] = useState({ src: "", seq: 0, status: kind === "ide" ? "Starting the IDE…" : "Loading…", reopen: false, open: "" });
  const frame = useRef<HTMLIFrameElement | null>(null);
  const r = useRef({ seq: 0, err: false, signin: false, cookies: false, live: true });

  const mint = useCallback(() => {
    const c = r.current, n = ++c.seq;
    c.err = false;
    set((x) => ({ ...x, src: "", seq: n, reopen: false, status: kind === "ide" ? "Starting the IDE…" : "Loading…" }));
    apiPost<FrameURL>(`sessions/${encodeURIComponent(sid)}/frame`, { name }).then((j) => {
      if (!c.live || c.seq !== n) return;
      const open = http(j.open) ? j.open : "";
      if (!http(j.url)) { c.err = true; set((x) => ({ ...x, open: open || x.open, status: "Couldn't open this view." })); return; }
      set((x) => ({ ...x, src: j.url, open: open || x.open }));
    }, (e: unknown) => {
      if (!c.live || c.seq !== n) return;
      c.err = true;
      set((x) => ({ ...x, status: (e instanceof ApiError && MINT_REFUSED[e.status]) || "Couldn't open this view." }));
    });
  }, [sid, name, kind]);

  const start = useCallback(() => { r.current.signin = false; r.current.cookies = false; mint(); }, [mint]);

  useEffect(() => {
    const c = r.current;
    c.live = true;
    start();
    const onMessage = (e: MessageEvent) => {
      const d = e.data as { jam?: unknown; status?: unknown } | null;
      if (!d || typeof d !== "object" || d.jam !== "frame-error") return;
      const w = frame.current?.contentWindow;
      if (!w || e.source !== w) return; // only this view's current frame
      const st = String(d.status);
      if (st === "signin") {
        if (!c.signin) { c.signin = true; mint(); return; }
        c.err = true;
        set((x) => ({ ...x, status: "", reopen: true }));
        return;
      }
      if (st === "cookies") { // an expired sign-in says "cookies" too: try once more first
        if (!c.cookies) { c.cookies = true; mint(); return; }
        c.err = true;
        set((x) => ({ ...x, status: "This browser can't sign in inside the pane — use Open in new tab." }));
        return;
      }
      let t = ({ "403": "No longer shared with you.", "404": "Nothing to show: the studio isn't running." } as Record<string, string>)[st];
      if (!t && (st === "502" || st === "503" || st === "504")) {
        t = kind === "ide" ? "The IDE didn't start. This studio's image may predate it: upgrade the studio, or expose an IDE as ide." : "The preview isn't answering.";
      }
      c.err = true;
      set((x) => ({ ...x, status: t ?? "Couldn't show this view." }));
    };
    // Restored from the back/forward cache: the frame's ticket is spent.
    const onShow = (e: PageTransitionEvent) => { if (e.persisted) start(); };
    window.addEventListener("message", onMessage);
    window.addEventListener("pageshow", onShow);
    return () => {
      c.live = false;
      window.removeEventListener("message", onMessage);
      window.removeEventListener("pageshow", onShow);
    };
  }, [start, mint, kind]);

  return { ...s, frame, start, loaded: () => { if (!r.current.err) set((x) => ({ ...x, status: "" })); } };
}
```

`FrameView.tsx`:

```tsx
import type { ReactNode } from "react";
import { type FrameKind, useFrame } from "./useFrame";
import "./frames.css";

// FrameView is a framed view with its bar: lead (e.g. a back button), the
// status, Reload (reload), and Open in new tab (the mint's plain URL, else
// open). The iframe is keyed by mint, so a replaced frame is a new element.
export function FrameView({ sid, name, kind, open, lead, reload }: { sid: string; name: string; kind: FrameKind; open?: string; lead?: ReactNode; reload?: boolean }) {
  const f = useFrame(sid, name, kind);
  const href = f.open || open || "";
  return (
    <div className="st-box">
      <div className="st-bar">
        {lead}
        <span className="st-status">{f.status}{f.reopen && <button type="button" className="st-reopen" onClick={f.start}>Reopen this view</button>}</span>
        <span style={{ flex: 1 }} />
        {reload && <button type="button" className="st-reload" onClick={f.start}>Reload</button>}
        <a className="st-open" href={href || undefined} target="_blank" rel="noopener">Open in new tab</a>
      </div>
      <div className="st-frame">
        {f.src && (
          <iframe key={f.seq} ref={f.frame} className="st-iframe" title={kind === "ide" ? "IDE" : name} src={f.src}
            allow="clipboard-read; clipboard-write" referrerPolicy="origin" onLoad={f.loaded} />
        )}
      </div>
    </div>
  );
}
```

Notes:
- **Open in new tab:** `a.st-open` without an `href` has no link role. The tests query it by role, so render it only when `href` is set (`{href && <a …>}`).
- **Reopen and the status:** the Reopen button lives inside `.st-status`, as in the old code.

`frames.css`:

```css
.st-box { flex: 1; min-height: 0; display: flex; flex-direction: column; height: 100%; }
.st-bar { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; padding: 6px 10px; border-bottom: 1px solid var(--mantine-color-default-border); font-size: 12.5px; }
.st-status { color: var(--mantine-color-dimmed); }
.st-bar button { font: inherit; font-size: 12px; cursor: pointer; background: none; border: 1px solid var(--mantine-color-default-border); border-radius: 6px; padding: 2px 8px; color: inherit; }
.st-frame { flex: 1; min-height: 0; display: flex; }
.st-iframe { flex: 1; width: 100%; height: 100%; border: 0; background: #fff; }
```

Check the lint rules on two points:
- **Writing a ref during render.** If ESLint's react-hooks rules flag `loaded: () => { if (!r.current.err) … }` in the returned object, wrap it in a `useCallback`.
- **Closure order.** If the rules flag `mint`'s use inside the effect's closure, pass it through the deps (as written).

- [ ] **Step 4: Run them to see them pass.**

Run: `npx vitest run src/session/frames && npm run typecheck && npm run lint`

Expected: PASS.

- [ ] **Step 5: Commit.** Message: `jam app: framed views (mint, retries, message filter)`.

---

### Task 4: The Studio tab, kept alive, widening on first open

**Files:**
- Create: `web/app/src/session/StudioTab.tsx`
- Modify: `web/app/src/session/layout.ts`, `web/app/src/session/usePaneLayout.ts`, `web/app/src/session/SessionView.tsx`, `web/app/src/session/PaneTabs.tsx`, `web/app/src/session/SessionView.test.tsx`

**Interfaces:**
- Consumes: `FrameView` (Task 3); `usePaneTab(id, "studio")` → `SessionPane.studio` / `ideOpen` (Task 1).
- Produces:
  - `export default function StudioTab({ id }: { id: string })`: "The studio isn't available to you here." when `!studio`, else `<FrameView sid={id} name="ide" kind="ide" open={ideOpen} reload />` inside `div#studio[data-session]`;
  - `PaneState.toggle(t: PaneTab, size?: number)`: opening a tab with `size`, and no size saved for the current placement, saves `size` too;
  - `PaneState.hasSize: boolean`, true when a size is saved for the effective placement.
- In `SessionView`:
  - while the pane is open, the Studio stays mounted (hidden with `display: none`) after its first show;
  - closing the pane (or `tab` becoming `null`) unmounts it;
  - first opening Studio with `!pane.hasSize` passes 60 % of the split's size to `toggle`.

- [ ] **Step 1: Write the failing tests** (append to `SessionView.test.tsx`'s pane `describe`):

```tsx
  it("opens Studio framed (its own chunk), widening to 60% on a first open, and keeps it across tabs", async () => {
    let mints = 0;
    server.use(http.post("/app/user/api/sessions/:id/frame", () => { mints++; return HttpResponse.json({ url: `https://ide--s.p.example/__jam/frame?t=${mints}`, open: "https://ide--s.p.example/" }); }));
    serve(head({ studio: true }), { studio: pane("studio", { studio: true, ideOpen: "https://ide--s.p.example/" }) });
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: 1000, height: 800 } as DOMRect);
    const user = userEvent.setup();
    renderApp(boot, "/app/next/c/chn_s");
    await user.click(await screen.findByRole("tab", { name: "Studio" }));
    await waitFor(() => expect(document.querySelector("#studio iframe.st-iframe")).not.toBeNull());
    expect(saved("ses_1")).toEqual({ tab: "studio", place: "right", w: 600 });
    const f = document.querySelector("#studio iframe.st-iframe");
    await user.click(screen.getByRole("tab", { name: "Status" }));
    expect(document.querySelector<HTMLElement>("#studio")!.closest<HTMLElement>("[data-keep]")!.style.display).toBe("none");
    await user.click(screen.getByRole("tab", { name: "Studio" }));
    expect(document.querySelector("#studio iframe.st-iframe")).toBe(f); // the same frame
    expect(mints).toBe(1);
    // Closing the pane tears it down; the next open mints afresh.
    await user.click(screen.getByRole("button", { name: "Close the pane" }));
    expect(document.querySelector("#studio")).toBeNull();
    await user.click(screen.getByRole("tab", { name: "Studio" }));
    await waitFor(() => expect(mints).toBe(2));
    expect(saved("ses_1").w).toBe(600); // a saved size isn't widened again
  });

  it("says the studio isn't available when the pane says so", async () => {
    serve(head({ studio: true }), { studio: pane("studio", { studio: false, ideOpen: "" }) });
    const user = userEvent.setup();
    renderApp(boot, "/app/next/c/chn_s");
    await user.click(await screen.findByRole("tab", { name: "Studio" }));
    expect(await screen.findByText("The studio isn't available to you here.")).toBeInTheDocument();
  });
```

`pane()`'s factory in this file gains `studio: false, ideOpen: ""`, now that `SessionPane` carries them.

Remove the Studio-link assertion from "lists previews, artifacts and status, and links Studio to the current Inbox" (rename it "lists previews, artifacts and status"), along with its `head({ studio: true })`. The placeholder is gone.

- [ ] **Step 2: Run them to see them fail.**

Run: `npx vitest run src/session/SessionView.test.tsx`

Expected: FAIL. The Studio tab still renders the placeholder, so no `#studio` exists.

- [ ] **Step 3: Implement.**

In `layout.ts`'s `PaneState`, change the line to `toggle(t: PaneTab, size?: number): void;` and add `hasSize: boolean;`.

In `usePaneLayout.ts`:

```ts
    hasSize: layout[key] !== undefined,
    toggle: (t: PaneTab, size?: number) => update({
      ...layout, tab: tab === t ? null : t, ...(size && tab !== t && layout[key] === undefined ? { [key]: Math.round(size) } : {}),
    }),
```

`StudioTab.tsx`:

```tsx
import { Loader, Text } from "@mantine/core";
import { FrameView } from "./frames/FrameView";
import { usePaneTab } from "./useSession";

// StudioTab is the session pane's Studio (a lazy chunk): the session's IDE,
// framed (FrameView). SessionView keeps it mounted across tabs, so the IDE
// keeps its state; the pane closing ends it.
export default function StudioTab({ id }: { id: string }) {
  const q = usePaneTab(id, "studio");
  if (q.isPending) return <Loader size="sm" m="sm" />;
  if (!q.data?.studio) return <Text size="sm" c="dimmed" p="sm">The studio isn't available to you here.</Text>;
  return (
    <div id="studio" data-session={id} style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: "column", height: "100%" }}>
      <FrameView sid={id} name="ide" kind="ide" open={q.data.ideOpen} reload />
    </div>
  );
}
```

`usePaneTab`'s 30 s poll refetches the Studio pane. That only re-renders it, because `FrameView`'s identity is stable, so it doesn't re-mint.

`SessionView.tsx`:

```tsx
const StudioTab = lazy(() => import("./StudioTab"));
// …
  const [studioKept, setStudioKept] = useState(false);
  // The Studio stays mounted (hidden) once shown, until the pane closes.
  if (tab === "studio" && !studioKept) setStudioKept(true);
  if (tab === null && studioKept) setStudioKept(false);
  const onTab = (t: PaneTab) => {
    if (t === "studio" && tab !== "studio" && !pane.hasSize && split.current) {
      const r = split.current.getBoundingClientRect();
      pane.toggle(t, (right ? r.width : r.height) * 0.6);
      return;
    }
    pane.toggle(t);
  };
```

Notes:
- **The two state adjustments** are React's "adjusting state while rendering" pattern, the same one `usePaneLayout` uses.
- **Header:** pass `onTab` to `SessionHeader`.
- **Pane body:** inside the `aside`, the body renders `{tab !== "studio" && <PaneBody …/>}`.
- **The kept Studio:**

```tsx
              {studioKept && (
                <div data-keep role="tabpanel" aria-label="Studio" style={{ display: tab === "studio" ? "flex" : "none", flex: 1, minHeight: 0, flexDirection: "column" }}>
                  <Suspense fallback={<Loader size="sm" m="sm" />}><StudioTab id={h!.id} /></Suspense>
                </div>
              )}
```

- **The body's style:** with `tab === "studio"`, hide the body div (`display: "none"`) and give it no tabpanel role, so only one panel shows.
- **Imports:** `lazy`, `Suspense`, `useState` and `Loader`.

`PaneTabs.tsx`: delete the Studio placeholder branch. `PaneBody` is never called with `"studio"` now, so narrow its `tab` type to `Exclude<PaneTab, "studio">`, or keep the type and return `null` for it.

- [ ] **Step 4: Run them to see them pass.**

Run: `npx vitest run && npm run typecheck && npm run lint`

Expected: PASS.

- [ ] **Step 5: Commit.** Message: `jam app: the Studio tab (lazy, kept across tabs)`.

---

### Task 5: Previews "View here"

**Files:**
- Modify: `web/app/src/session/PaneTabs.tsx`, `web/app/src/session/SessionView.test.tsx`

**Interfaces:**
- Consumes: `FrameView` (Task 3); `PreviewLink.frame`.
- Produces:
  - each preview with `frame` gets a "View here" button;
  - clicking it replaces the list with `<FrameView sid name kind="preview" lead={<>← All previews button, <b>{name}</b></>} />` inside `div.pv-framed`;
  - "← All previews" (`.pv-back`) returns to the list;
  - the framed state lives in the Previews component, so leaving the tab (unmount) closes it, while a refetch keeps it.

- [ ] **Step 1: Write the failing test** (append to the pane `describe`):

```tsx
  it("frames a preview with View here, keeps it across a refresh, and goes back", async () => {
    let mints = 0;
    server.use(http.post("/app/user/api/sessions/:id/frame", async ({ request }) => {
      mints++;
      const { name } = (await request.json()) as { name: string };
      return HttpResponse.json({ url: `https://${name}--s.p.example/__jam/frame?t=${mints}`, open: `https://${name}--s.p.example/` });
    }));
    serve(head(), { previews: pane("previews", { previews: [{ name: "web", url: "https://web--s.p.example/", port: 5173, frame: true }, { name: "api", url: "https://api--s.p.example/", port: 8080, frame: false }] }) });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    renderApp(boot, "/app/next/c/chn_s", client);
    await user.click(await screen.findByRole("tab", { name: "Previews" }));
    expect(await screen.findAllByRole("button", { name: "View here" })).toHaveLength(1); // api can't be framed
    await user.click(screen.getByRole("button", { name: "View here" }));
    await waitFor(() => expect(document.querySelector(".pv-framed iframe.st-iframe")).not.toBeNull());
    const f = document.querySelector(".pv-framed iframe.st-iframe")!;
    expect(f.getAttribute("title")).toBe("web");
    await client.invalidateQueries({ queryKey: ["session"] });
    await waitFor(() => expect(client.isFetching()).toBe(0));
    expect(document.querySelector(".pv-framed iframe.st-iframe")).toBe(f);
    await user.click(screen.getByRole("button", { name: "← All previews" }));
    expect(await screen.findByRole("link", { name: /web.*port 5173/ })).toBeInTheDocument();
    expect(document.querySelector(".pv-framed")).toBeNull();
    // Leaving the tab closes a framed preview.
    await user.click(screen.getByRole("button", { name: "View here" }));
    await waitFor(() => expect(document.querySelector(".pv-framed")).not.toBeNull());
    await user.click(screen.getByRole("tab", { name: "Status" }));
    await user.click(screen.getByRole("tab", { name: "Previews" }));
    expect(await screen.findByRole("button", { name: "View here" })).toBeInTheDocument();
    expect(document.querySelector(".pv-framed")).toBeNull();
  });
```

- [ ] **Step 2: Run it to see it fail.**

Run: `npx vitest run src/session/SessionView.test.tsx`

Expected: FAIL, because there's no "View here" button.

- [ ] **Step 3: Implement** (`PaneTabs.tsx`'s `Previews`):

```tsx
function Previews({ p }: { p: SessionPane }) {
  const [framed, setFramed] = useState<string | null>(null);
  if (p.previewsOff) return <Text size="sm" c="dimmed">Previews aren't available to you here.</Text>;
  if (framed) {
    return (
      <div className="pv-framed" style={{ display: "flex", flexDirection: "column", height: "100%", margin: "-8px -12px -14px" }}>
        <FrameView sid={p.id} name={framed} kind="preview"
          lead={<><button type="button" className="pv-back" onClick={() => setFramed(null)}>← All previews</button><b className="pv-name">{framed}</b></>} />
      </div>
    );
  }
  if (p.previews.length === 0) return <Text size="sm" c="dimmed">No previews exposed. A session exposes one with its <code>expose</code> tool.</Text>;
  return (
    <Stack gap={4}>
      {p.previews.map((v) => (
        <Group key={v.name} gap="xs" wrap="nowrap" justify="space-between">
          <Anchor href={v.url} target="_blank" rel="noopener" size="sm">{v.name} <Text span size="xs" c="dimmed">port {v.port} ↗</Text></Anchor>
          {v.frame && <Button size="compact-xs" variant="default" className="pv-here" onClick={() => setFramed(v.name)}>View here</Button>}
        </Group>
      ))}
    </Stack>
  );
}
```

Notes:
- `FrameView`'s `sid` is the pane's `id` (the session id).
- Import `useState`, `Group`, `Button` and `FrameView`.
- The negative margin cancels the pane body's padding, so the frame fills the pane. That matches the old `.sp-body:has(> .pv-framed){padding:0}`.

- [ ] **Step 4: Run them to see them pass.**

Run: `npx vitest run && npm run typecheck && npm run lint`

Expected: PASS.

- [ ] **Step 5: Commit.** Message: `jam app: Previews View here`.

---

### Task 6: End-to-end under the CSP, and docs

**Files:**
- Modify: `internal/jam/userui/next_browser_test.go`, `docs/web-app.md`, `docs/usage/jam/intercom-ui.md`, `docs/usage/jam/previews.md`, `docs/superpowers/specs/2026-10-26-jam-spa-design.md`

- [ ] **Step 1: Write the browser test** (append to `next_browser_test.go`; `framePage` and `studioFrames` are in `studio_browser_test.go`, which has the same build tag):

```go
// The app frames the Studio's IDE and a preview's View here from the frame
// route, under a CSP whose frame-src is the preview origin: the frame loads
// (status clears), a forged message from the page itself changes nothing,
// and a real frame-error from the frame is shown.
func TestBrowserNextStudio(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p, personal, _, _ := frameEnv(t)
	pv := httptest.NewServer(http.HandlerFunc(framePage))
	t.Cleanup(pv.Close)
	ff := &studioFrames{base: pv.URL, n: map[string]int{}, next: map[string][]string{}}
	e.Deps.Frames = ff
	api, app := Handler(e.Deps, nil), Next(e.Deps, nil, []string{pv.URL})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = asPrincipal(r, p, false)
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	has := func(js string) step { return chromedp.Poll[chromedp.Void](js, chromedp.WithPollingTimeout(5*time.Second)) }
	st := `(document.querySelector('#studio .st-status')||{textContent:'?'}).textContent`
	var problems pageProblems
	err := chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.EmulateViewport(1400, 900),
		chromedp.Navigate(srv.URL+"/app/next/c/"+e.homeOf(t, personal)),
		chromedp.Click(chromedp.CSS(`[role="tab"][data-tab="studio"]`)),
		has(`!!document.querySelector('#studio iframe.st-iframe') && `+st+`===''`), // loaded
		chromedp.Evaluate[chromedp.Void](`window.postMessage({jam:'frame-error',status:403},'*')`),
		chromedp.Sleep(300*time.Millisecond),
		has(st+`===''`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if n := ff.mints("ide"); n != 1 {
		t.Fatalf("ide mints = %d, want 1", n)
	}
	// A real refusal from the frame itself.
	ff.queue("ide", "post=403")
	if err := chromedp.Do(ctx,
		chromedp.Click(chromedp.CSS(`#studio .st-reload`)),
		has(st+`==='No longer shared with you.'`),
		// View here frames a preview the same way.
		chromedp.Click(chromedp.CSS(`[role="tab"][data-tab="previews"]`)),
		chromedp.Click(chromedp.CSS(`.pv-here`)),
		has(`!!document.querySelector('.pv-framed iframe.st-iframe') && document.querySelector('.pv-framed .st-status').textContent===''`),
	); err != nil {
		t.Fatal(err)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}
```

Notes:
- With no `frame-src` match, Chrome blocks the frame and logs a Log-domain `security` entry, which `pageProblems` would report. So this test also proves the CSP admits the preview origin.
- To see the negative, add one line after the loop: build `Next(e.Deps, nil, nil)` and check that it reports a CSP problem. That's optional, and should be skipped if it makes the test flaky.

Run: `just app && GOPROXY=https://proxy.golang.org go test -tags browser -run 'TestBrowserNext|TestBrowserStudio' ./internal/jam/userui/`

Expected: PASS, with the old Studio browser tests unchanged.

- [ ] **Step 2: Docs.**

In `docs/web-app.md`'s Layout, add after the `src/session/thinking/` bullet:

```markdown
  - `src/session/frames/` holds the framed views. `useFrame` mints with `POST /app/user/api/sessions/{id}/frame` (it shares `frameOutcome` with the form route), heeds a `frame-error` only from its own current frame, and re-mints at most once per signin/cookies per user action. `StudioTab` is a lazy chunk that `SessionView` keeps mounted across tabs. Previews' View here uses the same `FrameView`. The app page's CSP `frame-src` is `appFrameSrc` (cmd/at-jam): the preview hosts' wildcard on the admin port, when pane framing is on.
```

In `docs/usage/jam/intercom-ui.md`'s preview section, change "(Previews, Artifacts, Status and Thinking; Studio links here for now)" to "(Studio, Thinking, Previews with View here, Artifacts and Status)".

In `docs/usage/jam/previews.md`'s framing section, after the `frame-ancestors` paragraph, add one sentence. That doc owns framing, so the fact belongs there:

```markdown
The Jam app's page (`/app/next/`) allows frames only from the preview hosts (`frame-src https://*.<domain>[:port]`, the admin port) when the pane may frame them, and none otherwise.
```

In the spec's status line, change it to `slices 1–6a merged; slice 6b (Studio) in progress ([plan](../plans/2026-10-26-jam-spa-6b-studio.md))`.

- [ ] **Step 3: Full verification.**

Run:
- `just app`;
- `just app-check`;
- `./scripts/app-types-check.sh`;
- `GOPROXY=https://proxy.golang.org go test ./internal/jam/... ./cmd/at-jam/`;
- `GOPROXY=https://proxy.golang.org go test -tags browser ./internal/jam/userui/`;
- `STRICT=1 ./scripts/lint.sh`.

Expected: all PASS.

- [ ] **Step 4: Commit.** Message: `jam app: Studio end-to-end under the CSP; docs`.

---

## Self-review notes

- **Spec coverage:**
  - the framed IDE, ticket mint and fallback: Tasks 1, 3 and 4;
  - Previews "View here": Task 5;
  - the frame boundary, unchanged: Task 3's attributes and filter, and Task 1 sharing `frameOutcome`;
  - the CSP `frame-src`: Tasks 2 and 6;
  - the lazy chunk: Task 4 (`StudioTab`).
- **Parity checklist** (from `studio_browser_test.go`'s numbered cases):
  - (1) the mint and load: Task 3;
  - (2) kept across tabs: Task 4;
  - (3) Reload, and a replaced frame's late message: Task 3;
  - (4) a refusal survives load: Task 3;
  - (5) forged messages and the bfcache: Task 3;
  - (6) View here: Task 5;
  - (7) the 60 % widening: Task 4;
  - the edges (remembered Studio without ssh: slice 5; refused-mint Open in new tab, 429, bad scheme, preview 502): Task 3.
- **Not ported:** the old `pagehide` teardown of frames. React unmounts frames on navigation, and `pageshow` (persisted) re-mints, which is the behavior the old pair existed for.
- **Type consistency:**
  - `FrameURL` and `FrameRequest` are the same in Go, TS and the tests.
  - `PaneState.toggle(t, size?)` and `hasSize` are used by `SessionView` only.
