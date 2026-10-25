# Session studio view Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a Studio tab (built-in code-server IDE, gated on SSH) and inline previews in the `/app/user` session pane, framed through a frame-only login on the preview hosts.

**Architecture:**
1. code-server is baked into `cove-base-image`. cove-master starts it lazily on `127.0.0.1:13337` when Jam opens a tunnel there, and stops it when idle.
2. Jam's preview proxy resolves the name `ide` to a kit exposure or to the built-in port, and authorises it with the SSH right.
3. A frame ticket store, a `/__jam/frame` exchange and a partitioned `__Host-jam_frame` cookie let the Jam UI frame preview hosts; `frame-ancestors` limits framing to the UI origins.
4. userui gets a frame-minting route, a Studio tab and "View here" on previews.

**Tech Stack:** Go (net/http, html/template), vanilla JS, Docker (Ubuntu 24.04 base), chromedp browser tests.

**Spec:** `docs/superpowers/specs/2026-10-25-session-studio-view-design.md`

## Global Constraints

- Repo rules (AGENTS.md):
  - TDD: write the failing test first.
  - Tests are hermetic: no network, no Docker in `go test`.
  - Docs are updated in the same PR.
  - Never log secrets or tickets.
  - The hardening layer is untouched.
- `gofmt`; `bash scripts/lint.sh` and `go test ./...` pass; browser tests use `go test -tags browser ./internal/jam/userui/`.
- Commit trailer: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Values from the spec:

| Setting | Value |
|---|---|
| IDE port | **13337** (`preview.IDEPort`) |
| IDE start wait | **15 s** |
| IDE idle stop | **30 min**, SIGTERM then SIGKILL after **10 s** |
| Frame ticket | **32** random bytes, stored SHA-256, single-use, **60 s** |
| Frame session | **8 h** |
| Cookie | `__Host-jam_frame`: `Secure; HttpOnly; Path=/; SameSite=None; Partitioned` |
| Pane widen | **60%** on the Studio tab's first open |

- code-server command: `code-server --bind-addr 127.0.0.1:13337 --auth none --disable-telemetry --disable-update-check /home/agent/workspace`.
- Rights:
  - `ide` → `collab.SSH`;
  - every other preview name → `collab.Preview`;
  - the Studio tab shows only when `collab.Decide(…, collab.SSH)` allows.
- Framing headers: `Content-Security-Policy: frame-ancestors <ui-origins…>`, or `frame-ancestors 'none'` when no UI origins are configured.
- Delivery: four PRs, each on its own branch from main after the previous one merges.
  - PR1 = Tasks 1–3 (`feat/studio-ide`)
  - PR2 = Task 4 (`feat/ide-routing`)
  - PR3 = Tasks 5–7 (`feat/preview-framing`)
  - PR4 = Tasks 8–10 (`feat/studio-tab`)

## Review Focus

1. **A preview-only grantee requests `ide`:** they must get 403, never the IDE. (Task 4)
2. **A ticket replayed or used on another host:** 400. A ticket opened top-level (not in an iframe): 400. (Task 6)
3. **Another studio's page, framed in the same Jam page, fetches this studio's host with the frame cookie:** 403 cross-site. (Task 6)
4. **Exposure changes while the IDE is open:** `CloseUnexposed` must not close IDE connections on 13337 when `ide` is built-in. (Task 4)
5. **A studio on an old image (no code-server):** the IDE route gives a clear 502/504, and the tab explains it rather than hanging. (Tasks 3, 7, 9)

---

## PR1 — `feat/studio-ide`

### Task 1: Reserve the IDE port

**Files:**
- Modify: `internal/jam/preview/host.go` (consts, `ValidPort`)
- Test: `internal/jam/preview/host_test.go`
- Modify: `docs/usage/jam/previews.md`, the "Exposing a port" sentence listing refused ports.

**Interfaces:**
- Produces: `preview.IDEPort = 13337` and `preview.IDEName = "ide"`.

- [ ] **Step 1: Write the failing test.**

  ```go
  func TestIDEPortReserved(t *testing.T) {
  	if err := ValidPort(IDEPort); err == nil {
  		t.Fatal("the IDE port must not be exposable")
  	}
  	if err := ValidName(IDEName); err != nil {
  		t.Fatalf("ide must stay a valid name (a kit overrides the built-in IDE by exposing it): %v", err)
  	}
  }
  ```

- [ ] **Step 2: Run it and watch it fail.** `go test ./internal/jam/preview/` fails with "undefined: IDEPort".
- [ ] **Step 3: Implement.**
  - Add the consts, each with a doc comment. `IDEPort` is the built-in code-server port: cove-master tunnels to it, and it can never be exposed. `IDEName` is the preview name of the Studio tab's IDE.
  - Make `ValidPort` refuse `IDEPort`. The message lists it too: `not %d (sshd), %d (egress proxy) or %d (the built-in IDE)`.
