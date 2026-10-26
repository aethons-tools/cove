package managers

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/fleet"
	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

// Reconciler compares a connecting manager's inventory with Jam's records.
// (*jam.Supervisor) satisfies it.
type Reconciler interface {
	ReconcileManager(name string, inv jam.ManagerInventory) jam.ManagerReconcile
}

// Config configures a Hub.
type Config struct {
	Store      jam.Store
	Router     *fleet.Router
	Reconciler Reconciler            // nil: no reconcile
	Instances  func() []jam.Instance // studio counts; nil = Store.ListInstances
	Version    string
	Log        *slog.Logger
	Now        func() time.Time

	HelloTimeout, HeartbeatEvery, DeadAfter time.Duration // 0: wire defaults
}

// ErrManagerHasStudios refuses a revoke while the manager still hosts studios.
var ErrManagerHasStudios = errors.New("manager hosts studios")

// hasStudiosError carries the user-facing message and wraps
// ErrManagerHasStudios.
type hasStudiosError struct {
	name string
	n    int
}

func (e hasStudiosError) Error() string {
	return fmt.Sprintf("manager %s hosts %d studios; revoke with force to forget them", e.name, e.n)
}
func (hasStudiosError) Unwrap() error { return ErrManagerHasStudios }

// Causes recorded on a conn the hub closes itself.
var (
	errRotated = errors.New("manager token rotated")
	errRevoked = errors.New("manager revoked")

	errStreamEnded = errors.New("manager stream ended")
)

// Hub serves StudioManager.Connect: it authenticates manager streams and
// attaches them to the Remotes registered in the Router, and keeps the
// registry (add, rotate, revoke) and each manager's status.
type Hub struct {
	managerpb.UnimplementedStudioManagerServer

	cfg Config
	log *slog.Logger

	regMu sync.Mutex // serializes registry mutations: Add, Rotate, Revoke

	mu      sync.Mutex
	remotes map[string]*Remote
	live    map[string]*conn // the current conn per name
	status  map[string]*jam.ManagerStatus
}

func NewHub(cfg Config) *Hub {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Instances == nil {
		cfg.Instances = cfg.Store.ListInstances
	}
	if cfg.HelloTimeout <= 0 {
		cfg.HelloTimeout = wire.HelloTimeout
	}
	if cfg.HeartbeatEvery <= 0 {
		cfg.HeartbeatEvery = wire.HeartbeatEvery
	}
	if cfg.DeadAfter <= 0 {
		cfg.DeadAfter = wire.DeadAfter
	}
	return &Hub{
		cfg: cfg, log: cfg.Log,
		remotes: map[string]*Remote{}, live: map[string]*conn{}, status: map[string]*jam.ManagerStatus{},
	}
}

// Start registers every stored manager with the Router, disconnected, in
// ListManagers order. Call it before the supervisor runs, so instances on a
// manager that has not reconnected yet probe Unknown rather than Dead.
func (h *Hub) Start() error {
	for _, rec := range h.cfg.Store.ListManagers() {
		if _, err := h.register(rec.Name); err != nil {
			return err
		}
	}
	return nil
}

// register adds name's Remote to the Router if the hub has none yet.
func (h *Hub) register(name string) (*Remote, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.registerLocked(name)
}

func (h *Hub) registerLocked(name string) (*Remote, error) {
	if r := h.remotes[name]; r != nil {
		return r, nil
	}
	r := NewRemote(name, h.log)
	if err := h.cfg.Router.Add(r); err != nil {
		return nil, err
	}
	h.remotes[name] = r
	h.status[name] = &jam.ManagerStatus{Name: name}
	return r, nil
}

// Add registers a new manager and returns its token, shown only here.
func (h *Hub) Add(name string) (jam.ManagerToken, error) {
	h.regMu.Lock()
	defer h.regMu.Unlock()
	if err := jam.ValidManagerName(name); err != nil {
		return jam.ManagerToken{}, err
	}
	tok, err := jam.MintToken()
	if err != nil {
		return jam.ManagerToken{}, err
	}
	rec := jam.ManagerRecord{Name: name, TokenHash: jam.HashToken(tok), CreatedAt: h.cfg.Now()}
	if err := h.cfg.Store.AddManager(rec); err != nil {
		return jam.ManagerToken{}, err
	}
	if _, err := h.register(name); err != nil {
		_ = h.cfg.Store.RemoveManager(name)
		return jam.ManagerToken{}, err
	}
	h.log.Info("manager added", "manager", name)
	return jam.ManagerToken{Name: name, Token: tok}, nil
}

