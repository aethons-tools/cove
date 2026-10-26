package managers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/fleet"
	"github.com/aethons-tools/cove/internal/jam/managers/client"
	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

const testVersion = "v1"

// hubEnv is a Hub served over bufconn with a MemStore and an empty Router.
type hubEnv struct {
	hub    *Hub
	store  jam.Store
	router *fleet.Router
	cc     *grpc.ClientConn
}

// newHubEnv builds the env; mod (may be nil) adjusts the Config before
// NewHub, and pre (may be nil) runs on the store before Start.
func newHubEnv(t *testing.T, mod func(*Config), pre func(jam.Store)) *hubEnv {
	t.Helper()
	store := jam.NewMemStore()
	if pre != nil {
		pre(store)
	}
	cfg := Config{Store: store, Router: fleet.New(nil), Version: testVersion, Log: slog.New(slog.DiscardHandler)}
	if mod != nil {
		mod(&cfg)
	}
	h := NewHub(cfg)
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	managerpb.RegisterStudioManagerServer(srv, h)
	go func() { _ = srv.Serve(lis) }()
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close(); srv.Stop() })
	return &hubEnv{hub: h, store: store, router: cfg.Router, cc: cc}
}

func (e *hubEnv) add(t *testing.T, name string) string {
	t.Helper()
	tok, err := e.hub.Add(name)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Name != name || tok.Token == "" {
		t.Fatalf("Add = %+v", tok)
	}
	return tok.Token
}

// runClient runs client.Run in the background; stop cancels it and waits.
func runClient(t *testing.T, cc grpc.ClientConnInterface, cfg client.Config) (errc chan error, stop func()) {
	t.Helper()
	if cfg.Version == "" {
		cfg.Version = testVersion
	}
	if cfg.Launcher == nil {
		cfg.Launcher = &testLauncher{loc: "loc-" + cfg.Name}
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	finished := make(chan struct{})
	go func() { out <- client.Run(ctx, cc, cfg); close(finished) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Error("client.Run did not return after cancel")
			}
		})
	}
	t.Cleanup(stop)
	return out, stop
}

// runErr waits for a client's Run to return.
func runErr(t *testing.T, errc chan error) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("client.Run did not return")
		return nil
	}
}

// eventually polls cond for up to 2s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *hubEnv) available(name string) bool {
	e.hub.mu.Lock()
	r := e.hub.remotes[name]
	e.hub.mu.Unlock()
	return r != nil && r.Available()
}

func (e *hubEnv) status(t *testing.T, name string) jam.ManagerStatus {
	t.Helper()
	for _, s := range e.hub.Statuses() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no status for %s", name)
	return jam.ManagerStatus{}
}

// rawStream opens a Connect stream with token and sends nothing.
func rawStream(t *testing.T, cc grpc.ClientConnInterface, token string) managerpb.StudioManager_ConnectClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	s, err := managerpb.NewStudioManagerClient(cc).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// recvUntilErr reads until the stream fails.
func recvUntilErr(t *testing.T, s managerpb.StudioManager_ConnectClient) error {
	t.Helper()
	errc := make(chan error, 1)
	go func() {
		for {
			if _, err := s.Recv(); err != nil {
				errc <- err
				return
			}
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end")
		return nil
	}
}

func wantCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if status.Code(err) != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

// testLauncher is the manager's backend in hub and e2e tests.
type testLauncher struct {
	loc string

	mu     sync.Mutex
	creds  []jam.LaunchCreds
	torn   []string
	egress []*jam.EgressPolicy
}

func (l *testLauncher) Raise(_ context.Context, spec jam.RaiseSpec, c jam.LaunchCreds) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.creds = append(l.creds, c)
	return l.loc, nil
}

func (l *testLauncher) Teardown(_ context.Context, inst jam.Instance) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.torn = append(l.torn, inst.ActorID)
	return nil
}

