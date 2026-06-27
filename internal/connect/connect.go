package connect

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aethons-tools/at-sbx/internal/backend"
	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/secret"
	"github.com/aethons-tools/at-sbx/internal/sshargs"
)

// Options configures a connect.
type Options struct {
	Name          string
	Secrets       []secret.Spec
	IdentityFile  string
	KnownHostsDir string // per-sandbox known_hosts files live here
}

// Connect resolves secrets, verifies the VM is running, dials it, and launches
// claude with the secrets injected. Secret resolution happens before any SSH so
// a failure aborts cleanly (fail closed).
func Connect(b backend.Backend, r runner.Runner, t Transport, o Options) error {
	env, err := secret.Resolve(r, o.Secrets)
	if err != nil {
		return err
	}

	state, err := b.GetStatus(o.Name)
	if err != nil {
		return err
	}
	if state != backend.StateRunning {
		return fmt.Errorf("sandbox %q is not running; run `atsbx create` or start the VM first", o.Name)
	}

	ep, cleanup, err := b.Dial(o.Name)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := os.MkdirAll(o.KnownHostsDir, 0o700); err != nil {
		return err
	}
	knownHosts := filepath.Join(o.KnownHostsDir, o.Name)

	tgt := sshargs.Target{
		Host:           ep.Host,
		User:           ep.User,
		Port:           ep.Port,
		IdentityFile:   o.IdentityFile,
		KnownHostsFile: knownHosts,
	}
	return t.Launch(tgt, env)
}
