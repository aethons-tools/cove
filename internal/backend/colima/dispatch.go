package colima

import (
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/naming"
)

// Compile-time proof colima satisfies the dispatch surface.
var _ backend.DispatchOps = (*Colima)(nil)

// RunEphemeral starts a fresh, labeled container with --rm and a published sshd,
// so a force-remove (or --rm on stop) reclaims everything. A docker:true dispatch
// additionally runs it under Sysbox with a -docker cache volume named after the
// worker container (COV-117); docker:false is volume-less, exactly as before.
func (c *Colima) RunEphemeral(image, digest, name, label string, dns []string, docker bool) (backend.Instance, error) {
	if err := c.preflight(); err != nil {
		return backend.Instance{}, err
	}
	// docker:true needs the Sysbox runtime in the VM; detect + guide before we run
	// (at-cove does not install it) — COV-117.
	if docker {
		if err := c.requireSysboxRuntime(); err != nil {
			return backend.Instance{}, err
		}
	}
	// Pin the built-image digest when install captured one (COV-78), falling back
	// to the mutable tag for a legacy manifest; the tag is kept on the Instance.
	runArgs := []string{"run", "-d",
		"--name", name,
		"--rm",
		"--label", label,
		"--init",
		"--cap-add=NET_ADMIN",
	}
	runArgs = append(runArgs, dnsArgs(dns)...)
	runArgs = append(runArgs, dockerArgs(docker, naming.DockerVolume(name))...)
	runArgs = append(runArgs,
		"-p", "127.0.0.1::2222",
		runImage(image, digest),
	)
	if err := c.r.Run("docker", dargs(runArgs...)...); err != nil {
		return backend.Instance{}, err
	}
	return backend.Instance{Backend: "colima", Container: name, Image: image, ImageDigest: digest}, nil
}

func (c *Colima) RemoveContainer(name string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	return c.r.Run("docker", dargs("rm", "-f", name)...)
}

// ScavengeLabeled removes labeled containers older than olderThan. It never removes
// the image (shared across dispatches) or a volume (there are none).
func (c *Colima) ScavengeLabeled(label string, olderThan time.Duration, now time.Time) (int, error) {
	if err := c.preflight(); err != nil {
		return 0, err
	}
	out, err := c.r.Output("docker", dargs("ps", "-aq", "--filter", "label="+label)...)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, id := range strings.Fields(out) {
		created, err := c.r.Output("docker", dargs("inspect", "-f", "{{.Created}}", id)...)
		if err != nil {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(created))
		if err != nil {
			continue
		}
		if now.Sub(t) > olderThan {
			if err := c.r.Run("docker", dargs("rm", "-f", id)...); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}
