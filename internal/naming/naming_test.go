package naming

import (
	"strings"
	"testing"
)

func TestVolumes(t *testing.T) {
	c := StudioContainer("abc")
	if got := WorkspaceVolume(c); got != "atcove-cove-abc-workspace" {
		t.Fatalf("WorkspaceVolume = %q, want atcove-cove-abc-workspace", got)
	}
	if got := AgentDataVolume(c); got != "atcove-cove-abc-agent-data" {
		t.Fatalf("AgentDataVolume = %q, want atcove-cove-abc-agent-data", got)
	}
}

// DockerVolume backs the inner /var/lib/docker cache for a docker:true instance;
// it hangs off the container base like the other volumes, with a -docker suffix.
func TestDockerVolume(t *testing.T) {
	if got := DockerVolume(StudioContainer("abc")); got != "atcove-cove-abc-docker" {
		t.Fatalf("DockerVolume = %q, want atcove-cove-abc-docker", got)
	}
}

// StudioContainer names a managed cove's container from its actor id, sanitizing
// any docker-unsafe characters so the name is always valid.
func TestStudioContainer(t *testing.T) {
	if got := StudioContainer("w1"); got != "atcove-cove-w1" {
		t.Fatalf("StudioContainer = %q, want atcove-cove-w1", got)
	}
	// Unsafe chars are sanitized to a docker-safe name.
	if got := StudioContainer("proj/worker@1"); strings.ContainsAny(got, "/@") {
		t.Fatalf("StudioContainer left unsafe chars: %q", got)
	}
}
