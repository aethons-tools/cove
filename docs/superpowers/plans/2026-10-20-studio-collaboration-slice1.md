# Studio collaboration — slice 1 (tunnel core + SSH) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An authorized human — operator, personal-session requester, project member allowed by role policy, or share-grant holder — can `ssh` (and point VS Code Remote-SSH / JetBrains Gateway) at a running Jam studio through Jam alone, using a short-lived cert Jam issues.

**Architecture:** Jam asks a studio over its Attach stream (`OpenTunnel`) to dial back a per-connection `Tunnel` gRPC stream on the existing cove-facing `:443`; cove-master pipes it to `127.0.0.1:2222` (the studio's sshd). Human-facing HTTP upgrade + cert endpoints live on the admin listener — `/admin/studios/{id}/ssh*` for operators (admin bearer) and `/me/studios/{id}/ssh*` for participants (an OIDC ID-token bearer from `at-jam login --participant`, mapped to a roster user). Jam is an SSH CA (`golang.org/x/crypto/ssh`); the studio's sshd trusts its public key via the sealed hardening layer. Authorization (`collab.Decide`) combines operator, requester, a new role `collaborate.ssh` policy and session-keyed share grants.

**Tech Stack:** Go 1.26, gRPC + protobuf (`just buf-gen`, generated code committed), `golang.org/x/crypto/ssh` (new direct dependency), pgx/Postgres (migration `0015`), html/template + htmx, OpenSSH in the studio.

**Spec:** [`docs/superpowers/specs/2026-10-20-studio-collaboration-design.md`](../specs/2026-10-20-studio-collaboration-design.md) — §1–§3 and §5 (slice 1). **Amendment (operator decision 2026-10-20, option A):** the spec's "human entry point on the admin/UI listener" splits into two planes: operator endpoints under `/admin/studios/{id}/ssh*` (existing bearer auth = operator) and participant endpoints under `/me/studios/{id}/ssh*` that accept `Authorization: Bearer <OIDC ID token>` verified for the device client and mapped with `jam.ParticipantByIdentity`. Task 10 records this in the spec.

## Global Constraints

- Proto package stays `harbor.attach.v1`; `OpenTunnel` takes `ControlDown` oneof tag **7**; `Tunnel` is a new RPC on `service Runtime`; generated `attachpb` is committed (regenerate with `just buf-gen`).
- Studios never listen on a new port; cove-master dials **only `127.0.0.1`** and only ports on its allow-list (slice 1: **2222**).
- Pending tunnel ids: single-use, bound to the studio asked, **10 s** dial-back deadline. Per-studio cap **16** concurrent tunnels; idle timeout **30 min**.
- SSH cert: ed25519 CA; principal **`agent`**; key-id **`user=<user-id> studio=<session-id>`** (operators: `user=op:<operator-id>`); validity **8 h**; client renews when **< 1 h** remains. Port forwarding stays enabled (OpenSSH default).
- CA private key comes only from a demanded credential via serve config `ssh: { ca-cred: <name> }`; resolved in memory, never logged, never written to disk by Jam. Unset `ssh:` ⇒ the feature is off (endpoints 404).
- CA public key installed in the studio at **`/etc/ssh/jam_user_ca.pub`** (root-owned, 0644) via a privileged backend op; sshd config line `TrustedUserCAKeys /etc/ssh/jam_user_ca.pub` in the sealed hardening drop-in.
- Rights: `preview`, `ssh` — independent. Decision order: operator → personal-session requester (`Instance.OwnerID`) → project member with role `collaborate.ssh: members` → unexpired share grant. Role defaults: `preview: members`, `ssh: none`.
- Share grants: `{session_id, user_id, rights, expires_at, granted_by, created_at}`; default **8 h**, max **7 d**; keyed to the **session id**; removed when the session ends. Grantor must be an operator or hold the same right.
- Audit: every cert issue, tunnel open/close, share, unshare is logged with user, session, right, port — never a key or token. An SSH connect posts `"<user> connected over SSH"` to the session channel via `Intercom.Notify`.
- Secrets never reach disk, argv or logs; tests hermetic (`just test`); Postgres tests behind `//go:build integration` + `JAM_TEST_POSTGRES_DSN`; real-ssh tests behind `integration`.
- `GOPROXY=https://proxy.golang.org,direct` in this sandbox. Docs change in the same task as the behavior they describe.

## Review Focus

1. **A studio running an older cove-master** (ignores `OpenTunnel`) → the human gets a clear "studio does not support SSH yet / not connected" error after 10 s, never a hang. *(Task 1: `TestBrokerDialBackTimeout`.)*
2. **A tunnel claimed by the wrong studio or replayed id** → refused; the human's pending connection is untouched. *(Task 1: `TestBrokerRejectsForeignAndReplayedClaims`.)*
3. **Teardown or idle while an SSH session is live** → the human's connection closes promptly (no hung terminal). *(Task 3: `TestSupervisorClosesTunnelsOnTeardownAndIdle`.)*
4. **An expired share grant, a grant for another session, a member whose role says `ssh: none`** → 403 every time, evaluated live. *(Task 4: `TestDecideMatrix`.)*
5. **Participant ID token expired / unmapped / for the wrong audience** → 401/403 with a hint to `at-jam login --participant`, not a 500. *(Task 7: `TestParticipantBearer`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/attach/proto/attach.proto`, `attachpb/*` | `Tunnel` RPC, `TunnelFrame`, `OpenTunnel` |
| `internal/jam/attach/tunnel.go` | `TunnelBroker`: pending ids, dial-back, splice, caps, idle, CloseAll |
| `internal/covemaster/tunnel.go` | dial-back: allow-list, loopback dial, Tunnel stream pipe |
| `internal/jam/supervisor.go` (modify) | `SetTunnelCloser`, close on Teardown/Idle |
| `internal/jam/collab/{collab.go,grants.go}` | `Right`, `Principal`, `Decide`; grant validation |
| `internal/jam/identity.go`, `roleedit.go`, `admin.go`, `adminclient` (modify) | `Role.Collaborate` |
| `internal/jam/migrations/0015_share_grants.sql`, `sharegrants.go`, `memstate`/`memstore`/`pgstore_registry` (modify), `storetest` | grant persistence |
| `internal/jam/sshca/sshca.go` | load CA key, `IssueCert` |
| `internal/assemble/hardening/...` | `TrustedUserCAKeys`, `install-ssh-ca.sh` |
| `internal/backend/{backend.go,colima/sshca.go}`, `internal/jam/launcher/launcher.go` | `SSHCA` backend op, install at raise/resume |
| `internal/jam/collabhttp/{handler.go,participant.go}` | cert + upgrade endpoints, audit, notice; participant bearer |
| `internal/jam/deviceflow`, `cmd/at-jam/{main.go,clientconfig.go}` | `login --participant` keeps the ID token |
| `cmd/at-jam/ssh.go`, `adminclient` | `ssh-config`, `ssh-proxy`, `ssh` |
| `cmd/at-jam/share.go`, grant admin/me API, `adminui` access panel | sharing + visibility |
| `cmd/at-jam/config.go` (modify) | `ssh:` serve-config block |
| `docs/usage/jam/collaboration.md` + INDEX/serve/roster/ui | docs |

---

### Task 1: Tunnel protocol and Jam-side broker

**Files:**
- Modify: `internal/jam/attach/proto/attach.proto`; regenerate `internal/jam/attach/attachpb/*`; update `attachpb/attachpb_test.go` canary if it enumerates oneof cases
- Create: `internal/jam/attach/tunnel.go`
- Modify: `internal/jam/attach/server.go` (implement `Tunnel` RPC, `OpenTunnel` push)
- Test: `internal/jam/attach/tunnel_test.go`

**Interfaces:**
- Produces:
  - proto: `rpc Tunnel(stream TunnelFrame) returns (stream TunnelFrame);` `message TunnelFrame { oneof msg { TunnelOpen open = 1; bytes data = 2; TunnelClose close = 3; } }` `message TunnelOpen { string id = 1; }` `message TunnelClose { string reason = 1; }` `message OpenTunnel { string id = 1; int32 port = 2; }` and `OpenTunnel open_tunnel = 7;` in `ControlDown`.
  - `type TunnelBroker struct` with `func NewTunnelBroker(push func(actorID, id string, port int32) bool, opt TunnelOptions) *TunnelBroker`; `type TunnelOptions struct { DialBack time.Duration /*10s*/; MaxPerStudio int /*16*/; Idle time.Duration /*30m*/; Log *slog.Logger }`.
  - `func (b *TunnelBroker) Open(ctx context.Context, actorID string, port int32) (net.Conn, error)` — returns the human-side end; errors `ErrStudioNotConnected`, `ErrDialBackTimeout`, `ErrTooManyTunnels`.
  - `func (b *TunnelBroker) Claim(actorID string, stream TunnelStream) error` — called by the `Tunnel` RPC after auth; `type TunnelStream interface { Send(*attachpb.TunnelFrame) error; Recv() (*attachpb.TunnelFrame, error); Context() context.Context }`.
  - `func (b *TunnelBroker) CloseAll(actorID string)`.
  - `func (s *Server) SetTunnelBroker(b *TunnelBroker)`; `func (s *Server) PushOpenTunnel(actorID, id string, port int32) bool` (returns `Connected(actorID)` && enqueued).

- [ ] **Step 1: Edit the proto and regenerate**

Add the messages above to `attach.proto` (keep `package harbor.attach.v1`), then:

Run: `just buf-gen && go build ./...`
Expected: `attachpb/attach.pb.go` and `attach_grpc.pb.go` regenerated with `Tunnel`; build passes (every `RuntimeServer` implementation embeds `UnimplementedRuntimeServer`). Commit the generated files with the proto.

- [ ] **Step 2: Write the failing broker tests**

```go
// internal/jam/attach/tunnel_test.go
package attach

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

// fakeStream is an in-memory TunnelStream: frames Sent by the broker land in
// toStudio; frames the "studio" writes go into fromStudio.
type fakeStream struct {
	ctx        context.Context
	cancel     context.CancelFunc
	toStudio   chan *attachpb.TunnelFrame
	fromStudio chan *attachpb.TunnelFrame
}

func newFakeStream() *fakeStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeStream{ctx: ctx, cancel: cancel, toStudio: make(chan *attachpb.TunnelFrame, 16), fromStudio: make(chan *attachpb.TunnelFrame, 16)}
}
func (f *fakeStream) Send(fr *attachpb.TunnelFrame) error { f.toStudio <- fr; return nil }
func (f *fakeStream) Recv() (*attachpb.TunnelFrame, error) {
	select {
	case fr := <-f.fromStudio:
		return fr, nil
	case <-f.ctx.Done():
		return nil, io.EOF
	}
}
func (f *fakeStream) Context() context.Context { return f.ctx }

