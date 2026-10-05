package agentrun

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRunGateExitAndOutput(t *testing.T) {
	exit, to, out, tr := runGate(context.Background(), t.TempDir(), nil, `echo green; exit 0`, 5*time.Second)
	if exit != 0 || to || tr || string(out) != "green\n" {
		t.Fatalf("exit=%d to=%v tr=%v out=%q", exit, to, tr, out)
	}
	if exit, _, _, _ := runGate(context.Background(), t.TempDir(), nil, `exit 3`, 5*time.Second); exit != 3 {
		t.Fatalf("exit = %d, want 3", exit)
	}
	if exit, _, _, _ := runGate(context.Background(), t.TempDir(), nil, `definitely-not-a-command-xyz`, 5*time.Second); exit != 127 {
		t.Fatalf("missing command exit = %d, want 127", exit)
	}
}

func TestRunGateTimesOut(t *testing.T) {
	start := time.Now()
	exit, to, _, _ := runGate(context.Background(), t.TempDir(), nil, `sleep 30`, 200*time.Millisecond)
	if !to || exit != -1 || time.Since(start) > 5*time.Second {
		t.Fatalf("exit=%d timedOut=%v after %v", exit, to, time.Since(start))
	}
}

func TestRunGateCapsOutput(t *testing.T) {
	_, _, out, tr := runGate(context.Background(), t.TempDir(), nil, `head -c 20000 /dev/urandom`, 5*time.Second)
	if !tr || len(out) > gateOutputCap+64 || !utf8.Valid(out) || !strings.HasSuffix(string(out), "[output truncated]") {
		t.Fatalf("truncated=%v len=%d valid=%v", tr, len(out), utf8.Valid(out))
	}
}

func TestRunGateUsesDirAndEnv(t *testing.T) {
	dir := t.TempDir()
	_, _, out, _ := runGate(context.Background(), dir, []string{"GATE_X=42", "PATH=" + os.Getenv("PATH")}, `pwd; echo $GATE_X`, 5*time.Second)
	if got := strings.Fields(string(out)); len(got) != 2 || got[0] != dir || got[1] != "42" {
		t.Fatalf("out=%q", out)
	}
}
