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

type Colima struct{ r runner.Runner }

func New(r runner.Runner) backend.Backend { return &Colima{r: r} }

func image(name string) string { return "cove/" + name }

func (c *Colima) Create(ctx backend.CreateContext) error {
	if err := c.r.Run("docker", "build", "-t", image(ctx.Name), ctx.BuildDir); err != nil {
		return err
	}
	ws := ctx.Name + "-workspace:/home/agent/workspace"
	if ctx.Workspace.Mode == backend.Shared {
		ws = ctx.Workspace.HostPath + ":/home/agent/workspace"
	}
	return c.r.Run("docker", "run", "-d",
		"--name", ctx.Name,
		"--init",
		"--cap-add=NET_ADMIN",
		"--dns", "1.1.1.1",
		"-p", "127.0.0.1::2222",
		"-v", ctx.Name+"-state:/agent-data",
		"-v", ws,
		image(ctx.Name),
	)
}

func (c *Colima) Dial(name string) (backend.Endpoint, func(), error) {
	out, err := c.r.Output("docker", "port", name, "2222")
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

func (c *Colima) Destroy(name string) error {
	return c.r.Run("docker", "rm", "-f", name)
}

func (c *Colima) GetStatus(name string) (backend.State, error) {
	out, err := c.r.Output("docker", "inspect", "-f", "{{.State.Running}}", name)
	if err != nil {
		return backend.StateAbsent, nil // no such container
	}
	if strings.TrimSpace(out) == "true" {
		return backend.StateRunning, nil
	}
	return backend.StateStopped, nil
}