// Rotate replaces name's token and drops its live stream; the manager must
// reconnect with the new token, shown only here.
func (h *Hub) Rotate(name string) (jam.ManagerToken, error) {
	h.regMu.Lock()
	defer h.regMu.Unlock()
	tok, err := jam.MintToken()
	if err != nil {
		return jam.ManagerToken{}, err
	}
	if err := h.cfg.Store.SetManagerToken(name, jam.HashToken(tok)); err != nil {
		return jam.ManagerToken{}, err
	}
	h.mu.Lock()
	c := h.live[name]
	h.mu.Unlock()
	if c != nil {
		c.close(errRotated)
	}
	h.log.Info("manager token rotated", "manager", name)
	return jam.ManagerToken{Name: name, Token: tok}, nil
}

// Revoke forgets name: its registration, its live stream and its Router
// entry. While non-Gone instances run there it refuses unless force; with
// force the Router then probes those instances Dead, so the reconciler forgets
// them.
func (h *Hub) Revoke(name string, force bool) error {
	h.regMu.Lock()
	defer h.regMu.Unlock()
	n := 0
	for _, inst := range h.cfg.Instances() {
		if inst.Manager == name && inst.Phase != jam.PhaseGone {
			n++
		}
	}
	if n > 0 && !force {
		return hasStudiosError{name: name, n: n}
	}
	if err := h.cfg.Store.RemoveManager(name); err != nil {
		return err
	}
	h.mu.Lock()
	c := h.live[name]
	delete(h.live, name)
	delete(h.remotes, name)
	delete(h.status, name)
	h.mu.Unlock()
	if c != nil {
		c.close(errRevoked)
	}
	h.cfg.Router.Remove(name)
	if n > 0 {
		h.log.Warn("manager revoked with studios; they will be forgotten", "manager", name, "studios", n)
	} else {
		h.log.Info("manager revoked", "manager", name)
	}
	return nil
}

// Statuses lists every manager in Router order; the local manager is always
// connected. Studios counts non-Gone instances ("" counts as local).
func (h *Hub) Statuses() []jam.ManagerStatus {
	counts := map[string]int{}
	for _, inst := range h.cfg.Instances() {
		if inst.Phase == jam.PhaseGone {
			continue
		}
		m := inst.Manager
		if m == "" {
			m = fleet.LocalName
		}
		counts[m]++
	}
	names := h.cfg.Router.Names()
	out := make([]jam.ManagerStatus, 0, len(names))
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, name := range names {
		var st jam.ManagerStatus
		switch s := h.status[name]; {
		case name == fleet.LocalName:
			st = jam.ManagerStatus{Name: name, Local: true, Connected: true}
		case s != nil:
			st = *s
			st.Strays = append([]jam.ManagedStudio(nil), s.Strays...)
		default:
			st = jam.ManagerStatus{Name: name}
		}
		st.Studios = counts[name]
		out = append(out, st)
	}
	return out
}

func bearer(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	v := vals[0]
	const p = "bearer "
	if len(v) >= len(p) && strings.EqualFold(v[:len(p)], p) {
		return strings.TrimSpace(v[len(p):])
	}
	return strings.TrimSpace(v)
}

type upMsg struct {
	m   *managerpb.ManagerUp
	err error
}

