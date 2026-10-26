package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

// fakeJam plays Jam: it records the Hello and auth, then hands the stream to fn.
type fakeJam struct {
	managerpb.UnimplementedStudioManagerServer
	refuse  error
	welcome *managerpb.Welcome // nil: empty
	fn      func(t *testing.T, s *session)

	t     *testing.T
	hello chan *managerpb.Hello
	auth  chan []string
}

type session struct {
	srv  managerpb.StudioManager_ConnectServer
	mu   sync.Mutex
	up   chan *managerpb.ManagerUp
	done chan struct{}
}

func (s *session) send(f *managerpb.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.srv.Send(&managerpb.ManagerDown{Msg: &managerpb.ManagerDown_Frame{Frame: f}})
}

func (s *session) request(id uint64, op string, body any) {
	b, _ := json.Marshal(body)
	for _, f := range wire.Split(id, op, b) {
		s.send(f)
	}
}

// next returns the next non-heartbeat frame from the manager.
func (s *session) next(t *testing.T) *managerpb.Frame {
	t.Helper()
	for {
		select {
		case m, ok := <-s.up:
			if !ok {
				t.Fatal("stream ended")
			}
			if f := m.GetFrame(); f != nil {
				return f
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for frame")
		}
	}
}

func (f *fakeJam) Connect(srv managerpb.StudioManager_ConnectServer) error {
	md, _ := metadata.FromIncomingContext(srv.Context())
	m, err := srv.Recv()
	if err != nil {
		return err
	}
	f.auth <- md.Get("authorization")
	f.hello <- m.GetHello()
	if f.refuse != nil {
		return f.refuse
	}
	s := &session{srv: srv, up: make(chan *managerpb.ManagerUp, 64), done: make(chan struct{})}
	w := f.welcome
	if w == nil {
		w = &managerpb.Welcome{}
	}
	if err := srv.Send(&managerpb.ManagerDown{Msg: &managerpb.ManagerDown_Welcome{Welcome: w}}); err != nil {
		return err
	}
	go func() {
		defer close(s.up)
		for {
			m, err := srv.Recv()
			if err != nil {
				return
			}
			select {
			case s.up <- m:
			case <-srv.Context().Done():
				return
			}
		}
	}()
	f.fn(f.t, s)
	return nil
}

func start(t *testing.T, fj *fakeJam) grpc.ClientConnInterface {
	t.Helper()
	fj.t = t
	fj.hello = make(chan *managerpb.Hello, 1)
	fj.auth = make(chan []string, 1)
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	managerpb.RegisterStudioManagerServer(srv, fj)
	go func() { _ = srv.Serve(lis) }()
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close(); srv.Stop() })
	return cc
}

// runAsync starts Run and returns a channel of its result.
func runAsync(t *testing.T, cc grpc.ClientConnInterface, cfg Config) chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	finished := make(chan struct{})
	go func() { out <- Run(ctx, cc, cfg); close(finished) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return out
}

func decode(t *testing.T, f *managerpb.Frame, body any) error {
	t.Helper()
	if f.Op != wire.OpResult {
		t.Fatalf("op = %q, want result", f.Op)
	}
	return wire.DecodeResult(f.Data, body)
}

type fakeLauncher struct {
	raiseFn func(ctx context.Context, spec jam.RaiseSpec, c jam.LaunchCreds) (string, error)
}

func (l *fakeLauncher) Raise(ctx context.Context, spec jam.RaiseSpec, c jam.LaunchCreds) (string, error) {
	return l.raiseFn(ctx, spec, c)
}
func (l *fakeLauncher) Teardown(context.Context, jam.Instance) error              { return nil }
func (l *fakeLauncher) Probe(context.Context, jam.Instance) (jam.Liveness, error) { return 0, nil }
func (l *fakeLauncher) Pause(context.Context, jam.Instance) error                 { return nil }
func (l *fakeLauncher) Unpause(context.Context, jam.Instance) error               { return nil }
func (l *fakeLauncher) ApplyEgress(context.Context, jam.Instance, *jam.EgressPolicy) error {
	return nil
}
func (l *fakeLauncher) PrepareKit(context.Context, jam.KitDefinition) (jam.KitStatus, error) {
	return jam.KitStatus{}, nil
}

type keeperLauncher struct{ fakeLauncher }

func (keeperLauncher) PurgeState(context.Context, string) error { return nil }
func (keeperLauncher) StateOwners(context.Context) ([]string, error) {
	return []string{"a1", "a2"}, nil
}

type draftLauncher struct {
	fakeLauncher
	build func(ctx context.Context, w io.Writer) (string, error)
}

func (d draftLauncher) BuildKitDraft(ctx context.Context, _ jam.KitDefinition, _ string, w io.Writer) (string, error) {
	return d.build(ctx, w)
}
func (draftLauncher) HasKitDraftImage(string) (bool, error) { return true, nil }

