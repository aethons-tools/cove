//go:build integration

package colima

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

// A real docker copies a volume's files with content, owner and mode intact,
// and the copy's own verification passes. Skips without a docker daemon or
// the alpine image (pulled once, here: CopyVolume itself never pulls).
func TestCopyVolumeRealDocker(t *testing.T) {
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("no docker daemon")
	}
	if exec.Command("docker", "pull", "-q", "alpine:3").Run() != nil {
		t.Skip("alpine:3 unavailable")
	}
	c := NewWithContext(runner.OS{}, "")
	from, to := "jam-copytest-from", "jam-copytest-to"
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", from, to).Run() })
	for _, v := range []string{from, to} {
		if err := c.CreateVolume(v); err != nil {
			t.Fatal(err)
		}
	}
	seed := exec.Command("docker", "run", "--rm", "-v", from+":/v", "alpine:3", "sh", "-c",
		"mkdir -p /v/ws && echo hello > /v/ws/f && ln -s f /v/ws/l && chown -R 1000:1000 /v/ws && chmod 600 /v/ws/f && chown 1000:1000 /v")
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v %s", err, out)
	}
	if ok, err := c.VolumeExists(from); err != nil || !ok {
		t.Fatalf("VolumeExists(%s) = %v, %v", from, ok, err)
	}
	if err := c.CopyVolume("alpine:3", from, to); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "run", "--rm", "-v", to+":/v", "alpine:3", "sh", "-c",
		"stat -c '%u:%g %a' /v/ws/f; cat /v/ws/f; readlink /v/ws/l; stat -c '%u:%g' /v").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "1000:1000 600\nhello\nf\n1000:1000" {
		t.Fatalf("copied = %q, %v", out, err)
	}
}

// An image that is not on the host fails the copy: it is never pulled.
func TestCopyVolumeRealDockerNeverPulls(t *testing.T) {
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("no docker daemon")
	}
	c := NewWithContext(runner.OS{}, "")
	from, to := "jam-copytest-np-from", "jam-copytest-np-to"
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", from, to).Run() })
	for _, v := range []string{from, to} {
		if err := c.CreateVolume(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.CopyVolume("jam-copytest-no-such-image:never", from, to); err == nil {
		t.Fatal("a copy with an absent image must fail, not pull")
	}
}

// The verification step fails a copy whose content, owner or mode differs
// from the source (each tampered after a good copy). Skips without docker.
func TestCopyVolumeVerificationCatchesTampering(t *testing.T) {
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("no docker daemon")
	}
	if exec.Command("docker", "pull", "-q", "alpine:3").Run() != nil {
		t.Skip("alpine:3 unavailable")
	}
	c := NewWithContext(runner.OS{}, "")
	for name, tamper := range map[string]string{
		"content": "echo changed > /v/ws/f",
		"owner":   "chown 0:0 /v/ws/f",
		"mode":    "chmod 644 /v/ws/f",
		"extra":   "touch /v/ws/extra",
	} {
		t.Run(name, func(t *testing.T) {
			from, to := "jam-copytest-tamper-from", "jam-copytest-tamper-to"
			_ = exec.Command("docker", "volume", "rm", "-f", from, to).Run()
			t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", from, to).Run() })
			for _, v := range []string{from, to} {
				if err := c.CreateVolume(v); err != nil {
					t.Fatal(err)
				}
			}
			seed := exec.Command("docker", "run", "--rm", "-v", from+":/v", "alpine:3", "sh", "-c",
				"mkdir -p /v/ws && echo hello > /v/ws/f && chown -R 1000:1000 /v/ws && chmod 600 /v/ws/f")
			if out, err := seed.CombinedOutput(); err != nil {
				t.Fatalf("seed: %v %s", err, out)
			}
			if err := c.CopyVolume("alpine:3", from, to); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("docker", "run", "--rm", "-v", to+":/v", "alpine:3", "sh", "-c", tamper).CombinedOutput(); err != nil {
				t.Fatalf("tamper: %v %s", err, out)
			}
			out, err := exec.Command("docker", "run", "--rm", "--pull", "never", "--network", "none", "--user", "0:0", "--entrypoint", "/bin/sh",
				"-v", from+":/from:ro", "-v", to+":/to", "alpine:3", "-c", copyVolumeVerify).CombinedOutput()
			if err == nil || !strings.Contains(string(out), "copy verification failed") {
				t.Fatalf("verification passed a tampered copy: %v %s", err, out)
			}
		})
	}
}