- [ ] **Step 4: Run the tests.** `go test ./internal/jam/... ./cmd/...` passes. If any existing test asserts the old error text, update it.
- [ ] **Step 5: Update the docs and commit.** In previews.md, add "or 13337 (the built-in IDE, see [A web IDE](#a-web-ide))" to the refused ports and bump `updated`. Commit message: `feat(preview): reserve port 13337 for the built-in IDE`.

### Task 2: code-server in the base image

**Files:**
- Modify: `images/cove-base-image/Dockerfile`
- Modify: `renovate.json` (a regex custom manager for `CODE_SERVER_VERSION`)
- Modify: `.github/workflows/release.yml`, the "Smoke-test cove-image" step: add `code-server --version`.
- Modify: `docs/OVERVIEW.md`, the paragraph on what `cove-base-image` carries (around line 434): mention code-server for the built-in IDE.

**Interfaces:**
- Produces: `/usr/bin/code-server` in every studio image.

- [ ] **Step 1: Add the install.** It goes after the apt layer and before the user creation. Use the official release `.deb`, which installs `/usr/bin/code-server`, and verify it per architecture:

  ```dockerfile
  # code-server: the built-in Studio IDE (docs/superpowers/specs/2026-10-25-session-studio-view-design.md).
  # Renovate bumps CODE_SERVER_VERSION; update both SHA-256s with it (the build fails on a mismatch).
  ARG CODE_SERVER_VERSION=4.105.1
  ARG CODE_SERVER_SHA256_AMD64=<sha256 of code-server_${CODE_SERVER_VERSION}_amd64.deb>
  ARG CODE_SERVER_SHA256_ARM64=<sha256 of code-server_${CODE_SERVER_VERSION}_arm64.deb>
  RUN set -eux; arch="$(dpkg --print-architecture)"; \
      case "$arch" in amd64) sum="$CODE_SERVER_SHA256_AMD64";; arm64) sum="$CODE_SERVER_SHA256_ARM64";; *) echo "unsupported $arch"; exit 1;; esac; \
      curl -fsSL -o /tmp/cs.deb "https://github.com/coder/code-server/releases/download/v${CODE_SERVER_VERSION}/code-server_${CODE_SERVER_VERSION}_${arch}.deb"; \
      echo "${sum}  /tmp/cs.deb" | sha256sum -c -; \
      apt-get install -y --no-install-recommends /tmp/cs.deb; rm -f /tmp/cs.deb; \
      code-server --version
  ```

  - The implementer picks the latest stable code-server release and fills in the real SHA-256 values.
  - Get the hashes by downloading both `.deb` files over the network allowed for github.com: `curl -fsSL … | sha256sum`. github.com is on this sandbox's egress list.
  - If the release has a published checksum asset, cross-check against it.
  - `curl` must already be installed in the base. Check the apt list, and add it pinned if missing.
  - Also check `hadolint` (`bash scripts/lint.sh`) passes: DL3008 etc. Follow the existing `.hadolint.yaml` ignores.
- [ ] **Step 2: Add the Renovate manager** in `renovate.json`, next to the existing regex managers:

  ```json
  {"customType": "regex", "fileMatch": ["^images/cove-base-image/Dockerfile$"],
   "matchStrings": ["ARG CODE_SERVER_VERSION=(?<currentValue>\\S+)"],
   "datasourceTemplate": "github-releases", "depNameTemplate": "coder/code-server", "extractVersionTemplate": "^v(?<version>.*)$"}
  ```

  Match the file's existing key style (`managerFilePatterns` vs `fileMatch`), copying a neighbouring manager.
- [ ] **Step 3: Add the smoke test.** Add `code-server --version` to the cove-image smoke step in release.yml.
- [ ] **Step 4: Verify locally.** There is no Docker here, so run `bash scripts/lint.sh`, which includes hadolint and JSON checks if present. Validate JSON with `python3 -m json.tool renovate.json`.
- [ ] **Step 5: Docs and commit.** Update OVERVIEW.md, then commit with the message `feat(image): code-server in the base image for the built-in IDE`.

### Task 3: cove-master's IDE supervisor

**Files:**
- Create: `internal/covemaster/ide.go` and `internal/covemaster/ide_test.go`
- Modify: `internal/covemaster/tunnel.go` (`allowed`, and `dialBack` routes 13337 through the supervisor)
- Modify: `internal/covemaster/covemaster.go` (Config gains `IDE *IDE`)
- Modify: `cmd/cove-master/main.go` (`buildConfig` sets `IDE: covemaster.NewIDE(covemaster.IDEConfig{}, log)`)

