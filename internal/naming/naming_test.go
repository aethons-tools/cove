package naming

import (
	"strings"
	"testing"
)

func TestVolumes(t *testing.T) {
	c := CoveContainer("abc")
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
	if got := DockerVolume(CoveContainer("abc")); got != "atcove-cove-abc-docker" {
		t.Fatalf("DockerVolume = %q, want atcove-cove-abc-docker", got)
	}
}

// CoveContainer names a managed cove's container from its actor id, sanitizing
// any docker-unsafe characters so the name is always valid.
func TestCoveContainer(t *testing.T) {
	if got := CoveContainer("w1"); got != "atcove-cove-w1" {
		t.Fatalf("CoveContainer = %q, want atcove-cove-w1", got)
	}
	// Unsafe chars are sanitized to a docker-safe name.
	if got := CoveContainer("proj/worker@1"); strings.ContainsAny(got, "/@") {
		t.Fatalf("CoveContainer left unsafe chars: %q", got)
	}
}
