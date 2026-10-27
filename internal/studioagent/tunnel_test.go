package studioagent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
	"github.com/aethons-tools/cove/internal/jam/preview"
)

// tunnelRuntime is a fake Runtime whose Attach pushes one OpenTunnel and whose
// Tunnel records the claim, sends "ping" to the studio, then records the next
// data frame (or, if closeAfterOpen, sends a TunnelClose instead).
type tunnelRuntime struct {
	attachpb.UnimplementedRuntimeServer
	port           int32
	closeAfterOpen bool
	opened         chan string
	gotData        chan string
}

func newTunnelRuntime(port int32) *tunnelRuntime {
	return &tunnelRuntime{port: port, opened: make(chan string, 1), gotData: make(chan string, 1)}
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
	if f.closeAfterOpen {
		_ = s.Send(&attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Close{Close: &attachpb.TunnelClose{Reason: "jam closed"}}})
		return nil
	}
	_ = s.Send(&attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Data{Data: []byte("ping")}})
	if fr, err = s.Recv(); err == nil {
		f.gotData <- string(fr.GetData())
	}
	return nil
}

func tunnelClient(t *testing.T, f attachpb.RuntimeServer, dial func(context.Context, string) (net.Conn, error)) *Client {
	t.Helper()
	return New(Config{Addr: "bufnet", Token: "t", LaunchSecret: "s",
		DialOptions: []grpc.DialOption{fakeHarness(t, f), grpc.WithTransportCredentials(insecure.NewCredentials())},
		TunnelDial:  dial}, nil)
}

func runBlocked(ctx context.Context, c *Client) chan error {
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, &blockWorkload{first: Running, controls: make(chan Control, 4)})
	}()
	return done
}