type pushRec struct {
	mu   sync.Mutex
	ids  []string
	ok   bool
}

func (p *pushRec) push(actor, id string, port int32) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ids = append(p.ids, actor+"/"+id)
	return p.ok
}
func (p *pushRec) last() string { p.mu.Lock(); defer p.mu.Unlock(); return p.ids[len(p.ids)-1] }

func openFrame(id string) *attachpb.TunnelFrame {
	return &attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Open{Open: &attachpb.TunnelOpen{Id: id}}}
}
func dataFrame(b string) *attachpb.TunnelFrame {
	return &attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Data{Data: []byte(b)}}
}

func TestBrokerSplicesBothWays(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: time.Second})
	var human net.Conn
	errc := make(chan error, 1)
	go func() { var err error; human, err = b.Open(context.Background(), "ses_a", 2222); errc <- err }()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) == 1 })
	id := p.last()[len("ses_a/"):]
	s := newFakeStream()
	s.fromStudio <- openFrame(id)
	go func() { _ = b.Claim("ses_a", s) }()
	if err := <-errc; err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.fromStudio <- dataFrame("SSH-2.0-OpenSSH\r\n")
	buf := make([]byte, 64)
	n, _ := human.Read(buf)
	if string(buf[:n]) != "SSH-2.0-OpenSSH\r\n" {
		t.Fatalf("human read %q", buf[:n])
	}
	_, _ = human.Write([]byte("client hello"))
	got := <-s.toStudio
	if string(got.GetData()) != "client hello" {
		t.Fatalf("studio got %v", got)
	}
	_ = human.Close()
	if fr := <-s.toStudio; fr.GetClose() == nil {
		t.Fatalf("want close frame after human closes, got %v", fr)
	}
}

func TestBrokerDialBackTimeout(t *testing.T) {
	b := NewTunnelBroker((&pushRec{ok: true}).push, TunnelOptions{DialBack: 50 * time.Millisecond})
	_, err := b.Open(context.Background(), "ses_a", 2222)
	if !errors.Is(err, ErrDialBackTimeout) {
		t.Fatalf("err = %v, want ErrDialBackTimeout", err)
	}
	b2 := NewTunnelBroker((&pushRec{ok: false}).push, TunnelOptions{DialBack: time.Second})
	if _, err := b2.Open(context.Background(), "ses_a", 2222); !errors.Is(err, ErrStudioNotConnected) {
		t.Fatalf("err = %v, want ErrStudioNotConnected", err)
	}
}

func TestBrokerRejectsForeignAndReplayedClaims(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: 300 * time.Millisecond})
	errc := make(chan error, 1)
	go func() { _, err := b.Open(context.Background(), "ses_a", 2222); errc <- err }()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) == 1 })
	id := p.last()[len("ses_a/"):]
	foreign := newFakeStream()
	foreign.fromStudio <- openFrame(id)
	if err := b.Claim("ses_b", foreign); err == nil {
		t.Fatal("another studio claimed ses_a's tunnel")
	}
	ok := newFakeStream()
	ok.fromStudio <- openFrame(id)
	go func() { _ = b.Claim("ses_a", ok) }()
	if err := <-errc; err != nil {
		t.Fatalf("legit claim failed: %v", err)
	}
	replay := newFakeStream()
	replay.fromStudio <- openFrame(id)
	if err := b.Claim("ses_a", replay); err == nil {
		t.Fatal("replayed id accepted")
	}
}

func TestBrokerCapAndCloseAll(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: time.Second, MaxPerStudio: 1})
	conns := make(chan net.Conn, 1)
	go func() { c, _ := b.Open(context.Background(), "ses_a", 2222); conns <- c }()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) == 1 })
	s := newFakeStream()
	s.fromStudio <- openFrame(p.last()[len("ses_a/"):])
	go func() { _ = b.Claim("ses_a", s) }()
	c := <-conns
	if _, err := b.Open(context.Background(), "ses_a", 2222); !errors.Is(err, ErrTooManyTunnels) {
		t.Fatalf("second open err = %v, want ErrTooManyTunnels", err)
	}
	b.CloseAll("ses_a")
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("human conn still open after CloseAll")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met")
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/jam/attach/ -run Broker`
Expected: FAIL — undefined `NewTunnelBroker`, `TunnelOptions`, errors.

- [ ] **Step 4: Implement `tunnel.go`**

```go
// Package attach — tunnel.go: the Jam side of studio tunnels (studio
// collaboration, docs/usage/jam/collaboration.md). A human connection asks
// Open for a studio port; the broker pushes OpenTunnel down the studio's
// Attach stream and waits (DialBack) for the studio to dial a Tunnel stream
// back and claim the id, then splices the two. Ids are single-use and bound
// to the studio asked.
package attach

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

var (
	ErrStudioNotConnected = errors.New("studio not connected")
	ErrDialBackTimeout    = errors.New("studio did not open the tunnel in time (is its cove-master current?)")
	ErrTooManyTunnels     = errors.New("too many open tunnels for this studio")
	errUnknownTunnel      = errors.New("unknown or already-claimed tunnel id")
)

type TunnelOptions struct {
	DialBack     time.Duration
	MaxPerStudio int
	Idle         time.Duration
	Log          *slog.Logger
}

type TunnelStream interface {
	Send(*attachpb.TunnelFrame) error
	Recv() (*attachpb.TunnelFrame, error)
	Context() context.Context
}

type pending struct {
	actorID string
	ready   chan TunnelStream
}

type TunnelBroker struct {
	push func(actorID, id string, port int32) bool
	opt  TunnelOptions

	mu      sync.Mutex
	pending map[string]*pending
	live    map[string]map[*tunnelConn]struct{} // actorID → open human ends
}

func NewTunnelBroker(push func(actorID, id string, port int32) bool, opt TunnelOptions) *TunnelBroker {
	if opt.DialBack <= 0 {
		opt.DialBack = 10 * time.Second
	}
	if opt.MaxPerStudio <= 0 {
		opt.MaxPerStudio = 16
	}
	if opt.Idle <= 0 {
		opt.Idle = 30 * time.Minute
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &TunnelBroker{push: push, opt: opt, pending: map[string]*pending{}, live: map[string]map[*tunnelConn]struct{}{}}
}

func newID() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }

// Open returns the human-side end of a tunnel to actorID's 127.0.0.1:port.
func (b *TunnelBroker) Open(ctx context.Context, actorID string, port int32) (net.Conn, error) {
	b.mu.Lock()
	if len(b.live[actorID]) >= b.opt.MaxPerStudio {
		b.mu.Unlock()
		return nil, ErrTooManyTunnels
	}
	id := newID()
	p := &pending{actorID: actorID, ready: make(chan TunnelStream, 1)}
	b.pending[id] = p
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.pending, id); b.mu.Unlock() }()

	if !b.push(actorID, id, port) {
		return nil, ErrStudioNotConnected
	}
	t := time.NewTimer(b.opt.DialBack)
	defer t.Stop()
	select {
	case s := <-p.ready:
		return b.splice(actorID, s), nil
	case <-t.C:
		return nil, ErrDialBackTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Claim is the Tunnel RPC body after authentication: the first frame must be
// open{id} for a pending id bound to actorID. It blocks until the tunnel ends.
func (b *TunnelBroker) Claim(actorID string, s TunnelStream) error {
	fr, err := s.Recv()
	if err != nil {
		return err
	}
	open := fr.GetOpen()
	if open == nil {
		return errors.New("first tunnel frame must be open")
	}
	b.mu.Lock()
	p, ok := b.pending[open.GetId()]
	if ok && p.actorID == actorID {
		delete(b.pending, open.GetId())
	}
	b.mu.Unlock()
	if !ok || p.actorID != actorID {
		return errUnknownTunnel
	}
	p.ready <- s
	<-s.Context().Done()
	return nil
}

func (b *TunnelBroker) splice(actorID string, s TunnelStream) net.Conn {
	human, inner := net.Pipe()
	tc := &tunnelConn{Conn: human}
	b.mu.Lock()
	if b.live[actorID] == nil {
		b.live[actorID] = map[*tunnelConn]struct{}{}
	}
	b.live[actorID][tc] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	done := func(reason string) {
		once.Do(func() {
			_ = s.Send(&attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Close{Close: &attachpb.TunnelClose{Reason: reason}}})
			_ = inner.Close()
			b.mu.Lock()
			delete(b.live[actorID], tc)
			b.mu.Unlock()
		})
	}
	tc.closeAll = func() { done("closed by Jam") }
	idle := time.AfterFunc(b.opt.Idle, func() { done("idle timeout") })
	touch := func() { idle.Reset(b.opt.Idle) }

	go func() { // studio → human
		for {
			fr, err := s.Recv()
			if err != nil || fr.GetClose() != nil {
				done("studio closed")
				return
			}
			touch()
			if _, err := inner.Write(fr.GetData()); err != nil {
				done("human closed")
				return
			}
		}
	}()
	go func() { // human → studio
		buf := make([]byte, 32<<10)
		for {
			n, err := inner.Read(buf)
			if n > 0 {
				touch()
				if serr := s.Send(&attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Data{Data: append([]byte(nil), buf[:n]...)}}); serr != nil {
					done("studio gone")
					return
				}
			}
			if err != nil {
				done("human closed")
				return
			}
		}
	}()
	return tc
}

