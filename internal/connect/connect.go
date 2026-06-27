package connect

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aethons-tools/at-sbx/internal/backend"
	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/secret"
	"github.com/aethons-tools/at-sbx/internal/sshargs"
)

const (
	// authProbe reports whether the sandbox is already logged in, using claude's
	// own validated status check (`claude auth status` exits 0 when logged in,
	// non-zero otherwise). This beats statting a credentials file: it validates
	// (catches expired creds) and is not coupled to where creds are stored. The
	// wrapper always exits 0 and reports state on stdout, so a non-zero ssh exit
	// still means the connection itself failed, not "not logged in".
	authProbe  = `if claude auth status >/dev/null 2>&1; then echo atsbx-authed; else echo atsbx-noauth; fi`
	authedMark = "atsbx-authed"
	// loginCmd is the interactive subscription/OAuth login. --claudeai is claude's
	// default; it is stated explicitly to match managed forceLoginMethod=claudeai.
	loginCmd = "claude auth login --claudeai"
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

	if err := ensureAuthenticated(r, tgt); err != nil {
		return err
	}
	return t.Launch(tgt, env)
}

// ensureAuthenticated runs the interactive `claude auth login` the first time a
// sandbox is used, and is a no-op afterwards. It probes for stored credentials
// over a non-interactive ssh and only launches the OAuth flow when none exist,
// so the login dance happens once per sandbox — credentials persist on the
// /agent-data volume across reconnects. No secrets are injected for the login
// itself.
func ensureAuthenticated(r runner.Runner, tgt sshargs.Target) error {
	out, err := r.Output("ssh", append(sshargs.Base(tgt), authProbe)...)
	if err != nil {
		return fmt.Errorf("checking sandbox auth status: %w", err)
	}
	if strings.Contains(out, authedMark) {
		return nil
	}
	// First use: run the subscription OAuth login interactively (PTY). nil stdin
	// makes RunStdin attach the real terminal so the user can paste the code.
	if err := r.RunStdin(nil, "ssh", sshargs.Interactive(tgt, loginCmd)...); err != nil {
		return fmt.Errorf("interactive login (%s): %w", loginCmd, err)
	}
	return nil
}