func (l *testLauncher) Probe(context.Context, jam.Instance) (jam.Liveness, error) {
	return jam.LivenessAlive, nil
}
func (l *testLauncher) Pause(context.Context, jam.Instance) error   { return nil }
func (l *testLauncher) Unpause(context.Context, jam.Instance) error { return nil }
func (l *testLauncher) ApplyEgress(_ context.Context, _ jam.Instance, p *jam.EgressPolicy) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.egress = append(l.egress, p)
	return nil
}
func (l *testLauncher) PrepareKit(context.Context, jam.KitDefinition) (jam.KitStatus, error) {
	return jam.KitStatus{State: jam.KitReady}, nil
}
func (l *testLauncher) BuildKitDraft(_ context.Context, _ jam.KitDefinition, _ string, w io.Writer) (string, error) {
	_, _ = io.WriteString(w, "step 1\n")
	_, _ = io.WriteString(w, "step 2\n")
	return "img-draft", nil
}
func (l *testLauncher) HasKitDraftImage(string) (bool, error) { return true, nil }

func (l *testLauncher) gotCreds() []jam.LaunchCreds {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.creds)
}

func (l *testLauncher) gotEgress() []*jam.EgressPolicy {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.egress)
}

func (l *testLauncher) gotTorn() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.torn)
}

type fakeReconciler struct {
	mu   sync.Mutex
	name string
	inv  jam.ManagerInventory
	out  jam.ManagerReconcile
}

func (f *fakeReconciler) ReconcileManager(name string, inv jam.ManagerInventory) jam.ManagerReconcile {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.name, f.inv = name, inv
	return f.out
}

// forgetter is a Reconciler that also records ForgetManager calls, and
// whether the Router still had the manager at the time.
type forgetter struct {
	fakeReconciler
	router    *fleet.Router
	forgot    []string
	hadRouter bool
}

func (f *forgetter) ForgetManager(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, name)
	f.hadRouter = f.router.HasManager(name)
	return nil
}

