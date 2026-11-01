package agentrun

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps every test off the real /agent-data: Run clears or writes
// ContextDir and may write the conversation and clear markers, which default
// there. It also fails the run if any test touched the harness's real generated files —
// in a cove those belong to the live agent, and deleting them breaks every
// later episode (see guardRealPaths).
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "agentrun-context-")
	if err != nil {
		panic(err)
	}
	defaultContextDir = filepath.Join(tmp, "context")
	// Every session writes its conversation marker once the agent replies;
	// the shared default lives in a directory that doesn't exist, so it is
	// never written and one test's conversation can't make the next resume.
	// Tests of resuming set their own marker (conversationWL).
	defaultConversationMarker = filepath.Join(tmp, "no-such-dir", ".cove-conversation")
	defaultClearMarker = filepath.Join(tmp, ".cove-clear-pending")
	check := guardRealPaths(claudeSettingsPath, claudeMCPConfigPath)
	code := m.Run()
	if err := check(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		code = 1
	}
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// guardRealPaths snapshots paths and returns a check that reports (and undoes)
// any change since: a test must give the harness its own paths (testClaude),
// never use the zero value's production ones.
func guardRealPaths(paths ...string) func() error {
	type snap struct {
		exists bool
		data   []byte
	}
	read := func(p string) snap {
		b, err := os.ReadFile(p)
		return snap{exists: !errors.Is(err, fs.ErrNotExist), data: b}
	}
	before := map[string]snap{}
	for _, p := range paths {
		before[p] = read(p)
	}
	return func() error {
		var errs []error
		for _, p := range paths {
			was, now := before[p], read(p)
			if was.exists == now.exists && bytes.Equal(was.data, now.data) {
				continue
			}
			errs = append(errs, fmt.Errorf("a test changed the real harness file %s (give the harness test paths)", p))
			if was.exists {
				_ = os.WriteFile(p, was.data, 0o600)
			} else {
				_ = os.Remove(p)
			}
		}
		return errors.Join(errs...)
	}
}
