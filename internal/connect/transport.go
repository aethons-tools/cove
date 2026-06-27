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

// launchCmd is the remote program a transport exec's. Empty means the agent
// (claude); `connect --raw` sets it to "bash" to drop into a debug shell with
// the same injected environment the agent would see.
func launchCmd(cmd string) string {
	if cmd == "" {
		return "claude"
	}
	return cmd
}

// SendEnv forwards secrets via ssh SendEnv: values live only in the ssh child's
// environment (never on argv, never on disk). The VM's sshd AcceptEnv allowlist
// (shipped in the hardening layer) accepts them.
type SendEnv struct {
	R   runner.Runner
	Cmd string // remote program to exec; "" => claude
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
	args := sshargs.InteractiveSendEnv(t, names, "exec "+launchCmd(s.Cmd))
	return s.R.RunEnv(childEnv, "ssh", args...)
}

// StdinScript is the spec's primary transport: it writes an env-export script
// into the VM's tmpfs (/dev/shm) over ssh stdin — so secret values never appear
// on argv — then opens an interactive ssh that sources the file, removes it, and
// exec's the program. The file lives only in tmpfs and is deleted before the
// shell hands off.
type StdinScript struct {
	R   runner.Runner
	Cmd string // remote program to exec; "" => claude
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
	remote := "set -a; . " + file + "; rm -f " + file + "; exec " + launchCmd(s.Cmd)
	runArgs := append([]string{"-tt"}, append(sshargs.Base(t), remote)...)
	return s.R.RunStdin(nil, "ssh", runArgs...)
}

// shellQuote single-quotes s for safe inclusion in a POSIX shell script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