func TestRunSendsHelloWithInventoryAndOwners(t *testing.T) {
	fj := &fakeJam{fn: func(t *testing.T, s *session) { <-s.srv.Context().Done() }}
	cc := start(t, fj)
	runAsync(t, cc, Config{
		Name: "m1", Token: "tok", Version: "v9", Capacity: 3,
		Launcher: &keeperLauncher{},
		Inventory: func(context.Context) ([]jam.ManagedStudio, error) {
			return []jam.ManagedStudio{{ActorID: "a1", Location: "l1"}}, nil
		},
	})
	h := <-fj.hello
	if h.GetName() != "m1" || h.GetVersion() != "v9" || h.GetCapacity() != 3 {
		t.Fatalf("hello = %v", h)
	}
	if len(h.Studios) != 1 || h.Studios[0].ActorId != "a1" || h.Studios[0].Location != "l1" {
		t.Fatalf("studios = %v", h.Studios)
	}
	if fmt.Sprint(h.StateOwners) != "[a1 a2]" {
		t.Fatalf("owners = %v", h.StateOwners)
	}
	if a := <-fj.auth; len(a) != 1 || a[0] != "Bearer tok" {
		t.Fatalf("auth = %v", a)
	}
}

func TestRunCallsOnWelcomeBeforeServing(t *testing.T) {
	var mu sync.Mutex
	var ca []byte
	seen := make(chan []byte, 1)
	l := &fakeLauncher{raiseFn: func(context.Context, jam.RaiseSpec, jam.LaunchCreds) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		seen <- ca
		return "loc", nil
	}}
	fj := &fakeJam{
		welcome: &managerpb.Welcome{SshUserCa: []byte("ssh-ed25519 AAAA ca")},
		fn: func(t *testing.T, s *session) {
			s.request(1, wire.OpRaise, wire.RaiseRequest{Spec: jam.RaiseSpec{ActorID: "a"}})
			s.next(t)
			<-s.srv.Context().Done()
		},
	}
	runAsync(t, start(t, fj), Config{Name: "m", Launcher: l, OnWelcome: func(w *managerpb.Welcome) {
		mu.Lock()
		ca = w.GetSshUserCa()
		mu.Unlock()
	}})
	if got := string(<-seen); got != "ssh-ed25519 AAAA ca" {
		t.Fatalf("CA seen by first request = %q", got)
	}
}

func TestRunServesRaise(t *testing.T) {
	l := &fakeLauncher{raiseFn: func(_ context.Context, spec jam.RaiseSpec, c jam.LaunchCreds) (string, error) {
		if spec.ActorID != "actor-7" || c.IdentityToken != "idt" || c.LaunchSecret != "sec" {
			return "", fmt.Errorf("bad args")
		}
		return "loc-1", nil
	}}
	got := make(chan *managerpb.Frame, 1)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		s.request(1, wire.OpRaise, wire.RaiseRequest{Spec: jam.RaiseSpec{ActorID: "actor-7"}, Creds: jam.LaunchCreds{IdentityToken: "idt", LaunchSecret: "sec"}})
		got <- s.next(t)
		<-s.srv.Context().Done()
	}}
	runAsync(t, start(t, fj), Config{Name: "m", Launcher: l})
	f := <-got
	var r wire.RaiseResult
	if err := decode(t, f, &r); err != nil || r.Location != "loc-1" || f.Id != 1 {
		t.Fatalf("r=%v err=%v id=%d", r, err, f.Id)
	}
}

func TestRunMapsKitNotReady(t *testing.T) {
	l := &fakeLauncher{raiseFn: func(context.Context, jam.RaiseSpec, jam.LaunchCreds) (string, error) {
		return "", fmt.Errorf("wrapped: %w", jam.ErrKitNotReady)
	}}
	got := make(chan *managerpb.Frame, 1)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		s.request(1, wire.OpRaise, wire.RaiseRequest{})
		got <- s.next(t)
		<-s.srv.Context().Done()
	}}
	runAsync(t, start(t, fj), Config{Name: "m", Launcher: l})
	if err := decode(t, <-got, nil); !errors.Is(err, jam.ErrKitNotReady) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunStreamsDraftLog(t *testing.T) {
	l := &draftLauncher{build: func(_ context.Context, w io.Writer) (string, error) {
		_, _ = w.Write([]byte("a"))
		_, _ = w.Write([]byte("b"))
		return "img", nil
	}}
	got := make(chan []*managerpb.Frame, 1)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		s.request(5, wire.OpBuildKitDraft, wire.DraftBuildRequest{Tag: "t"})
		got <- []*managerpb.Frame{s.next(t), s.next(t), s.next(t)}
		<-s.srv.Context().Done()
	}}
	runAsync(t, start(t, fj), Config{Name: "m", Launcher: l})
	fs := <-got
	if fs[0].Op != wire.OpLog || string(fs[0].Data) != "a" || fs[0].Id != 5 ||
		fs[1].Op != wire.OpLog || string(fs[1].Data) != "b" {
		t.Fatalf("logs = %v %v", fs[0], fs[1])
	}
	var r wire.DraftBuildResult
	if err := decode(t, fs[2], &r); err != nil || r.ImageID != "img" {
		t.Fatalf("r=%v err=%v", r, err)
	}
}

