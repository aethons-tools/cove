package colima

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

// The copy runs a fixed script in a throwaway no-network root container that
// never pulls: only the two volume names and the image reach argv.
func TestCopyVolumeArgv(t *testing.T) {
	f := &runner.Fake{}
	c := NewWithContext(f, "")
	if err := c.CopyVolume("img@sha256:x", "old-agent-data", "new-agent-data"); err != nil {
		t.Fatal(err)
	}
	got := dockerCall(f.Calls, "run")
	want := []string{"run", "--rm", "--pull", "never", "--network", "none", "--user", "0:0", "--entrypoint", "/bin/sh",
		"-v", "old-agent-data:/from:ro", "-v", "new-agent-data:/to", "img@sha256:x", "-c", copyVolumeScript}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv =\n%q\nwant\n%q", got, want)
	}
}

// The script copies with cp -a, then verifies the copy (every path's owner
// and mode, every file's content) and fails when the two differ.
func TestCopyVolumeScriptVerifies(t *testing.T) {
	if copyVolumeScript != copyVolumeCopy+copyVolumeVerify {
		t.Fatal("the copy script is the copy, then the verification")
	}
	if strings.Contains(strings.ReplaceAll(copyVolumeVerify, "||", ""), "|") {
		t.Fatalf("no pipelines in the verification (a failure on one side could be masked; dash has no pipefail):\n%s", copyVolumeVerify)
	}
	for _, want := range []string{"set -e", "cp -a /from/. /to/", "stat -c '%n %u:%g %a'", "md5sum", "cmp -s"} {
		if !strings.Contains(copyVolumeScript, want) {
			t.Errorf("copy script lacks %q:\n%s", want, copyVolumeScript)
		}
	}
}

func TestVolumeExistsMatchesExactly(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "jam-studio-s1-agent-data-x\njam-studio-s1-agent-data\n"}, {Stdout: "jam-studio-s1-agent-data-x\n"}}}
	c := NewWithContext(f, "")
	if ok, err := c.VolumeExists("jam-studio-s1-agent-data"); err != nil || !ok {
		t.Fatalf("exists = %v, %v", ok, err)
	}
	if ok, err := c.VolumeExists("jam-studio-s1-agent-data"); err != nil || ok {
		t.Fatalf("a prefix match is not existence: %v, %v", ok, err)
	}
	if args := strings.Join(dockerCall(f.Calls, "volume"), " "); args != "volume ls --quiet --filter name=jam-studio-s1-agent-data" {
		t.Fatalf("argv = %q", args)
	}
}

func TestFindContainersParsesNamesImagesAndMounts(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "box-a\timg-a@sha256:1\tvol-1,vol-2\nbox-b\timg-b\t\n\n"}}}
	got, err := NewWithContext(f, "").FindContainers("label=some.label")
	if err != nil {
		t.Fatal(err)
	}
	a, b := got["box-a"], got["box-b"]
	if len(got) != 2 || a.Image != "img-a@sha256:1" || strings.Join(a.Mounts, " ") != "vol-1 vol-2" || b.Image != "img-b" || len(b.Mounts) != 0 {
		t.Fatalf("containers = %+v", got)
	}
	want := "ps --all --no-trunc --filter label=some.label --format {{.Names}}\t{{.Image}}\t{{.Mounts}}"
	if args := strings.Join(dockerCall(f.Calls, "ps"), " "); args != want {
		t.Fatalf("argv = %q, want %q", args, want)
	}
}