// Connect serves one manager stream: auth, Hello, reconcile, attach, then
// frames and heartbeats until the stream ends.
func (h *Hub) Connect(stream managerpb.StudioManager_ConnectServer) error {
	ctx := stream.Context()
	md, _ := metadata.FromIncomingContext(ctx)
	tok := bearer(md.Get("authorization"))
	hash := jam.HashToken(tok)
	rec, ok := h.cfg.Store.ManagerByTokenHash(hash)
	if tok == "" || !ok {
		return status.Error(codes.Unauthenticated, "unknown manager token")
	}
	name := rec.Name

	// The only Recv caller: the handler cannot unblock a Recv, so it reads
	// from this goroutine, which ends when the stream does.
	ups := make(chan upMsg)
	go func() {
		for {
			m, err := stream.Recv()
			select {
			case ups <- upMsg{m, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	helloTimer := time.NewTimer(h.cfg.HelloTimeout)
	var hello *managerpb.Hello
	select {
	case u := <-ups:
		helloTimer.Stop()
		if errors.Is(u.err, io.EOF) {
			return status.Error(codes.InvalidArgument, "expected hello")
		}
		if u.err != nil {
			return streamStatus(u.err)
		}
		if hello = u.m.GetHello(); hello == nil {
			return status.Error(codes.InvalidArgument, "expected hello")
		}
	case <-helloTimer.C:
		return status.Error(codes.DeadlineExceeded, "no hello within the timeout")
	case <-ctx.Done():
		helloTimer.Stop()
		return status.FromContextError(ctx.Err()).Err()
	}
	if hello.GetName() != name {
		return status.Errorf(codes.PermissionDenied, "token is for manager %s, hello names %s", name, hello.GetName())
	}
	if hello.GetVersion() != h.cfg.Version {
		return status.Errorf(codes.FailedPrecondition, "version mismatch: jam %s, manager %s", h.cfg.Version, hello.GetVersion())
	}

	inv := jam.ManagerInventory{StateOwners: hello.GetStateOwners()}
	for _, s := range hello.GetStudios() {
		inv.Studios = append(inv.Studios, jam.ManagedStudio{ActorID: s.GetActorId(), Location: s.GetLocation()})
	}
	var rc jam.ManagerReconcile
	if h.cfg.Reconciler != nil {
		rc = h.cfg.Reconciler.ReconcileManager(name, inv)
	}

	// One mutex serializes every Send on this stream: frames, Welcome and
	// heartbeats. Once the handler is returning, sends fail rather than
	// touch a finished stream.
	var sendMu sync.Mutex
	var ended atomic.Bool
	defer ended.Store(true)
	send := func(m *managerpb.ManagerDown) error {
		if ended.Load() {
			return errStreamEnded
		}
		sendMu.Lock()
		defer sendMu.Unlock()
		if ended.Load() {
			return errStreamEnded
		}
		return stream.Send(m)
	}
	c := newConn(send)
	now := h.cfg.Now()

	// Become current, unless a rotate or revoke since auth invalidated the
	// token; both change the store before taking h.mu.
	h.mu.Lock()
	if cur, ok := h.cfg.Store.ManagerByTokenHash(hash); !ok || cur.Name != name {
		h.mu.Unlock()
		return status.Error(codes.Unauthenticated, "unknown manager token")
	}
	remote, err := h.registerLocked(name)
	if err != nil {
		h.mu.Unlock()
		return status.Error(codes.Internal, "register manager")
	}
	h.live[name] = c
	h.status[name] = &jam.ManagerStatus{
		Name: name, Connected: true, Version: hello.GetVersion(), Capacity: int(hello.GetCapacity()),
		ConnectedAt: now, LastSeen: now, Strays: rc.Strays,
	}
	// Attach under h.mu so live[name] and the Remote's current conn always
	// agree, and a rotate or revoke can only close a conn already attached.
	// Lock order: h.mu, then the Remote's and conns' own locks.
	remote.attach(c)
	h.mu.Unlock()

	cause := h.serve(stream, c, name, ups, send)

	c.close(cause)
	remote.detach(c)
	h.mu.Lock()
	if h.live[name] == c {
		delete(h.live, name)
		if st := h.status[name]; st != nil {
			st.Connected = false
			st.LastSeen = h.cfg.Now()
		}
	}
	h.mu.Unlock()
	h.log.Info("manager disconnected", "manager", name, "err", errText(cause))
	return streamStatus(cause)
}

// serve sends Welcome and runs the stream until it ends, returning why.
func (h *Hub) serve(stream managerpb.StudioManager_ConnectServer, c *conn, name string, ups <-chan upMsg, send func(*managerpb.ManagerDown) error) error {
	ctx := stream.Context()
	if err := send(&managerpb.ManagerDown{Msg: &managerpb.ManagerDown_Welcome{Welcome: &managerpb.Welcome{}}}); err != nil {
		return err
	}
	h.cfg.Router.Kick()
	h.log.Info("manager connected", "manager", name)

	stopHB := make(chan struct{})
	defer close(stopHB)
	go func() {
		t := time.NewTicker(h.cfg.HeartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-stopHB:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if err := send(&managerpb.ManagerDown{Msg: &managerpb.ManagerDown_Heartbeat{Heartbeat: &managerpb.Heartbeat{}}}); err != nil {
					return
				}
			}
		}
	}()

	dead := time.NewTimer(h.cfg.DeadAfter)
	defer dead.Stop()
	for {
		select {
		case u := <-ups:
			if u.err != nil {
				return u.err
			}
			dead.Reset(h.cfg.DeadAfter)
			h.touch(name, c)
			if f := u.m.GetFrame(); f != nil {
				if err := c.deliver(f); err != nil {
					return status.Errorf(codes.InvalidArgument, "protocol violation: %v", err)
				}
			}
		case <-dead.C:
			return status.Error(codes.DeadlineExceeded, "heartbeat timeout")
		case <-c.done:
			c.mu.Lock()
			cause := c.cause
			c.mu.Unlock()
			return cause
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// touch records traffic from c, if it is still name's current conn.
func (h *Hub) touch(name string, c *conn) {
	now := h.cfg.Now()
	h.mu.Lock()
	if h.live[name] == c {
		if st := h.status[name]; st != nil {
			st.LastSeen = now
		}
	}
	h.mu.Unlock()
}

// streamStatus is the status Connect returns for cause.
func streamStatus(cause error) error {
	switch {
	case cause == nil, errors.Is(cause, io.EOF):
		return nil
	case errors.Is(cause, errRotated), errors.Is(cause, errRevoked):
		return status.Error(codes.Unauthenticated, cause.Error())
	}
	if _, ok := status.FromError(cause); ok {
		return cause
	}
	if st := status.FromContextError(cause); st.Code() != codes.Unknown {
		return st.Err()
	}
	return status.Error(codes.Aborted, cause.Error())
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
