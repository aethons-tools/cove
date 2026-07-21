package naming

import "testing"

// Image is kit-scoped and carries no class — every collaborator of a kit shares
// one image.
func TestImage(t *testing.T) {
	if got := Image("box"); got != "atcove-box" {
		t.Fatalf("Image(box) = %q, want atcove-box", got)
	}
}

// Container is atcove-{kit} for a plain instance and atcove-{kit}-{class} for a
// collaborator instance.
func TestContainer(t *testing.T) {
	if got := Container("box", ""); got != "atcove-box" {
		t.Fatalf("Container(box, plain) = %q, want atcove-box", got)
	}
	if got := Container("box", "steward"); got != "atcove-box-steward" {
		t.Fatalf("Container(box, steward) = %q, want atcove-box-steward", got)
	}
}

// The volume names hang off the container (instance) base, so every resource of
// an instance shares the atcove-{kit}-{class} prefix. Workspace uses -workspace;
// the /agent-data volume uses -agent-data (matching the mount, not the old -state).
func TestVolumes(t *testing.T) {
	plain := Container("box", "")
	if got := WorkspaceVolume(plain); got != "atcove-box-workspace" {
		t.Fatalf("WorkspaceVolume(plain) = %q, want atcove-box-workspace", got)
	}
	if got := AgentDataVolume(plain); got != "atcove-box-agent-data" {
		t.Fatalf("AgentDataVolume(plain) = %q, want atcove-box-agent-data", got)
	}

	collab := Container("box", "steward")
	if got := WorkspaceVolume(collab); got != "atcove-box-steward-workspace" {
		t.Fatalf("WorkspaceVolume(collab) = %q, want atcove-box-steward-workspace", got)
	}
	if got := AgentDataVolume(collab); got != "atcove-box-steward-agent-data" {
		t.Fatalf("AgentDataVolume(collab) = %q, want atcove-box-steward-agent-data", got)
	}
}

// WorkerContainer carries the atcove- prefix like every other resource, plus the
// pid+nanotime suffix that keeps concurrent dispatches of one kit from colliding.
func TestWorkerContainer(t *testing.T) {
	if got := WorkerContainer("box", 42, 1234567890); got != "atcove-work-box-42-1234567890" {
		t.Fatalf("WorkerContainer(box, 42, 1234567890) = %q, want atcove-work-box-42-1234567890", got)
	}
}