**Interfaces:**
- Consumes: `preview.IDEPort` (Task 1).
- Produces:

  ```go
  // IDEConfig tunes the built-in IDE; zero values mean the spec's defaults.
  type IDEConfig struct {
  	Bin       string        // "code-server" (looked up on PATH)
  	Workspace string        // "/home/agent/workspace"
  	Addr      string        // "127.0.0.1:13337"
  	StartWait time.Duration // 15s
  	Idle      time.Duration // 30m
  	Grace     time.Duration // 10s (SIGTERM → SIGKILL)
  }
  type IDE struct{ /* mu, cmd, open int, idle *time.Timer, log */ }
  func NewIDE(cfg IDEConfig, log *slog.Logger) *IDE
  // Acquire ensures code-server is running and accepting on Addr (starting it
  // if needed, waiting up to StartWait) and counts one open tunnel; the caller
  // must Release when the tunnel ends. ErrIDEUnavailable when Bin is missing.
  func (i *IDE) Acquire(ctx context.Context) error
  func (i *IDE) Release()
  var ErrIDEUnavailable = errors.New("ide unavailable: code-server not installed")
  ```

- **Behaviour:**
  - `allowed(port)` returns true for `preview.IDEPort` when `c.cfg.IDE != nil`.
  - In `dialBack`, when `ot.GetPort() == preview.IDEPort`:
    1. Call `c.cfg.IDE.Acquire(ctx)`. On error, log `tunnel: ide` with the error and return. Jam then sees its dial-back time out, which gives the existing 504.
    2. `defer c.cfg.IDE.Release()`.
    3. Continue to the normal dial.
  - `Acquire`:
    - If not running, `exec.LookPath(Bin)` (missing gives `ErrIDEUnavailable`). Start `Bin --bind-addr Addr --auth none --disable-telemetry --disable-update-check Workspace` with `Setpgid`, stdout and stderr to the log at debug, `cmd.Cancel` sending SIGTERM, and `WaitDelay = Grace`.
    - Log `ide started` with the pid.
    - Poll `net.DialTimeout("tcp", Addr, 200ms)` every 100 ms until success or `StartWait`. On timeout, kill the process and return an error.
    - Increment `open` and stop any idle timer.
    - A process that exits on its own (reaped by a goroutine on `cmd.Wait`) clears the running state, and the next Acquire restarts it.
  - `Release`:
    - Decrement `open`. At 0, start an idle timer of `Idle`.
    - When the timer fires with `open == 0`, send SIGTERM to the process group, SIGKILL after `Grace`, and log `ide stopped` (reason idle).
  - Concurrent Acquires during a start share one process: a `starting` channel under the mutex.

- [ ] **Step 1: Write the failing tests** in `ide_test.go`. Use a fake binary: a shell script written to `t.TempDir()` that listens on the given `--bind-addr` port. Use `python3 -c`, or better a tiny Go helper via the `TestMain` re-exec pattern:
  - `os.Args[0]` with env `IDE_FAKE=1` runs a listener on the address parsed from `--bind-addr` until SIGTERM.
  - Set `Bin` to a script that execs `os.Args[0]` with `IDE_FAKE=1`.
  - Pick a free port: `net.Listen` on `:0`, close it, use the address.

  Cases:
  - Acquire starts the process, and the port then accepts.
  - A second Acquire reuses it (one pid).
  - Release, then `Idle = 200ms`, stops it (the port refuses and the process has exited).
  - Acquire after a stop restarts it.
  - A missing `Bin` gives `ErrIDEUnavailable`.
  - `StartWait = 300ms` with a binary that never listens gives an error, and the process is killed.
  - Concurrent Acquires (10 goroutines) start exactly one process. Count the starts with a counter file the fake appends to.

  In `tunnel_test.go`:
  - `TestDialBackIDEPortStartsIDE`: an `IDE` whose fake Acquire is observed. Use an interface seam if simpler: `Config.IDE` as an interface `ideRunner{Acquire(ctx) error; Release()}` with `*IDE` implementing it. Opening a tunnel to 13337 calls Acquire, then dials `127.0.0.1:13337` via `TunnelDial`, and Release runs when the tunnel ends.
  - `TestIDEPortRefusedWithoutIDE`: `Config.IDE == nil` refuses 13337.
