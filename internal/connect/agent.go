package connect

import (
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// RunAgent runs Claude Code headlessly in the workspace — `claude -p <prompt>`
// — with secrets injected (including ANTHROPIC_API_KEY, which authenticates the
// unattended run; no interactive login is attempted). The prompt is shell-quoted
// so it reaches claude as a single argument. Output streams live; a non-zero
// claude exit is returned so the caller can record the run's outcome.
func RunAgent(r runner.Runner, tgt sshargs.Target, env map[string]string, prompt string) error {
	return runInjected(r, tgt, env, "agent", "cd "+workspaceDir+" && exec claude -p "+shellQuote(prompt))
}
