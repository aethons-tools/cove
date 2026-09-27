// Package launcher is the real harbor.Launcher: it raises/tears down/probes a
// managed cove on a Colima backend and bootstraps cove-master over SSH. It lives
// outside internal/harbor core (which stays backend/connect-free) and is wired
// from cmd/at-harbor.
package launcher

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/connect"
	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/naming"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

const Label = "harbor.cove"

// Backend is the backend surface the launcher needs: the ephemeral run/dial/remove
// ops plus GetStatus for probing. The Colima backend value satisfies both.
type Backend interface {
	backend.DispatchOps // RunEphemeral, Dial, RemoveContainer, ScavengeLabeled
	GetStatus(container string) (backend.State, error)
}

// Config configures the Colima launcher.
type Config struct {
	Ops           Backend
	Runner        runner.Runner
	Image         string
	ImageDigest   string
	HarborHost    string
	RuntimeAddr   string
	IdentityFile  string
	KnownHostsDir string
	DNS           []string
	Docker        bool
	WorkDir       string              // AT_COVE_WORKDIR; default /home/agent/workspace
	Log           *slog.Logger        // nil → discard
	sleep         func(time.Duration) // wait-for-sshd backoff; nil → time.Sleep
}

type Launcher struct{ cfg Config }

var _ harbor.Launcher = (*Launcher)(nil)

func New(cfg Config) *Launcher {
	if cfg.sleep == nil {
		cfg.sleep = time.Sleep
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "/home/agent/workspace"
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Launcher{cfg: cfg}
}

func (l *Launcher) Raise(ctx context.Context, spec harbor.RaiseSpec, creds harbor.LaunchCreds) (string, error) {
	name := naming.CoveContainer(spec.ActorID)
	if _, err := l.cfg.Ops.RunEphemeral(l.cfg.Image, l.cfg.ImageDigest, name, Label, l.cfg.DNS, []string{l.cfg.HarborHost}, l.cfg.Docker); err != nil {
		return "", fmt.Errorf("raise %s: run: %w", name, err)
	}
	// From here, clean up the container on any failure so a failed raise leaks nothing.
	launch := func() error {
		ep, cleanup, err := l.cfg.Ops.Dial(name)
		if err != nil {
			return fmt.Errorf("dial: %w", err)
		}
		defer cleanup()
		tgt := sshargs.Target{
			Host: ep.Host, User: ep.User, Port: ep.Port,
			IdentityFile:   l.cfg.IdentityFile,
			KnownHostsFile: filepath.Join(l.cfg.KnownHostsDir, name),
		}
		if err := l.waitForSSH(tgt); err != nil {
			return fmt.Errorf("wait for sshd: %w", err)
		}
		// The role's egress policy lands before cove-master (and so the agent)
		// starts, so the agent never runs under the wider kit default.
		if err := l.applyRoleEgress(name, spec); err != nil {
			return err
		}
		return connect.LaunchCoveMaster(l.cfg.Runner, connect.CoveMasterOptions{
			Target: tgt, HarborHost: l.cfg.HarborHost, RuntimeAddr: l.cfg.RuntimeAddr,
			IdentityToken: creds.IdentityToken, LaunchSecret: creds.LaunchSecret,
			WorkDir: l.cfg.WorkDir, Prompt: spec.Prompt,
			// A personal session is a long-lived conversation: its agent stays
			// resident, waiting for its owner's reply after every turn.
			Resident: harbor.IsResident(spec.SessionKind),
		})
	}
	if err := launch(); err != nil {
		if rmErr := l.cfg.Ops.RemoveContainer(name); rmErr != nil {
			l.cfg.Log.Warn("raise cleanup: remove container failed", "name", name, "error", rmErr)
		}
		return "", fmt.Errorf("raise %s: %w", name, err)
	}
	return name, nil
}

// applyRoleEgress pushes spec's role egress policy into the raised container via
// the backend's privileged RoleEgress op (the sealed in-box helper enforces the
// kit's ceiling). A nil policy is the kit default: nothing to apply. Fail closed:
// a policy the backend can't apply fails the raise rather than falling back to
// the wider kit default.
func (l *Launcher) applyRoleEgress(name string, spec harbor.RaiseSpec) error {
	if spec.Egress == nil {
		return nil
	}
	project := spec.Project
	if project == "" {
		project = harbor.DefaultProject
	}
	re, ok := l.cfg.Ops.(backend.RoleEgress)
	if !ok {
		return fmt.Errorf("backend does not support role egress (required for role %s/%s)", project, spec.Role)
	}
	if err := re.ApplyRoleEgress(name, spec.Egress.Domains); err != nil {
		return fmt.Errorf("apply role egress: %w", err)
	}
	l.cfg.Log.Info("role egress applied", "id", spec.ActorID, "project", project, "role", spec.Role, "domains", len(spec.Egress.Domains))
	return nil
}

func (l *Launcher) Teardown(ctx context.Context, inst harbor.Instance) error {
	return l.cfg.Ops.RemoveContainer(inst.Location)
}

func (l *Launcher) Probe(ctx context.Context, inst harbor.Instance) (harbor.Liveness, error) {
	st, err := l.cfg.Ops.GetStatus(inst.Location)
	if err != nil {
		return harbor.LivenessUnknown, nil // transient — don't reap (COV-151)
	}
	switch st {
	case backend.StateRunning:
		return harbor.LivenessAlive, nil
	default:
		return harbor.LivenessDead, nil
	}
}

func (l *Launcher) Pause(ctx context.Context, inst harbor.Instance) error {
	return l.cfg.Ops.Pause(inst.Location)
}

func (l *Launcher) Unpause(ctx context.Context, inst harbor.Instance) error {
	return l.cfg.Ops.Unpause(inst.Location)
}

// waitForSSH polls sshd with a trivial command until it answers or attempts run out.
func (l *Launcher) waitForSSH(tgt sshargs.Target) error {
	const attempts = 30
	probe := append(sshargs.Base(tgt), "true")
	var err error
	for i := 0; i < attempts; i++ {
		if err = l.cfg.Runner.Run("ssh", probe...); err == nil {
			return nil
		}
		if i < attempts-1 {
			l.cfg.sleep(time.Second)
		}
	}
	return err
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
