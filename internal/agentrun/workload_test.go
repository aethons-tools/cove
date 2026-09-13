package agentrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
)

// recordHandle records the activities the workload reports.
type recordHandle struct{ got []covemaster.Activity }

func (h *recordHandle) Report(a covemaster.Activity) { h.got = append(h.got, a) }

// scriptedProc runs a closure as its Wait.
type scriptedProc struct{ wait func() error }

func (p scriptedProc) Wait() error { return p.wait() }

type fakeSpawner struct {
	bin, dir string
	args     []string
	proc     Process
	err      error
}

func (f *fakeSpawner) Spawn(ctx context.Context, bin string, args []string, dir string) (Process, error) {
	f.bin, f.args, f.dir = bin, args, dir
	if f.err != nil {
		return nil, f.err
	}
	return f.proc, nil
}

func writeResult(t *testing.T, dir, body string) {
	t.Helper()
	atTask := filepath.Join(dir, ".at-task")
	if err := os.MkdirAll(atTask, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(atTask, "worker-result.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newWL(t *testing.T, dir string, f *fakeSpawner) (*Workload, *recordHandle) {
	t.Helper()
	w := New(Config{WorkDir: dir, Prompt: "do the thing", Spawner: f}, nil)
	return w, &recordHandle{}
}

func TestRunOK(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatalf("Run: want nil, got %v", err)
	}
	if len(h.got) != 1 || h.got[0] != covemaster.Running {
		t.Fatalf("activities: want [Running], got %v", h.got)
	}
}

func TestRunNeedsInput(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"needs-input":{"doing":"x","blocker":"y","need":"z","tried":"w"}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatalf("Run: want nil, got %v", err)
	}
	want := []covemaster.Activity{covemaster.Running, covemaster.Waiting}
	if len(h.got) != 2 || h.got[0] != want[0] || h.got[1] != want[1] {
		t.Fatalf("activities: want [Running Waiting], got %v", h.got)
	}
}

func TestRunErrorResult(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"error":{"message":"boom"}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	err := w.Run(context.Background(), h)
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	if len(h.got) != 1 || h.got[0] != covemaster.Running {
		t.Fatalf("activities: want [Running], got %v", h.got)
	}
}

func TestRunNoResultFile(t *testing.T) {
	dir := t.TempDir() // no .at-task/worker-result.json
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err == nil {
		t.Fatal("Run: want error for missing result, got nil")
	}
	_ = h
}

func TestRunUnparseableResult(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{}}`) // no variant set → Active() errors
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err == nil {
		t.Fatal("Run: want error for empty status, got nil")
	}
	_ = h
}

func TestRunTeardownCancel(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`) // present, but must NOT be consulted
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { <-ctx.Done(); return ctx.Err() }}}
	w, h := newWL(t, dir, f)
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx, h) }()
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run: want ctx error on teardown, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if len(h.got) != 1 || h.got[0] != covemaster.Running {
		t.Fatalf("activities: want [Running] (no Waiting from the ok file), got %v", h.got)
	}
	w.Control(covemaster.Control{Kind: covemaster.Teardown}) // must not panic
}

func TestRunSpawnArgs(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if f.bin != "claude" {
		t.Errorf("bin: want claude, got %q", f.bin)
	}
	want := []string{"-p", "--dangerously-skip-permissions", "--mcp-config", "/etc/claude-code/mcp.json", "--strict-mcp-config", "do the thing"}
	if len(f.args) != len(want) {
		t.Fatalf("args: want %v, got %v", want, f.args)
	}
	for i := range want {
		if f.args[i] != want[i] {
			t.Errorf("args[%d]: want %q, got %q (full: want %v, got %v)", i, want[i], f.args[i], want, f.args)
		}
	}
	if f.dir != dir {
		t.Errorf("dir: want %q, got %q", dir, f.dir)
	}
}

func TestRunSpawnFailure(t *testing.T) {
	dir := t.TempDir()
	f := &fakeSpawner{err: os.ErrPermission}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err == nil {
		t.Fatal("Run: want spawn error, got nil")
	}
	if len(h.got) != 0 {
		t.Fatalf("activities: want none (spawn failed before Running), got %v", h.got)
	}
}

func TestControlWakeNoop(t *testing.T) {
	w := New(Config{WorkDir: t.TempDir(), Prompt: "x", Spawner: &fakeSpawner{}}, nil)
	w.Control(covemaster.Control{Kind: covemaster.Wake}) // must not panic
}
