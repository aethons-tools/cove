package attachpb

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// Guards that the generated surface the server depends on exists and has the
// expected shape (a cheap canary against a bad/partial regen).
func TestGeneratedSurface(t *testing.T) {
	// Activity enum values map to the proto.
	if Activity_RUNNING == Activity_DONE {
		t.Fatal("activity enum collapsed")
	}
	// StatusUp oneof wrappers.
	_ = &StatusUp{Msg: &StatusUp_Status{Status: Activity_WAITING}}
	_ = &StatusUp{Msg: &StatusUp_Heartbeat{Heartbeat: &Heartbeat{}}}
	// ControlDown oneof wrappers, including the reserved RotateToken.
	_ = &ControlDown{Msg: &ControlDown_Teardown{Teardown: &Teardown{Reason: "x"}}}
	_ = &ControlDown{Msg: &ControlDown_Wake{Wake: &Wake{}}}
	_ = &ControlDown{Msg: &ControlDown_Rotate{Rotate: &RotateToken{}}}
	// Server registration symbol exists.
	var _ = RegisterRuntimeServer
	var _ RuntimeServer = (*UnimplementedRuntimeServer)(nil)
}

func TestSessionEventRoundTrip(t *testing.T) {
	in := &StatusUp{Msg: &StatusUp_Event{Event: &SessionEvent{
		StreamId: "0123456789abcdef0123456789abcdef", Seq: 7, Turn: 2,
		ObservedUnixMs: 1700000000000, Raw: []byte(`{"type":"result"}`), TruncatedBytes: 5,
	}}}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out StatusUp
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	ev := out.GetEvent()
	if ev.GetSeq() != 7 || ev.GetTurn() != 2 || ev.GetTruncatedBytes() != 5 || string(ev.GetRaw()) != `{"type":"result"}` {
		t.Fatalf("round trip mismatch: %+v", ev)
	}
	ack := &ControlDown{Msg: &ControlDown_Ack{Ack: &EventAck{StreamId: "s", Seq: 9}}}
	b, _ = proto.Marshal(ack)
	var outAck ControlDown
	if err := proto.Unmarshal(b, &outAck); err != nil || outAck.GetAck().GetSeq() != 9 {
		t.Fatalf("ack round trip: %v %+v", err, outAck.GetAck())
	}
}