func TestDialBackPipesToLoopbackPort(t *testing.T) {
	f := newTunnelRuntime(2222)
	dialed := make(chan string, 1)
	c := tunnelClient(t, f, func(ctx context.Context, addr string) (net.Conn, error) {
		dialed <- addr
		a, b := net.Pipe()
		go func() { // fake sshd: answers "ping" with "pong"
			buf := make([]byte, 4)
			if _, err := io.ReadFull(b, buf); err == nil && string(buf) == "ping" {
				_, _ = b.Write([]byte("pong"))
			}
		}()
		return a, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runBlocked(ctx, c)
	select {
	case id := <-f.opened:
		if id != "t1" {
			t.Fatalf("claimed id %q", id)
		}
	case <-ctx.Done():
		t.Fatal("tunnel never opened")
	}
	select {
	case got := <-f.gotData:
		if got != "pong" {
			t.Fatalf("studio side relayed %q", got)
		}
	case <-ctx.Done():
		t.Fatal("no data relayed to Jam")
	}
	if addr := <-dialed; addr != "127.0.0.1:2222" {
		t.Fatalf("dialed %q, want loopback 2222", addr)
	}
}

func TestDialBackRefusesPortNotOnAllowList(t *testing.T) {
	f := newTunnelRuntime(22)
	called := make(chan struct{}, 1)
	c := tunnelClient(t, f, func(context.Context, string) (net.Conn, error) {
		called <- struct{}{}
		return nil, errors.New("x")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = <-runBlocked(ctx, c)
	select {
	case <-f.opened:
		t.Fatal("opened a tunnel for a port not on the allow-list")
	default:
	}
	select {
	case <-called:
		t.Fatal("dialed a refused port")
	default:
	}
}

func TestDialBackCustomAllowList(t *testing.T) {
	f := newTunnelRuntime(22)
	dialed := make(chan string, 1)
	c := tunnelClient(t, f, func(_ context.Context, addr string) (net.Conn, error) {
		dialed <- addr
		a, _ := net.Pipe()
		return a, nil
	})
	c.cfg.TunnelPorts = []int32{22}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runBlocked(ctx, c)
	select {
	case addr := <-dialed:
		if addr != "127.0.0.1:22" {
			t.Fatalf("dialed %q", addr)
		}
	case <-ctx.Done():
		t.Fatal("allow-listed port never dialed")
	}
}

// A TunnelClose from Jam must end dialBack and close the local conn.
func TestDialBackJamCloseClosesLocal(t *testing.T) {
	f := newTunnelRuntime(2222)
	f.closeAfterOpen = true
	remote := make(chan net.Conn, 1)
	c := tunnelClient(t, f, func(context.Context, string) (net.Conn, error) {
		a, b := net.Pipe()
		remote <- b
		return a, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runBlocked(ctx, c)
	var b net.Conn
	select {
	case b = <-remote:
	case <-ctx.Done():
		t.Fatal("never dialed")
	}
	_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := b.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("local conn read err = %v, want EOF (closed by dialBack)", err)
	}
}

// Cancelling Run's ctx must end an idle tunnel and close the local conn.
func TestDialBackEndsOnContextCancel(t *testing.T) {
	f := newTunnelRuntime(2222)
	remote := make(chan net.Conn, 1)
	c := tunnelClient(t, f, func(context.Context, string) (net.Conn, error) {
		a, b := net.Pipe()
		remote <- b
		return a, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runBlocked(ctx, c)
	b := <-remote
	<-f.opened
	cancel()
	_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
	// drain the "ping" the fake Jam sent, then expect EOF
	buf := make([]byte, 16)
	for {
		if _, err := b.Read(buf); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("read err = %v, want EOF", err)
			}
			return
		}
	}
}

// tailRuntime's Tunnel collects every data frame until Jam sees the close, so
// a test can check nothing the local side wrote before closing was lost.
type tailRuntime struct {
	attachpb.UnimplementedRuntimeServer
	got chan []byte
}

func (f *tailRuntime) Attach(s attachpb.Runtime_AttachServer) error {
	_ = s.Send(&attachpb.ControlDown{Msg: &attachpb.ControlDown_OpenTunnel{OpenTunnel: &attachpb.OpenTunnel{Id: "t1", Port: 2222}}})
	<-s.Context().Done()
	return nil
}

func (f *tailRuntime) Tunnel(s attachpb.Runtime_TunnelServer) error {
	if _, err := s.Recv(); err != nil {
		return err
	}
	var all []byte
	for {
		fr, err := s.Recv()
		if err != nil || fr.GetClose() != nil {
			f.got <- all
			return nil
		}
		all = append(all, fr.GetData()...)
	}
}

// When the local side writes and closes at once (sshd's last bytes, then
// EOF), every byte must still reach Jam before the tunnel ends.
func TestDialBackDeliversTailBeforeLocalClose(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 1<<16) // 1 MiB
	f := &tailRuntime{got: make(chan []byte, 1)}
	c := tunnelClient(t, f, func(context.Context, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { _, _ = b.Write(payload); _ = b.Close() }()
		return a, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBlocked(ctx, c)
	select {
	case got := <-f.got:
		if !bytes.Equal(got, payload) {
			t.Fatalf("Jam received %d of %d bytes", len(got), len(payload))
		}
	case <-ctx.Done():
		t.Fatal("tunnel never ended")
	}
}

// scriptRuntime's Attach sends its script in order, pausing between frames so
// each OpenTunnel's outcome is observable before the next control arrives.
type scriptRuntime struct {
	attachpb.UnimplementedRuntimeServer
	script []*attachpb.ControlDown
	opened chan string
}

func (f *scriptRuntime) Attach(s attachpb.Runtime_AttachServer) error {
	for _, cd := range f.script {
		_ = s.Send(cd)
		time.Sleep(100 * time.Millisecond)
	}
	<-s.Context().Done()
	return nil
}

func (f *scriptRuntime) Tunnel(s attachpb.Runtime_TunnelServer) error {
	fr, err := s.Recv()
	if err != nil {
		return err
	}
	f.opened <- fr.GetOpen().GetId()
	<-s.Context().Done()
	return nil
}

func newTestClient(t *testing.T, f attachpb.RuntimeServer, dial func(context.Context, string) (net.Conn, error)) *Client {
	return tunnelClient(t, f, dial)
}

func runFor(t *testing.T, c *Client, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	<-runBlocked(ctx, c)
}

func drain[T any](ch chan T) []T {
	var out []T
	for {
		select {
		case v := <-ch:
			out = append(out, v)
		default:
			return out
		}
	}
}

func openTunnel(id string, port int32) *attachpb.ControlDown {
	return &attachpb.ControlDown{Msg: &attachpb.ControlDown_OpenTunnel{OpenTunnel: &attachpb.OpenTunnel{Id: id, Port: port}}}
}

func setPorts(ports ...int32) *attachpb.ControlDown {
	return &attachpb.ControlDown{Msg: &attachpb.ControlDown_TunnelPorts{TunnelPorts: &attachpb.SetTunnelPorts{Ports: ports}}}
}

func TestDialBackFollowsSetTunnelPorts(t *testing.T) {
	f := &scriptRuntime{script: []*attachpb.ControlDown{
		setPorts(5173), openTunnel("t1", 5173), setPorts(), openTunnel("t2", 5173),
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

func TestSetTunnelPortsReplacesAndKeepsSSHD(t *testing.T) {
	f := &scriptRuntime{script: []*attachpb.ControlDown{
		setPorts(5173), setPorts(3000), openTunnel("t1", 5173), openTunnel("t2", 3000), setPorts(), openTunnel("t3", 2222),
	}, opened: make(chan string, 4)}
	dialed := make(chan string, 4)
	c := newTestClient(t, f, func(_ context.Context, addr string) (net.Conn, error) {
		dialed <- addr
		a, _ := net.Pipe()
		return a, nil
	})
	runFor(t, c, 2*time.Second)
	if got := drain(dialed); !slices.Equal(got, []string{"127.0.0.1:3000", "127.0.0.1:2222"}) {
		t.Fatalf("dialed %v", got)
	}
}

func TestSSHDPortAllowedRegardlessOfConfig(t *testing.T) {
	f := &scriptRuntime{script: []*attachpb.ControlDown{openTunnel("t1", 2222)}, opened: make(chan string, 2)}
	dialed := make(chan string, 2)
	c := newTestClient(t, f, func(_ context.Context, addr string) (net.Conn, error) {
		dialed <- addr
		a, _ := net.Pipe()
		return a, nil
	})
	c.cfg.TunnelPorts = []int32{9999}
	runFor(t, c, time.Second)
	if got := drain(dialed); !slices.Equal(got, []string{"127.0.0.1:2222"}) {
		t.Fatalf("dialed %v, want sshd port allowed despite config", got)
	}
}

// recIDE is a fake ideRunner that records its calls, in order, alongside the
// tunnel's dials.
type recIDE struct {
	mu       sync.Mutex
	calls    []string
	err      error
	released chan struct{}
}

func (r *recIDE) record(s string) {
	r.mu.Lock()
	r.calls = append(r.calls, s)
	r.mu.Unlock()
}

func (r *recIDE) Acquire(context.Context) error { r.record("acquire"); return r.err }
func (r *recIDE) Release()                      { r.record("release"); close(r.released) }
func (r *recIDE) Close()                        { r.record("close") }

func (r *recIDE) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func TestDialBackIDEPortStartsIDE(t *testing.T) {
	f := newTunnelRuntime(preview.IDEPort)
	f.closeAfterOpen = true // the tunnel ends right after the claim
	ide := &recIDE{released: make(chan struct{})}
	c := tunnelClient(t, f, func(_ context.Context, addr string) (net.Conn, error) {
		ide.record("dial " + addr)
		a, _ := net.Pipe()
		return a, nil
	})
	c.cfg.IDE = ide
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runBlocked(ctx, c)
	select {
	case <-ide.released:
	case <-ctx.Done():
		t.Fatalf("Release never ran; calls %v", ide.got())
	}
	want := []string{"acquire", "dial 127.0.0.1:13337", "release"}
	if got := ide.got(); !slices.Equal(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

// When Run returns, the IDE is closed: no code-server outlives jam-studio-agent.
func TestRunClosesIDE(t *testing.T) {
	f := &scriptRuntime{opened: make(chan string, 1)}
	ide := &recIDE{released: make(chan struct{})}
	c := tunnelClient(t, f, nil)
	c.cfg.IDE = ide
	runFor(t, c, 300*time.Millisecond)
	if got := ide.got(); !slices.Equal(got, []string{"close"}) {
		t.Fatalf("calls %v, want close once Run returned", got)
	}
}

// An IDE that can't start means no dial and no tunnel: Jam's dial-back times
// out (its 504).
func TestDialBackIDEAcquireFailsNoTunnel(t *testing.T) {
	f := newTunnelRuntime(preview.IDEPort)
	ide := &recIDE{err: ErrIDEUnavailable, released: make(chan struct{})}
	c := tunnelClient(t, f, func(_ context.Context, addr string) (net.Conn, error) {
		ide.record("dial " + addr)
		a, _ := net.Pipe()
		return a, nil
	})
	c.cfg.IDE = ide
	runFor(t, c, 500*time.Millisecond)
	// "close" is Run's shutdown, not part of the tunnel.
	if got := ide.got(); !slices.Equal(got, []string{"acquire", "close"}) {
		t.Fatalf("calls %v, want only the failed acquire (then Run's close)", got)
	}
	select {
	case <-f.opened:
		t.Fatal("opened a tunnel though the IDE failed to start")
	default:
	}
}

func TestIDEPortRefusedWithoutIDE(t *testing.T) {
	f := newTunnelRuntime(preview.IDEPort)
	dialed := make(chan string, 1)
	c := tunnelClient(t, f, func(_ context.Context, addr string) (net.Conn, error) {
		dialed <- addr
		a, _ := net.Pipe()
		return a, nil
	})
	runFor(t, c, 500*time.Millisecond)
	if got := drain(dialed); len(got) != 0 {
		t.Fatalf("dialed %v with no IDE configured", got)
	}
	select {
	case <-f.opened:
		t.Fatal("opened a tunnel to the IDE port with no IDE")
	default:
	}
}