- [ ] **Step 2: Run them and watch them fail.**
- [ ] **Step 3: Implement** `ide.go` and the tunnel and config changes. Wire `cmd/cove-master/main.go`.
- [ ] **Step 4: Run the tests.** `go test ./internal/covemaster/ ./cmd/cove-master/`, then the full `go test ./...`, then `bash scripts/lint.sh`.
- [ ] **Step 5: Docs and commit.**
  - In previews.md "A web IDE", replace the paragraph with: every studio has a built-in code-server, started on first use and stopped after 30 idle minutes. It opens from the Studio tab ([session-view.md](session-view.md)). A kit can still provide its own by exposing it as `ide`, which takes precedence.
  - Leave the Jam routing details to Task 4.
  - Commit message: `feat(covemaster): lazily run the built-in IDE on 127.0.0.1:13337`.

---

## PR2 — `feat/ide-routing`

### Task 4: Jam routes `ide` with the SSH right

**Files:**
- Modify: `internal/jam/collabhttp/preview.go`
  - `serve`: resolve the port and the right.
  - `previewTarget`: add `right collab.Right`, included in `poolHost()`.
  - `dial`: use `t.right` for `liveEntry` and the re-check.
  - `proxyError`: the 403 text uses the right.
- Modify: `internal/jam/collabhttp/live.go`: `CloseUnexposed` must skip entries on `preview.IDEPort`.
- Test: `internal/jam/collabhttp/preview_test.go` and `internal/jam/collabhttp/live_test.go` (or wherever `CloseUnexposed` is tested).
- Docs: `docs/usage/jam/previews.md` and `docs/usage/jam/collaboration.md`.

**Interfaces:**
- Consumes: `preview.IDEPort` and `preview.IDEName` (Task 1).
- Produces: `func previewPortAndRight(exps []jam.Exposure, name string) (port int, right collab.Right, ok bool)`. For `ide`, an exposure named `ide` gives its port; otherwise `preview.IDEPort`; the right is always `collab.SSH`. Any other name gives its exposure with `collab.Preview`, or `!ok`.

- **Behaviour:**
  - `serve` replaces `exposedPort` with `previewPortAndRight`, then calls `authorizeInst(…, right)`.
  - `audit` records the right.
  - `poolHost()` hashes `user\0port\0actorID\0right`, so a connection pooled under one right is never reused under the other.
  - `errPreviewAccessEnded` gives 403 `"you may not use <right> on this studio"`, using `t.right`. `proxyError` needs the right: pass it through `previewRequest` (it already carries fields), or look it up by host.
  - `CloseUnexposed(session, exposed)`: entries whose `port == preview.IDEPort` are never closed by this sweep. A kit exposure named `ide` on another port follows the normal rule.

