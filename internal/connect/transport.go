package connect

import (
	"sort"

	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/sshargs"
)

// Transport injects env and launches claude interactively over SSH.
type Transport interface {
	Launch(t sshargs.Target, env map[string]string) error
}

// SendEnv forwards secrets via ssh SendEnv: values live only in the ssh child's
// environment (never on argv, never on disk). The VM's sshd AcceptEnv allowlist
// (shipped in the hardening layer) accepts them.
type SendEnv struct{ R runner.Runner }

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
	args := sshargs.InteractiveSendEnv(t, names, "exec claude")
	return s.R.RunEnv(childEnv, "ssh", args...)
}
