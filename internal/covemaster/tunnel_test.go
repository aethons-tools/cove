package covemaster

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
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

func tunnelClient(t *testing.T, f *tunnelRuntime, dial func(context.Context, string) (net.Conn, error)) *Client {
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
