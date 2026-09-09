package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/switchboard"
)

// fakeAgent stands in for a real ClaudeAgent: it optionally writes a canned
// result file (as a real agent would) and returns a canned result/error.
type fakeAgent struct {
	workDir   string
	writeFile string
	res       switchboard.TurnResult
	err       error
}

func (f *fakeAgent) RunTurn(_ context.Context, _ string) (switchboard.TurnResult, error) {
	if f.writeFile != "" {
		d := filepath.Join(f.workDir, ".switchboard")
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "turn-result.json"), []byte(f.writeFile), 0o644)
	}
	return f.res, f.err
}

func TestDoOnce_Success_PrintsActionAndMessages(t *testing.T) {
	dir := t.TempDir()
	fa := &fakeAgent{
		workDir:   dir,
		writeFile: `{"messages":[{"channel":"demo","content":"on it"}],"action":"wait"}`,
		res:       switchboard.TurnResult{Messages: []switchboard.Outbound{{Channel: "demo", Content: "on it"}}, Action: switchboard.ActionWait},
	}
	var out strings.Builder
	code := doOnce(fa, "New Discord messages:\n[#demo] brent: hi\n", dir, &out)
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out.String())
	}
	s := out.String()
	for _, want := range []string{"[#demo] brent: hi", "on it", "wait", "OK", "turn-result.json"} {
		if !strings.Contains(s, want) {
			t.Fatalf("output missing %q:\n%s", want, s)
		}
	}
}

func TestDoOnce_ClaudeRunFailure_NotMisdiagnosedAsProse(t *testing.T) {
	dir := t.TempDir()
	// claude itself exits non-zero (e.g. not logged in) — no result file. This
	// is the case the old file-existence heuristic mislabeled as "answered in
	// prose"; the verdict must instead point at claude/auth.
	fa := &fakeAgent{workDir: dir, err: fmt.Errorf("%w: exit status 1", switchboard.ErrClaudeRun)}
	var out strings.Builder
	code := doOnce(fa, "input", dir, &out)
	if code == 0 {
		t.Fatal("expected non-zero exit when claude did not run")
	}
	s := out.String()
	if !strings.Contains(s, "FAIL") || !strings.Contains(s, "did not run") || !strings.Contains(s, "auth status") {
		t.Fatalf("expected a claude-did-not-run / auth diagnosis, not a prose one:\n%s", s)
	}
	if strings.Contains(s, "prose") {
		t.Fatalf("must NOT mislabel a claude-run failure as answering in prose:\n%s", s)
	}
}

func TestDoOnce_NoResultFile_DiagnosesTheGap(t *testing.T) {
	dir := t.TempDir()
	fa := &fakeAgent{workDir: dir, err: fmt.Errorf("%w: open .../turn-result.json: no such file or directory", switchboard.ErrNoResult)}
	var out strings.Builder
	code := doOnce(fa, "input", dir, &out)
	if code == 0 {
		t.Fatal("expected non-zero exit when the agent wrote no result file")
	}
	s := out.String()
	if !strings.Contains(s, "FAIL") || !strings.Contains(s, "wrote no result file") {
		t.Fatalf("expected a 'wrote no result file' diagnosis:\n%s", s)
	}
}

func TestDoOnce_UnparseableResultFile_DistinctDiagnosis(t *testing.T) {
	dir := t.TempDir()
	fa := &fakeAgent{
		workDir:   dir,
		writeFile: "sorry, I can't do that", // agent replied in prose, not JSON
		err:       fmt.Errorf("%w: invalid character 's'", switchboard.ErrBadResult),
	}
	var out strings.Builder
	code := doOnce(fa, "input", dir, &out)
	if code == 0 {
		t.Fatal("expected non-zero exit for an unparseable result file")
	}
	s := out.String()
	if !strings.Contains(s, "FAIL") || !strings.Contains(s, "did not parse") {
		t.Fatalf("expected a 'wrote a result file but it did not parse' diagnosis:\n%s", s)
	}
	if !strings.Contains(s, "sorry, I can't do that") {
		t.Fatalf("expected the raw result file contents to be echoed:\n%s", s)
	}
}
