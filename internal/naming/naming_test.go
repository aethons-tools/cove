package naming

import (
	"strings"
	"testing"
)

func TestVolumes(t *testing.T) {
	c := StudioContainer("abc")
	if got := WorkspaceVolume(c); got != "jam-studio-abc-workspace" {
		t.Fatalf("WorkspaceVolume = %q, want jam-studio-abc-workspace", got)
	}
	if got := AgentDataVolume(c); got != "jam-studio-abc-agent-data" {
		t.Fatalf("AgentDataVolume = %q, want jam-studio-abc-agent-data", got)
	}
}

// DockerVolume backs the inner /var/lib/docker cache for a docker:true instance;
// it hangs off the container base like the other volumes, with a -docker suffix.
func TestDockerVolume(t *testing.T) {
	if got := DockerVolume(StudioContainer("abc")); got != "jam-studio-abc-docker" {
		t.Fatalf("DockerVolume = %q, want jam-studio-abc-docker", got)
	}
}

// StudioContainer names a managed cove's container from its actor id, sanitizing
// any docker-unsafe characters so the name is always valid.
func TestStudioContainer(t *testing.T) {
	if got := StudioContainer("w1"); got != "jam-studio-w1" {
		t.Fatalf("StudioContainer = %q, want jam-studio-w1", got)
	}
	// Unsafe chars are sanitized to a docker-safe name.
	if got := StudioContainer("proj/worker@1"); strings.ContainsAny(got, "/@") {
		t.Fatalf("StudioContainer left unsafe chars: %q", got)
	}
}

func TestStudioNames(t *testing.T) {
	c := StudioContainer("ses_1/x")
	if c != "jam-studio-ses_1-x" {
		t.Fatalf("StudioContainer = %q", c)
	}
	if AgentDataVolume(c) != "jam-studio-ses_1-x-agent-data" || WorkspaceVolume(c) != "jam-studio-ses_1-x-workspace" ||
		DockerVolume(c) != "jam-studio-ses_1-x-docker" {
		t.Fatal("volumes must be named after the new container")
	}
}
