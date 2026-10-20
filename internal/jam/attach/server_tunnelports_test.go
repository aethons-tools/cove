package attach

import (
	"fmt"
	"strings"

	"context"
	"github.com/aethons-tools/cove/internal/jam"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

func firstTunnelPorts(t *testing.T, stream attachpb.Runtime_AttachClient) []int32 {
	t.Helper()
	cd, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	tp, ok := cd.GetMsg().(*attachpb.ControlDown_TunnelPorts)
	if !ok {
		t.Fatalf("first control = %T, want TunnelPorts", cd.GetMsg())
	}
	return tp.TunnelPorts.GetPorts()
}

func TestExposuresChangedReachesConnectedStudio(t *testing.T) {
	_, _, srv, dial, tok, secret := harness(t)
	cc := dial()
	defer cc.Close()
	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if got := firstTunnelPorts(t, stream); len(got) != 0 { // seed: nothing exposed yet
		t.Fatalf("seed = %v", got)
	}
	srv.ExposuresChanged("w1", []int32{5173})
	if got := firstTunnelPorts(t, stream); !slices.Equal(got, []int32{5173}) {
		t.Fatalf("pushed %v", got)
	}
}

func TestReconnectRelearnsOfflineChange(t *testing.T) {
	_, _, srv, dial, tok, secret := harness(t)
	cc := dial()
	defer cc.Close()
	ctx1, cancel := context.WithCancel(authCtx(tok, secret))
	stream, err := attachpb.NewRuntimeClient(cc).Attach(ctx1)
	if err != nil {
		t.Fatal(err)
	}
	firstTunnelPorts(t, stream)
	cancel()
	if !eventually(func() bool { return !srv.Connected("w1") }) {
		t.Fatal("stream never deregistered")
	}
	srv.ExposuresChanged("w1", []int32{3000}) // while offline
	stream, err = attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if got := firstTunnelPorts(t, stream); !slices.Equal(got, []int32{3000}) {
		t.Fatalf("reconnect first control = %v", got)
	}
}

func TestAttachSeedsFromExposureSource(t *testing.T) {
	_, _, srv, dial, tok, secret := harness(t)
	srv.SetExposureSource(func(actorID string) []int32 {
		if actorID != "w1" {
			t.Errorf("source asked for %q", actorID)
		}
		return []int32{4000}
	})
	cc := dial()
	defer cc.Close()
	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if got := firstTunnelPorts(t, stream); !slices.Equal(got, []int32{4000}) {
		t.Fatalf("seed = %v", got)
	}
}

func TestEmptySetForgetsStoredPorts(t *testing.T) {
	s := NewServer(nil, nil, nil)
	s.ExposuresChanged("w1", []int32{1})
	s.ExposuresChanged("w1", nil)
	if _, ok := s.ports["w1"]; ok {
		t.Fatal("empty set kept in the server's map")
	}
}

// recvSkippingSeed receives the next control message, discarding the tunnel
// allow-list seed every Attach stream starts with.
func recvSkippingSeed(stream attachpb.Runtime_AttachClient) (*attachpb.ControlDown, error) {
	cd, err := stream.Recv()
	if err == nil && cd.GetTunnelPorts() != nil {
		return stream.Recv()
	}
	return cd, err
}

func TestPortsSurviveFullControlQueueAndPrecedeOpenTunnel(t *testing.T) {
	_, _, srv, dial, tok, secret := harness(t)
	cc := dial()
	defer cc.Close()
	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	firstTunnelPorts(t, stream) // seed
	// Stall the send loop on flow control: big wakes the client does not read.
	big := strings.Repeat("x", 200<<10)
	for i := 0; i < 40; i++ {
		srv.Wake("w1", jam.WakeReason{Kind: jam.WakeIdle, Detail: big})
	}
	srv.ExposuresChanged("w1", []int32{7000}) // queue is full: must not be lost
	srv.ExposuresChanged("w1", []int32{7001}) // coalesces
	pushed := false
	var seq []string // "wake" | "ports:<p>" | "open"
	for i := 0; i < 200 && !(pushed && len(seq) > 0 && seq[len(seq)-1] == "open"); i++ {
		cd, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		switch m := cd.GetMsg().(type) {
		case *attachpb.ControlDown_Wake:
			seq = append(seq, "wake")
		case *attachpb.ControlDown_TunnelPorts:
			seq = append(seq, fmt.Sprint("ports:", m.TunnelPorts.GetPorts()))
		case *attachpb.ControlDown_OpenTunnel:
			seq = append(seq, "open")
		}
		if !pushed {
			pushed = srv.PushOpenTunnel("w1", "t1", 7001)
		}
	}
	portsAt, openAt := -1, -1
	for i, e := range seq {
		if e == "ports:[7001]" {
			portsAt = i
		}
		if e == "open" {
			openAt = i
		}
		if e == "ports:[7000]" {
			t.Fatalf("stale set delivered: %v", seq)
		}
	}
	if portsAt < 0 || openAt < 0 || portsAt > openAt {
		t.Fatalf("latest ports must arrive before the OpenTunnel: ports@%d open@%d", portsAt, openAt)
	}
}
