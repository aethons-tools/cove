package switchboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
)

// RunTurn failure modes, distinguishable with errors.Is so callers (notably the
// `at-switchboard once` tracer) can diagnose accurately rather than guessing.
var (
	// ErrClaudeRun: the `claude` process itself failed to run to completion
	// (e.g. not logged in, quota/auth error, crash) — no turn was produced.
	ErrClaudeRun = errors.New("switchboard: claude did not run")
	// ErrNoResult: claude ran but wrote no .switchboard/turn-result.json.
	ErrNoResult = errors.New("switchboard: agent wrote no result file")
	// ErrBadResult: the result file exists but did not parse as a TurnResult.
	ErrBadResult = errors.New("switchboard: result file did not parse")
)

// ClaudeAgent runs one turn by shelling a headless `claude` with the turn input
// and reading back the agent's JSON result file (mirrors the worker-result.json
// contract in internal/dispatchrun).
type ClaudeAgent struct {
	r       runner.Runner
	workDir string
}

// NewClaudeAgent builds an Agent backed by the local `claude` CLI.
func NewClaudeAgent(r runner.Runner, workDir string) *ClaudeAgent {
	return &ClaudeAgent{r: r, workDir: workDir}
}

// turnProtocol is prepended to every turn's input so the headless agent knows
// the result-file contract it must honor.
const turnProtocol = `You are a Discord teammate. Below are new messages (each tagged [#channel] author). ` +
	`Do the work, then write your reply to .switchboard/turn-result.json as EXACTLY: ` +
	`{"messages":[{"channel":"<id>","content":"<text>"}],"action":"exit|wait|get"}. ` +
	`Use "wait" to sleep until someone writes, "get" to check again immediately, "exit" to stop.`

// RunTurn writes the turn input, truncates any stale result, shells a headless
// `claude -p --continue` in workDir (so --continue resumes this sandbox's
// rolling session — the one externally-dependent, unverified-in-hermetic-tests
// assumption in this adapter), then reads and parses the result file it wrote.
func (a *ClaudeAgent) RunTurn(ctx context.Context, input string) (TurnResult, error) {
	dir := filepath.Join(a.workDir, ".switchboard")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return TurnResult{}, fmt.Errorf("switchboard: prepare turn dir: %w", err)
	}
	inputPath := filepath.Join(dir, "turn-input.txt")
	resultPath := filepath.Join(dir, "turn-result.json")

	// Clear any stale result BEFORE writing input / running claude, so the file
	// is present afterward only if THIS turn's claude wrote it — which lets
	// callers trust its presence as a signal.
	if err := os.Remove(resultPath); err != nil && !os.IsNotExist(err) {
		return TurnResult{}, fmt.Errorf("switchboard: clear stale turn result: %w", err)
	}
	if err := os.WriteFile(inputPath, []byte(turnProtocol+"\n\n"+input), 0o644); err != nil {
		return TurnResult{}, fmt.Errorf("switchboard: write turn input: %w", err)
	}

	// claude -p --continue "$(cat <inputPath>)" — run in workDir so --continue
	// resumes this sandbox's rolling session.
	cmd := fmt.Sprintf("cd %s && claude -p --continue \"$(cat %s)\"", shellQuote(a.workDir), shellQuote(inputPath))
	if err := a.r.Run("sh", "-c", cmd); err != nil {
		return TurnResult{}, fmt.Errorf("%w: %v", ErrClaudeRun, err)
	}

	data, err := os.ReadFile(resultPath)
	if err != nil {
		return TurnResult{}, fmt.Errorf("%w: %v", ErrNoResult, err)
	}
	res, err := ParseTurnResult(data)
	if err != nil {
		return TurnResult{}, fmt.Errorf("%w: %v", ErrBadResult, err)
	}
	return res, nil
}

// shellQuote POSIX single-quotes s for use in a /bin/sh -c command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
