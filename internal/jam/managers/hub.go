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

var (
	_ Reconciler = (*jam.Supervisor)(nil)
	_ Forgetter  = (*jam.Supervisor)(nil)
)

// Forgetter is the optional part of a Reconciler a forced revoke uses: it
// marks the revoked manager's studios Lost, so the next reconcile pass revokes
// their identities and drops their records. (*jam.Supervisor) satisfies it.
type Forgetter interface {
	ForgetManager(name string) []string
}

// Config configures a Hub.
type Config struct {
	Store      jam.Store
	Router     *fleet.Router
	Reconciler Reconciler            // nil: no reconcile
	Instances  func() []jam.Instance // studio counts; nil = Store.ListInstances
	Version    string
	SSHUserCA  []byte // sent in Welcome: the OpenSSH user CA public key studios trust
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
	errRotated  = errors.New("manager token rotated")
	errRevoked  = errors.New("manager revoked")
	errReplaced = errors.New("replaced by a newer stream")

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

	afterAdmit func() // test seam: runs between admit and serve, widening that window
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
// force it then marks them Lost (when the Reconciler is a Forgetter), so the
// next reconcile pass revokes their identities and drops their records.
// Containers still on the manager's host are left to the operator.
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
		// After Router.Remove, so their teardown finds no manager to call.
		if f, ok := h.cfg.Reconciler.(Forgetter); ok {
			f.ForgetManager(name)
		}
		h.log.Warn("manager revoked with studios; they will be forgotten", "manager", name, "studios", n)
	} else {
		h.log.Info("manager revoked", "manager", name)
	}
	return nil
}

// Statuses lists every registered manager in Router order. Studios counts
// non-Gone instances ("" counts as local, which is never registered).
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

	remote, old, err := h.admit(name, hash, hello, c, send)
	if err != nil {
		return err
	}
	if h.afterAdmit != nil {
		h.afterAdmit()
	}
	// Outside every hub lock: closing waits out a log write in progress on
	// the old conn, and a slow log sink must not stall the hub.
	if old != nil {
		old.close(errReplaced)
	}

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

// admit makes c name's current conn, unless a rotate or revoke since auth
// invalidated the token. Holding regMu, no rotate or revoke can land between
// that check, the inventory reconcile, Welcome and the attach, so a refused stream
// never marks studios Lost. It returns the Remote and the conn c replaced
// (nil if none), which the caller closes.
func (h *Hub) admit(name, hash string, hello *managerpb.Hello, c *conn, send func(*managerpb.ManagerDown) error) (*Remote, *conn, error) {
	h.regMu.Lock()
	defer h.regMu.Unlock()
	if cur, ok := h.cfg.Store.ManagerByTokenHash(hash); !ok || cur.Name != name {
		return nil, nil, status.Error(codes.Unauthenticated, "unknown manager token")
	}

	inv := jam.ManagerInventory{StateOwners: hello.GetStateOwners()}
	for _, s := range hello.GetStudios() {
		inv.Studios = append(inv.Studios, jam.ManagedStudio{ActorID: s.GetActorId(), Location: s.GetLocation()})
	}
	var rc jam.ManagerReconcile
	if h.cfg.Reconciler != nil {
		rc = h.cfg.Reconciler.ReconcileManager(name, inv)
	}

	// regMu is held across this single Welcome send.
	// Welcome goes out before the conn is attached: once attached the manager
	// is routable, and a request frame must never precede Welcome (the client
	// expects Welcome first). A send error attaches nothing.
	if err := send(&managerpb.ManagerDown{Msg: &managerpb.ManagerDown_Welcome{Welcome: &managerpb.Welcome{SshUserCa: h.cfg.SSHUserCA}}}); err != nil {
		return nil, nil, err
	}

	now := h.cfg.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	remote, err := h.registerLocked(name)
	if err != nil {
		return nil, nil, status.Error(codes.Internal, "register manager")
	}
	h.live[name] = c
	h.status[name] = &jam.ManagerStatus{
		Name: name, Connected: true, Version: hello.GetVersion(), Capacity: int(hello.GetCapacity()),
		ConnectedAt: now, LastSeen: now, Strays: rc.Strays,
	}
	// Attach under h.mu so live[name] and the Remote's current conn always
	// agree. Lock order: regMu, h.mu, then the Remote's and conns' own locks.
	return remote, remote.attach(c), nil
}

// serve runs the stream until it ends, returning why.
func (h *Hub) serve(stream managerpb.StudioManager_ConnectServer, c *conn, name string, ups <-chan upMsg, send func(*managerpb.ManagerDown) error) error {
	ctx := stream.Context()
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