func TestRunCancel(t *testing.T) {
	unblocked := make(chan struct{})
	started := make(chan struct{})
	l := &fakeLauncher{raiseFn: func(ctx context.Context, _ jam.RaiseSpec, _ jam.LaunchCreds) (string, error) {
		close(started)
		<-ctx.Done()
		close(unblocked)
		return "", ctx.Err()
	}}
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		s.request(1, wire.OpRaise, wire.RaiseRequest{})
		<-started
		s.send(&managerpb.Frame{Id: 1, Op: wire.OpCancel})
		<-s.srv.Context().Done()
	}}
	runAsync(t, start(t, fj), Config{Name: "m", Launcher: l})
	select {
	case <-unblocked:
	case <-time.After(time.Second):
		t.Fatal("raise not cancelled")
	}
}

func TestRunUnsupported(t *testing.T) {
	got := make(chan []*managerpb.Frame, 1)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		s.request(1, wire.OpBuildKitDraft, wire.DraftBuildRequest{})
		s.request(2, wire.OpImageFacts, wire.ImageFactsRequest{})
		s.request(3, "bogus", wire.Empty{})
		got <- []*managerpb.Frame{s.next(t), s.next(t), s.next(t)}
		<-s.srv.Context().Done()
	}}
	runAsync(t, start(t, fj), Config{Name: "m", Launcher: &fakeLauncher{}})
	want := map[uint64]string{1: "manager does not support build-kit-draft", 2: "manager does not support image-facts", 3: "unknown op bogus"}
	for _, f := range <-got {
		err := decode(t, f, nil)
		if err == nil || err.Error() != want[f.Id] {
			t.Fatalf("id %d: err = %v, want %q", f.Id, err, want[f.Id])
		}
	}
}

func TestRunRefused(t *testing.T) {
	refuse := status.Error(codes.FailedPrecondition, "version mismatch: jam a, manager b")
	fj := &fakeJam{refuse: refuse}
	out := runAsync(t, start(t, fj), Config{Name: "m", Launcher: &fakeLauncher{}})
	select {
	case err := <-out:
		if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "version mismatch: jam a, manager b") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestRunHeartbeatTimeout(t *testing.T) {
	fj := &fakeJam{fn: func(t *testing.T, s *session) { <-s.srv.Context().Done() }}
	out := runAsync(t, start(t, fj), Config{Name: "m", Launcher: &fakeLauncher{}, DeadAfter: 50 * time.Millisecond})
	select {
	case err := <-out:
		if err == nil || !strings.Contains(err.Error(), "heartbeat timeout") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no timeout")
	}
}

// A request over the size cap ends Run with the error; a tail frame of the
// dropped id that was already buffered is never dispatched truncated.
func TestRunOversizeRequestEndsRun(t *testing.T) {
	var mu sync.Mutex
	raised := 0
	l := &fakeLauncher{raiseFn: func(context.Context, jam.RaiseSpec, jam.LaunchCreds) (string, error) {
		mu.Lock()
		raised++
		mu.Unlock()
		return "loc", nil
	}}
	body, _ := json.Marshal(wire.RaiseRequest{Spec: jam.RaiseSpec{ActorID: "a"}})
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		s.send(&managerpb.Frame{Id: 1, Op: wire.OpRaise, Data: make([]byte, len(body)+1), More: true})
		s.send(&managerpb.Frame{Id: 1, Op: wire.OpRaise, Data: body})
		<-s.srv.Context().Done()
	}}
	out := runAsync(t, start(t, fj), Config{Name: "m", Launcher: l, maxMessage: len(body)})
	select {
	case err := <-out:
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not end")
	}
	mu.Lock()
	defer mu.Unlock()
	if raised != 0 {
		t.Fatalf("raise dispatched %d times from a dropped request", raised)
	}
}

type capturingLauncher struct{ fakeLauncher }

func (capturingLauncher) TeardownCapture(context.Context, jam.Instance) (string, error) {
	return "why it died", nil
}

func serveTeardown(t *testing.T, l jam.Launcher) wire.TeardownResult {
	t.Helper()
	got := make(chan *managerpb.Frame, 1)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		s.request(1, wire.OpTeardown, wire.InstanceRequest{Instance: jam.Instance{Location: "loc"}})
		got <- s.next(t)
		<-s.srv.Context().Done()
	}}
	runAsync(t, start(t, fj), Config{Name: "m", Launcher: l})
	var r wire.TeardownResult
	if err := decode(t, <-got, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestServeTeardownReturnsAgentLog(t *testing.T) {
	if r := serveTeardown(t, &capturingLauncher{}); r.AgentLog != "why it died" {
		t.Fatalf("AgentLog = %q", r.AgentLog)
	}
}

func TestServeTeardownPlainLauncher(t *testing.T) {
	if r := serveTeardown(t, &fakeLauncher{}); r.AgentLog != "" {
		t.Fatalf("AgentLog = %q, want empty", r.AgentLog)
	}
}