func TestHubForcedRevokeForgetsStudios(t *testing.T) {
	fg := &forgetter{}
	e := newHubEnv(t, func(c *Config) { fg.router = c.Router; c.Reconciler = fg }, nil)
	e.add(t, "m1")
	e.add(t, "m2")
	if err := e.store.PutInstance(jam.Instance{ActorID: "w1", Role: "guest", Manager: "m1", Location: "l", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if err := e.hub.Revoke("m1", false); !errors.Is(err, ErrManagerHasStudios) {
		t.Fatalf("err = %v", err)
	}
	if err := e.hub.Revoke("m2", false); err != nil { // no studios: nothing to forget
		t.Fatal(err)
	}
	if len(fg.forgot) != 0 {
		t.Fatalf("forgot = %v before a forced revoke", fg.forgot)
	}
	if err := e.hub.Revoke("m1", true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fg.forgot, []string{"m1"}) || fg.hadRouter {
		t.Fatalf("forgot = %v (router still had it: %v), want [m1] after the router dropped it", fg.forgot, fg.hadRouter)
	}
}

func TestHubRejectsBadToken(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	e.add(t, "m1")
	errc, _ := runClient(t, e.cc, client.Config{Name: "m1", Token: "not-a-token"})
	err := runErr(t, errc)
	wantCode(t, err, codes.Unauthenticated)
	if !strings.Contains(err.Error(), "unknown manager token") {
		t.Fatalf("err = %v", err)
	}
}

func TestHubRejectsNameMismatch(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	tok := e.add(t, "a")
	e.add(t, "b")
	errc, _ := runClient(t, e.cc, client.Config{Name: "b", Token: tok})
	wantCode(t, runErr(t, errc), codes.PermissionDenied)
}

func TestHubRejectsVersionMismatch(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	tok := e.add(t, "m1")
	errc, _ := runClient(t, e.cc, client.Config{Name: "m1", Token: tok, Version: "v2"})
	err := runErr(t, errc)
	wantCode(t, err, codes.FailedPrecondition)
	if got := status.Convert(err).Message(); got != "version mismatch: jam v1, manager v2" {
		t.Fatalf("message = %q", got)
	}
}

func TestHubHelloTimeout(t *testing.T) {
	e := newHubEnv(t, func(c *Config) { c.HelloTimeout = 50 * time.Millisecond }, nil)
	tok := e.add(t, "m1")
	s := rawStream(t, e.cc, tok)
	wantCode(t, recvUntilErr(t, s), codes.DeadlineExceeded)
}

func TestHubHelloStreamClosedIsInvalidArgument(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	tok := e.add(t, "m1")
	s := rawStream(t, e.cc, tok)
	if err := s.CloseSend(); err != nil {
		t.Fatal(err)
	}
	wantCode(t, recvUntilErr(t, s), codes.InvalidArgument)
}

func TestHubFirstMessageNotHello(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	tok := e.add(t, "m1")
	s := rawStream(t, e.cc, tok)
	if err := s.Send(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Heartbeat{Heartbeat: &managerpb.Heartbeat{}}}); err != nil {
		t.Fatal(err)
	}
	err := recvUntilErr(t, s)
	wantCode(t, err, codes.InvalidArgument)
	if !strings.Contains(err.Error(), "expected hello") {
		t.Fatalf("err = %v", err)
	}
}

func TestHubStartRegistersStoredManagersDisconnected(t *testing.T) {
	e := newHubEnv(t, nil, func(s jam.Store) {
		for i, n := range []string{"m1", "m2"} {
			rec := jam.ManagerRecord{Name: n, TokenHash: jam.HashToken("tok-" + n), CreatedAt: time.Unix(int64(100+i), 0)}
			if err := s.AddManager(rec); err != nil {
				t.Fatal(err)
			}
		}
	})
	if got := e.router.Names(); !slices.Equal(got, []string{"m1", "m2"}) {
		t.Fatalf("Names = %v", got)
	}
	lv, err := e.router.Probe(context.Background(), jam.Instance{ActorID: "x", Manager: "m1"})
	if lv != jam.LivenessUnknown || err != nil {
		t.Fatalf("Probe = %v, %v", lv, err)
	}
	if st := e.status(t, "m1"); st.Connected {
		t.Fatalf("status = %+v", st)
	}
}

func TestHubConnectMakesAvailableAndReconciles(t *testing.T) {
	rec := &fakeReconciler{out: jam.ManagerReconcile{Strays: []jam.ManagedStudio{{ActorID: "s1", Location: "ls"}}}}
	e := newHubEnv(t, func(c *Config) { c.Reconciler = rec }, nil)
	tok := e.add(t, "m1")

	type res struct {
		p   jam.Placed
		err error
	}
	raised := make(chan res, 1)
	go func() {
		spec := jam.RaiseSpec{ActorID: "w1", Placement: jam.Placement{Managers: []string{"m1"}, Timeout: 5 * time.Second}}
		p, err := e.router.RaiseOn(context.Background(), spec, jam.LaunchCreds{})
		raised <- res{p, err}
	}()

	inv := []jam.ManagedStudio{{ActorID: "s1", Location: "ls"}}
	runClient(t, e.cc, client.Config{
		Name: "m1", Token: tok, Capacity: 4, Launcher: &testLauncher{loc: "loc-1"},
		Inventory: func(context.Context) ([]jam.ManagedStudio, error) { return inv, nil },
	})
	eventually(t, "m1 available", func() bool { return e.available("m1") })
	connected := time.Now()
	if !e.router.HasManager("m1") {
		t.Fatal("router lacks m1")
	}
	rec.mu.Lock()
	if rec.name != "m1" || !slices.Equal(rec.inv.Studios, inv) {
		t.Errorf("reconciled %q %+v", rec.name, rec.inv)
	}
	rec.mu.Unlock()
	st := e.status(t, "m1")
	if !st.Connected || st.Version != testVersion || st.Capacity != 4 || st.ConnectedAt.IsZero() || st.LastSeen.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	if !slices.Equal(st.Strays, inv) {
		t.Fatalf("strays = %+v", st.Strays)
	}
	// The router re-polls a waiting raise every second; finishing well
	// before that shows the hub's Kick woke it.
	select {
	case r := <-raised:
		if r.err != nil || r.p.Manager != "m1" || r.p.Location != "loc-1" {
			t.Fatalf("RaiseOn = %+v, %v", r.p, r.err)
		}
	case <-time.After(750*time.Millisecond - time.Since(connected)):
		t.Fatal("waiting RaiseOn was not woken by the connect")
	}
}

func TestHubReplacesStream(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	tok := e.add(t, "m1")
	errA, _ := runClient(t, e.cc, client.Config{Name: "m1", Token: tok, Launcher: &testLauncher{loc: "A"}})
	eventually(t, "A connected", func() bool { return e.available("m1") })
	runClient(t, e.cc, client.Config{Name: "m1", Token: tok, Launcher: &testLauncher{loc: "B"}})
	runErr(t, errA) // A is closed once B attaches
	eventually(t, "B connected", func() bool { return e.available("m1") })
	p, err := e.router.RaiseOn(context.Background(), jam.RaiseSpec{ActorID: "w1", Placement: jam.Placement{Managers: []string{"m1"}, Timeout: 5 * time.Second}}, jam.LaunchCreds{})
	if err != nil || p.Location != "B" {
		t.Fatalf("RaiseOn = %+v, %v", p, err)
	}
	sts := e.hub.Statuses()
	if len(sts) != 1 || !sts[0].Connected {
		t.Fatalf("statuses = %+v", sts)
	}
}

func TestHubDisconnectProbesUnknown(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	tok := e.add(t, "m1")
	_, stop := runClient(t, e.cc, client.Config{Name: "m1", Token: tok})
	eventually(t, "connected", func() bool { return e.available("m1") })
	stop()
	eventually(t, "disconnected", func() bool { return !e.status(t, "m1").Connected })
	lv, err := e.router.Probe(context.Background(), jam.Instance{ActorID: "x", Manager: "m1"})
	if lv != jam.LivenessUnknown || err != nil {
		t.Fatalf("Probe = %v, %v", lv, err)
	}
	if st := e.status(t, "m1"); st.LastSeen.IsZero() {
		t.Fatalf("status = %+v", st)
	}
}

func TestHubHeartbeatTimeout(t *testing.T) {
	e := newHubEnv(t, func(c *Config) { c.DeadAfter = 50 * time.Millisecond }, nil)
	tok := e.add(t, "m1")
	s := rawStream(t, e.cc, tok)
	if err := s.Send(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Hello{Hello: &managerpb.Hello{Name: "m1", Version: testVersion}}}); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Recv(); err != nil || m.GetWelcome() == nil {
		t.Fatalf("first message = %v, %v", m, err)
	}
	if err := recvUntilErr(t, s); status.Code(err) == codes.OK {
		t.Fatalf("err = %v, want non-OK", err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for e.available("m1") {
		if time.Now().After(deadline) {
			t.Fatal("manager still available after heartbeat timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHubRevoke(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	tok := e.add(t, "m1")
	errc, _ := runClient(t, e.cc, client.Config{Name: "m1", Token: tok})
	eventually(t, "connected", func() bool { return e.available("m1") })
	if err := e.store.PutInstance(jam.Instance{ActorID: "w1", Role: "guest", Manager: "m1", Location: "l", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}

	err := e.hub.Revoke("m1", false)
	if !errors.Is(err, ErrManagerHasStudios) {
		t.Fatalf("err = %v, want ErrManagerHasStudios", err)
	}
	if want := "manager m1 hosts 1 studios; revoke with force to forget them"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
	if !e.router.HasManager("m1") || !e.available("m1") {
		t.Fatal("refused revoke changed state")
	}

	if err := e.hub.Revoke("m1", true); err != nil {
		t.Fatal(err)
	}
	runErr(t, errc)
	if e.router.HasManager("m1") {
		t.Fatal("router still has m1")
	}
	if lv, _ := e.router.Probe(context.Background(), jam.Instance{ActorID: "w1", Manager: "m1"}); lv != jam.LivenessDead {
		t.Fatalf("Probe after revoke = %v, want Dead", lv)
	}
	errc2, _ := runClient(t, e.cc, client.Config{Name: "m1", Token: tok})
	wantCode(t, runErr(t, errc2), codes.Unauthenticated)
	if len(e.hub.Statuses()) != 0 {
		t.Fatalf("statuses = %+v", e.hub.Statuses())
	}
}

func TestHubRevokeUnknown(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	if err := e.hub.Revoke("nope", true); !errors.Is(err, jam.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestHubRotate(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	oldTok := e.add(t, "m1")
	errc, _ := runClient(t, e.cc, client.Config{Name: "m1", Token: oldTok})
	eventually(t, "connected", func() bool { return e.available("m1") })

	nt, err := e.hub.Rotate("m1")
	if err != nil {
		t.Fatal(err)
	}
	if nt.Name != "m1" || nt.Token == "" || nt.Token == oldTok {
		t.Fatalf("Rotate = %+v", nt)
	}
	runErr(t, errc)

	errOld, _ := runClient(t, e.cc, client.Config{Name: "m1", Token: oldTok})
	wantCode(t, runErr(t, errOld), codes.Unauthenticated)

	runClient(t, e.cc, client.Config{Name: "m1", Token: nt.Token})
	eventually(t, "new token connects", func() bool { return e.available("m1") })
}

func TestHubRotateUnknown(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	if _, err := e.hub.Rotate("nope"); !errors.Is(err, jam.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestHubAddRefusesLocal(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	_, err := e.hub.Add("local")
	if err == nil || !strings.Contains(err.Error(), `manager name "local" is reserved`) {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.hub.Add("m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.hub.Add("m1"); !errors.Is(err, jam.ErrManagerExists) {
		t.Fatalf("duplicate err = %v", err)
	}
	if got := e.router.Names(); !slices.Equal(got, []string{"m1"}) {
		t.Fatalf("Names = %v", got)
	}
}

func TestHubStatusesIncludeLocalAndStudioCounts(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	if err := e.router.Add(fleet.Local(&testLauncher{})); err != nil {
		t.Fatal(err)
	}
	e.add(t, "m1")
	for _, in := range []jam.Instance{
		{ActorID: "a", Manager: "", Phase: jam.PhaseLive},
		{ActorID: "b", Manager: "local", Phase: jam.PhaseLive},
		{ActorID: "c", Manager: "m1", Phase: jam.PhaseLive},
		{ActorID: "d", Manager: "m1", Phase: jam.PhaseGone},
	} {
		in.Role = "guest"
		if err := e.store.PutInstance(in); err != nil {
			t.Fatal(err)
		}
	}
	sts := e.hub.Statuses()
	if len(sts) != 2 || sts[0].Name != "m1" || sts[1].Name != fleet.LocalName {
		t.Fatalf("statuses = %+v", sts)
	}
	if sts[0].Studios != 1 || sts[0].Connected || sts[0].Local {
		t.Fatalf("m1 = %+v", sts[0])
	}
	if sts[1].Studios != 2 || !sts[1].Connected || !sts[1].Local {
		t.Fatalf("local = %+v", sts[1])
	}
}

func TestHubConcurrentConnectsOneServes(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	tok := e.add(t, "m1")
	const n = 16
	errcs := make([]chan error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errcs[i], _ = runClient(t, e.cc, client.Config{Name: "m1", Token: tok, Launcher: &testLauncher{loc: fmt.Sprint("L", i)}})
		}()
	}
	close(start)
	wg.Wait()

	// Every stream but one is replaced and ends; the survivor serves.
	ended := map[int]bool{}
	eventually(t, "all but one stream replaced", func() bool {
		for i, c := range errcs {
			select {
			case <-c:
				ended[i] = true
			default:
			}
		}
		return len(ended) == n-1
	})
	survivor := -1
	for i := range n {
		if !ended[i] {
			survivor = i
		}
	}
	eventually(t, "survivor available", func() bool { return e.available("m1") })
	if st := e.status(t, "m1"); !st.Connected {
		t.Fatalf("status = %+v", st)
	}
	p, err := e.router.RaiseOn(context.Background(), jam.RaiseSpec{ActorID: "w1", Placement: jam.Placement{Managers: []string{"m1"}, Timeout: 5 * time.Second}}, jam.LaunchCreds{})
	if err != nil || p.Location != fmt.Sprint("L", survivor) {
		t.Fatalf("RaiseOn = %+v, %v; survivor L%d", p, err, survivor)
	}
	e.hub.mu.Lock()
	live, cur := e.hub.live["m1"], e.hub.remotes["m1"].conn()
	e.hub.mu.Unlock()
	if live == nil || live != cur {
		t.Fatal("hub's live conn is not the Remote's current conn")
	}

	if _, err := e.hub.Rotate("m1"); err != nil {
		t.Fatal(err)
	}
	runErr(t, errcs[survivor])
	eventually(t, "unavailable after rotate", func() bool { return !e.available("m1") })
}

// TestHubLateResultOnReplacedStream (Review Focus 3): a result the replaced
// stream sends for an id never resolves the new stream's call of that id.
func TestHubLateResultOnReplacedStream(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	tok := e.add(t, "m1")
	hello := func(s managerpb.StudioManager_ConnectClient) {
		t.Helper()
		if err := s.Send(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Hello{Hello: &managerpb.Hello{Name: "m1", Version: testVersion}}}); err != nil {
			t.Fatal(err)
		}
		if m, err := s.Recv(); err != nil || m.GetWelcome() == nil {
			t.Fatalf("first message = %v, %v", m, err)
		}
	}
	a := rawStream(t, e.cc, tok)
	hello(a)
	b := rawStream(t, e.cc, tok)
	hello(b)

	type res struct {
		lv  jam.Liveness
		err error
	}
	got := make(chan res, 1)
	go func() {
		lv, err := e.router.Probe(context.Background(), jam.Instance{ActorID: "x", Manager: "m1"})
		got <- res{lv, err}
	}()
	var f *managerpb.Frame
	for f == nil {
		m, err := b.Recv()
		if err != nil {
			t.Fatal(err)
		}
		f = m.GetFrame()
	}
	if f.Op != wire.OpProbe {
		t.Fatalf("op = %q", f.Op)
	}

	reply := func(s managerpb.StudioManager_ConnectClient, lv jam.Liveness) {
		data, err := wire.EncodeResult(wire.ProbeResult{Liveness: lv}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, fr := range wire.Split(f.Id, wire.OpResult, data) {
			_ = s.Send(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Frame{Frame: fr}}) // A's may fail: it is closed
		}
	}
	reply(a, jam.LivenessDead) // the late result on the replaced stream
	select {
	case r := <-got:
		t.Fatalf("B's call resolved by A's result: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	reply(b, jam.LivenessAlive)
	select {
	case r := <-got:
		if r.err != nil || r.lv != jam.LivenessAlive {
			t.Fatalf("Probe = %v, %v; want B's Alive", r.lv, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B's own result did not resolve the call")
	}
}

// authSignalStore signals each ManagerByTokenHash lookup, so a test knows the
// hub has authenticated a stream.
type authSignalStore struct {
	jam.Store
	looked chan struct{}
}

func (s authSignalStore) ManagerByTokenHash(h string) (jam.ManagerRecord, bool) {
	select {
	case s.looked <- struct{}{}:
	default:
	}
	return s.Store.ManagerByTokenHash(h)
}

// TestHubRefusedAtRecheckDoesNotReconcile: a stream authenticated with a token
// rotated before its Hello is refused, and its inventory never reconciled (it
// would mark Jam's studios Lost on a stream Jam does not accept).
func TestHubRefusedAtRecheckDoesNotReconcile(t *testing.T) {
	rec := &fakeReconciler{}
	looked := make(chan struct{}, 1)
	e := newHubEnv(t, func(c *Config) {
		c.Reconciler = rec
		c.Store = authSignalStore{Store: c.Store, looked: looked}
	}, nil)
	tok := e.add(t, "m1")
	select { // drop any lookup made before the stream
	case <-looked:
	default:
	}
	s := rawStream(t, e.cc, tok)
	select {
	case <-looked: // authenticated, now awaiting Hello
	case <-time.After(5 * time.Second):
		t.Fatal("stream never authenticated")
	}
	if _, err := e.hub.Rotate("m1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Hello{Hello: &managerpb.Hello{Name: "m1", Version: testVersion}}}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, recvUntilErr(t, s), codes.Unauthenticated)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.name != "" {
		t.Fatalf("reconciled %q for a refused stream", rec.name)
	}
}

// blockingWriter signals its first Write, then blocks it until release.
type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

// TestHubReplaceDoesNotStallOnSlowLogSink: replacing a stream whose call is
// mid log write must not hold the hub's lock while the old conn closes.
func TestHubReplaceDoesNotStallOnSlowLogSink(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	tok := e.add(t, "m1")
	hello := func(s managerpb.StudioManager_ConnectClient) {
		t.Helper()
		if err := s.Send(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Hello{Hello: &managerpb.Hello{Name: "m1", Version: testVersion}}}); err != nil {
			t.Fatal(err)
		}
	}
	a := rawStream(t, e.cc, tok)
	hello(a)
	if m, err := a.Recv(); err != nil || m.GetWelcome() == nil {
		t.Fatalf("first message = %v, %v", m, err)
	}
	e.hub.mu.Lock()
	remote := e.hub.remotes["m1"]
	e.hub.mu.Unlock()
	connA := remote.conn()

	w := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(w.release) })
	go func() {
		_, _ = e.router.BuildKitDraft(context.Background(), jam.KitDefinition{Ref: jam.KitRef{ID: "k"}}, "t", w)
	}()
	var f *managerpb.Frame
	for f == nil {
		m, err := a.Recv()
		if err != nil {
			t.Fatal(err)
		}
		f = m.GetFrame()
	}
	if err := a.Send(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Frame{Frame: &managerpb.Frame{Id: f.Id, Op: wire.OpLog, Data: []byte("x")}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("log write never started")
	}

	hello(rawStream(t, e.cc, tok))
	eventually(t, "replacement attached", func() bool { return remote.conn() != connA })
	done := make(chan struct{})
	go func() { e.hub.Statuses(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Statuses blocked while the replaced conn waits on its log sink")
	}
}

func helloStream(t *testing.T, e *hubEnv, tok string) managerpb.StudioManager_ConnectClient {
	t.Helper()
	s := rawStream(t, e.cc, tok)
	if err := s.Send(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Hello{Hello: &managerpb.Hello{Name: "m1", Version: testVersion}}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHubWelcomeBeforeRoutable(t *testing.T) {
	for i := 0; i < 50; i++ {
		e := newHubEnv(t, nil, nil)
		tok := e.add(t, "m1")
		s := helloStream(t, e, tok)
		remote := func() *Remote {
			e.hub.mu.Lock()
			defer e.hub.mu.Unlock()
			return e.hub.remotes["m1"]
		}()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			for !e.available("m1") {
				if ctx.Err() != nil {
					return
				}
				time.Sleep(50 * time.Microsecond)
			}
			_, _ = remote.Probe(ctx, jam.Instance{ActorID: "x", Manager: "m1"})
		}()
		m, err := s.Recv()
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if m.GetWelcome() == nil {
			t.Fatalf("iteration %d: first message = %v, want Welcome", i, m)
		}
	}
}

func TestHubWelcomeCarriesSSHUserCA(t *testing.T) {
	ca := []byte("ssh-ed25519 AAAA ca")
	e := newHubEnv(t, func(c *Config) { c.SSHUserCA = ca }, nil)
	s := helloStream(t, e, e.add(t, "m1"))
	m, err := s.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if string(m.GetWelcome().GetSshUserCa()) != string(ca) {
		t.Fatalf("welcome = %v", m)
	}
}