// CloseAll closes every live tunnel to actorID (teardown, idle, revocation).
func (b *TunnelBroker) CloseAll(actorID string) {
	b.mu.Lock()
	var all []*tunnelConn
	for tc := range b.live[actorID] {
		all = append(all, tc)
	}
	b.mu.Unlock()
	for _, tc := range all {
		tc.closeAll()
	}
}

type tunnelConn struct {
	net.Conn
	closeAll func()
}
```

Then in `server.go` add:

```go
// SetTunnelBroker wires the studio-collaboration tunnel broker (nil = Tunnel RPC refused).
func (s *Server) SetTunnelBroker(b *TunnelBroker) { s.tunnels = b }

// PushOpenTunnel asks actorID's studio to dial a Tunnel back for id → 127.0.0.1:port.
// False when the studio has no live Attach stream.
func (s *Server) PushOpenTunnel(actorID, id string, port int32) bool {
	if !s.Connected(actorID) {
		return false
	}
	s.enqueue(actorID, &attachpb.ControlDown{Msg: &attachpb.ControlDown_OpenTunnel{OpenTunnel: &attachpb.OpenTunnel{Id: id, Port: port}}})
	return true
}

// Tunnel is a studio's dial-back for one human connection, authenticated like Attach.
func (s *Server) Tunnel(stream attachpb.Runtime_TunnelServer) error {
	if s.tunnels == nil {
		return status.Error(codes.Unavailable, "tunnels not enabled")
	}
	actorID, err := s.authenticate(stream.Context())
	if err != nil {
		return err
	}
	return s.tunnels.Claim(actorID, stream)
}
```

(add `tunnels *TunnelBroker` to `Server`; `codes`/`status` are already imported in `server.go`).

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/jam/attach/... -count=1` (and `CGO_ENABLED=1 go test -race ./internal/jam/attach/` where a C compiler exists)
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/jam/attach/
git commit -m "feat(jam): Tunnel RPC + OpenTunnel and the Jam-side tunnel broker"
```

---

### Task 2: cove-master dial-back

**Files:**
- Create: `internal/covemaster/tunnel.go`
- Modify: `internal/covemaster/covemaster.go` (`Config.TunnelPorts []int32`, default `{2222}`), `internal/covemaster/client.go` (intercept `ControlDown_OpenTunnel` in the recv goroutine, like `Ack`)
- Test: `internal/covemaster/tunnel_test.go`

**Interfaces:**
- Consumes: `attachpb.RuntimeClient.Tunnel`, `ControlDown_OpenTunnel` (Task 1).
- Produces: `func (c *Client) dialBack(ctx context.Context, cc grpc.ClientConnInterface, md metadata.MD, ot *attachpb.OpenTunnel)`; `Config.TunnelPorts`, `Config.TunnelDial func(ctx context.Context, addr string) (net.Conn, error)` (test seam; default `net.Dialer.DialContext` on `"tcp"`).

- [ ] **Step 1: Write the failing tests** — using the `fakeRuntime` pattern from `events_test.go`:

```go
// internal/covemaster/tunnel_test.go
package covemaster

// tunnelRuntime is a fake Runtime whose Attach pushes one OpenTunnel and whose
// Tunnel records the claim and echoes the studio's bytes back as a check.
type tunnelRuntime struct {
	attachpb.UnimplementedRuntimeServer
	port   int32
	opened chan string
	gotHello chan string
}

func (f *tunnelRuntime) Attach(s attachpb.Runtime_AttachServer) error {
	_ = s.Send(&attachpb.ControlDown{Msg: &attachpb.ControlDown_OpenTunnel{OpenTunnel: &attachpb.OpenTunnel{Id: "t1", Port: f.port}}})
	<-s.Context().Done()
	return nil
}

func (f *tunnelRuntime) Tunnel(s attachpb.Runtime_TunnelServer) error {
	fr, err := s.Recv()
	if err != nil {
		return err
	}
	f.opened <- fr.GetOpen().GetId()
	_ = s.Send(&attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Data{Data: []byte("ping")}})
	fr, err = s.Recv()
	if err == nil {
		f.gotHello <- string(fr.GetData())
	}
	return nil
}

func TestDialBackPipesToLoopbackPort(t *testing.T) {
	// a fake "sshd": echoes "pong" when it reads "ping"
	var dialed string
	f := &tunnelRuntime{port: 2222, opened: make(chan string, 1), gotHello: make(chan string, 1)}
	dialOpt := fakeHarness(t, f)
	c := New(Config{Addr: "bufnet", Token: "t", LaunchSecret: "s", DialOptions: dialOpt,
		TunnelDial: func(ctx context.Context, addr string) (net.Conn, error) {
			dialed = addr
			a, b := net.Pipe()
			go func() { buf := make([]byte, 4); _, _ = io.ReadFull(b, buf); if string(buf) == "ping" { _, _ = b.Write([]byte("pong")) } }()
			return a, nil
		}}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx, &blockWorkload{first: make(chan struct{}), controls: make(chan Control, 4)}) }()
	if id := <-f.opened; id != "t1" {
		t.Fatalf("claimed id %q", id)
	}
	if got := <-f.gotHello; got != "pong" {
		t.Fatalf("studio side relayed %q", got)
	}
	if dialed != "127.0.0.1:2222" {
		t.Fatalf("dialed %q, want loopback 2222", dialed)
	}
}

