package connect

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// Transport injects env and launches the remote program interactively over SSH.
type Transport interface {
	Launch(t sshargs.Target, env map[string]string) error
}

// workspaceDir is where the kit's workspace volume is mounted inside the VM (see
// the colima backend). Sessions start here so the agent — and the raw debug
// shell — land in the project, not the home directory.
const workspaceDir = "/home/agent/workspace"

// resumeLaunch resumes the most-recent Claude Code session when a transcript
// exists, else starts a fresh one — making a first-ever connect deterministic.
// CLAUDE_CONFIG_DIR is baked into the sandbox image (=/agent-data); the :-
// fallback keeps detection working if it is absent from this shell.
//
// Two deliberate shapes:
//   - The for/`[ -e ]` form is immune to `nullglob`: with no matching session
//     the loop either skips its body (nullglob on) or tests the literal,
//     unexpanded pattern and fails the -e check (nullglob off). Both fall
//     through to a fresh `exec claude`, so detection never wrongly resumes and
//     never blocks.
//   - It globs for ANY session under projects/*/*.jsonl rather than replicating
//     Claude Code's internal per-directory folder hash. `claude --continue` is
//     itself cwd-scoped, and the sandbox's only project dir is the workspace
//     (sessions always start from /home/agent/workspace) — so "any session
//     exists" stands in for "the workspace has a session". Do not broaden the
//     glob past that workspace-is-the-only-project assumption.
//
// The brace group keeps the enclosing `cd <workspace> &&` guarding the whole
// thing: a cd failure must abort, not silently run claude in the home dir.
const resumeLaunch = `{ for f in "${CLAUDE_CONFIG_DIR:-/agent-data}"/projects/*/*.jsonl; do [ -e "$f" ] && exec claude --continue; done; exec claude; }`

// launchProgram returns the remote shell tail that exec's the session program.
// Empty cmd means the agent (claude); a non-empty cmd (e.g. "bash" from --raw)
// is a whole-program replacement that never resumes. When resume is set, claude
// reopens the most-recent session for the workspace dir if one exists, else
// starts fresh — making a first-ever connect deterministic.
func launchProgram(cmd string, resume bool) string {
	if cmd != "" {
		return "exec " + cmd
	}
	if !resume {
		return "exec claude"
	}
	return resumeLaunch
}

// remoteExec is the tail of a transport's remote command: cd into the workspace,
// then exec the launch program. Using && (not ;) fails loudly if the workspace
// mount is missing rather than silently dropping the session in the home dir.
func remoteExec(cmd string, resume bool) string {
	return "cd " + workspaceDir + " && " + launchProgram(cmd, resume)
}

// SendEnv forwards secrets via ssh SendEnv: values live only in the ssh child's
// environment (never on argv, never on disk). The VM's sshd AcceptEnv allowlist
// (shipped in the hardening layer) accepts them.
type SendEnv struct {
	R      runner.Runner
	Cmd    string // remote program to exec; "" => claude
	Resume bool   // when launching claude, resume the most-recent session if one exists
}

func (s SendEnv) Launch(t sshargs.Target, env map[string]string) error {
	names := make([]string, 0, len(env))
	childEnv := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names) // deterministic argv/env
	for _, k := range names {
		childEnv = append(childEnv, k+"="+env[k])
	}
	args := sshargs.InteractiveSendEnv(t, names, remoteExec(s.Cmd, s.Resume))
	return s.R.RunEnv(childEnv, "ssh", args...)
}

// StdinScript is the spec's primary transport: it writes an env-export script
// into the VM's tmpfs (/dev/shm) over ssh stdin — so secret values never appear
// on argv — then opens an interactive ssh that sources the file, removes it, and
// exec's the program. The file lives only in tmpfs and is deleted before the
// shell hands off.
type StdinScript struct {
	R      runner.Runner
	Cmd    string // remote program to exec; "" => claude
	Resume bool   // when launching claude, resume the most-recent session if one exists
}

func (s StdinScript) Launch(t sshargs.Target, env map[string]string) error {
	// host+port keeps the path distinct across concurrently-connected sandboxes
	// (Colima maps every sandbox to 127.0.0.1 on a different port).
	file := fmt.Sprintf("/dev/shm/cove-env-%s-%d", t.Host, t.Port)
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	var script strings.Builder
	for _, k := range names {
		fmt.Fprintf(&script, "export %s=%s\n", k, shellQuote(env[k]))
	}

	// 1) write the script into tmpfs (values arrive via stdin, never on argv).
	writeArgs := append(sshargs.Base(t), "umask 077; cat > "+file)
	if err := s.R.RunStdin(strings.NewReader(script.String()), "ssh", writeArgs...); err != nil {
		return err
	}
	// 2) interactive: source the file, remove it, then launch the program.
	remote := "set -a; . " + file + "; rm -f " + file + "; " + remoteExec(s.Cmd, s.Resume)
	runArgs := append([]string{"-tt"}, append(sshargs.Base(t), remote)...)
	return s.R.RunStdin(nil, "ssh", runArgs...)
}

// shellQuote single-quotes s for safe inclusion in a POSIX shell script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
