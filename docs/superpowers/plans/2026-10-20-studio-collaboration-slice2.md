# Studio collaboration — slice 2 (previews) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An agent runs `expose(port, name)` and gets `https://<name>--<label>.preview.<domain>/`; an authorized human opens it in a browser (or calls it with an `at-jam login` bearer) and reaches the studio's `127.0.0.1:<port>` — HTTP/1.1, HTTP/2 to Jam, WebSocket — through Jam alone.

**Architecture:** Exposures live on the session's `Instance` (like alarms), set through new brokered cove tools; Jam pushes each studio's exposed-port set down Attach (`SetTunnelPorts`) so cove-master's dial-back allow-list follows it. The admin/UI listener gains a wildcard preview certificate (SNI) and a Host router: `*.<previews.domain>` goes to a preview handler in `collabhttp` that resolves host → session → exposure → `collab.Decide(Preview)` and reverse-proxies over slice-1 tunnels. Browser identity comes from one reserved login host (`jam.<domain>`) that runs the OIDC flow and hands the preview host a single-use code, which it trades for a **host-only** cookie naming a server-side preview session.

**Tech Stack:** Go 1.26, gRPC/protobuf (`just buf-gen`, generated code committed), `net/http/httputil.ReverseProxy`, `crypto/tls` multi-cert SNI, go-oidc v3, MCP go-sdk (cove-master tools), html/template + htmx.

**Spec:** [`docs/superpowers/specs/2026-10-20-studio-collaboration-design.md`](../specs/2026-10-20-studio-collaboration-design.md) §1, §3, §4, §5 (slice 2). **Amendments (operator decision 2026-10-20):** (1) the preview cookie is **host-only per preview host**, set via a single-use code from the reserved login host `jam.<domain>` — not one cookie on `.<domain>` (a domain-wide cookie would let one studio's preview page drive another studio's app as the viewer); it carries a random server-side session id, never a token. (2) The preview login requests the same token as `/ui`, so operators are recognized as operators; everyone else maps to a Jam user by OIDC binding. (3) cove-master's allow-list becomes `{2222} ∪ exposed ports`, pushed by Jam in `ControlDown.SetTunnelPorts` (tag 8). (4) `<label>` is the session id's 26-char body. Task 8 records these in the spec.

## Global Constraints

