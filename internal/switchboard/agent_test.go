package switchboard

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

// containsArgs reports whether any recorded call invokes name with args as a
// contiguous subsequence of its argv (e.g. "sh -c '...claude -p --continue...'"
// records name=sh, so we look inside the shell command string too).
func containsArgs(calls []runner.Call, name string, args ...string) bool {
	for _, c := range calls {
		joined := c.Name
		for _, a := range c.Args {
			joined += " " + a
		}
		if containsSubsequence(joined, append([]string{name}, args...)) {
			return true
		}
	}
	return false
}

func containsSubsequence(haystack string, parts []string) bool {
	for _, p := range parts {
		idx := indexOf(haystack, p)
		if idx < 0 {
			return false
		}
		haystack = haystack[idx+len(p):]
	}
	return true
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// claudeStub wraps runner.Fake to simulate the one side effect a real `claude
// -p --continue` process has that runner.Fake cannot: writing the turn-result
// file. RunTurn truncates that file before invoking claude (so a stale result
// is never mistaken for a fresh one), which means a plain runner.Fake — which
// only records calls and cannot run side effects — can't stand in for a
// successful run: the file it would need to "produce" is gone by the time
// Run() fires. claudeStub writes the canned result at Run-time, then delegates
// to the embedded Fake so call recording (fake.Calls) still works for argv
// assertions.
type claudeStub struct {
	*runner.Fake
	resultPath string
	resultData []byte
}

func (s *claudeStub) Run(name string, args ...string) error {
	if err := os.WriteFile(s.resultPath, s.resultData, 0o644); err != nil {
		return err
	}
	return s.Fake.Run(name, args...)
}

func TestClaudeAgent_RunTurn_WritesInput_RunsClaude_ParsesResult(t *testing.T) {
	dir := t.TempDir()
	resultPath := filepath.Join(dir, ".switchboard", "turn-result.json")
	fake := &runner.Fake{}
	stub := &claudeStub{
		Fake:       fake,
		resultPath: resultPath,
		resultData: []byte(`{"messages":[{"channel":"cx","content":"done"}],"action":"wait"}`),
	}

	a := NewClaudeAgent(stub, dir)
	res, err := a.RunTurn(context.Background(), "New Discord messages:\n[#cx] brent: hi\n")
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionWait || len(res.Messages) != 1 || res.Messages[0].Content != "done" {
		t.Fatalf("res = %+v", res)
	}
	// input file was written
	in, _ := os.ReadFile(filepath.Join(dir, ".switchboard", "turn-input.txt"))
	if string(in) == "" {
		t.Fatal("turn input not written")
	}
	// claude was invoked with -p --continue
	if len(fake.Calls) == 0 || !containsArgs(fake.Calls, "claude", "-p", "--continue") {
		t.Fatalf("claude not invoked with -p --continue: %+v", fake.Calls)
	}
}

// TestClaudeAgent_RunTurn_TruncatesStaleResult guards against reading a
// previous turn's leftover result file when the claude invocation itself
// doesn't (in this hermetic test, can't) write a fresh one: RunTurn must
// remove any pre-existing result before running claude.
func TestClaudeAgent_RunTurn_ClearsStaleResultBeforeRun(t *testing.T) {
	dir := t.TempDir()
	resultPath := filepath.Join(dir, ".switchboard", "turn-result.json")
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, []byte(`{"messages":[],"action":"exit"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &runner.Fake{}
	a := NewClaudeAgent(fake, dir)
	// No result file is (re)written by the fake claude run, so RunTurn must
	// surface an error reading the truncated result rather than silently
	// returning the stale one.
	if _, err := a.RunTurn(context.Background(), "hi"); err == nil {
		t.Fatal("expected error reading missing result file, got nil")
	}
}
