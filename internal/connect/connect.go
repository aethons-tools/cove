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
	// loginCmd is the interactive subscription/OAuth login. `--claudeai` selects
	// the subscription method explicitly — managed settings no longer force a
	// login method (auth is env-driven so dispatched `work` can use an API key),
	// so connect states its choice here rather than relying on a forced default.
	loginCmd = "claude auth login --claudeai"
	// credsVMPath is where claude stores the subscription OAuth credentials inside
	// the VM (CLAUDE_CONFIG_DIR=/agent-data). cove copies this file to and from a
	// host-side copy so one login is reusable across sandboxes.
	credsVMPath = "/agent-data/.credentials.json"
	// collaboratorVMPath is where chat writes the selected collaborator's role
	// prompt; the seeded CLAUDE.md @-includes it. Not a secret (source-controlled
	// kit content), but written the same in-memory-over-ssh way.
	collaboratorVMPath = "/agent-data/COLLABORATOR.md"
	// workspaceProbe reports whether the isolated workspace already holds a
	// checkout, so the first-session clone runs exactly once for the VM's lifetime
	// (a reconnect reuses the existing, possibly in-progress, workspace). It always
	// exits 0 and reports on stdout, so a non-zero ssh exit still means the
	// connection failed, not "empty workspace" (mirrors authProbe).
	workspaceProbe      = `if [ -e ` + workspaceDir + `/.git ]; then echo ` + workspaceClonedMark + `; else echo cove-ws-empty; fi`
	workspaceClonedMark = "cove-ws-cloned"
	// cloneEnvVMPath lives on tmpfs (/dev/shm): the git token is staged into this
	// env file in memory (never on disk or argv), then sourced into at-task's
	// process env so its own env-only askpass (worker/git.go) supplies credentials.
	// Connect owns no askpass of its own — at-task is the single git-with-token path.
	cloneEnvVMPath = "/dev/shm/cove-clone-env"
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
	// CredentialsFile is the host path of the saved subscription login, shared
	// across sandboxes. When set (and auth is not skipped), connect seeds it into
	// the VM before the auth probe and saves the VM's copy back to it after a
	// login or whenever a session rotates the token. "" disables the feature.
	CredentialsFile string
	// CollaboratorPrompt is the selected collaborator's role prompt, written to
	// collaboratorVMPath before launch so the session's CLAUDE.md includes it.
	// Empty writes a benign placeholder (clears any prior role).
	CollaboratorPrompt string
	// WorkspaceClone, when non-nil, clones the repo's default branch into the VM's
	// isolated workspace on first session start — once for the VM's lifetime (a
	// reconnect that finds an existing checkout reuses it). nil means never clone
	// (a shared share-repo-dir workspace, or source-control/token not configured).
	WorkspaceClone *WorkspaceClone
}

// WorkspaceClone describes a first-session clone of the target repo into the
// isolated workspace. The Token is resolved in memory just for the clone and is
// never added to the session env handed to the agent (the code-host air-gap).
type WorkspaceClone struct {
	RepoURL string      // https clone URL, e.g. https://github.com/owner/name.git
	Branch  string      // default branch to check out
	Token   secret.Spec // code-host token; flows into the VM via an env-only askpass
}