func TestDialBackRefusesPortNotOnAllowList(t *testing.T) {
	f := &tunnelRuntime{port: 22, opened: make(chan string, 1), gotHello: make(chan string, 1)}
	dialOpt := fakeHarness(t, f)
	called := false
	c := New(Config{Addr: "bufnet", Token: "t", LaunchSecret: "s", DialOptions: dialOpt,
		TunnelDial: func(context.Context, string) (net.Conn, error) { called = true; return nil, errors.New("x") }}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = c.Run(ctx, &blockWorkload{first: make(chan struct{}), controls: make(chan Control, 4)})
	select {
	case <-f.opened:
		t.Fatal("opened a tunnel for a port not on the allow-list")
	default:
	}
	if called {
		t.Fatal("dialed a refused port")
	}
}
```

(Match `fakeHarness`/`blockWorkload`'s real signatures in `events_test.go`/`client_test.go`; adapt the constructor call if `fakeHarness` returns `[]grpc.DialOption`.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/covemaster/ -run DialBack`
Expected: FAIL — unknown field `TunnelDial`; OpenTunnel ignored ("ignoring unsupported control message").

- [ ] **Step 3: Implement**

In `client.go`'s recv goroutine, next to the `ControlDown_Ack` case, add:

```go
case *attachpb.ControlDown_OpenTunnel:
	go c.dialBack(ctx, cc, md, m.OpenTunnel) // never block the recv loop
	continue
```

`tunnel.go`:

```go
package covemaster

// dialBack serves one OpenTunnel: only allow-listed ports, only 127.0.0.1.
// It opens a Tunnel stream to Jam (same conn + auth metadata as Attach), sends
// open{id}, and pipes bytes until either side closes.
func (c *Client) dialBack(ctx context.Context, cc grpc.ClientConnInterface, md metadata.MD, ot *attachpb.OpenTunnel) {
	if !slices.Contains(c.cfg.TunnelPorts, ot.GetPort()) {
		c.log.Warn("tunnel refused: port not allowed", "port", ot.GetPort())
		return
	}
	dial := c.cfg.TunnelDial
	if dial == nil {
		var d net.Dialer
		dial = func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }
	}
	local, err := dial(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(int(ot.GetPort()))))
	if err != nil {
		c.log.Warn("tunnel: local dial failed", "port", ot.GetPort(), "err", err.Error())
		return
	}
	defer local.Close()
	sctx, cancel := context.WithCancel(metadata.NewOutgoingContext(ctx, md))
	defer cancel()
	stream, err := attachpb.NewRuntimeClient(cc).Tunnel(sctx)
	if err != nil {
		c.log.Warn("tunnel: open stream failed", "err", err.Error())
		return
	}
	if err := stream.Send(&attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Open{Open: &attachpb.TunnelOpen{Id: ot.GetId()}}}); err != nil {
		return
	}
	go func() { // Jam → local
		defer cancel()
		for {
			fr, err := stream.Recv()
			if err != nil || fr.GetClose() != nil {
				return
			}
			if _, err := local.Write(fr.GetData()); err != nil {
				return
			}
		}
	}()
	buf := make([]byte, 32<<10) // local → Jam
	for {
		n, err := local.Read(buf)
		if n > 0 {
			if serr := stream.Send(&attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Data{Data: append([]byte(nil), buf[:n]...)}}); serr != nil {
				return
			}
		}
		if err != nil {
			_ = stream.Send(&attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Close{Close: &attachpb.TunnelClose{Reason: "local closed"}}})
			_ = stream.CloseSend()
			return
		}
	}
}
```

Default `TunnelPorts` to `[]int32{2222}` in `New` when empty. The `md` and `cc` used by `session()` must be in scope of the recv goroutine (they are created in `session()` — pass them through).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/covemaster/ -count=1`
Expected: PASS (existing tests too).

- [ ] **Step 5: Commit**

```bash
git add internal/covemaster/
git commit -m "feat(cove-master): dial back OpenTunnel to allow-listed loopback ports"
```

---

### Task 3: Close tunnels on teardown and idle

**Files:**
- Modify: `internal/jam/supervisor.go` (`TunnelCloser` interface, `SetTunnelCloser`, calls in `Teardown` before `launcher.Teardown`, and in `Idle` before `launcher.Pause`)
- Test: `internal/jam/supervisor_tunnels_test.go`

**Interfaces:**
- Produces: `type TunnelCloser interface { CloseAll(actorID string) }`; `func (s *Supervisor) SetTunnelCloser(c TunnelCloser)`. (`*attach.TunnelBroker` satisfies it.)

- [ ] **Step 1: Failing test**

```go
package jam

type recCloser struct{ ids []string }

func (r *recCloser) CloseAll(id string) { r.ids = append(r.ids, id) }

func TestSupervisorClosesTunnelsOnTeardownAndIdle(t *testing.T) {
	sup, store := newTestSupervisor(t) // use the existing supervisor test helper; see supervisor_test.go
	rc := &recCloser{}
	sup.SetTunnelCloser(rc)
	raiseTestInstance(t, sup, "ses_a")
	raiseTestInstance(t, sup, "ses_b")
	if err := sup.Idle(context.Background(), "ses_a"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Teardown(context.Background(), "ses_b"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rc.ids, []string{"ses_a", "ses_b"}) {
		t.Fatalf("CloseAll calls = %v", rc.ids)
	}
	_ = store
}
```

(Use the real helper names from `supervisor_test.go` — `grep -n '^func new.*Supervisor\|Raise(' internal/jam/supervisor_test.go` — and keep the assertion.)

- [ ] **Step 2: Run** `go test ./internal/jam/ -run ClosesTunnels` → FAIL (undefined `SetTunnelCloser`).

- [ ] **Step 3: Implement** — field `tunnels TunnelCloser`; in `Teardown`, right after `sink.RequestTeardown` and before marking terminating / `launcher.Teardown`: `if s.tunnels != nil { s.tunnels.CloseAll(actorID) }`; same at the start of `Idle` before `launcher.Pause`. Nil-safe.

- [ ] **Step 4: Run** `go test ./internal/jam/ -count=1` → PASS.

- [ ] **Step 5: Commit** `git commit -m "feat(jam): supervisor closes a studio's tunnels on teardown and idle"`

---

### Task 4: Authorization — role policy, share grants, `Decide`

**Files:**
- Create: `internal/jam/collab/collab.go`, `internal/jam/collab/collab_test.go`, `internal/jam/migrations/0015_share_grants.sql`, `internal/jam/sharegrants.go`
- Modify: `internal/jam/identity.go` (`Role.Collaborate`), `internal/jam/roleedit.go` (`PutRoleKeeping` carries `Collaborate` when the incoming role leaves it zero), `internal/jam/admin.go` (`RoleBody`/`RoleSummary` gain `collaborate_preview`, `collaborate_ssh`), `internal/jam/adminclient/adminclient.go` (map both ways), `cmd/at-jam/main.go` (`role add --collaborate-ssh members|none --collaborate-preview members|none`; `role list` shows them), `internal/jam/store.go` (embed `ShareGrantStore`), `memstate.go`/`memstore.go`/`pgstore_registry.go`/`pgstore.go` (load), `pgstore_testhelpers.go` (TRUNCATE list), `internal/jam/storetest/` (conformance)
- Test: `internal/jam/collab/collab_test.go`, `internal/jam/storetest/sharegrants.go`

**Interfaces:**
- Produces:
  - `type CollaboratePolicy struct { Preview string \`json:"preview,omitempty"\`; SSH string \`json:"ssh,omitempty"\` }` on `Role.Collaborate \`json:"collaborate,omitzero"\``; values `""` (default), `"members"`, `"none"`; `func (p CollaboratePolicy) Allows(right string) bool` — default `preview: members`, `ssh: none`.
  - `type ShareGrant struct { SessionID string; UserID ident.ID; Rights []string; ExpiresAt, CreatedAt time.Time; GrantedBy string }` (JSON tags snake_case).
  - `type ShareGrantStore interface { PutShareGrant(ShareGrant) error; RemoveShareGrant(sessionID string, user ident.ID) error; ShareGrants(sessionID string) []ShareGrant; RemoveShareGrantsOf(sessionID string) error }`.
  - package `collab`: `type Right string` (`Preview Right = "preview"`, `SSH Right = "ssh"`); `type Principal struct { Operator string /*operator id, "" if not*/; UserID ident.ID }`; `type Reason string` (`"operator"`, `"requester"`, `"role"`, `"grant"`); `func Decide(store jam.Store, now time.Time, p Principal, inst jam.Instance, r Right) (Reason, bool)`; `func ValidateGrant(now time.Time, g jam.ShareGrant) error` (rights ⊆ {preview,ssh}, `ExpiresAt` ≤ now+7d and > now); `const DefaultGrant = 8*time.Hour; MaxGrant = 7*24*time.Hour`.

- [ ] **Step 1: Failing `Decide` matrix test**

```go
package collab_test

func TestDecideMatrix(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	st := jam.NewMemStore()
	proj := mustProject(t, st, "acme")
	alice := mustUser(t, st, "alice", proj)   // member of acme
	bob := mustUser(t, st, "bob", proj)       // member of acme
	carol := mustUser(t, st, "carol")         // not a member
	mustRole(t, st, proj, jam.Role{Name: "ic"})                                                       // default: ssh none
	mustRole(t, st, proj, jam.Role{Name: "pair", Collaborate: jam.CollaboratePolicy{SSH: "members"}}) // members may ssh
	personal := jam.Instance{ActorID: "ses_p", Project: proj, Role: "ic", SessionKind: jam.SessionKindPersonal, OwnerID: alice}
	ic := jam.Instance{ActorID: "ses_i", Project: proj, Role: "ic"}
	pair := jam.Instance{ActorID: "ses_x", Project: proj, Role: "pair"}
	_ = st.PutShareGrant(jam.ShareGrant{SessionID: "ses_i", UserID: carol, Rights: []string{"ssh"}, ExpiresAt: now.Add(time.Hour), GrantedBy: "op:local"})
	_ = st.PutShareGrant(jam.ShareGrant{SessionID: "ses_i", UserID: bob, Rights: []string{"ssh"}, ExpiresAt: now.Add(-time.Minute), GrantedBy: "op:local"}) // expired

	for _, tc := range []struct {
		name string
		p    collab.Principal
		inst jam.Instance
		r    collab.Right
		want collab.Reason
		ok   bool
	}{
		{"operator any", collab.Principal{Operator: "op1"}, ic, collab.SSH, "operator", true},
		{"requester own personal", collab.Principal{UserID: alice}, personal, collab.SSH, "requester", true},
		{"member default ssh none", collab.Principal{UserID: bob}, ic, collab.SSH, "", false},
		{"member default preview", collab.Principal{UserID: bob}, ic, collab.Preview, "role", true},
		{"member role allows ssh", collab.Principal{UserID: bob}, pair, collab.SSH, "role", true},
		{"non-member role policy", collab.Principal{UserID: carol}, pair, collab.SSH, "", false},
		{"grant holder", collab.Principal{UserID: carol}, ic, collab.SSH, "grant", true},
		{"grant other session", collab.Principal{UserID: carol}, pair, collab.SSH, "", false},
		{"expired grant", collab.Principal{UserID: bob}, ic, collab.SSH, "", false},
		{"grant without that right", collab.Principal{UserID: carol}, ic, collab.Preview, "", false},
		{"nobody", collab.Principal{}, ic, collab.Preview, "", false},
	} {
		got, ok := collab.Decide(st, now, tc.p, tc.inst, tc.r)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: Decide = (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestValidateGrant(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ok := jam.ShareGrant{SessionID: "s", UserID: "usr_1", Rights: []string{"ssh"}, ExpiresAt: now.Add(collab.DefaultGrant)}
	if err := collab.ValidateGrant(now, ok); err != nil {
		t.Fatal(err)
	}
	for name, g := range map[string]jam.ShareGrant{
		"bad right": {SessionID: "s", UserID: "usr_1", Rights: []string{"root"}, ExpiresAt: now.Add(time.Hour)},
		"too long":  {SessionID: "s", UserID: "usr_1", Rights: []string{"ssh"}, ExpiresAt: now.Add(collab.MaxGrant + time.Hour)},
		"past":      {SessionID: "s", UserID: "usr_1", Rights: []string{"ssh"}, ExpiresAt: now.Add(-time.Hour)},
		"no rights": {SessionID: "s", UserID: "usr_1", ExpiresAt: now.Add(time.Hour)},
	} {
		if collab.ValidateGrant(now, g) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
```

(Write `mustProject`/`mustUser`/`mustRole` helpers in the test file using the store's real methods: `grep -n 'func (.*) AddUser\|AddMembership\|CreateProject\|PutRole' internal/jam/*.go` — membership is what `jam.MembersOf`/`MemberByLogin` read.)

- [ ] **Step 2: Run** `go test ./internal/jam/collab/` → FAIL (package/fields undefined).

- [ ] **Step 3: Implement**

`collab.go`:

```go
// Package collab decides who may reach a running studio (studio
// collaboration, docs/usage/jam/collaboration.md). Rights are independent;
// the first matching rule wins and every check is live.
package collab

type Right string

const (
	Preview Right = "preview"
	SSH     Right = "ssh"

	DefaultGrant = 8 * time.Hour
	MaxGrant     = 7 * 24 * time.Hour
)

type Principal struct {
	Operator string   // operator id when the caller is on the operator plane
	UserID   ident.ID // roster user, when known
}

type Reason string

func Decide(store jam.Store, now time.Time, p Principal, inst jam.Instance, r Right) (Reason, bool) {
	if p.Operator != "" {
		return "operator", true
	}
	if p.UserID == "" {
		return "", false
	}
	if inst.SessionKind == jam.SessionKindPersonal && inst.OwnerID == p.UserID {
		return "requester", true
	}
	if role, ok := store.GetRole(inst.Project, inst.Role); ok && role.Collaborate.Allows(string(r)) && isMember(store, inst.Project, p.UserID) {
		return "role", true
	}
	for _, g := range store.ShareGrants(inst.ActorID) {
		if g.UserID == p.UserID && now.Before(g.ExpiresAt) && slices.Contains(g.Rights, string(r)) {
			return "grant", true
		}
	}
	return "", false
}

func ValidateGrant(now time.Time, g jam.ShareGrant) error {
	if g.SessionID == "" || g.UserID == "" {
		return errors.New("grant needs a session and a user")
	}
	if len(g.Rights) == 0 {
		return errors.New("grant needs at least one right (preview, ssh)")
	}
	for _, r := range g.Rights {
		if r != string(Preview) && r != string(SSH) {
			return fmt.Errorf("unknown right %q (want preview or ssh)", r)
		}
	}
	if !g.ExpiresAt.After(now) {
		return errors.New("grant already expired")
	}
	if g.ExpiresAt.After(now.Add(MaxGrant)) {
		return fmt.Errorf("grant longer than %s", MaxGrant)
	}
	return nil
}
```

(`isMember` uses the existing membership lookup — `jam.MembersOf(store, projectID)` and compare user ids. `store.GetRole` takes the project id; confirm with `grep -n 'GetRole(' internal/jam/store.go`.)

`CollaboratePolicy.Allows` in `identity.go`:

```go
// CollaboratePolicy is a role's studio-collaboration policy: which rights the
// project's members get on its studios. "" means the default (preview:
// members, ssh: none).
type CollaboratePolicy struct {
	Preview string `json:"preview,omitempty"` // members | none
	SSH     string `json:"ssh,omitempty"`     // members | none
}

func (p CollaboratePolicy) Allows(right string) bool {
	switch right {
	case "preview":
		return p.Preview != "none"
	case "ssh":
		return p.SSH == "members"
	}
	return false
}
```

Validate `Preview`/`SSH` ∈ {"", "members", "none"} in `POST /admin/roles` with a 400.

Migration `0015_share_grants.sql`:

```sql
-- Studio collaboration share grants (docs/usage/jam/collaboration.md): ad-hoc
-- access to one session for one user, time-boxed. Keyed by session id; removed
-- when the session ends.
CREATE TABLE share_grants (
    session_id text        NOT NULL,
    user_id    text        NOT NULL REFERENCES users(id),
    doc        jsonb       NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (session_id, user_id)
);
```

`sharegrants.go`: the `ShareGrant` type, `ShareGrantStore` interface, memState map `grants map[string]map[ident.ID]ShareGrant` with read methods and `applyPutShareGrant`/`applyRemoveShareGrant`/`applyRemoveShareGrantsOf`, following `standing_sessions.go`. MemStore and PostgresStore implement the interface (Postgres: `INSERT ... ON CONFLICT (session_id,user_id) DO UPDATE`, `DELETE`, load at startup). Add `share_grants` to the TRUNCATE list in `pgstore_testhelpers.go`. Supervisor `Teardown`: after `RemoveInstance`, `store.RemoveShareGrantsOf(actorID)` (best-effort, logged).

Storetest conformance `storetest/sharegrants.go` (wire into the conformance runner like `channels.go`): put/list/replace/remove/remove-of, and a grant for a removed session disappears.

- [ ] **Step 4: Run** `go test ./internal/jam/... ./cmd/at-jam/ -count=1` and `go test -tags integration ./internal/jam/... ` with `JAM_TEST_POSTGRES_DSN` where available → PASS.

- [ ] **Step 5: Docs** — `docs/usage/jam/roster.md`: one paragraph + flag rows for `--collaborate-preview` / `--collaborate-ssh` (defaults), linking `collaboration.md` (created in Task 10).

- [ ] **Step 6: Commit** `git commit -m "feat(jam): collaboration authz — role collaborate policy, share grants, Decide"`

---

### Task 5: Jam SSH CA and the studio's trust of it

**Files:**
- Create: `internal/jam/sshca/sshca.go`, `internal/jam/sshca/sshca_test.go`, `internal/assemble/hardening/image-files/usr/local/lib/cove/install-ssh-ca.sh`, `internal/backend/colima/sshca.go`
- Modify: `go.mod` (`golang.org/x/crypto`), `internal/assemble/hardening/image-files/etc/ssh/sshd_config.d/cove.conf`, `internal/assemble/embed_test.go` (key files list), `internal/assemble/assemble.go` (`Revision` bump), `internal/backend/backend.go` (`SSHCA` interface), `internal/jam/launcher/launcher.go` (install at Raise and Resume), `internal/jam/supervisor.go` (`RaiseSpec` unchanged — CA pubkey goes on `launcher.Config.SSHUserCA []byte`), `cmd/at-jam/config.go` (`ssh:` block + validation), `cmd/at-jam/main.go` (resolve CA, build `*sshca.CA`, pass pubkey to launcher)
- Test: `internal/jam/sshca/sshca_test.go`, `cmd/at-jam/config_test.go`, `internal/backend/colima/sshca_test.go`, `internal/jam/launcher/launcher_test.go`

**Interfaces:**
- Produces:
  - `func sshca.Parse(pemKey []byte) (*CA, error)` (OpenSSH or PKCS#8 ed25519 private key); `func (c *CA) PublicKey() []byte` (authorized_keys line); `func (c *CA) Issue(userPub []byte, keyID string, now time.Time) ([]byte, time.Time, error)` → authorized-key-format cert, expiry (`now+8h`); principal `agent`; extensions `permit-pty`, `permit-port-forwarding`, `permit-agent-forwarding: no`, `permit-user-rc`.
  - `const sshca.Validity = 8 * time.Hour`, `sshca.Principal = "agent"`.
  - backend: `type SSHCA interface { InstallSSHUserCA(container string, pub []byte) error }`; Colima impl execs `install-ssh-ca.sh` as root with the pubkey on stdin.
  - serve config: `type sshConfig struct { CACred string \`yaml:"ca-cred"\` }`; `serveConfig.SSH *sshConfig \`yaml:"ssh"\``; `func (c serveConfig) validateSSH() error` (required, demanded, plain static — same rules as `validateMetrics`).

- [ ] **Step 1: Failing CA test**

```go
package sshca

func TestIssueCertTrustedBySSH(t *testing.T) {
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	pemBlock, _ := ssh.MarshalPrivateKey(caPriv, "")
	ca, err := Parse(pem.EncodeToMemory(pemBlock))
	if err != nil {
		t.Fatal(err)
	}
	userPub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshUser, _ := ssh.NewPublicKey(userPub)
	now := time.Unix(1_000_000, 0)
	certBytes, exp, err := ca.Issue(ssh.MarshalAuthorizedKey(sshUser), "user=usr_1 studio=ses_a", now)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(Validity)) {
		t.Fatalf("expiry %v", exp)
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey(certBytes)
	if err != nil {
		t.Fatal(err)
	}
	cert := pk.(*ssh.Certificate)
	if cert.KeyId != "user=usr_1 studio=ses_a" || !slices.Equal(cert.ValidPrincipals, []string{"agent"}) || cert.CertType != ssh.UserCert {
		t.Fatalf("cert = %+v", cert)
	}
	caPub, _, _, _, _ := ssh.ParseAuthorizedKey(ca.PublicKey())
	checker := ssh.CertChecker{IsUserAuthority: func(k ssh.PublicKey) bool { return bytes.Equal(k.Marshal(), caPub.Marshal()) }, Clock: func() time.Time { return now.Add(time.Hour) }}
	if err := checker.CheckCert("agent", cert); err != nil {
		t.Fatalf("cert not accepted by an ssh checker: %v", err)
	}
	checker.Clock = func() time.Time { return now.Add(Validity + time.Minute) }
	if checker.CheckCert("agent", cert) == nil {
		t.Fatal("expired cert accepted")
	}
	if _, ok := cert.Permissions.Extensions["permit-port-forwarding"]; !ok {
		t.Fatal("port forwarding not permitted")
	}
	if _, ok := cert.Permissions.Extensions["permit-agent-forwarding"]; ok {
		t.Fatal("agent forwarding must not be permitted")
	}
}

func TestParseRejectsNonEd25519AndGarbage(t *testing.T) {
	if _, err := Parse([]byte("not a key")); err == nil {
		t.Fatal("garbage accepted")
	}
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	b, _ := ssh.MarshalPrivateKey(rsaKey, "")
	if _, err := Parse(pem.EncodeToMemory(b)); err == nil {
		t.Fatal("non-ed25519 CA accepted")
	}
}
```

- [ ] **Step 2: Run** `GOPROXY=https://proxy.golang.org,direct go get golang.org/x/crypto/ssh && go test ./internal/jam/sshca/` → FAIL (undefined `Parse`).

- [ ] **Step 3: Implement `sshca.go`**

```go
// Package sshca is Jam's SSH certificate authority for studio collaboration:
// short-lived user certs for principal "agent" that a studio's sshd trusts via
// TrustedUserCAKeys. The CA key lives only in memory; errors never echo it.
package sshca

const (
	Validity  = 8 * time.Hour
	Principal = "agent"
)

type CA struct{ signer ssh.Signer }

func Parse(pemKey []byte) (*CA, error) {
	raw, err := ssh.ParseRawPrivateKey(pemKey)
	if err != nil {
		return nil, errors.New("ssh CA key is not a valid private key")
	}
	k, ok := raw.(*ed25519.PrivateKey)
	if !ok {
		if v, ok2 := raw.(ed25519.PrivateKey); ok2 {
			k = &v
		} else {
			return nil, errors.New("ssh CA key must be ed25519")
		}
	}
	s, err := ssh.NewSignerFromKey(*k)
	if err != nil {
		return nil, err
	}
	return &CA{signer: s}, nil
}

func (c *CA) PublicKey() []byte { return ssh.MarshalAuthorizedKey(c.signer.PublicKey()) }

func (c *CA) Issue(userPub []byte, keyID string, now time.Time) ([]byte, time.Time, error) {
	pk, _, _, _, err := ssh.ParseAuthorizedKey(userPub)
	if err != nil {
		return nil, time.Time{}, errors.New("not an SSH public key")
	}
	if pk.Type() != ssh.KeyAlgoED25519 {
		return nil, time.Time{}, errors.New("user key must be ed25519")
	}
	exp := now.Add(Validity)
	serial := make([]byte, 8)
	_, _ = rand.Read(serial)
	cert := &ssh.Certificate{
		Key: pk, Serial: binary.BigEndian.Uint64(serial), CertType: ssh.UserCert, KeyId: keyID,
		ValidPrincipals: []string{Principal},
		ValidAfter:      uint64(now.Add(-time.Minute).Unix()), ValidBefore: uint64(exp.Unix()),
		Permissions: ssh.Permissions{Extensions: map[string]string{
			"permit-pty": "", "permit-port-forwarding": "", "permit-user-rc": "",
		}},
	}
	if err := cert.SignCert(rand.Reader, c.signer); err != nil {
		return nil, time.Time{}, err
	}
	return ssh.MarshalAuthorizedKey(cert), exp, nil
}
```

- [ ] **Step 4: Studio side** — append to `cove.conf`:

```
# Studio collaboration: trust Jam's SSH user CA (short-lived certs for "agent",
# issued only to authorized humans; reachable only through Jam's tunnel). The
# file is installed per studio at setup; absent = no human SSH.
TrustedUserCAKeys /etc/ssh/jam_user_ca.pub
```

`install-ssh-ca.sh` (sealed, root-only, mode 0755 via the Dockerfile's existing chmod pattern for `/usr/local/lib/cove/*`):

```bash
#!/usr/bin/env bash
# Install Jam's SSH user-CA public key (stdin, one authorized_keys line) as
# /etc/ssh/jam_user_ca.pub. SEALED + ROOT-ONLY: invoked only by the host via
# `docker exec -u root` at studio setup. Validates the line before writing.
set -euo pipefail
key="$(head -c 4096)"
case "$key" in
  ssh-ed25519\ *) ;;
  *) echo "install-ssh-ca: not an ssh-ed25519 public key" >&2; exit 2 ;;
esac
umask 022
printf '%s\n' "$key" > /etc/ssh/jam_user_ca.pub.tmp
mv /etc/ssh/jam_user_ca.pub.tmp /etc/ssh/jam_user_ca.pub
```

sshd reads `TrustedUserCAKeys` per connection, so no reload is needed. Add both files to `TestEmbedsContainKeyFiles`; bump `assemble.Revision` (`"2"` → `"3"`).

Backend: `SSHCA` interface in `backend.go`; `colima/sshca.go`:

```go
const sshCAHelper = "/usr/local/lib/cove/install-ssh-ca.sh"

// InstallSSHUserCA installs Jam's SSH user-CA public key in container via the
// sealed helper, as root, the key on stdin.
func (c *Colima) InstallSSHUserCA(container string, pub []byte) error {
	return c.runRoleEgress(bytes.NewReader(pub), c.helperArgs(container, sshCAHelper))
}
```

(test with `runner.Fake` like `egress_test.go`: asserts argv `docker ... exec -i -u root <ctr> /usr/local/lib/cove/install-ssh-ca.sh` and stdin = the key.)

Launcher: `Config.SSHUserCA []byte`; in `Raise` after `applyRoleEgress` and before `LaunchCoveMaster`, and in `Resume`/`Unpause` paths: if `len(l.cfg.SSHUserCA) > 0`, type-assert `backend.SSHCA` and install; a backend without it logs a WARN once and continues (SSH collaboration unavailable for that studio — never fail the raise). Launcher test with a fake Ops asserting the install call happens at raise with the key.

Serve config (`config.go`) + `validateSSH` (call next to `validateMetrics`); in `main.go` resolve `specs[cfg.SSH.CACred]` with `secret.Resolve`, `sshca.Parse`, set `launcher.Config.SSHUserCA = ca.PublicKey()`. `config_test.go`: valid / missing ca-cred / undemanded / gcp credential refused.

- [ ] **Step 5: Run** `go test ./internal/jam/sshca/ ./internal/assemble/ ./internal/backend/... ./internal/jam/launcher/ ./cmd/at-jam/ -count=1 && just lint` → PASS.

- [ ] **Step 6: Docs** — `docs/usage/jam/serve.md`: an `ssh` row (`ca-cred` required; unset = SSH collaboration off; the key is ed25519 in the credentials file, generate with `ssh-keygen -t ed25519 -f jam-ssh-ca -N ''`).

- [ ] **Step 7: Commit** `git commit -m "feat(jam): SSH user CA — issue short-lived certs, studios trust it via the hardening layer"`

---

### Task 6: Cert and SSH upgrade endpoints

**Files:**
- Create: `internal/jam/collabhttp/handler.go`, `internal/jam/collabhttp/handler_test.go`
- Modify: `cmd/at-jam/main.go` (wire broker → attach server, supervisor; mount routes)

**Interfaces:**
- Consumes: `TunnelBroker.Open` (T1), `collab.Decide` (T4), `sshca.CA.Issue` (T5), `Intercom.Notify`.
- Produces: `type Deps struct { Store jam.Store; CA *sshca.CA; Tunnels interface{ Open(context.Context, string, int32) (net.Conn, error) }; Notify func(inst jam.Instance, body string); Now func() time.Time; Log *slog.Logger; Principal func(*http.Request) (collab.Principal, string /*display name*/, bool) }`; `func Routes(prefix string, d Deps) http.Handler` serving `POST {prefix}/studios/{id}/ssh/cert` (JSON `{"public_key": "..."}` → `{"certificate": "...", "expires_at": "..."}`) and `GET {prefix}/studios/{id}/ssh` (requires `Connection: Upgrade`, `Upgrade: jam-tunnel`; responds `101 Switching Protocols`, hijacks, splices to `Tunnels.Open(id, 2222)`).
  - Status codes: no principal → 401; unknown studio → 404; `Decide` false → 403; `ErrStudioNotConnected` → 503 `studio not connected`; `ErrDialBackTimeout` → 504 with its message; `ErrTooManyTunnels` → 429.

- [ ] **Step 1: Failing tests** (httptest server; a fake `Tunnels` returning one end of `net.Pipe`):

```go
func TestCertEndpointIssuesForAuthorizedOnly(t *testing.T) {
	d, st := testDeps(t) // MemStore with an instance ses_a (role ic, ssh: members) and users alice (member), carol (none)
	h := collabhttp.Routes("/me", d)
	pub := testUserPub(t)
	for name, tc := range map[string]struct{ who string; want int }{
		"member allowed": {"alice", 200}, "stranger": {"carol", 403}, "anonymous": {"", 401},
	} {
		req := httptest.NewRequest("POST", "/me/studios/ses_a/ssh/cert", strings.NewReader(`{"public_key":`+strconv.Quote(pub)+`}`))
		req = withTestPrincipal(req, tc.who)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	req := httptest.NewRequest("POST", "/me/studios/ses_nope/ssh/cert", strings.NewReader(`{"public_key":"x"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withTestPrincipal(req, "alice"))
	if rec.Code != 404 {
		t.Errorf("unknown studio: %d", rec.Code)
	}
	_ = st
}

func TestUpgradeSplicesAndNotifies(t *testing.T) {
	d, _ := testDeps(t)
	var notices []string
	d.Notify = func(inst jam.Instance, body string) { notices = append(notices, body) }
	studio, human := net.Pipe()
	d.Tunnels = fakeTunnels{conn: human}
	srv := httptest.NewServer(withTestPrincipalMW(collabhttp.Routes("/me", d), "alice"))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "GET /me/studios/ses_a/ssh HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: jam-tunnel\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	go func() { _, _ = studio.Write([]byte("SSH-2.0-x\r\n")) }()
	line, _ := br.ReadString('\n')
	if line != "SSH-2.0-x\r\n" {
		t.Fatalf("relayed %q", line)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "alice connected over SSH") {
		t.Fatalf("notices %v", notices)
	}
}

func TestUpgradeMapsBrokerErrors(t *testing.T) {
	for err, want := range map[error]int{attach.ErrStudioNotConnected: 503, attach.ErrDialBackTimeout: 504, attach.ErrTooManyTunnels: 429} {
		d, _ := testDeps(t)
		d.Tunnels = fakeTunnels{err: err}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/me/studios/ses_a/ssh", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "jam-tunnel")
		collabhttp.Routes("/me", d).ServeHTTP(rec, withTestPrincipal(req, "alice"))
		if rec.Code != want {
			t.Errorf("%v → %d, want %d", err, rec.Code, want)
		}
	}
}
```

(Helpers `testDeps`, `withTestPrincipal`, `withTestPrincipalMW`, `testUserPub`, `fakeTunnels` live in the test file; `Deps.Principal` is how the test injects the caller.)

- [ ] **Step 2: Run** → FAIL (package missing).

- [ ] **Step 3: Implement `handler.go`** — the cert handler: principal → instance (`store.GetInstance(id)`) → `collab.Decide(..., collab.SSH)` → key-id `user=<id> studio=<session>` (operators `user=op:<id>`) → `CA.Issue` → audit log `"ssh cert issued"` with user/session/reason/expires. The upgrade handler: same checks, then `Tunnels.Open(r.Context()-independent ctx with 15 s timeout, id, 2222)` **before** writing 101 (so errors map to status codes), then `http.ResponseController(w).Hijack()`, write `HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: jam-tunnel\r\n\r\n`, flush, `Notify(inst, name+" connected over SSH")`, audit `"ssh tunnel opened"`, `io.Copy` both ways, audit `"ssh tunnel closed"` with duration. Body capped at 16 KiB. Nil `CA` ⇒ `Routes` returns a handler that 404s everything (feature off).

- [ ] **Step 4: Wire in `main.go`**
  - `broker := attach.NewTunnelBroker(rsrv.PushOpenTunnel, attach.TunnelOptions{Log: log})`; `rsrv.SetTunnelBroker(broker)`; `sup.SetTunnelCloser(broker)`.
  - Operator plane: `jam.WithAdminRoute("/admin/studios/", collabhttp.Routes("/admin", deps(operatorPrincipal)))` where `operatorPrincipal` returns `collab.Principal{Operator: jam.OperatorID(r)}` (and the user id from `st.UserByLogin(jam.OperatorID(r))` when present, for audit names).
  - Participant plane (Task 7) mounts the same Routes under `/me`.
  - `Notify: func(inst jam.Instance, body string) { _, _ = ic.Notify(inst, "", body) }`.

- [ ] **Step 5: Run** `go test ./internal/jam/collabhttp/ ./cmd/at-jam/ -count=1` → PASS.

- [ ] **Step 6: Commit** `git commit -m "feat(jam): SSH cert + tunnel upgrade endpoints with audit and session notice"`

---

### Task 7: Participant bearer (`at-jam login --participant`)

**Files:**
- Create: `internal/jam/collabhttp/participant.go`, `internal/jam/collabhttp/participant_test.go`
- Modify: `internal/jam/deviceflow/deviceflow.go` (`Token.IDToken`), `cmd/at-jam/main.go` (`login --participant`; mount `/me/studios/` before the cookie gate), `cmd/at-jam/clientconfig.go` (`cachedToken.IDToken`, `resolveParticipantToken(app)`)
- Test: `internal/jam/deviceflow/deviceflow_test.go`, `cmd/at-jam/clientconfig_test.go`

**Interfaces:**
- Produces: `func ParticipantBearer(verify func(ctx context.Context, raw string) (issuer, subject string, err error), store jam.ParticipantStore) func(*http.Request) (collab.Principal, string, bool)`; `deviceflow.Token{AccessToken, IDToken string; ExpiresIn int}`; `cachedToken.IDToken`; `func resolveParticipantToken(app string) (string, error)` (error says `run: at-jam login --participant`).
- The verifier: `oidc.NewProvider(issuer).Verifier(&oidc.Config{ClientID: <operator-auth.oidc.device-client-id>})` — the device client is the ID token's audience.

- [ ] **Step 1: Failing test**

```go
func TestParticipantBearer(t *testing.T) {
	st := participantStore(t) // alice bound to issuer https://idp / sub "alice-sub", member of acme
	verify := func(_ context.Context, raw string) (string, string, error) {
		switch raw {
		case "good":
			return "https://idp", "alice-sub", nil
		case "unbound":
			return "https://idp", "nobody", nil
		}
		return "", "", errors.New("bad token")
	}
	pr := collabhttp.ParticipantBearer(verify, st)
	for name, tc := range map[string]struct{ hdr string; ok bool }{
		"good": {"Bearer good", true}, "unbound": {"Bearer unbound", false},
		"expired or forged": {"Bearer bad", false}, "missing": {"", false}, "basic": {"Basic Z29vZA==", false},
	} {
		req := httptest.NewRequest("GET", "/me/studios/x/ssh", nil)
		if tc.hdr != "" {
			req.Header.Set("Authorization", tc.hdr)
		}
		p, name2, ok := pr(req)
		if ok != tc.ok {
			t.Errorf("%s: ok=%v", name, ok)
		}
		if ok && (p.UserID == "" || p.Operator != "" || name2 != "alice") {
			t.Errorf("%s: principal %+v %q", name, p, name2)
		}
	}
}
```

and in `deviceflow_test.go`: the fake token endpoint returns `{"access_token":"a","id_token":"i","expires_in":60}` → `PollToken` returns `IDToken == "i"`.

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: Implement** — `ParticipantBearer` strips `Bearer `, calls `verify`, then `jam.ParticipantByIdentity(store, iss, sub)` → `collab.Principal{UserID: p.UserID}`, `p.Name`. `Routes` returns 401 with `WWW-Authenticate: Bearer error="invalid_token"` and body `sign in: at-jam login --participant` when the principal func returns false and an `Authorization` header was present but invalid; 403 for a verified-but-unbound identity (the func reports that via a sentinel — extend the signature to return an `outcome` if needed, mirroring `browserauth.SessionOutcome`). `deviceflow`: decode `id_token` into `Token.IDToken`. `cmdLogin --participant`: same flow, requires scope to contain `openid` (default is `openid`), saves to a separate token profile `<app>-participant` (reuse `saveToken` with that app name) storing `IDToken` and its `exp` (from `parseJWTClaims`). Mount in `main.go`: `meMux.Handle("/me/studios/", collabhttp.Routes("/me", deps(participantBearer)))` — **outside** `meGate` (bearer, not cookie) and outside `RequireSameOrigin` (no browser). Only when `operator-auth.oidc.device-client-id` is set; otherwise not mounted.

- [ ] **Step 4: Run** `go test ./internal/jam/collabhttp/ ./internal/jam/deviceflow/ ./cmd/at-jam/ -count=1` → PASS.

- [ ] **Step 5: Commit** `git commit -m "feat(jam): participant bearer for /me/studios — at-jam login --participant"`

---

### Task 8: `at-jam ssh-config`, `ssh-proxy`, `ssh`

**Files:**
- Create: `cmd/at-jam/ssh.go`, `cmd/at-jam/ssh_test.go`
- Modify: `internal/jam/adminclient/adminclient.go` (`IssueSSHCert`), `cmd/at-jam/main.go` (register commands)

**Interfaces:**
- Consumes: the endpoints (T6) under `/admin` (operator token) or `/me` (participant token; chosen by `--participant`, default: participant token if present, else operator).
- Produces: `func (c *Client) IssueSSHCert(prefix, studio string, pub []byte) (cert []byte, expires time.Time, err error)`; files under `~/.config/at-jam/ssh/`: `id_ed25519` (0600, generated once), `<studio>-cert.pub`; ssh config block written to `~/.ssh/config.d/at-jam-<studio>` with `Include` guidance printed (or `--write` appends a `Host` block to `~/.ssh/config` once, idempotent by marker comments).

- [ ] **Step 1: Failing tests** — (a) `ssh-config` output contains `Host jam-ses_a`, `ProxyCommand at-jam ssh-proxy ses_a`, `User agent`, `IdentityFile`, `CertificateFile`; (b) `ssh-proxy` against an httptest server implementing the 101 upgrade echoes stdin→stdout bytes; (c) `ssh-proxy` renews the cert file when the existing cert has < 1 h left and leaves it alone when > 1 h (inject a clock); (d) a 403 from the server prints `you don't have SSH access to this studio (ask an operator: at-jam studio share …)` and exits 1.

```go
func TestSSHConfigBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	var out, errb bytes.Buffer
	if code := run([]string{"ssh-config", "ses_a"}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"Host jam-ses_a", "ProxyCommand at-jam ssh-proxy ses_a", "User agent", "IdentityFile ", "CertificateFile "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if fi, err := os.Stat(filepath.Join(home, ".config", "at-jam", "ssh", "id_ed25519")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key not created 0600: %v %v", fi, err)
	}
}

func TestSSHProxyPipesThroughUpgrade(t *testing.T) {
	srv := upgradeEchoServer(t) // 101 then echoes bytes
	stdin := strings.NewReader("hello\n")
	var stdout bytes.Buffer
	err := proxyOnce(context.Background(), proxyOpts{BaseURL: srv.URL, Prefix: "/me", Studio: "ses_a", Token: "tok", In: stdin, Out: &stdout})
	if err != nil || stdout.String() != "hello\n" {
		t.Fatalf("err=%v out=%q", err, stdout.String())
	}
}
```

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: Implement** — `ssh-proxy`: ensure key; read cert expiry with `ssh.ParseAuthorizedKey` → `ValidBefore`; renew via `IssueSSHCert` when < 1 h; then a raw `net.Dial`/`tls.Dial` to the admin URL host, write the upgrade request with `Authorization: Bearer <token>`, read the `101`, then `io.Copy` stdin→conn and conn→stdout until either ends. TLS: use the system roots (`tls.Config{ServerName: host}`), plain TCP for `http://` loopback URLs. `ssh`: run `ssh-config` logic, then `syscall.Exec` the `ssh` binary with `jam-<studio>` and pass-through args. No token or key ever on argv or in logs.

- [ ] **Step 4: Run** `go test ./cmd/at-jam/ ./internal/jam/adminclient/ -count=1 && just lint` → PASS.

- [ ] **Step 5: Commit** `git commit -m "feat(at-jam): ssh-config, ssh-proxy and ssh for studio collaboration"`

---

### Task 9: Sharing CLI/API and the studio Access panel

**Files:**
- Create: `cmd/at-jam/share.go`, `internal/jam/sharegrants_http.go`, `internal/jam/adminui/access.go`, `internal/jam/adminui/templates/access.html` (partial)
- Modify: `internal/jam/adminclient/adminclient.go`, `cmd/at-jam/main.go` (`studio share|unshare|access` subcommands), `internal/jam/adminui/agent.go` + `templates/agent.html` (Access card), `internal/jam/adminui/adminui.go` (`WithAccess` option), `internal/jam/supervisor.go`/broker wiring (revocation closes the user's tunnels — slice 1 closes **all** tunnels of the session on unshare, which is acceptable and simple)
- Test: `internal/jam/sharegrants_http_test.go`, `cmd/at-jam/share_test.go`, `internal/jam/adminui/access_test.go`

**Interfaces:**
- Produces: admin API `POST /admin/studios/{id}/shares` `{user, rights, for}` → 201; `DELETE /admin/studios/{id}/shares/{user}`; `GET /admin/studios/{id}/access` → `{grants: [...], live: [...]}`; participant equivalents under `/me/studios/{id}/shares` (grantor must hold each right being granted: `collab.Decide` per right). CLI: `at-jam studio share <studio> --with user:<name> --rights preview,ssh --for 4h`, `studio unshare <studio> --with user:<name>`, `studio access <studio>`.

- [ ] **Step 1: Failing tests** — share as operator creates a grant (default 8 h, `--for 8d` → 400 "longer than 168h0m0s"); a participant holding `ssh` can share `ssh` but not `preview` they lack (403); unshare removes and calls `CloseAll`; the Access card lists each grant with user, rights, expiry and the "why" for requester/role access; unshare button is CSRF-guarded (`originGuard`) like other UI writes.

```go
func TestShareAPI(t *testing.T) {
	h, st, closer := shareHarness(t) // admin handler + MemStore + recording tunnel closer
	body := `{"user":"carol","rights":["ssh"]}`
	rec := do(h, "POST", "/admin/studios/ses_a/shares", body)
	if rec.Code != 201 {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}
	g := st.ShareGrants("ses_a")
	if len(g) != 1 || g[0].ExpiresAt.Sub(g[0].CreatedAt) != collab.DefaultGrant {
		t.Fatalf("grants %+v", g)
	}
	if rec := do(h, "POST", "/admin/studios/ses_a/shares", `{"user":"carol","rights":["ssh"],"for":"192h"}`); rec.Code != 400 {
		t.Fatalf("over-long grant: %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/admin/studios/ses_a/shares/carol", ""); rec.Code != 204 {
		t.Fatalf("unshare: %d", rec.Code)
	}
	if len(st.ShareGrants("ses_a")) != 0 || !slices.Equal(closer.ids, []string{"ses_a"}) {
		t.Fatalf("unshare did not remove/close: %v %v", st.ShareGrants("ses_a"), closer.ids)
	}
}
```

- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** per the interfaces (resolve `user:<name>` via the registry's user-by-name lookup; `granted_by` = `op:<id>` or `usr_…`; audit log lines for share/unshare). **Step 4: Run** `go test ./internal/jam/... ./cmd/at-jam/ -count=1` → PASS.

- [ ] **Step 5: Commit** `git commit -m "feat(jam): studio share/unshare/access and the Access panel"`

---

### Task 10: Real-ssh integration test and docs

**Files:**
- Create: `internal/jam/collabhttp/integration_test.go` (`//go:build integration`), `docs/usage/jam/collaboration.md`
- Modify: `docs/usage/jam/INDEX.md`, `docs/usage/jam/ui.md` (Access panel), `docs/usage/jam/coves.md` (Attach carries OpenTunnel; Tunnel RPC), `docs/OVERVIEW.md` (one line in the Jam section), spec `docs/superpowers/specs/2026-10-20-studio-collaboration-design.md` (§2 two planes amendment), `justfile` (`integration-jam` includes `./internal/jam/collabhttp/`)

- [ ] **Step 1: Integration test** — start a real `sshd` on a free loopback port with a temp config (template: `internal/connect/integration_test.go`: `ListenAddress 127.0.0.1`, `TrustedUserCAKeys <tmp ca.pub>`, `AuthorizedPrincipalsCommand none`, run as the current user so the principal is `$USER` — issue the cert with principal = current user for the test via a test-only `sshca.IssueFor(principal)` helper or by building the cert in the test), a `TunnelBroker` whose `push` immediately runs `covemaster`'s dial-back against an in-process bufconn gRPC server, `collabhttp.Routes` behind httptest, and run `ssh -o ProxyCommand="go run ./cmd/at-jam ssh-proxy ..."` — or, simpler and still end to end, call `proxyOnce` with stdin/stdout piped to an `ssh -o ProxyCommand="cat <fifo>"`-free client: use `golang.org/x/crypto/ssh` client over the upgraded conn, authenticate with the cert, run `echo ok`, assert `ok`. Skip unless `sshd` is on PATH.

- [ ] **Step 2: Write `docs/usage/jam/collaboration.md`**

```markdown
---
summary: Studio collaboration — reaching an agent's running studio through Jam: SSH and IDEs (VS Code Remote-SSH, JetBrains Gateway) via `at-jam ssh`, who may connect (operators, the session's requester, role `collaborate` policy, share grants), and how it works (Jam-issued short-lived certs over a studio-dialed tunnel).
read_when: You want to SSH or point an IDE at an agent's studio, let a teammate in (`studio share`), set a role's `collaborate` policy, enable the `ssh:` serve config, or an `at-jam ssh` connection was refused or timed out.
owns: the studio-collaboration user and operator guide — at-jam ssh-config/ssh-proxy/ssh and login --participant, the access rules and share grants, the ssh: serve-config block, and the tunnel/cert mechanism at the level an operator needs
prereqs: serve.md (serve config, credentials), roster.md (roles, users), operators.md (login)
tier: leaf
updated: <date>
---
```

Body sections: **Quick start** (operator: `ssh-keygen -t ed25519 -f jam-ssh-ca -N ''`, add it to `credentials.yml`, `ssh: { ca-cred: … }`, restart; collaborator: `at-jam login --participant`, `at-jam ssh <studio>` or `at-jam ssh-config <studio>` then VS Code "Connect to Host… jam-<studio>"), **Who may connect** (the four rules, defaults, `role add --collaborate-ssh members`), **Sharing** (`studio share|unshare|access`, 8 h default, 7 d max, keyed to the session), **How it works** (Attach `OpenTunnel` → studio dials `Tunnel` back on 443 → loopback `:2222`; 8 h certs, principal `agent`; nothing new listens in the studio; egress unchanged), **Troubleshooting** table (401 → `at-jam login --participant`; 403 → ask for a share; 503 not connected; 504 dial-back timeout → studio image predates collaboration, `at-jam standing upgrade`; cert expired → just reconnect). Note: participant ID tokens are short-lived (typically ~1 h); renewal needs a fresh `at-jam login --participant`.

INDEX row (after `standing-state.md`):

```markdown
| [collaboration.md](collaboration.md) | You want to SSH or point an IDE at an agent's studio, share a studio with a teammate, set a role's `collaborate` policy or the `ssh:` serve config, or an `at-jam ssh` connection was refused or timed out. |
```

Amend spec §2's "Human entry point" paragraph to the two-plane wording from this plan's header.

- [ ] **Step 3: Docs audit + full suite** — `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -3` (no new errors), `just test && just lint`, and `just integration-jam` where sshd/Postgres exist.

- [ ] **Step 4: Commit** `git commit -m "docs(jam): studio collaboration guide; real-sshd integration test"`

---

### Task 11: Manual acceptance on the Jam host (operator)

Not code — record results on the PR.

- [ ] Generate a CA key, add `ssh: { ca-cred: jam-ssh-ca }`, restart serve; raise (or `standing upgrade`) a studio so it gets the new cove-master and CA file.
- [ ] As operator: `at-jam ssh <studio> -- echo ok` → `ok`; the session channel shows "<you> connected over SSH".
- [ ] VS Code Remote-SSH to `jam-<studio>`: open the workspace, edit a file, see the agent and you share it live.
- [ ] As a non-member participant: `at-jam login --participant`, `at-jam ssh <studio>` → 403 with the share hint; operator `studio share <studio> --with user:<them> --rights ssh`; retry → works; `studio unshare` → the session drops.
- [ ] `studio teardown` while connected → the ssh session closes within seconds.
