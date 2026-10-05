package agentrun

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps every test off the real /agent-data/context and the image's
// seed: Run clears or writes ContextDir, which defaults there, and restores
// SANDBOX.md from the seed.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "agentrun-context-")
	if err != nil {
		panic(err)
	}
	defaultContextDir = filepath.Join(tmp, "context")
	sandboxDocSeed = filepath.Join(tmp, "seed", "SANDBOX.md") // absent unless a test writes it
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}
