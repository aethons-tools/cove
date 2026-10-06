package agentrun

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps every test off the real /agent-data: Run clears or writes
// ContextDir and may write the conversation marker, which default there.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "agentrun-context-")
	if err != nil {
		panic(err)
	}
	defaultContextDir = filepath.Join(tmp, "context")
	defaultConversationMarker = filepath.Join(tmp, ".cove-conversation")
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}