// Connect resolves secrets, verifies the VM is running, dials it, and launches
// claude with the secrets injected. Secret resolution happens before any SSH so
// a failure aborts cleanly (fail closed).
func Connect(b backend.Backend, r runner.Runner, t Transport, aw awake.Inhibitor, o Options) error {
	env, err := secret.Resolve(r, nil, o.Secrets)
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

	if err := writeCollaboratorRole(r, tgt, o.CollaboratorPrompt); err != nil {
		return err
	}

	// Populate an empty isolated workspace with the repo's default branch, once
	// for the VM's lifetime, so the checkout is ready when the chat opens. A
	// configured clone that fails is a hard error (fail closed) — it aborts before
	// the session launches rather than dropping the collaborator into an empty dir.
	if err := ensureWorkspace(r, tgt, o.WorkspaceClone); err != nil {
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

// writeCollaboratorRole writes the role prompt (or a placeholder when empty) to
// the VM's COLLABORATOR.md over ssh, so the session's CLAUDE.md include resolves
// to the active role.
func writeCollaboratorRole(r runner.Runner, tgt sshargs.Target, prompt string) error {
	body := prompt
	if body == "" {
		body = "# (no collaborator role active)\n"
	}
	args := append(sshargs.Base(tgt), "umask 077; cat > "+collaboratorVMPath)
	if err := r.RunStdin(bytes.NewReader([]byte(body)), "ssh", args...); err != nil {
		return fmt.Errorf("writing collaborator role: %w", err)
	}
	return nil
}

// ensureWorkspace populates the isolated workspace with the repo's default branch
// the first time a session starts, and never again for the VM's lifetime. It is
// a no-op when w is nil (a shared workspace, or source-control/token not
// configured). It probes for an existing checkout first (host-observable, so the
// once-per-lifetime guard is testable and a reconnect reuses in-progress work),
// then stages the token into at-task's VM env in memory and runs at-task's
// task-less clone verb — the single git-with-token path (worker/git.go's env-only
// askpass). The token never reaches argv, disk, or the session env. A clone
// failure is returned so the caller fails closed rather than launching an empty
// session.
func ensureWorkspace(r runner.Runner, tgt sshargs.Target, w *WorkspaceClone) error {
	if w == nil {
		return nil
	}
	out, err := r.Output("ssh", append(sshargs.Base(tgt), workspaceProbe)...)
	if err != nil {
		return fmt.Errorf("checking workspace state: %w", err)
	}
	if strings.Contains(out, workspaceClonedMark) {
		return nil // already cloned: reuse the existing workspace, never re-clone
	}
	// Resolve the token on the host, in memory only — it is deliberately NOT part
	// of o.Secrets, so it never enters the agent's session env.
	env, err := secret.Resolve(r, nil, []secret.Spec{w.Token})
	if err != nil {
		return err
	}
	// Stage the token into a tmpfs env file over ssh stdin (never on argv). The
	// clone command sources it into at-task's process env, so at-task's own
	// env-only askpass (internal/dispatch/worker/git.go) supplies the credentials —
	// connect implements no askpass of its own.
	tokenScript := fmt.Sprintf("export AT_TASK_GIT_TOKEN=%s\n", shellQuote(env[w.Token.Name]))
	if err := writeVM(r, tgt, tokenScript, cloneEnvVMPath); err != nil {
		return fmt.Errorf("staging clone token: %w", err)
	}
	if _, err := r.Output("ssh", append(sshargs.Base(tgt), cloneCmd(w.RepoURL, w.Branch))...); err != nil {
		return fmt.Errorf("cloning %s into workspace: %w", w.RepoURL, err)
	}
	return nil
}

// cloneCmd builds the remote shell that populates the workspace: source the token
// env from tmpfs into at-task's process env then scrub the file, and run at-task's
// task-less clone-workspace verb, which clones the default branch into the
// workspace with its own env-only askpass. The token never appears on this command
// line — only the tmpfs path does.
func cloneCmd(url, branch string) string {
	return "set -a; . " + cloneEnvVMPath + "; set +a; rm -f " + cloneEnvVMPath + "; " +
		"at-task clone-workspace --repo " + shellQuote(url) + " --branch " + shellQuote(branch) + " " + workspaceDir
}

// writeVM writes data to vmPath in the VM over ssh stdin (mode 077, never on
// argv), the same in-memory transport used for the collaborator role and creds.
func writeVM(r runner.Runner, tgt sshargs.Target, data, vmPath string) error {
	args := append(sshargs.Base(tgt), "umask 077; cat > "+vmPath)
	return r.RunStdin(strings.NewReader(data), "ssh", args...)
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
