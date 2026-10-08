package covemaster

import (
	"reflect"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

func TestControlFromPBRunGate(t *testing.T) {
	got, ok := controlFromPB(&attachpb.ControlDown{Msg: &attachpb.ControlDown_Gate{Gate: &attachpb.RunGate{RunId: "r1", Alarm: "ci", Command: "true", TimeoutS: 60}}})
	want := Control{Kind: RunGate, Gate: &GateRequest{RunID: "r1", Alarm: "ci", Command: "true", Timeout: 60 * time.Second}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v %v, want %+v", got, ok, want)
	}
	got, ok = controlFromPB(&attachpb.ControlDown{Msg: &attachpb.ControlDown_Wake{Wake: &attachpb.Wake{Reasons: []*attachpb.WakeReason{{Kind: "squawk"}}}}})
	if !ok || !reflect.DeepEqual(got, Control{Kind: Wake, Reasons: []WakeReason{{Kind: "squawk"}}}) {
		t.Fatalf("wake = %+v %v", got, ok)
	}
	if _, ok := controlFromPB(&attachpb.ControlDown{Msg: &attachpb.ControlDown_Tier{Tier: &attachpb.TierChanged{}}}); ok {
		t.Fatal("tier change decoded as a control")
	}
}

func TestGateResultMsg(t *testing.T) {
	m := gateResultMsg(GateResult{RunID: "r1", Exit: 3, Output: []byte("x"), Truncated: true})
	g := m.GetGate()
	if g == nil || g.GetRunId() != "r1" || g.GetExit() != 3 || g.GetTimedOut() || string(g.GetOutput()) != "x" || !g.GetTruncated() {
		t.Fatalf("msg = %+v", m)
	}
}

func TestClientGateResultNeverBlocks(t *testing.T) {
	c := New(Config{Addr: "unused"}, nil)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			c.GateResult(GateResult{RunID: "r"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GateResult blocked with no stream")
	}
}
