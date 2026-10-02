package agentrun

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps every test off the real /agent-data/context: Run clears or
// writes ContextDir, which defaults there.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "agentrun-context-")
	if err != nil {
		panic(err)
	}
	defaultContextDir = filepath.Join(tmp, "context")
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}
