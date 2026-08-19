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

// DockerVolume backs the inner /var/lib/docker cache for a docker:true instance;
// it hangs off the container base like the other volumes, with a -docker suffix.
func TestDockerVolume(t *testing.T) {
	if got := DockerVolume(Container("box", "")); got != "atcove-box-docker" {
		t.Fatalf("DockerVolume(plain) = %q, want atcove-box-docker", got)
	}
	if got := DockerVolume(Container("box", "steward")); got != "atcove-box-steward-docker" {
		t.Fatalf("DockerVolume(collab) = %q, want atcove-box-steward-docker", got)
	}
}

// WorkerContainer carries the atcove- prefix like every other resource, plus the
// pid+nanotime suffix that keeps concurrent dispatches of one kit from colliding.
func TestWorkerContainer(t *testing.T) {
	if got := WorkerContainer("box", 42, 1234567890); got != "atcove-work-box-42-1234567890" {
		t.Fatalf("WorkerContainer(box, 42, 1234567890) = %q, want atcove-work-box-42-1234567890", got)
	}
}

// ShadowVolume hangs a per-dir volume off the instance's container base, with a
// -shadow- token whose sanitized suffix drops path separators and a leading dot,
// so it sorts with the instance's other volumes and never collides (COV-130).
func TestShadowVolume(t *testing.T) {
	collab := Container("box", "human") // atcove-box-human
	cases := map[string]string{
		".venv":         "atcove-box-human-shadow-venv",
		"node_modules":  "atcove-box-human-shadow-node_modules",
		"foo/bar":       "atcove-box-human-shadow-foo-bar",
		".pytest_cache": "atcove-box-human-shadow-pytest_cache",
	}
	for dir, want := range cases {
		if got := ShadowVolume(collab, dir); got != want {
			t.Errorf("ShadowVolume(%q) = %q, want %q", dir, got, want)
		}
	}
}

// SanitizeShadowDir is the single source of the sanitize rule shared by
// ShadowVolume and config validation, so a collision check matches the real name.
func TestSanitizeShadowDir(t *testing.T) {
	if got := SanitizeShadowDir(".venv"); got != "venv" {
		t.Fatalf("SanitizeShadowDir(.venv) = %q, want venv", got)
	}
	if got := SanitizeShadowDir("a/b/c"); got != "a-b-c" {
		t.Fatalf("SanitizeShadowDir(a/b/c) = %q, want a-b-c", got)
	}
}
