//go:build integration

package launcher

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend/colima"
	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
	"github.com/aethons-tools/cove/internal/runner"
)

// The chain kits, each a kit directory (kit.yml + Dockerfile): parent builds
// on the seeded default (the blessed base), child builds on parent and needs
// parent's file to exist in its stage.
var (
	chainParentKit = kittreetest.Files("kit.yml", "kind: studio\nbase:\n  kit: default\n",
		"Dockerfile", "ARG JAM_STUDIO_BASE_IMAGE\nFROM ${JAM_STUDIO_BASE_IMAGE}\nRUN mkdir -p /opt && echo parent > /opt/parent\n")
	chainChildKit = kittreetest.Files("kit.yml", "kind: studio\nbase:\n  kit: chain-parent\n",
		"Dockerfile", "ARG JAM_STUDIO_BASE_IMAGE\nFROM ${JAM_STUDIO_BASE_IMAGE}\nRUN test -f /opt/parent && echo child > /opt/child\n")
)

// hardeningMarker is a command only the sealed hardening layer runs (not
// cove-base-image): the layer that re-owns the agent's .ssh. Exactly one
// history entry of the final image must carry it.
const hardeningMarker = "chown -R agent:agent /home/agent/.ssh"

// TestChainBuildTwoLevels builds default → chain-parent → chain-child for real
// on a docker daemon: each stage builds FROM the previous one and the harness
// and hardening layers are applied once, on top. It needs a reachable colima
// docker context (JAM_DOCKER_CONTEXT, default "colima") with the blessed base
// available, and network for the harness layer; it skips without a daemon. It
// leaves cove-kit-base:* stage tags in that daemon (they are the stage cache).
// Run: `go test -tags integration ./internal/jam/launcher/ -run TestChainBuildTwoLevels`.
func TestChainBuildTwoLevels(t *testing.T) {
	dctx := os.Getenv("JAM_DOCKER_CONTEXT")
	if dctx == "" {
		dctx = "colima"
	}
	if err := exec.Command("docker", "--context", dctx, "info").Run(); err != nil {
		t.Skipf("no docker daemon on context %q (docker info: %v)", dctx, err)
	}
	docker := func(args ...string) (string, error) {
		out, err := exec.Command("docker", append([]string{"--context", dctx}, args...)...).CombinedOutput()
		return string(out), err
	}

	store := jam.NewMemStore()
	if _, err := jam.EnsureDefaultStudioKit(store); err != nil {
		t.Fatal(err)
	}
	for _, k := range []struct {
		name  string
		files []kittree.File
	}{
		{"chain-parent", chainParentKit},
		{"chain-child", chainChildKit},
	} {
		if _, _, err := jam.PushKitFiles(store, k.name, k.files); err != nil {
			t.Fatalf("push %s: %v", k.name, err)
		}
	}
	def, err := jam.StudioKitDefinition(store, "chain-child", harnessinstall.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Chain) != 3 {
		t.Fatalf("chain = %d links, want default → chain-parent → chain-child", len(def.Chain))
	}

	l := New(Config{
		Ops:       colima.NewWithContext(runner.OS{}, dctx),
		Runner:    runner.OS{},
		BuildRoot: t.TempDir(),
		PublicKey: []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIChainIntegrationTestKeyOnlyForBuilds chain-test\n"),
	})
	st, err := l.PrepareKit(context.Background(), def)
	if err != nil {
		t.Fatalf("PrepareKit: %v", err)
	}
	if st.State != KitReady {
		t.Fatalf("PrepareKit state = %v, want ready", st.State)
	}
	tag := l.ImageTag(def.Ref)
	t.Cleanup(func() { _, _ = docker("image", "rm", "-f", tag) })

	out, err := docker("run", "--rm", "--entrypoint", "cat", tag, "/opt/parent", "/opt/child")
	if err != nil {
		t.Fatalf("read the chain's files: %v\n%s", err, out)
	}
	if got := strings.Fields(out); len(got) != 2 || got[0] != "parent" || got[1] != "child" {
		t.Fatalf("/opt/parent + /opt/child = %q, want parent, child", out)
	}

	hist, err := docker("history", "--no-trunc", "--format", "{{.CreatedBy}}", tag)
	if err != nil {
		t.Fatalf("docker history: %v\n%s", err, hist)
	}
	n := 0
	for _, entry := range strings.Split(hist, "\n") {
		if strings.Contains(entry, hardeningMarker) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("hardening marker %q is in %d image history entries, want 1:\n%s", hardeningMarker, n, hist)
	}
}
