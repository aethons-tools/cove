// Package colima implements the Backend interface over local Docker (Colima).
package colima

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
)

func init() {
	backend.Register("colima", func(r runner.Runner) backend.Backend { return New(r) })
}

// dockerContext is the docker context colima creates for its default profile.
// Every docker invocation is pinned to it so cove never silently talks to a
// different daemon (e.g. Docker Desktop) than the colima backend intends —
// the failure mode where a stopped colima falls through to the wrong backend.
const dockerContext = "colima"

type Colima struct{ r runner.Runner }

func New(r runner.Runner) backend.Backend { return &Colima{r: r} }

// dargs prepends the pinned docker context to a docker subcommand's arguments.
func dargs(args ...string) []string {
	return append([]string{"--context", dockerContext}, args...)
}

// preflight fails fast with an actionable message when the colima docker context
// is not reachable (colima stopped or never set up), rather than letting docker
// fall through to another daemon or emit a cryptic error. `docker info` exits
// non-zero only when the daemon behind the context is unreachable.
func (c *Colima) preflight() error {
	// Probe (not Output): we want only the exit status. `docker info` writes
	// benign daemon warnings (e.g. "bridge-nf-call-iptables is disabled") to
	// stderr, which Output would stream to the terminal on every connect.
	if err := c.r.Probe("docker", dargs("info")...); err != nil {
		// %v, not %w: wrapping the *ExitError would make main treat this as a
		// child command that already printed its own message and exit silently
		// with the code — swallowing this guidance. A plain error gets printed.
		return fmt.Errorf("colima is not reachable (docker context %q is stopped or not set up). Start it with:\n  colima start\n(docker: %v)", dockerContext, err)
	}
	return nil
}

// image is the human-readable tag for a kit's image — readable in
// `docker images`/`ps` for troubleshooting.
func image(name string) string { return "at-cove-for-" + name }

// dockerBuild is the single docker-build site (COV-38): it resolves + gates the
// base, then builds buildDir FROM it and tags the result. Only Install routes its
// build through here, so `docker build` appears in exactly one place and the gate
// can never be bypassed. The run paths never build — create/recreate, chat, and
// work/dispatch all run the image Install already produced.
func (c *Colima) dockerBuild(buildDir, tag string, base backend.BaseSpec) (resolvedBase string, err error) {
	if err := c.preflight(); err != nil {
		return "", err
	}
	resolvedBase, err = c.resolveBase(base)
	if err != nil {
		return "", err
	}
	if err := c.r.Run("docker", dargs("build", "--build-arg", "BASE="+resolvedBase, "-t", tag, buildDir)...); err != nil {
		return "", err
	}
	return resolvedBase, nil
}

// Install builds + gates + tags a kit's hardened image and reports the result —
// the only build path (COV-38). Run commands consume the tagged image; the base
// is resolved and the provenance gate runs exactly here.
func (c *Colima) Install(ctx backend.InstallContext) (backend.InstalledImage, error) {
	img := image(ctx.Kit)
	base, err := c.dockerBuild(ctx.BuildDir, img, ctx.Base)
	if err != nil {
		return backend.InstalledImage{}, err
	}
	return backend.InstalledImage{Ref: img, BaseDigest: base}, nil
}

// Create runs a pre-built image (COV-38): `docker run` only. The image comes
// from install.json (via ctx.Image) — Create never builds, resolves a base, or
// runs the gate; those live in Install.
func (c *Colima) Create(ctx backend.CreateContext) (backend.Instance, error) {
	if err := c.preflight(); err != nil {
		return backend.Instance{}, err
	}
	img := ctx.Image
	ws := ctx.Name + "-workspace:/home/agent/workspace"
	if ctx.Workspace.Mode == backend.Shared {
		ws = ctx.Workspace.HostPath + ":/home/agent/workspace"
	}
	if err := c.r.Run("docker", dargs("run", "-d",
		"--name", ctx.Name,
		"--init",
		"--cap-add=NET_ADMIN",
		"--dns", "1.1.1.1",
		"-p", "127.0.0.1::2222",
		"-v", ctx.Name+"-state:/agent-data",
		"-v", ws,
		img,
	)...); err != nil {
		return backend.Instance{}, err
	}
	return backend.Instance{
		Backend:   "colima",
		Container: ctx.Name,
		Image:     img,
		Workspace: ctx.Workspace,
	}, nil
}

func (c *Colima) Dial(name string) (backend.Endpoint, func(), error) {
	if err := c.preflight(); err != nil {
		return backend.Endpoint{}, func() {}, err
	}
	out, err := c.r.Output("docker", dargs("port", name, "2222")...)
	if err != nil {
		return backend.Endpoint{}, func() {}, err
	}
	hostport := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]) // "127.0.0.1:49153"
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return backend.Endpoint{}, func() {}, fmt.Errorf("colima: cannot parse docker port output %q", out)
	}
	port, err := strconv.Atoi(hostport[i+1:])
	if err != nil {
		return backend.Endpoint{}, func() {}, fmt.Errorf("colima: bad port in %q: %w", hostport, err)
	}
	return backend.Endpoint{Host: hostport[:i], Port: port, User: "agent"}, func() {}, nil
}

func (c *Colima) Destroy(inst backend.Instance, keepVolumes bool) error {
	if err := c.preflight(); err != nil {
		return err
	}
	// Force-remove the container (the rm itself never carries -v; volumes are
	// named, not anonymous, so -v wouldn't touch them anyway). recreate passes
	// keepVolumes=true so /agent-data (saved login) and the workspace survive.
	if err := c.r.Run("docker", dargs("rm", "-f", inst.Container)...); err != nil {
		return err
	}
	// A real destroy purges the instance's named volumes now that the container
	// (their only user) is gone. Best-effort: `-workspace` is absent for a shared
	// (bind-mount) workspace, so a missing volume must not fail the teardown.
	if !keepVolumes {
		_ = c.r.Run("docker", dargs("volume", "rm", "-f", inst.Container+"-state", inst.Container+"-workspace")...)
	}
	// The image is deliberately NOT removed: it is an `install` artifact (COV-38),
	// not a per-create build. create/recreate/work consume it without rebuilding,
	// so removing it here would break `recreate` (destroy→create finds no image)
	// and leave a still-present install.json pointing at a deleted image (COV-63).
	// Image lifecycle belongs to `install` (a re-install overwrites the tag).
	return nil
}

// RemoveImage removes a kit's compiled image (`docker rmi`) — the inverse of
// Install and the sole image-teardown path (COV-64). It is invoked only by
// `at-cove uninstall`; the container lifecycle (Destroy/recreate) never removes
// the image, which is an `install` artifact (COV-63). The command layer treats a
// failure here as best-effort (the image may already be gone), so RemoveImage
// simply reports what `docker rmi` did.
func (c *Colima) RemoveImage(image string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	return c.r.Run("docker", dargs("rmi", image)...)
}

func (c *Colima) GetStatus(name string) (backend.State, error) {
	if err := c.preflight(); err != nil {
		return backend.StateAbsent, err
	}
	out, err := c.r.Output("docker", dargs("inspect", "-f", "{{.State.Running}}", name)...)
	if err != nil {
		return backend.StateAbsent, nil // no such container
	}
	if strings.TrimSpace(out) == "true" {
		return backend.StateRunning, nil
	}
	return backend.StateStopped, nil
}
