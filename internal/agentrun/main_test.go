package agentrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain points the default stream log at a per-run temp file, so a test that
// builds a Workload without StreamLogPath never appends fixture lines to the
// real /agent-data/agent-stream.jsonl — which, when the suite runs inside a
// studio, is that studio's own stream log.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agentrun-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentrun tests:", err)
		os.Exit(1)
	}
	defaultStreamLogPath = filepath.Join(dir, "agent-stream.jsonl")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// A Workload with no StreamLogPath writes to defaultStreamLogPath, which the
// tests redirect away from the production path.
func TestDefaultStreamLogRedirectedInTests(t *testing.T) {
	if defaultStreamLogPath == streamLogPath {
		t.Fatalf("tests would write the real stream log %s", streamLogPath)
	}
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	f := &fakeSpawner{}
	f.proc = scriptedProc{wait: func() error {
		_, err := f.stdout.Write([]byte("{\"type\":\"guard\"}\n"))
		return err
	}}
	w := New(Config{WorkDir: dir, Prompt: "p", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f}, nil)
	if err := w.Run(context.Background(), &recordHandle{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(defaultStreamLogPath)
	if err != nil {
		t.Fatalf("redirected stream log not written: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("redirected stream log is empty")
	}
}
