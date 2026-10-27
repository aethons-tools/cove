package studioagentbin

import (
	"strings"
	"testing"
)

func TestBinaryUnstagedErrorsActionably(t *testing.T) {
	// A fresh checkout stages only bin/README + bin/.gitignore, so the real
	// per-arch binaries are absent and Binary must error actionably (never panic).
	_, err := Binary("amd64")
	if err == nil {
		t.Skip("jam-studio-agent binary is staged in this build; nothing to assert")
	}
	if !strings.Contains(err.Error(), "jam-studio-agent") {
		t.Fatalf("error should name jam-studio-agent, got: %v", err)
	}
}

func TestBinFSNonNil(t *testing.T) {
	if BinFS() == nil {
		t.Fatal("BinFS() returned nil")
	}
}
