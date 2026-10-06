package colima

import (
	"bytes"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/naming"
)

// Compile-time proof colima satisfies the dispatch surface.
var (
	_ backend.DispatchOps = (*Colima)(nil)
	_ backend.VolumeOps   = (*Colima)(nil)
)

// RunEphemeral starts a fresh, labeled container with --rm and a published sshd,
// so a force-remove (or --rm on stop) reclaims everything. A docker:true dispatch
// additionally runs it under Sysbox with a -docker cache volume named after the
// worker container (COV-117); docker:false is volume-less, exactly as before.
// Each mount adds `-v <volume>:<target>`: named volumes survive the --rm
// (which removes only anonymous ones), so a re-run re-attaches them (COV-249).
func (c *Colima) RunEphemeral(image, digest, name, label string, dns, addHosts []string, docker bool, mounts ...backend.Mount) (backend.Instance, error) {
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
	}
	runArgs = append(runArgs, initArgs(docker)...)
	runArgs = append(runArgs, "--cap-add=NET_ADMIN")
	runArgs = append(runArgs, dnsArgs(dns)...)
	runArgs = append(runArgs, addHostArgs(addHosts)...)
	runArgs = append(runArgs, dockerArgs(docker, naming.DockerVolume(name))...)
	for _, m := range mounts {
		runArgs = append(runArgs, "-v", m.Volume+":"+m.Target)
	}
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

// RemoveVolumes deletes the named volumes (`docker volume rm -f`: an absent
// volume is a no-op; one still in use by a container errors). No names is a
// no-op that runs nothing.
func (c *Colima) RemoveVolumes(names ...string) error {
	if len(names) == 0 {
		return nil
	}
	if err := c.preflight(); err != nil {
		return err
	}
	return c.r.Run("docker", dargs(append([]string{"volume", "rm", "-f"}, names...)...)...)
}

// CreateVolume creates a labeled named volume (`docker volume create`;
// idempotent for an existing one). A volume `-v` auto-creates carries no
// label, so a caller that needs one creates it first.
func (c *Colima) CreateVolume(name string, labels ...string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	args := []string{"volume", "create"}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	return c.r.Run("docker", dargs(append(args, name)...)...)
}

// ListVolumes lists the volumes carrying label key, name → value.
func (c *Colima) ListVolumes(key string) (map[string]string, error) {
	if err := c.preflight(); err != nil {
		return nil, err
	}
	out, err := c.r.Output("docker", dargs("volume", "ls", "--filter", "label="+key,
		"--format", "{{.Name}}\t{{.Label \""+key+"\"}}")...) // a real tab: no shell, no escape processing
	if err != nil {
		return nil, err
	}
	vols := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, val, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if name != "" {
			vols[name] = val
		}
	}
	return vols, nil
}

// Pause freezes a running container (docker pause; cgroup freezer) so an idle
// cove burns ~0 CPU (COV-162).
func (c *Colima) Pause(name string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	return c.dockerIdempotent("is already paused", dargs("pause", name)...)
}

// Unpause thaws a paused container (docker unpause), the inverse of Pause.
func (c *Colima) Unpause(name string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	return c.dockerIdempotent("is not paused", dargs("unpause", name)...)
}

// dockerIdempotent runs `docker <args>` and treats a benign "already in the
// target state" daemon message as success — so pausing an already-paused
// container (or unpausing a running one) is a no-op, not an error. Without this
// the idle ladder fails on every reconcile against a cove it already paused
// ("container … is already paused"), never records the Idled phase, and retries
// forever. The daemon writes that message to stderr, so capture both streams.
func (c *Colima) dockerIdempotent(benign string, args ...string) error {
	var buf bytes.Buffer
	if err := c.r.RunIO(nil, &buf, &buf, "docker", args...); err != nil {
		if strings.Contains(buf.String(), benign) {
			return nil
		}
		return err
	}
	return nil
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
