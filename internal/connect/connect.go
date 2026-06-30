package connect

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aethons-tools/cove/internal/awake"
	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/sshargs"
)

const (
	// authProbe reports whether the sandbox is already logged in, using claude's
	// own validated status check (`claude auth status` exits 0 when logged in,
	// non-zero otherwise). This beats statting a credentials file: it validates
	// (catches expired creds) and is not coupled to where creds are stored. The
	// wrapper always exits 0 and reports state on stdout, so a non-zero ssh exit
	// still means the connection itself failed, not "not logged in".
	authProbe  = `if claude auth status >/dev/null 2>&1; then echo cove-authed; else echo cove-noauth; fi`
	authedMark = "cove-authed"
	// loginCmd is the interactive subscription/OAuth login. --claudeai is claude's
	// default; it is stated explicitly to match managed forceLoginMethod=claudeai.
	loginCmd = "claude auth login --claudeai"
	// credsVMPath is where claude stores the subscription OAuth credentials inside
	// the VM (CLAUDE_CONFIG_DIR=/agent-data). cove copies this file to and from a
	// host-side copy so one login is reusable across sandboxes.
	credsVMPath = "/agent-data/.credentials.json"
)

// Options configures a connect. Container/Secrets come from the recorded state,
// not the kit config.
type Options struct {
	Container     string // backend container handle (from state)
	Secrets       []secret.Spec
	IdentityFile  string
	KnownHostsDir string    // per-sandbox known_hosts files live here
	SkipAuth      bool      // skip the interactive `claude auth login` step (--no-auth)
	Stderr        io.Writer // where the host-sleep warning is written; nil => os.Stderr
	Setup         string    // command to seed an empty isolated workspace; "" => no setup (also blanked for --ws)
	// CredentialsFile is the host path of the saved subscription login, shared
	// across sandboxes. When set (and auth is not skipped), connect seeds it into
	// the VM before the auth probe and saves the VM's copy back to it after a
	// login or whenever a session rotates the token. "" disables the feature.
	CredentialsFile string
}

// Connect resolves secrets, verifies the VM is running, dials it, and launches
// claude with the secrets injected. Secret resolution happens before any SSH so
// a failure aborts cleanly (fail closed).
func Connect(b backend.Backend, r runner.Runner, t Transport, aw awake.Inhibitor, o Options) error {
	env, err := secret.Resolve(r, o.Secrets)
	if err != nil {
		return err
	}

	stderr := o.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	state, err := b.GetStatus(o.Container)
	if err != nil {
		return err
	}
	if state != backend.StateRunning {
		return fmt.Errorf("sandbox %q is not running; run `at-cove create` or start the VM first", o.Container)
	}

	ep, cleanup, err := b.Dial(o.Container)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := os.MkdirAll(o.KnownHostsDir, 0o700); err != nil {
		return err
	}
	knownHosts := filepath.Join(o.KnownHostsDir, o.Container)

	tgt := sshargs.Target{
		Host:           ep.Host,
		User:           ep.User,
		Port:           ep.Port,
		IdentityFile:   o.IdentityFile,
		KnownHostsFile: knownHosts,
	}

	if !o.SkipAuth {
		if err := ensureAuthenticated(r, tgt, o.CredentialsFile, stderr); err != nil {
			return err
		}
	}

	if err := RunSetup(r, tgt, env, o.Setup); err != nil {
		return err
	}

	// Keep the host awake for the session only: idle work happens between here
	// and Launch returning. A failed assertion is a warning, never fatal.
	// The deferred release runs when Launch returns, covering the whole
	// session: Launch opens an interactive PTY ssh, so a Ctrl-C goes to the
	// remote tty rather than this process — Launch only returns when the
	// session itself ends, which is exactly when the assertion should drop.
	if release, err := aw.Inhibit(); err != nil {
		fmt.Fprintf(stderr, "at-cove: warning: could not prevent host sleep: %v\n", err)
	} else {
		defer release()
	}
	launchErr := t.Launch(tgt, env)

	// A long session may have refreshed (and possibly rotated) the credentials;
	// save the latest copy so the next sandbox seeds valid tokens. Best-effort —
	// never let a save failure mask the session's own outcome.
	if !o.SkipAuth {
		if err := saveCredentials(r, tgt, o.CredentialsFile); err != nil {
			fmt.Fprintf(stderr, "at-cove: warning: could not save credentials to %s: %v\n", o.CredentialsFile, err)
		}
	}
	return launchErr
}

// ensureAuthenticated makes the sandbox logged in, reusing a saved login when
// possible. It first seeds any host-saved credentials into the VM, then probes
// `claude auth status` over a non-interactive ssh; only when that fails (no
// creds, or expired beyond refresh) does it launch the interactive OAuth flow.
// A fresh login is saved straight back to the host. No secrets are injected for
// the login itself. credsFile == "" disables the host-shared-credentials path,
// preserving the original per-sandbox-only behaviour.
func ensureAuthenticated(r runner.Runner, tgt sshargs.Target, credsFile string, stderr io.Writer) error {
	if err := seedCredentials(r, tgt, credsFile); err != nil {
		return err
	}
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
	// Persist the freshly minted credentials so other sandboxes can reuse them.
	if err := saveCredentials(r, tgt, credsFile); err != nil {
		fmt.Fprintf(stderr, "at-cove: warning: could not save credentials to %s: %v\n", credsFile, err)
	}
	return nil
}

// seedCredentials copies the host-saved login into the VM before the auth probe,
// so a login obtained on one sandbox validates on the next. It is a no-op when
// the feature is disabled (credsFile == "") or the host has no saved login yet
// (the first ever login). The bytes arrive via stdin under umask 077, never on
// argv, matching how secrets are injected.
func seedCredentials(r runner.Runner, tgt sshargs.Target, credsFile string) error {
	if credsFile == "" {
		return nil
	}
	data, err := os.ReadFile(credsFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading saved credentials %s: %w", credsFile, err)
	}
	writeArgs := append(sshargs.Base(tgt), "umask 077; cat > "+credsVMPath)
	if err := r.RunStdin(bytes.NewReader(data), "ssh", writeArgs...); err != nil {
		return fmt.Errorf("seeding credentials into sandbox: %w", err)
	}
	return nil
}

// saveCredentials copies the VM's live login back to the host file (mode 0600),
// so a fresh login or a refresh-rotated token is reused by other sandboxes. It
// is a no-op when disabled, when the VM has no readable credentials, or when the
// content already matches the host copy — so it is cheap to call after every
// session. Failures are returned for the caller to surface as a warning, never
// fatal.
func saveCredentials(r runner.Runner, tgt sshargs.Target, credsFile string) error {
	if credsFile == "" {
		return nil
	}
	out, err := r.Output("ssh", append(sshargs.Base(tgt), "cat "+credsVMPath+" 2>/dev/null")...)
	if err != nil || strings.TrimSpace(out) == "" {
		return nil // nothing to save: no login yet, or unreadable
	}
	if prev, err := os.ReadFile(credsFile); err == nil && string(prev) == out {
		return nil // unchanged
	}
	if err := os.MkdirAll(filepath.Dir(credsFile), 0o700); err != nil {
		return err
	}
	// Write atomically: other sandboxes seed from this same file concurrently, so
	// a torn read would seed invalid JSON and force a needless re-login.
	tmp := credsFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, credsFile); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
