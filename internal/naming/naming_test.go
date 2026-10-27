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

func TestCoveContainerOn(t *testing.T) {
	for _, c := range []struct{ m, id, want string }{
		{"", "w1", "atcove-cove-w1"},
		{"m1", "w1", "atcove.m1.cove-w1"},
		{"m1", "a/b", "atcove.m1.cove-a-b"},
	} {
		if got := CoveContainerOn(c.m, c.id); got != c.want {
			t.Errorf("CoveContainerOn(%q,%q) = %q, want %q", c.m, c.id, got, c.want)
		}
	}
}

// Managers sharing a docker daemon never collide: a local name starts atcove-,
// a manager's atcove.<manager>. (manager names hold no '.'), so neither a
// crafted manager name nor a crafted id can reproduce another's name.
func TestCoveContainerOnNeverCollides(t *testing.T) {
	cases := []struct{ m, id string }{
		{"", "a-cove-b"},
		{"cove-a", "b"},
		{"a", "b"},
		{"a-cove", "b"},
		{"a", "cove-b"},
		{"a", "x.cove-b"},
		{"a", "b.c"},
		{"a-cove-b", "c"},
		{"", "b"},
		{"", "a.cove-b"},
	}
	seen := map[string]string{}
	for _, c := range cases {
		got := CoveContainerOn(c.m, c.id)
		key := c.m + "|" + c.id
		if prev, dup := seen[got]; dup {
			t.Errorf("CoveContainerOn(%q) = %q collides with %q", key, got, prev)
		}
		seen[got] = key
	}
}
