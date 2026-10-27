package attach

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

// The jam rename renamed the Attach wire: service and launch-secret metadata.
func TestAttachWireNames(t *testing.T) {
	if got := attachpb.Runtime_ServiceDesc.ServiceName; got != "jam.attach.v1.Runtime" {
		t.Fatalf("service = %q", got)
	}
	if attachpb.Runtime_Attach_FullMethodName != "/jam.attach.v1.Runtime/Attach" {
		t.Fatalf("attach method = %q", attachpb.Runtime_Attach_FullMethodName)
	}
	if mdLaunchSecret != "x-jam-launch-secret" {
		t.Fatalf("launch-secret metadata key = %q", mdLaunchSecret)
	}
}
