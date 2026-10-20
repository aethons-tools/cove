package attach

import (
	"context"
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