- [ ] **Step 1: Write the failing tests**, using the existing preview_test fixtures. Read how the tests stub Tunnels, Exposures and grants.
  - **SSH-only principal:** with no exposure named `ide`, requesting `ide--<label>` dials port 13337 (assert on the fake Tunnels' `OpenPreview` port) and returns 200.
  - **Preview-only grantee:** gets 403 `you may not use ssh on this studio`.
  - **Kit override:** an exposure `ide → 8443` makes the proxy dial 8443.
  - **Normal names:** an exposure `app` still uses Preview. An SSH-only grantee (a grant with rights `ssh` only) gets 403 on `app`.
  - **Pool key:** `poolHost` differs between Preview and SSH for the same user, port and actor.
  - **Live:** `CloseUnexposed(session, nil)` leaves an entry on 13337 open and closes one on 8080.
- [ ] **Step 2: Run them and watch them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests.** `go test ./internal/jam/collabhttp/ ./cmd/at-jam/`, then the full suite, then lint.
- [ ] **Step 5: Docs and commit.**
  - previews.md: add a "The built-in IDE (`ide`)" note. `ide--<label>.<domain>` is the studio's IDE. It needs the **ssh** right, not preview. A kit exposure named `ide` replaces the built-in one. Port 13337 is reserved.
  - collaboration.md "Who may connect": the `ide` preview checks `ssh`.
  - Bump `updated` on both.
  - Commit message: `feat(collabhttp): route the ide preview to the built-in IDE under the ssh right`.

---

## PR3 — `feat/preview-framing`

### Task 5: Frame tickets

**Files:**
- Create: `internal/jam/collabhttp/frame.go` and `internal/jam/collabhttp/frame_test.go`

**Interfaces:**
- Produces:

  ```go
  // FrameTicketTTL bounds a frame ticket; FrameSessionTTL a frame session.
  const FrameTicketTTL = 60 * time.Second
  const FrameSessionTTL = 8 * time.Hour
  // FrameTickets mints and redeems one-time frame tickets (docs/usage/jam/previews.md#framing).
  type FrameTickets struct{ /* mu; m map[[32]byte]frameTicket; now func() time.Time */ }
  type frameTicket struct {
  	principal collab.Principal
  	name      string // display name for audit / X-Jam-User
  	host      string // canonical preview host it is for
  	right     collab.Right
  	expires   time.Time
  }
  func NewFrameTickets(now func() time.Time) *FrameTickets
  // Mint returns a new raw ticket (base64url of 32 random bytes) for host.
  func (f *FrameTickets) Mint(p collab.Principal, name, host string, right collab.Right) (string, error)
  // Redeem consumes raw if it exists, is unexpired and is for host.
  func (f *FrameTickets) Redeem(raw, host string) (frameTicket, bool)
  func (f *FrameTickets) Prune()
  ```

  Redeem deletes the ticket on any lookup that finds it, even for the wrong host. A probed ticket is spent.

- [ ] **Step 1: Write the failing tests:**
  - mint, then redeem on the right host, is ok;
  - a second redeem fails;
  - the wrong host fails, and the ticket is then also gone for the right host;
  - after 61 s (fake clock) it fails;
  - Prune removes expired tickets;
  - the raw ticket is ≥ 43 chars of base64url;
  - the map is keyed by hash: the raw ticket never appears as a map key (assert `len` and not storing raw, via an internal test).
- [ ] **Step 2: Run them and watch them fail.**
- [ ] **Step 3: Implement**, using `newSecret()` from previewauth.go.
- [ ] **Step 4: Run the tests.**
- [ ] **Step 5: Commit.** Message: `feat(collabhttp): one-time frame tickets`.

### Task 6: The frame exchange, cookie and session

**Files:**
- Modify: `internal/jam/collabhttp/previewauth.go`
  - `PreviewAuthOptions` gains `Frames *FrameTickets`.
  - `PreviewHostRoutes`: `GET /__jam/frame`.
  - New `frameSessions` map, pruned in `prune()`.
  - `Principal` accepts the frame cookie.
  - `LoginRedirect`: the framed variant.
- Test: `internal/jam/collabhttp/previewauth_test.go`

**Interfaces:**
- Consumes: `FrameTickets`, `FrameSessionTTL` (Task 5).
- Produces:
  - const `FrameCookie = "__Host-jam_frame"`.
  - `func FrameAuthenticated(r *http.Request) bool`, which reports whether `Principal` resolved this request from the frame cookie. Use a request-scoped marker: `Principal` cannot mutate `r`, so instead add `func (a *PreviewAuth) PrincipalFrame(r *http.Request) (collab.Principal, string, bool, error)`, which returns `framed`. `serve` calls that and stores `framed` in the `previewRequest`.
  - `isJamCookie` must include `FrameCookie`, so apps can't set it and Jam strips it from forwarded requests.

- **Behaviour:**
  - **`/__jam/frame`:**
    - Sets `Cache-Control: no-store` and `Referrer-Policy: no-referrer`.
    - Requires `Sec-Fetch-Dest: iframe`, else 400 `"frame links open only inside the Jam pane"`.
    - `Frames.Redeem(t, canonicalHost)` failing gives 400 `"frame link expired — reopen the tab"`.
    - On success it creates a `previewEntry` in `frameSessions`, keyed by the hash of a new secret, with `expires = now + FrameSessionTTL` and the host. It must also store the **right**: add a `right collab.Right` field to `previewEntry`. The entry is used only for audit, because rights are re-checked live.
    - It sets the cookie by hand, because Go's `http.Cookie` gained `Partitioned` only in recent versions; use the field if the module's Go version has it (check go.mod):
      `__Host-jam_frame=<v>; Path=/; Max-Age=28800; Secure; HttpOnly; SameSite=None; Partitioned`.
    - It logs `preview frame opened` (user, host, right) and redirects 303 to `/`.
  - **Principal order:**
    1. If `__Host-jam_preview` is present, use the existing logic, unchanged.
    2. Else if `__Host-jam_frame` is present: it must pass `frameCookieUseAllowed(r)`, else `ErrCrossSite`. More than one gives `ErrNoCredentials`. It looks up `frameSessions` with the host match, as for the preview cookie.
    3. Else Bearer.
  - `frameCookieUseAllowed(r)` allows only:
    - a WebSocket upgrade with `Origin` exactly equal to `https://<r.Host>`;
    - `Sec-Fetch-Site: same-origin`;
    - `Sec-Fetch-Mode: navigate` with `Sec-Fetch-Dest: iframe`.

    Nothing else: no missing-Fetch-Metadata fallback, since frame-capable browsers all send it.
  - **`LoginRedirect`:** when `Sec-Fetch-Dest: iframe`, do not redirect. Respond 401 with a small HTML page:
    - it says "This browser can't sign in inside the pane."
    - it has a link `<a href="https://<host><uri>" target="_top">Open in new tab</a>`;
    - its script posts `parent.postMessage({jam:"frame-error",status:"cookies"}, "*")`. `"*"` is acceptable because the message carries no secret, and Task 7 replaces it with the UI origin.

- [ ] **Step 1: Write the failing tests:**
  - the exchange with `Sec-Fetch-Dest: iframe` gives 303 and a cookie with all attributes, `Partitioned` included (assert on the raw `Set-Cookie` string);
  - without it, 400;
  - a reused ticket gives 400;
  - a ticket minted for another host gives 400;
  - **frame cookie accepted:** a request carrying it with `Sec-Fetch-Dest: iframe` navigation resolves the principal, as do a `Sec-Fetch-Site: same-origin` fetch and a WebSocket upgrade with `Origin: https://<same host>`;
  - **frame cookie refused:** a `Sec-Fetch-Site: cross-site` fetch gives `ErrCrossSite`, as do a WebSocket with another preview host's Origin (even the same session) and a top-level `document` navigation;
  - the preview cookie's behaviour is unchanged (the existing tests);
  - `LoginRedirect` with `Sec-Fetch-Dest: iframe` returns 401 with `target="_top"` and the postMessage;
  - an app `Set-Cookie: __Host-jam_frame=…` is stripped (the sanitize path).
- [ ] **Step 2: Run them and watch them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests.** The collabhttp package, then the full suite, then lint.
- [ ] **Step 5: Commit.** Message: `feat(collabhttp): frame exchange and partitioned frame session`.

### Task 7: Framing headers, framed errors, wiring

**Files:**
- Modify: `internal/jam/collabhttp/preview.go`
  - `ModifyResponse` adds `frame-ancestors`, and strips `X-Frame-Options` when framed.
  - `serve` and the error helpers get framed variants that postMessage.
  - `Preview` gains an option for UI origins.
- Modify: `internal/jam/collabhttp/previewauth.go`: `PreviewAuthOptions.FrameAncestors []string`, used by the Safari page's postMessage target.
- Modify: `cmd/at-jam/previewhost.go` (`newPreviewAuth` takes the UI origins and `*FrameTickets`) and `cmd/at-jam/main.go`:
  - create `frames := collabhttp.NewFrameTickets(time.Now)` before line ~2690 (userui) when previews are on;
  - pass it to `newPreviewAuth`, and keep it for userui in Task 8;
  - prune it in `previewAuth.Run`, or add a call in `prune()` if `PreviewAuth` owns it.
- Test: `internal/jam/collabhttp/preview_test.go`

**Interfaces:**
- Consumes: `FrameAuthenticated`/`PrincipalFrame`, and the `previewRequest.framed` flag (Task 6).
- Produces: `func frameAncestors(origins []string) string`, which gives `"frame-ancestors " + strings.Join(origins," ")`, or `"frame-ancestors 'none'"`.

- **Behaviour:**
  - Every response through the proxy, and Jam's own preview-host error pages, get `Content-Security-Policy: frame-ancestors …`. Use `h.Add`, not `Set`, so an app's CSP header stays (browsers enforce all CSP headers).
  - When the request was frame-authenticated, `ModifyResponse` deletes `X-Frame-Options`.
  - Framed error pages: when `framed`, the 403 (authorizeInst refusal), 404 (`notExposed`, also reachable before auth), 502, 503 and 504 (`proxyError`) respond with a tiny HTML page. It carries the same text, plus
    `<script>parent.postMessage({jam:"frame-error",status:<code>}, <first UI origin as JSON>)</script>`.
    - `notExposed` before auth can't know `framed`. Use `Sec-Fetch-Dest: iframe` as the signal for the error-page variant only: it is presentation, not auth.
    - Script content is a JSON-encoded origin string, so there is no injection.
  - The Safari page's postMessage target becomes the first UI origin.

- [ ] **Step 1: Write the failing tests:**
  - every proxied response has `frame-ancestors https://jam.example`, given UI origins `["https://jam.example"]`;
  - with no origins, `frame-ancestors 'none'`;
  - an app CSP header is kept alongside ours (two values);
  - an app `X-Frame-Options: DENY` is stripped on a frame-authenticated request and kept on a top-level one;
  - the framed 404 (`Sec-Fetch-Dest: iframe`, an unknown name) has a body containing `postMessage` and `"https://jam.example"`;
  - the framed 403 likewise;
  - the plain (unframed) errors are unchanged text.
- [ ] **Step 2: Run them and watch them fail.**
- [ ] **Step 3: Implement**, including main.go wiring. `go build ./...`.
- [ ] **Step 4: Run the tests.** The full suite, then lint.
- [ ] **Step 5: Docs and commit.**
  - previews.md gets a new "Framing (the Jam pane)" section after "Signing in":
    - frame tickets (60 s, single-use, bound);
    - the `/__jam/frame` exchange (iframe only);
    - `__Host-jam_frame` (partitioned, 8 h);
    - when it counts;
    - `frame-ancestors` and `ui-origins`;
    - `X-Frame-Options` stripped only when framed;
    - Safari falls back to Open in new tab.
  - Also update "Signing in" ("Anything else — an iframe… gets 403") to point at Framing.
  - Bump `updated`.
  - Commit message: `feat(collabhttp): frame-ancestors, framed error pages, frame wiring`.

---

## PR4 — `feat/studio-tab`

### Task 8: The frame route and the Studio tab shell

**Files:**
- Modify: `internal/jam/userui/userui.go`. Deps gains:

  ```go
  // Frames mints frame URLs for the pane's framed views (nil: framing off;
  // Studio and View here are hidden).
  Frames interface {
  	FrameURL(p collab.Principal, display, actorID, name string, right collab.Right) (string, error)
  }
  ```

- Create: `internal/jam/userui/frame.go` (the route) and `internal/jam/userui/frame_test.go`
- Modify: `internal/jam/userui/handler.go`: the route `POST /app/user/session/{id}/frame`.
- Modify: `internal/jam/userui/session.go`: `SessionHead.Studio bool` (true when SSH is allowed and `Frames != nil`).
- Modify: `internal/jam/userui/sessionpane.go`:
  - `paneTabs` gains `"studio"`;
  - `SessionPane` gains `Studio bool`, and `PreviewLink` gains `Frame bool` (View here offered);
  - previews skip the exposure named `ide`.
- Modify: `internal/jam/userui/templates/inbox.html`:
  - the header gets a Studio tab button `{{if .Studio}}`, placed first;
  - the `sessionpane` studio branch is `<div class="studio" id="studio" data-session="{{.ID}}"><div class="st-bar"><span class="st-status">Starting the IDE…</span><span class="spacer"></span><button type="button" class="st-reload">Reload</button><a class="st-open" target="_blank" rel="noopener">Open in new tab</a></div><div class="st-frame"></div></div>`;
  - previews rows get `{{if .Frame}}<button type="button" class="pv-here" data-name="{{.Name}}">View here</button>{{end}}`.
- Modify: `cmd/at-jam/main.go`: an adapter implementing `FrameURL` with `frames.Mint` and `preview.Host(domain, name, actorID)`. It returns `https://<host>[:port]/__jam/frame?t=<ticket>`, with the port as in `previewURLFunc`. It is wired into userui Deps only when previews are on.

**Interfaces:**
- Consumes: `collabhttp.FrameTickets.Mint` (Task 5), `preview.Host`, and `preview.IDEName`.
- **Route behaviour:** `POST /app/user/session/{id}/frame`, form `name`:
  - `sessionFor` fails → 404;
  - `Frames == nil` → 404;
  - `name == "ide"` → the right is SSH; otherwise the name must be in `inst.Exposures`, else 404, and the right is Preview;
  - `collab.Decide` fails → 403;
  - otherwise 200 JSON `{"url": …, "open": <plain preview URL>}`. `open` comes from `PreviewURL(actorID, name)`, for Open in new tab.
  - It is same-origin protected like the other POST routes (the existing mux wrapper).

- [ ] **Step 1: Write the failing tests:**
  - outsider → 404;
  - a member without SSH asking for `ide` → 403;
  - an SSH holder → 200 with `url` from the fake Frames (assert the right passed is `ssh`) and `open`;
  - `app` exposed → right `preview`;
  - a non-exposed name → 404;
  - `Frames` nil → 404;
  - the header shows the Studio tab only with SSH (and Frames);
  - pane `?tab=studio` renders `id="studio"`;
  - the previews pane has `pv-here` and omits an `ide` exposure.
- [ ] **Step 2: Run them and watch them fail.**
- [ ] **Step 3: Implement**, including the main.go adapter. `go build ./...`.
- [ ] **Step 4: Run the tests.** The full suite, then lint.
- [ ] **Step 5: Commit.** Message: `feat(userui): frame route and the Studio tab shell`.

### Task 9: Studio and View here behaviour (JS) and the browser test

**Files:**
- Modify: `internal/jam/userui/templates/layout.html`: the Studio/frame JS and CSS. Add `studio:'Studio'` to `mePaneTabs`.
- Create: `internal/jam/userui/studio_browser_test.go` (`//go:build browser`).

**Interfaces:**
- Consumes: the Task 8 route and DOM (`#studio`, `.st-*`, `.pv-here`).
- **Behaviour:**
  - **`meStudioOpen(el)`, on the studio tab settle:**
    - POST the frame route with `name=ide`, then create `<iframe class="st-iframe" src=url allow="clipboard-read; clipboard-write" referrerpolicy="no-referrer">` in `.st-frame`.
    - Set `.st-open` `href=open`.
    - On iframe `load`, clear `.st-status`.
    - On a fetch error (403/404), show "No longer shared with you." or "Nothing to show." in `.st-status`.
  - **Keep alive:**
    - The pane fetch replaces `#side-pane-body` innerHTML on tab switch, which would destroy the iframe. Keep the studio iframe in a persistent container outside `#side-pane-body`: a sibling `div#studio-keep` inside `.side-pane`. Show it, and hide the body, when the tab is studio; hide it otherwise.
    - The pane's refresh must skip studio, as it does for thinking.
    - Closing the pane or unloading the page removes the iframe.
  - **Reload:** request a new ticket and replace the iframe.
  - **Widen:** on the first open of the studio tab, when the session's saved layout has no size (`w`/`h` unset), set the pane size to 60% of the split dimension and save.
  - **`frame-error` messages:** `window.addEventListener('message', e => …)`.
    - Accept only from the iframe's `contentWindow` (`e.source === iframe.contentWindow`) and only with `e.data && e.data.jam === 'frame-error'`.
    - Map the status:
      - 403 → "No longer shared with you."
      - 404 → "Nothing to show: the studio isn't running."
      - 502/503/504 → "The IDE didn't start. This studio's image may predate it: upgrade the studio, or expose an IDE as `ide`." For a framed preview (not the IDE): "The preview isn't answering."
      - `cookies` → "This browser can't sign in inside the pane — use Open in new tab."
    - Show the text in `.st-status` (or the preview's status line) via `textContent`.
  - **Previews View here:** clicking `.pv-here` POSTs the frame route with that name and replaces the list with a framed view: "← All previews", the name, Open in new tab, and the iframe. It uses the same message handling. Only one at a time. The back link restores the list (re-fetch the tab).
- **Browser test:** a fake `Frames` returns the URL of a second `httptest.Server` (the "preview"). That page posts `frame-error` 403 when its query asks for it, and otherwise renders `<p id="ok">ide</p>`. Assert:
  1. Opening Studio creates an iframe whose `src` is the fake URL, and `.st-status` clears after load.
  2. Switching to Thinking and back keeps the same iframe element: set `iframe.dataset.mark='1'` before, and check it after.
  3. Reload replaces it: the mark is gone.
  4. A frame posting `frame-error` 403 shows "No longer shared with you."
  5. A message from a non-iframe source is ignored: `window.postMessage` from the top page itself.
  6. View here on a preview frames it, and back restores the list.
  7. The first open widens the pane to about 60% (within ±2%) when no size was saved.

  Headless Chrome may block the cross-origin frame's paint in tests; poll on `iframe.contentDocument` only where same-origin. Otherwise assert on attributes and messages.

- [ ] **Step 1: Write the failing browser test.**
- [ ] **Step 2: Run it and watch it fail.** `go test -tags browser ./internal/jam/userui/ -run BrowserStudio -v`.
- [ ] **Step 3: Implement** the JS and CSS.
- [ ] **Step 4: Run the tests.** The full browser suite, `go test ./...` and lint.
- [ ] **Step 5: Commit.** Message: `feat(userui): Studio IDE and View here framed in the pane`.

### Task 10: Docs

**Files:**
- Modify: `docs/usage/jam/session-view.md`:
  - a "Studio" section: what it is; the SSH gate; first start; kept across tab switches; reloads on a conversation switch (state survives); Open in new tab; errors; Safari;
  - Previews View here;
  - the tab list;
  - frontmatter `owns`/`read_when`;
  - bump `updated`.
- Modify: `docs/usage/jam/INDEX.md`: the session-view row mentions Studio.
- Modify: `docs/superpowers/specs/2026-10-25-session-studio-view-design.md`: status built, with the PR numbers, plus any amendments made during implementation.

- [ ] **Step 1: Edit**, linking to previews.md for the framing and login details. Do not repeat them.
- [ ] **Step 2: Audit.** `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep -E "session-view|previews|collaboration|OVERVIEW|jam/INDEX"` shows no new errors.
- [ ] **Step 3: Commit.** Message: `docs: the Studio tab and inline previews`.
