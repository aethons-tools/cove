package connect

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// loopSentinel marks that a loop workspace has been fully seeded. It lives on the
// persistent state volume (not in the workspace), so it survives reconnects but
// is removed by ResetLoopWorkspace for a fresh-workspace re-seed.
const loopSentinel = "/agent-data/.cove-loop-seeded"

// SeedLoopWorkspace populates an unattended loop workspace exactly once: when the
// sentinel is absent it clears the workspace, runs setupCmd with secrets injected,
// and writes the sentinel only on success. A present sentinel means a prior seed
// completed, so it returns immediately. Clearing before seeding makes a partial or
// interrupted seed self-heal on the next attempt (no sentinel => reclear + retry).
// An empty setupCmd is a no-op.
func SeedLoopWorkspace(r runner.Runner, tgt sshargs.Target, env map[string]string, setupCmd string) error {
	if setupCmd == "" {
		return nil
	}
	probe := "[ -e " + loopSentinel + " ] && echo cove-seeded || echo cove-unseeded"
	out, err := r.Output("ssh", append(sshargs.Base(tgt), probe)...)
	if err != nil {
		return fmt.Errorf("checking seed sentinel: %w", err)
	}
	if strings.Contains(out, "cove-seeded") {
		return nil
	}
	tail := "find " + workspaceDir + " -mindepth 1 -delete; cd " + workspaceDir +
		" && " + setupCmd + " && touch " + loopSentinel
	return runInjected(r, tgt, env, "seed", tail)
}

// ResetLoopWorkspace removes the seed sentinel and clears the workspace so the
// next SeedLoopWorkspace re-seeds — used for a loop's fresh-workspace mode.
func ResetLoopWorkspace(r runner.Runner, tgt sshargs.Target) error {
	cmd := "rm -f " + loopSentinel + "; find " + workspaceDir + " -mindepth 1 -delete"
	if err := r.RunStdin(strings.NewReader(""), "ssh", append(sshargs.Base(tgt), cmd)...); err != nil {
		return fmt.Errorf("resetting loop workspace: %w", err)
	}
	return nil
}
