package colima

import (
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
)

// Compile-time proof colima satisfies the dispatch surface.
var _ backend.DispatchOps = (*Colima)(nil)

// RunEphemeral starts a fresh, labeled, volume-less container with --rm and a
// published sshd, so a force-remove (or --rm on stop) reclaims everything.
func (c *Colima) RunEphemeral(image, digest, name, label string, dns []string) (backend.Instance, error) {
	if err := c.preflight(); err != nil {
		return backend.Instance{}, err
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