- Exposure `name`: `^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$` (1–30 chars), and must not contain `--`. Port: 1024–65535 and not 2222. At most **8** exposures per session. Re-exposing a name replaces its port.
- Preview host: `<name>--<label>.<domain>` where `<label>` = the session id with its `ses_` prefix removed (26 chars, Crockford lowercase); the whole first DNS label ≤ 63 chars. Reserved login host: `jam.<domain>` (has no `--`, so it never collides with a preview host).
- Exposures are session-scoped, logged (`preview exposed` / `preview unexposed`: session, name, port), and cleared when the studio is (re)raised; the agent re-exposes after a restart.
- Jam-reserved path prefix on every preview host and on the login host: **`/__jam/`** — never proxied.
- Preview session: random 32-byte id, server-side (in memory), lifetime **8 h**, bound to a principal (operator id or user id + display name). Cookie `jam_preview`: host-only (no `Domain`), `Path=/`, `HttpOnly`, `Secure`, `SameSite=Lax`. Single-use login code: random 32 bytes, **60 s**, consumed on first use, bound to the target preview host.
- Every proxied request runs `collab.Decide(…, collab.Preview)` live; a paused studio is resumed for an allowed caller (as slice 1). Long-lived proxied connections (WebSocket, streaming) register in the live registry and are dropped by the 1-min sweep when the right ends.
- Jam strips `Cookie: jam_preview=…` (keeping the app's own cookies), `Authorization` when it carried a Jam bearer, and any client-sent `X-Jam-User`; it sets `X-Forwarded-Proto`, `X-Forwarded-Host`, and an informational `X-Jam-User` (display name).
- `previews: { domain, tls: { cert, key } }` in the serve config; unset ⇒ previews off (no tools succeed, preview hosts 404). Requires the admin listener on TLS and `operator-auth.oidc.browser-client-id`.
- Secrets (tokens, codes, session ids) never logged, never on argv. Tests hermetic; real-network/browser behaviour behind `integration`.
- `GOPROXY=https://proxy.golang.org,direct`. Docs change in the same task as the behaviour.

## Review Focus

1. **An exposed app sets its own cookies or reads `Cookie`** → it sees its own cookies but never `jam_preview`; a Jam bearer never reaches it. *(Task 6: `TestPreviewStripsJamCredentials`.)*
2. **A crafted `return_to` / host on the login bounce** (`https://evil.example/`, `//evil`, another domain, the login host itself) → refused; only `https://<valid preview host>/<path>` is accepted. *(Task 5: `TestLoginBounceRejectsForeignReturn`.)*
3. **A replayed or cross-host login code** → the second use fails; a code minted for host A is refused on host B. *(Task 5: `TestCodeSingleUseAndHostBound`.)*
4. **The agent exposes then unexposes while a WebSocket is open, or the viewer's grant expires** → the socket closes (unexpose immediately, revocation within a minute). *(Task 6: `TestPreviewWebSocketClosedOnUnexpose`, `TestPreviewSweepRevokes`.)*
5. **A studio on an older cove-master** (ignores `SetTunnelPorts`) → the viewer gets a clear 504 "studio could not open the preview (is its image current?)", never a hang. *(Task 6: `TestPreviewDialBackTimeoutPage`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/preview/host.go` | name/port validation, host build/parse, reserved host |
| `internal/jam/exposures.go` (+ `instance.go` field) | `Exposure`, `Supervisor.Expose/Unexpose/Exposures`, clear on raise |
| `internal/jam/attach/proto/attach.proto`, `attachpb/*`, `attach/server.go` | `SetTunnelPorts` (tag 8), push on change + on Attach connect |
| `internal/covemaster/{client.go,tunnel.go}` | dynamic allow-list |
| `cmd/cove-master/mcp.go` | `expose`, `unexpose`, `exposures` tools |
| `internal/jam/exposure_handler.go`, `cmd/at-jam/mux.go` | cove-facing `/exposures` endpoints |
| `internal/jam/sessionctx/{sessionctx.go,boilerplate.go}` | `SessionFacts.Previews` + one line |
| `cmd/at-jam/config.go`, `cmd/at-jam/main.go` | `previews:` block, SNI multi-cert, Host router, wiring |
| `internal/jam/collabhttp/previewauth.go` | preview sessions, login codes, bounce routes, principal resolution |
| `internal/jam/browserauth/{mount.go,service.go}` | `Mount.Complete` + `Mount.ReturnTo` hooks |
| `internal/jam/collabhttp/preview.go`, `live.go` | the proxy; live registry gains a right |
| `internal/jam/collabwire`, `collabhttp/share.go`, `adminui/access.go`, `cmd/at-jam/share.go` | exposures in access view, UI links, CLI |
| `docs/usage/jam/collaboration.md`, `serve.md`, `session-context.md`, INDEX | docs |

---

### Task 1: Preview naming and the exposure model

**Files:**
- Create: `internal/jam/preview/host.go`, `internal/jam/preview/host_test.go`, `internal/jam/exposures.go`, `internal/jam/exposures_test.go`
- Modify: `internal/jam/instance.go` (field `Exposures []Exposure \`json:"exposures,omitempty"\``), `internal/jam/supervisor.go` (`Raise` clears `Exposures` on the stored instance; `SetExposureSink`)

**Interfaces:**
- Produces (package `preview`): `const SSHPort = 2222; MaxExposures = 8; ReservedLabel = "jam"`; `func ValidName(name string) error`; `func ValidPort(port int) error`; `func Host(domain, name, actorID string) (string, error)`; `func Parse(host, domain string) (name, actorID string, ok bool)` (strips a `:port`, lowercases, requires the exact `.<domain>` suffix and one label `<name>--<26 chars>`); `func LoginHost(domain string) string` (= `jam.<domain>`); `func IsPreviewHost(host, domain string) bool` (preview or login host).
- Produces (package `jam`): `type Exposure struct { Name string \`json:"name"\`; Port int \`json:"port"\`; CreatedAt time.Time \`json:"created_at"\` }`; `func (s *Supervisor) Expose(actorID, name string, port int) (Exposure, error)`; `func (s *Supervisor) Unexpose(actorID, name string) error` (`ErrNoExposure` when absent); `func (s *Supervisor) Exposures(actorID string) []Exposure`; `var ErrExposureLimit`; `type ExposureSink interface { ExposuresChanged(actorID string, ports []int32) }`; `func (s *Supervisor) SetExposureSink(ExposureSink)` — called (outside `instMu`) after every change and after Raise clears them.

- [ ] **Step 1: Failing tests**

```go
package preview

func TestNamesAndPorts(t *testing.T) {
	for _, ok := range []string{"a", "app", "vite-dev", "a1", strings.Repeat("x", 30)} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a_b", "a--b", "a.b", strings.Repeat("x", 31), "jam"} {
		if ValidName(bad) == nil {
			t.Errorf("ValidName(%q) accepted", bad)
		}
	}
	for _, p := range []int{1024, 5173, 65535} {
		if ValidPort(p) != nil {
			t.Errorf("port %d refused", p)
		}
	}
	for _, p := range []int{0, 80, 1023, 2222, 65536} {
		if ValidPort(p) == nil {
			t.Errorf("port %d accepted", p)
		}
	}
}

func TestHostRoundTrip(t *testing.T) {
	const id = "ses_01m4bc9fmg5v4bc7cq3at6jp6n"
	h, err := Host("preview.jam.example", "vite", id)
	if err != nil || h != "vite--01m4bc9fmg5v4bc7cq3at6jp6n.preview.jam.example" {
		t.Fatalf("Host = %q, %v", h, err)
	}
	for _, in := range []string{h, strings.ToUpper(h), h + ":443"} {
		name, actor, ok := Parse(in, "preview.jam.example")
		if !ok || name != "vite" || actor != id {
			t.Errorf("Parse(%q) = %q %q %v", in, name, actor, ok)
		}
	}
	for _, bad := range []string{
		"vite--01m4bc9fmg5v4bc7cq3at6jp6n.preview.jam.example.evil.com",
		"vite--01m4bc9fmg5v4bc7cq3at6jp6n.other.example",
		"a.vite--01m4bc9fmg5v4bc7cq3at6jp6n.preview.jam.example",
		"vite-01m4bc9fmg5v4bc7cq3at6jp6n.preview.jam.example",
		"vite--short.preview.jam.example",
		"jam.preview.jam.example",
		"preview.jam.example",
	} {
		if _, _, ok := Parse(bad, "preview.jam.example"); ok {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
	if LoginHost("preview.jam.example") != "jam.preview.jam.example" || !IsPreviewHost("jam.preview.jam.example:443", "preview.jam.example") {
		t.Fatal("login host")
	}
}
```

```go
package jam

func TestSupervisorExposures(t *testing.T) {
	sup, _ := exposureTestSupervisor(t, "ses_a") // raise one instance with the real supTestKit helpers
	rec := &recSink{}
	sup.SetExposureSink(rec)
	if _, err := sup.Expose("ses_a", "app", 5173); err != nil {
		t.Fatal(err)
	}
	if _, err := sup.Expose("ses_a", "app", 3000); err != nil { // replace
		t.Fatal(err)
	}
	if got := sup.Exposures("ses_a"); len(got) != 1 || got[0].Port != 3000 {
		t.Fatalf("exposures %+v", got)
	}
	for i := range 7 {
		if _, err := sup.Expose("ses_a", fmt.Sprintf("x%d", i), 4000+i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sup.Expose("ses_a", "ninth", 4100); !errors.Is(err, ErrExposureLimit) {
		t.Fatalf("9th expose err = %v", err)
	}
	if _, err := sup.Expose("ses_a", "bad--name", 4100); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := sup.Expose("ses_a", "ssh", 2222); err == nil {
		t.Fatal("sshd port accepted")
	}
	if err := sup.Unexpose("ses_a", "app"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(sup.Unexpose("ses_a", "app"), ErrNoExposure) {
		t.Fatal("second unexpose")
	}
	last := rec.last()
	if last.actor != "ses_a" || slices.Contains(last.ports, 3000) || len(last.ports) != 7 {
		t.Fatalf("sink last = %+v", last)
	}
}

func TestRaiseClearsExposures(t *testing.T) {
	sup, store := exposureTestSupervisor(t, "ses_a")
	rec := &recSink{}
	sup.SetExposureSink(rec)
	if _, err := sup.Expose("ses_a", "app", 5173); err != nil {
		t.Fatal(err)
	}
	reraise(t, sup, "ses_a") // the standing-restart path the supervisor tests already drive
	if inst, _ := store.GetInstance("ses_a"); len(inst.Exposures) != 0 {
		t.Fatalf("exposures survived a raise: %+v", inst.Exposures)
	}
	if last := rec.last(); last.actor != "ses_a" || len(last.ports) != 0 {
		t.Fatalf("sink not told the set is empty: %+v", last)
	}
}
```

(`exposureTestSupervisor`, `recSink` and `reraise` are small helpers in `exposures_test.go` over the existing `supTestKit`; `reraise` drives the same re-raise path the standing-session supervisor tests use — `grep -n "Raise(" internal/jam/supervisor*_test.go`.)

- [ ] **Step 2: Run** `go test ./internal/jam/preview/ ./internal/jam/ -run 'Names|Host|Exposure' -count=1` → FAIL (undefined).

- [ ] **Step 3: Implement**

```go
// Package preview names studio previews (studio collaboration slice 2,
// docs/usage/jam/collaboration.md#previews): which ports and names an agent
// may expose and the host each is served at.
package preview

const (
	SSHPort       = 2222
	MaxExposures  = 8
	ReservedLabel = "jam"
	labelLen      = 26
	sessionPrefix = "ses_"
)

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$`)

func ValidName(name string) error {
	if !nameRE.MatchString(name) || strings.Contains(name, "--") || name == ReservedLabel {
		return fmt.Errorf("preview name %q: want 1-30 of [a-z0-9-], not starting/ending with '-', no '--', not %q", name, ReservedLabel)
	}
	return nil
}

func ValidPort(port int) error {
	if port < 1024 || port > 65535 || port == SSHPort {
		return fmt.Errorf("port %d: want 1024-65535, not %d (sshd)", port, SSHPort)
	}
	return nil
}

func Host(domain, name, actorID string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	label, ok := strings.CutPrefix(actorID, sessionPrefix)
	if !ok || len(label) != labelLen {
		return "", fmt.Errorf("not a session id: %q", actorID)
	}
	return name + "--" + label + "." + domain, nil
}

func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func Parse(host, domain string) (name, actorID string, ok bool) {
	h := strings.ToLower(stripPort(host))
	first, ok := strings.CutSuffix(h, "."+strings.ToLower(domain))
	if !ok || strings.Contains(first, ".") {
		return "", "", false
	}
	name, label, ok := strings.Cut(first, "--")
	if !ok || ValidName(name) != nil || len(label) != labelLen || strings.ContainsFunc(label, func(r rune) bool {
		return !(('0' <= r && r <= '9') || ('a' <= r && r <= 'z'))
	}) {
		return "", "", false
	}
	return name, sessionPrefix + label, true
}

func LoginHost(domain string) string { return ReservedLabel + "." + strings.ToLower(domain) }

func IsPreviewHost(host, domain string) bool {
	h := strings.ToLower(stripPort(host))
	if h == LoginHost(domain) {
		return true
	}
	_, _, ok := Parse(h, domain)
	return ok
}
```

`exposures.go` mirrors `SetAlarm` (`supervisor.go:871`): under `instMu`, validate with `preview.ValidName`/`ValidPort`, clone `inst.Exposures`, replace by name or append (limit `preview.MaxExposures` → `ErrExposureLimit`), `PutInstance`, log `preview exposed`/`preview unexposed` (session, name, port); after unlocking call `s.exposureSink.ExposuresChanged(actorID, ports)` with the sorted distinct ports. In `Raise`, set `Exposures = nil` on the instance it stores and notify the sink with an empty set. (`package jam` must not import `collabhttp`; it may import `internal/jam/preview`, which imports only stdlib.)

- [ ] **Step 4: Run** `go test ./internal/jam/... -count=1` → PASS.

- [ ] **Step 5: Commit** `git commit -m "feat(jam): preview naming and session exposures"`

---

### Task 2: Dynamic tunnel allow-list (`SetTunnelPorts`)

**Files:**
- Modify: `internal/jam/attach/proto/attach.proto` (+ regenerate `attachpb`, extend `TestTunnelSurface`), `internal/jam/attach/server.go`, `internal/covemaster/client.go`, `internal/covemaster/tunnel.go`, `internal/covemaster/covemaster.go`
- Test: `internal/jam/attach/server_tunnelports_test.go`, `internal/covemaster/tunnel_test.go`

**Interfaces:**
- Consumes: `jam.ExposureSink` (Task 1).
- Produces: proto `message SetTunnelPorts { repeated int32 ports = 1; }`, `SetTunnelPorts tunnel_ports = 8;` in `ControlDown`; `func (s *Server) ExposuresChanged(actorID string, ports []int32)` (records the latest set per actor and enqueues it if connected); on every new Attach stream the server enqueues the actor's current set first (a studio that reconnects re-learns it); `func (s *Server) SetExposureSource(func(actorID string) []int32)` so a restarted Jam can seed the set from the store (`sup.Exposures`). cove-master: allowed = `cfg.TunnelPorts ∪ latest SetTunnelPorts`, guarded by a mutex; `SetTunnelPorts` replaces (not merges) the dynamic set; 2222 is always allowed.

- [ ] **Step 1: Failing tests**
  - attach: connect a fake studio stream (existing server test harness), call `ExposuresChanged("ses_a", []int32{5173})` → the stream receives `ControlDown{tunnel_ports:[5173]}`; disconnect, `ExposuresChanged("ses_a", []int32{3000})` while offline, reconnect → first control received is `tunnel_ports:[3000]`; with `SetExposureSource` returning `[4000]` and no prior change, a fresh Attach receives `[4000]`.
  - cove-master (extend `tunnelRuntime` from `tunnel_test.go`): Attach sends `SetTunnelPorts{5173}` then `OpenTunnel{port:5173}` → dial-back dials `127.0.0.1:5173`; a following `SetTunnelPorts{}` then `OpenTunnel{5173}` → refused (no dial, no Tunnel opened); `OpenTunnel{2222}` still works with an empty dynamic set.

```go
func TestDialBackFollowsSetTunnelPorts(t *testing.T) {
	f := &scriptRuntime{script: []*attachpb.ControlDown{
		{Msg: &attachpb.ControlDown_TunnelPorts{TunnelPorts: &attachpb.SetTunnelPorts{Ports: []int32{5173}}}},
		{Msg: &attachpb.ControlDown_OpenTunnel{OpenTunnel: &attachpb.OpenTunnel{Id: "t1", Port: 5173}}},
		{Msg: &attachpb.ControlDown_TunnelPorts{TunnelPorts: &attachpb.SetTunnelPorts{}}},
		{Msg: &attachpb.ControlDown_OpenTunnel{OpenTunnel: &attachpb.OpenTunnel{Id: "t2", Port: 5173}}},
	}, opened: make(chan string, 4)}
	dialed := make(chan string, 4)
	c := newTestClient(t, f, func(_ context.Context, addr string) (net.Conn, error) {
		dialed <- addr
		a, _ := net.Pipe()
		return a, nil
	})
	runFor(t, c, 2*time.Second)
	if got := drain(dialed); !slices.Equal(got, []string{"127.0.0.1:5173"}) {
		t.Fatalf("dialed %v, want only the first (exposed) open", got)
	}
	if got := drain(f.opened); !slices.Equal(got, []string{"t1"}) {
		t.Fatalf("claimed %v", got)
	}
}
```

(`scriptRuntime` sends its script in order, pausing until each OpenTunnel's outcome is observable — e.g. wait 100 ms between frames; `newTestClient`/`runFor`/`drain` are small helpers in the test file built on the existing `fakeHarness`.)

- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** — proto + `just buf-gen`; server: `ports map[string][]int32` under its mutex, `ExposuresChanged` stores + `enqueue`s; `register` path enqueues the stored (or source-seeded) set right after registering; cove-master recv loop intercepts `ControlDown_TunnelPorts` like `OpenTunnel` (inline, no goroutine) and stores it under a mutex; `dialBack` checks `c.allowed(port)`. Wire in main.go: `sup.SetExposureSink(rsrv)`, `rsrv.SetExposureSource(func(id string) []int32 { … sup.Exposures(id) … })`.

- [ ] **Step 4: Run** `just buf-gen && go test ./internal/jam/attach/... ./internal/covemaster/ ./cmd/at-jam/ -count=1` → PASS.

- [ ] **Step 5: Docs** — `docs/usage/jam/coves.md` Attach paragraph: one sentence on `SetTunnelPorts` (link `collaboration.md#previews`).

- [ ] **Step 6: Commit** `git commit -m "feat(jam): push exposed ports to cove-master's tunnel allow-list"`

---

### Task 3: Agent tools `expose` / `unexpose` / `exposures`

**Files:**
- Create: `internal/jam/exposure_handler.go`, `internal/jam/exposure_handler_test.go`
- Modify: `cmd/cove-master/mcp.go` (+ `mcp_test.go`), `cmd/at-jam/mux.go` (`squawksMux`/`coveHTTPHandler` route `/exposures`), `cmd/at-jam/main.go` (wiring), `internal/jam/sessionctx/sessionctx.go` (`SessionFacts.Previews bool`), `internal/jam/sessionctx/boilerplate.go`, the code that builds `SessionFacts` (`grep -n 'SessionFacts{' internal/jam/*.go`)

**Interfaces:**
- Consumes: `Supervisor.Expose/Unexpose/Exposures` (T1), `preview.Host` (T1).
- Produces: cove-facing HTTP (authenticated by `authenticateCove`, `turnend_handler.go:98`): `PUT /exposures/{name}` body `{"port": N}` → 200 `{"name","port","url"}`; `DELETE /exposures/{name}` → 204 (404 none); `GET /exposures` → 200 `{"exposures":[{"name","port","url"}]}`. Previews off (no domain configured) → 409 `{"error":"previews are not enabled on this Jam"}`. Validation errors → 400 with the validator's message; limit → 409. `func NewExposureHandler(store Store, sup ExposureSetter, urlFor func(actorID, name string) (string, error), log *slog.Logger) *ExposureHandler` (`urlFor == nil` ⇒ previews off). MCP tools in cove-master: `expose {port:int, name:string}` → text "exposed <name> → <url> (post the link in your channel)"; `unexpose {name}`; `exposures {}` → one line per exposure.
- Session context: when `SessionFacts.Previews`, `Boilerplate` adds the line: ``- Previews: `expose(port, name)` makes a port you serve reachable by your collaborators at a URL — post the link in your channel; `unexpose`/`exposures` manage them. A studio restart clears them: expose again.`` (stay within `BudgetBoilerplate`; the existing budget test must still pass).

- [ ] **Step 1: Failing tests** — handler: table over PUT/DELETE/GET with a fake setter and `urlFor`; unauthenticated → 401; previews off → 409; bad name → 400 with message; the URL in the PUT response equals `urlFor`'s. cove-master `mcp_test.go`: follow the `alarm_set` test pattern — an httptest Jam asserting method/path/body, tool result text contains the URL. sessionctx: `Boilerplate(SessionFacts{Previews:true})` contains "`expose(port, name)`"; `Previews:false` does not; budget test passes.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** per interfaces (model `AlarmHandler`, `alarm_handler.go:21`, and the `alarm_set` tool, `mcp.go:630`). `urlFor` in main.go: `preview.Host(cfg.Previews.Domain, name, actorID)` prefixed with `https://` and suffixed `/`.
- [ ] **Step 4: Run** `go test ./internal/jam/... ./cmd/cove-master/ ./cmd/at-jam/ -count=1` → PASS.
- [ ] **Step 5: Docs** — `docs/usage/jam/session-context.md`: the Previews line in the boilerplate description.
- [ ] **Step 6: Commit** `git commit -m "feat(jam): expose/unexpose/exposures agent tools"`

---

### Task 4: `previews:` config, SNI certificate and the Host router

**Files:**
- Modify: `cmd/at-jam/config.go` (+ `config_test.go`), `cmd/at-jam/main.go` (admin server construction ~:2405-2420)
- Create: `cmd/at-jam/previewhost.go`, `cmd/at-jam/previewhost_test.go`

**Interfaces:**
- Produces: `type previewsConfig struct { Domain string \`yaml:"domain"\`; TLS *tlsFiles \`yaml:"tls"\` }` (reuse the existing cert/key struct type used by `admin-tls`); `serveConfig.Previews *previewsConfig \`yaml:"previews"\``; `func (c serveConfig) validatePreviews() error` — domain is a lowercase DNS name with ≥ 2 labels; `tls.cert`/`tls.key` required; the admin listener must use TLS (`adminUsesTLS()`); `operator-auth.oidc.browser-client-id` required. `func adminTLSConfig(adminCert, previewCert tls.Certificate) *tls.Config` (both in `Certificates`; Go picks by SNI); `func hostRouter(domain string, preview, rest http.Handler) http.Handler` — `preview.IsPreviewHost(r.Host, domain)` → `preview`, else `rest`.
- At startup, load the preview cert and check its leaf covers `*.<domain>` (`x509.Certificate.VerifyHostname("x--" + 26 a's + "." + domain)` and `LoginHost(domain)`); a mismatch fails serve with a clear error.

- [ ] **Step 1: Failing tests** — config: valid; missing tls; admin not TLS; no browser-client-id; bad domain (`Preview.Jam.Example`, `example`, `a..b`). previewhost: router dispatch for preview host, login host, the admin host, `Host` with port; `adminTLSConfig` with two generated self-signed certs (admin `jam.example`, preview `*.preview.jam.example`) served by `httptest.NewUnstartedServer` + `StartTLS`, dialed with `tls.Dial` + `ServerName` → the presented leaf matches the SNI each time; the cert-coverage check rejects a cert for `*.other.example`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement**; in main.go replace `ListenAndServeTLS(cert, key)` with `http.Server{TLSConfig: adminTLSConfig(...)}.ListenAndServeTLS("", "")` when previews are on (unchanged otherwise), and wrap `admin` in `hostRouter` with the preview handler from Task 6 (until then `http.NotFoundHandler()` behind a clearly named variable `previewHandler`).
- [ ] **Step 4: Run** `go test ./cmd/at-jam/ -count=1 && just lint` → PASS.
- [ ] **Step 5: Docs** — `docs/usage/jam/serve.md`: a `previews` row (domain, wildcard cert covering `*.<domain>`, DNS `*.<domain>` → Jam host, admin TLS + browser client required, the IdP redirect URI `https://jam.<domain>/__jam/auth/callback` to register; unset ⇒ off), linking `collaboration.md#previews`.
- [ ] **Step 6: Commit** `git commit -m "feat(jam): previews serve config, wildcard SNI cert and preview host router"`

---

### Task 5: Preview login — sessions, codes and the bounce

**Files:**
- Create: `internal/jam/collabhttp/previewauth.go`, `internal/jam/collabhttp/previewauth_test.go`
- Modify: `internal/jam/browserauth/mount.go`, `internal/jam/browserauth/service.go` (+ tests)

**Interfaces:**
- browserauth: `Mount` gains `Complete func(w http.ResponseWriter, r *http.Request, idToken, accessToken, returnTo string)` (when set, `callback` calls it instead of `setSession` + redirect) and `ReturnTo func(string) bool` (when set, replaces `safeReturnTo`'s prefix check for `login`'s `return_to`; the value is stored in the state cookie as today). Existing mounts unchanged (both nil). New `func PreviewMount(complete …, returnTo …) Mount` with `Prefix: "/__jam"`.
- collabhttp: `type PreviewAuth struct` with `func NewPreviewAuth(o PreviewAuthOptions) *PreviewAuth`; `type PreviewAuthOptions struct { Domain string; Login http.Handler /* the browserauth.Service.Routes() main builds over PreviewMount(pa.Complete, pa.ValidReturn) — so Complete/ValidReturn are exported methods */; Operator func(ctx context.Context, accessToken string) (operatorID string, ok bool)`; `Participant func(ctx context.Context, idToken string) (user ident.ID, name string, ok bool)`; `Bearer func(*http.Request) (collab.Principal, string, error)` (operator access token or participant ID token — reuse slice 1's `ParticipantBearer` plus `OIDCAuthenticator.VerifyToken`); `Now func() time.Time; Log *slog.Logger }`.
  - `func (a *PreviewAuth) Principal(r *http.Request) (collab.Principal, string, error)` — `jam_preview` cookie → session (expired/unknown ⇒ `ErrNoCredentials`); else `Authorization: Bearer` via `Bearer`; else `ErrNoCredentials`.
  - `func (a *PreviewAuth) LoginHostHandler() http.Handler` — serves `jam.<domain>`: `/__jam/auth/*` (browserauth routes), `GET /__jam/start?return_to=<url>` → redirects into `/__jam/auth/login?return_to=…`; anything else 404.
  - `func (a *PreviewAuth) PreviewHostRoutes(next http.Handler) http.Handler` — on a preview host: `GET /__jam/session?code=…&return_to=/path` trades the code (single-use, bound to this host) for a host-only `jam_preview` cookie and redirects to `return_to` (path-only, must start with `/`, not `//` or `/\`); `GET /__jam/logout` clears the cookie and drops the session; other `/__jam/*` → 404; everything else → `next`.
  - `func (a *PreviewAuth) LoginRedirect(w http.ResponseWriter, r *http.Request)` — for browsers (`Accept` contains `text/html`) on a preview host without a session: 302 to `https://jam.<domain>/__jam/start?return_to=https://<host><path?query>`; for others 401 `WWW-Authenticate: Bearer` with a one-line hint.
  - `Complete` (the Mount hook): operator if `Operator(accessToken)` ok, else participant via `Participant(idToken)`; neither ⇒ 403 page "your login is not linked to a Jam user". Creates a session (8 h), mints a code bound to the return_to's host, redirects to `https://<that host>/__jam/session?code=…&return_to=<path>`.
  - `ValidReturn(u string) bool` — `https` scheme, host is a preview host (`preview.Parse` ok; **not** the login host), no userinfo, no fragment tricks.

- [ ] **Step 1: Failing tests**

```go
func TestLoginBounceRejectsForeignReturn(t *testing.T) {
	a := newTestPreviewAuth(t) // Domain "preview.jam.example", fake Operator/Participant
	for _, rt := range []string{
		"https://evil.example/", "//evil.example/x", "http://vite--" + label26 + ".preview.jam.example/",
		"https://jam.preview.jam.example/", "https://vite--" + label26 + ".preview.jam.example.evil/",
		"https://user@vite--" + label26 + ".preview.jam.example/", "javascript:alert(1)",
	} {
		if a.ValidReturn(rt) {
			t.Errorf("accepted return_to %q", rt)
		}
	}
	if !a.ValidReturn("https://vite--" + label26 + ".preview.jam.example/app?x=1") {
		t.Fatal("valid return refused")
	}
}

func TestCodeSingleUseAndHostBound(t *testing.T) {
	a := newTestPreviewAuth(t)
	hostA, hostB := "vite--"+label26+".preview.jam.example", "api--"+label26+".preview.jam.example"
	rec := httptest.NewRecorder()
	a.Complete(rec, httptest.NewRequest("GET", "https://jam.preview.jam.example/__jam/auth/callback", nil), "id-alice", "", "https://"+hostA+"/app")
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Host != hostA || loc.Path != "/__jam/session" {
		t.Fatalf("bounce to %v", loc)
	}
	code := loc.Query().Get("code")
	h := a.PreviewHostRoutes(http.NotFoundHandler())
	trade := func(host string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://"+host+"/__jam/session?code="+url.QueryEscape(code)+"&return_to=/app", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := trade(hostB); w.Code != http.StatusBadRequest {
		t.Fatalf("code used on another host: %d", w.Code)
	}
	w := trade(hostA)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/app" {
		t.Fatalf("trade: %d %v", w.Code, w.Header())
	}
	ck := w.Result().Cookies()[0]
	if ck.Name != "jam_preview" || ck.Domain != "" || !ck.HttpOnly || !ck.Secure || ck.Path != "/" || ck.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie %+v", ck)
	}
	if w := trade(hostA); w.Code != http.StatusBadRequest {
		t.Fatalf("replayed code: %d", w.Code)
	}
}
```

Plus: code older than 60 s refused (injected clock); session older than 8 h ⇒ `Principal` returns `ErrNoCredentials`; `return_to` on `/__jam/session` refusing `//x` and `https://…`; `LoginRedirect` 302 for `Accept: text/html` with the right `return_to`, 401 otherwise; `complete` with an operator token yields `Principal{Operator: "op1"}`, with an unbound ID token 403; browserauth: a Mount with `Complete` set calls it with both tokens and does not set the mount cookie; `ReturnTo` replaces the prefix check.

- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** — sessions and codes: `map[string]…` under a mutex keyed by the SHA-256 of the random value (the raw value never stored or logged), expired entries pruned on access and by a 5-min ticker started by `Run(ctx)`. Use `crypto/rand` 32 bytes, base64url.
- [ ] **Step 4: Run** `go test ./internal/jam/browserauth/ ./internal/jam/collabhttp/ -count=1` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): preview login — host-only sessions via a single-use code bounce"`

---

### Task 6: The preview proxy

**Files:**
- Create: `internal/jam/collabhttp/preview.go`, `internal/jam/collabhttp/preview_test.go`
- Modify: `internal/jam/collabhttp/live.go` (entries carry a `right`; `Sweep` checks each entry's own right; `revokeReason` uses it; new `CloseExposure(session, port)`), `internal/jam/collabhttp/handler.go` (factor `authorize`'s body into `authorizeInst(r, inst, right)` reused by the proxy), `cmd/at-jam/main.go` (build `PreviewAuth` + the proxy, replace `previewHandler`; `sup` exposure removal closes live preview conns via `CloseExposure` — wire through `ExposureSink` fan-out)

**Interfaces:**
- Consumes: `preview.Parse`, `Supervisor.Exposures` (via `Deps.Exposures func(actorID string) []jam.Exposure`), `PreviewAuth` (T5), `Tunnels.Open`, `Live`.
- Produces: `func Preview(d Deps, auth *PreviewAuth, domain string) http.Handler` — on the login host → `auth.LoginHostHandler()`; on a preview host → `auth.PreviewHostRoutes(proxy)` where `proxy`:
  1. `preview.Parse(r.Host)` → session + name; unknown session or name not exposed → 404 page "nothing is exposed at this address".
  2. `auth.Principal(r)`; `ErrNoCredentials` → `auth.LoginRedirect`; `ErrInvalidToken` → 401; `ErrUnbound` → 403.
  3. `authorizeInst(r, inst, collab.Preview)` (Decide, wake an idled studio, `PhaseLive`) → 403/409/503/504 pages.
  4. `httputil.ReverseProxy` with `Rewrite`: target `http://<poolkey>/` where `poolkey = fmt.Sprintf("%s.%d.%s.tunnel", userKey, port, actorID)` (`userKey` = `op-<id>` or the user id); `Out.Host` = the original preview host; delete `Cookie` entries named `jam_preview` (keep others), delete `Authorization` if it was the Jam bearer, delete client `X-Jam-User`; set `X-Forwarded-Proto: https`, `X-Forwarded-Host`, `X-Jam-User: <display name>`. A shared `http.Transport` with `DialContext` that parses the pool key and calls `Tunnels.Open(ctx, actorID, port)`, `MaxConnsPerHost: 6`, `IdleConnTimeout: 90s`, `ForceAttemptHTTP2: false`. Each dialed conn is wrapped and registered in `Live` with `{session, user, operator, right: preview, port}` and removed on close.
  5. `ErrorHandler` maps `attach.ErrDialBackTimeout` → 504 "studio could not open the preview (is its image current?)", `ErrStudioNotConnected` → 503, `ErrTooManyTunnels` → 429, other dial errors → 502 "the app at port N is not answering". Error pages are plain text, no app content.
  - Audit: first request per (session, user, name) per 10 min logs `preview opened` (user, session, name, port, right).
- `Live.CloseExposure(session string, port int)` closes every preview conn to that port; the supervisor sink fan-out calls it for ports removed from a session's set.

- [ ] **Step 1: Failing tests** (httptest backend app reached through a fake `Tunnels` whose `Open` dials the backend's listener, recording `(actor, port)`):

```go
func TestPreviewStripsJamCredentials(t *testing.T) {
	var got http.Header
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		fmt.Fprint(w, "hello from the app")
	}))
	defer app.Close()
	h, sess := previewHarness(t, app) // session ses_<label26> exposes "app" at app's port; alice may preview
	req := httptest.NewRequest("GET", "https://app--"+label26+".preview.jam.example/x", nil)
	req.Header.Set("Cookie", "theme=dark; jam_preview="+sess+"; other=1")
	req.Header.Set("X-Jam-User", "forged")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "hello from the app" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	if c := got.Get("Cookie"); strings.Contains(c, "jam_preview") || !strings.Contains(c, "theme=dark") || !strings.Contains(c, "other=1") {
		t.Fatalf("app saw Cookie %q", c)
	}
	if got.Get("X-Jam-User") != "alice" || got.Get("X-Forwarded-Proto") != "https" || got.Get("X-Forwarded-Host") != "app--"+label26+".preview.jam.example" {
		t.Fatalf("forwarding headers %v", got)
	}
	breq := httptest.NewRequest("GET", "https://app--"+label26+".preview.jam.example/x", nil)
	breq.Header.Set("Authorization", "Bearer participant-token")
	h.ServeHTTP(httptest.NewRecorder(), breq)
	if got.Get("Authorization") != "" {
		t.Fatal("Jam bearer forwarded to the app")
	}
}
```

Plus: unexposed name → 404; no credentials + `Accept: text/html` → 302 to the login host; carol (no right) → 403; studio paused → resumed then 200; `TestPreviewWebSocketClosedOnUnexpose` (a gorilla-free WebSocket: raw `Upgrade: websocket` echo backend using `http.Hijacker`; client upgrades through the proxy, exchanges a frame, then `Live.CloseExposure` → client read gets EOF within 1 s); `TestPreviewSweepRevokes` (grant expires → `Live.Sweep` closes the preview conn and logs `ssh`-free reason with `right=preview`); `TestPreviewDialBackTimeoutPage` (fake `Open` returns `attach.ErrDialBackTimeout` → 504 with the image hint); two users' requests never share a pooled conn (record which user each dialed conn was registered for).

- [ ] **Step 2: Run** → FAIL. **Step 3: Implement**. **Step 4: Run** `go test ./internal/jam/collabhttp/ ./cmd/at-jam/ -count=1 && just lint` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): preview proxy over studio tunnels"`

---

### Task 7: Previews in access views — UI, API and CLI

**Files:**
- Modify: `internal/jam/collabwire/collabwire.go` (`AccessView.Previews []PreviewView{Name, Port, URL}`), `internal/jam/collabhttp/share.go` (`GET …/access` fills it via `Deps.Exposures` + `urlFor`), `internal/jam/adminui/access.go` + `templates/access*.html` (a "Previews" list with links opening in a new tab, `rel="noopener noreferrer"`; "Previews are off on this Jam" when the domain is unset; live preview connections show with right `preview`), `cmd/at-jam/share.go` (`studio access` prints a `PREVIEWS` section: name, port, URL)
- Test: the corresponding `_test.go` files

- [ ] **Step 1: Failing tests** — access JSON includes `previews` with URLs; adminui renders the link for each exposure and escapes names; CLI prints `app  5173  https://app--….preview.jam.example/`; previews-off states.
- [ ] **Step 2–4:** implement; `go test ./internal/jam/... ./cmd/at-jam/ -count=1` → PASS.
- [ ] **Step 5: Docs** — `docs/usage/jam/ui-pages.md` Access card: Previews list.
- [ ] **Step 6: Commit** `git commit -m "feat(jam): previews in the access view, Access card and studio access"`

---

### Task 8: End-to-end test and docs

**Files:**
- Create: `cmd/at-jam/preview_integration_test.go` (`//go:build integration`)
- Modify: `docs/usage/jam/collaboration.md` (new `## Previews` section), `docs/usage/jam/INDEX.md` (row read_when gains previews), `docs/OVERVIEW.md` (one clause), spec (§4 amendments from this plan's header)

- [ ] **Step 1: Integration test** — the slice-1 e2e harness (`ssh_integration_test.go`) with: a real `TunnelBroker`, a real cove-master `Client.Run` over bufconn whose `TunnelDial` dials real loopback listeners, `Supervisor.Expose` → `SetTunnelPorts` pushed → the proxy (`collabhttp.Preview`) behind `httptest.NewTLSServer` with the router; a backend serving `GET /` = "ok" and a WebSocket echo on `/ws`. Assert: an operator bearer `GET https://app--<label>.preview.<domain>/` (custom `DialContext` to the test server, SNI/Host set) → "ok"; a WebSocket round trip through the proxy; `Unexpose` → the socket closes and the next GET is 404; a port not in the pushed set is never dialed by cove-master.
- [ ] **Step 2: Docs** — `collaboration.md#previews`: what an agent does (`expose(port, name)` → URL, posts it), who may open it (the four rules with `preview`; role default `members`; `studio share … --rights preview`), how login works (reserved `jam.<domain>` host, host-only cookie, 8 h, re-login is automatic while the IdP session lives), API clients (`Authorization: Bearer` with an `at-jam login` or `login --participant` token, e.g. `curl -H "Authorization: Bearer $(…)"`), what the app sees (`X-Forwarded-*`, `X-Jam-User`; never Jam's cookie or bearer), operator setup (DNS `*.<domain>`, wildcard cert, IdP redirect URI, `previews:` block), the web IDE pattern (a kit installing code-server; `expose(8443, "ide")`), troubleshooting (404 not exposed, 403, 504 old image → re-raise, 502 app not answering, previews off). Spec §4 amendment text from this plan's header.
- [ ] **Step 3:** `just test && just lint && go test -tags integration ./cmd/at-jam/ -run Preview -count=1` and the docs audit (`python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` — no new errors).
- [ ] **Step 4: Commit** `git commit -m "docs(jam): studio previews guide; preview end-to-end test"`

---

### Task 9: Manual acceptance on the Jam host (operator)

- [ ] DNS `*.preview.<jam-host>` → Jam host; a wildcard cert; IdP redirect URI `https://jam.preview.<jam-host>/__jam/auth/callback`; `previews:` block; restart serve; re-raise a studio.
- [ ] Ask the agent to run a Vite dev server and `expose(5173, "app")`; open the link it posts; edit a file over SSH (slice 1) and watch HMR update the page (WebSocket through the proxy).
- [ ] As a participant without the right → 403; `studio share … --rights preview` → works; `unshare` → the page's live connection drops.
- [ ] `curl -H "Authorization: Bearer <at-jam login token>" https://app--….preview.<jam-host>/` returns the app.
