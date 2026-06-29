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
	if _, err := c.r.Output("docker", dargs("info")...); err != nil {
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

func (c *Colima) Create(ctx backend.CreateContext) (backend.Instance, error) {
	if err := c.preflight(); err != nil {
		return backend.Instance{}, err
	}
	kit := ctx.Kit
	if kit == "" {
		kit = ctx.Name
	}
	img := image(kit)
	if err := c.r.Run("docker", dargs("build", "-t", img, ctx.BuildDir)...); err != nil {
		return backend.Instance{}, err
	}
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

func (c *Colima) Destroy(inst backend.Instance) error {
	if err := c.preflight(); err != nil {
		return err
	}
	// Force-remove the container (never -v: named volumes survive). Then remove
	// the image so the namespace stays clean — best-effort, since the build cache
	// is separate and a missing image shouldn't fail the teardown.
	if err := c.r.Run("docker", dargs("rm", "-f", inst.Container)...); err != nil {
		return err
	}
	if inst.Image != "" {
		_ = c.r.Run("docker", dargs("rmi", inst.Image)...)
	}
	return nil
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
