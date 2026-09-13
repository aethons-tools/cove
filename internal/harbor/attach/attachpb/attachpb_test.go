package attachpb

import "testing"

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
