//go:build integration

package switchboard

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

// TestClaudeAgentLive drives a REAL headless `claude` through two turns in a
// fresh workspace and asserts the agent honored the turn-result contract — the
// claude side of the conductor that hermetic tests can only fake. It locks in
// the three assumptions validated by hand via `at-switchboard once`:
//   - `claude -p --continue` runs headless on a FIRST turn (no prior session),
//   - the agent has headless write permission and emits a parseable TurnResult,
//   - `--continue` carries session state into a SECOND turn (continuity).
//
// It uses the ambient `claude` (must be signed in — run inside a logged-in
// sandbox) and your Claude session, so it is gated behind SWITCHBOARD_IT=1 in
// addition to the `integration` tag.
//
// Run: SWITCHBOARD_IT=1 go test -tags integration ./internal/switchboard/ -run TestClaudeAgentLive -v
func TestClaudeAgentLive(t *testing.T) {
	if os.Getenv("SWITCHBOARD_IT") == "" {
		t.Skip("set SWITCHBOARD_IT=1 to run the live claude turn (uses the ambient, signed-in claude session)")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("no `claude` on PATH")
	}

	// Fresh workspace: turn 1 exercises `--continue` with NO prior session.
	dir := t.TempDir()
	a := NewClaudeAgent(runner.OS{}, dir)
	ctx := context.Background()

	const codeword = "BANANA47"
	res1, err := a.RunTurn(ctx, RenderInbox([]Message{{
		Channel: "demo", Author: "tracer",
		Content: "Remember this codeword for later: " + codeword + ". Acknowledge briefly, then wait for the next message.",
	}}))
	if err != nil {
		t.Fatalf("turn 1 (first-turn --continue) failed: %v", err)
	}
	assertValidTurn(t, "turn 1", res1)

	// Turn 2 (same workspace) must resume turn 1's session via --continue.
	res2, err := a.RunTurn(ctx, RenderInbox([]Message{{
		Channel: "demo", Author: "tracer",
		Content: "What was the codeword I gave you? Reply with just the codeword, then wait.",
	}}))
	if err != nil {
		t.Fatalf("turn 2 (--continue continuity) failed: %v", err)
	}
	assertValidTurn(t, "turn 2", res2)

	var joined strings.Builder
	for _, m := range res2.Messages {
		joined.WriteString(m.Content)
		joined.WriteByte('\n')
	}
	if !strings.Contains(strings.ToUpper(joined.String()), codeword) {
		t.Fatalf("turn 2 did not recall the codeword %q via --continue; got messages: %q", codeword, joined.String())
	}
}

func assertValidTurn(t *testing.T, label string, r TurnResult) {
	t.Helper()
	switch r.Action {
	case ActionExit, ActionWait, ActionGet:
	default:
		t.Fatalf("%s: invalid action %q (want exit|wait|get)", label, r.Action)
	}
}
