package sshconfig

import (
	"strings"
	"testing"
)

func TestUpsertManagedBlock_AppendThenIdempotent(t *testing.T) {
	base := "Host existing\n    HostName 10.0.0.1\n"
	block := "Host cove-demo\n    User agent\n"
	once := UpsertManagedBlock(base, "cove-demo", block)
	if !strings.Contains(once, "# >>> at-cove managed block: cove-demo >>>") ||
		!strings.Contains(once, "# <<< at-cove managed block: cove-demo <<<") {
		t.Fatalf("markers missing:\n%s", once)
	}
	if !strings.HasPrefix(once, base) {
		t.Fatalf("existing content not preserved:\n%s", once)
	}
	twice := UpsertManagedBlock(once, "cove-demo", block)
	if once != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestUpsertManagedBlock_Replace(t *testing.T) {
	first := UpsertManagedBlock("", "cove-demo", "Host cove-demo\n    Port 111\n")
	second := UpsertManagedBlock(first, "cove-demo", "Host cove-demo\n    Port 222\n")
	if strings.Contains(second, "Port 111") {
		t.Fatalf("old block not replaced:\n%s", second)
	}
	if strings.Count(second, "at-cove managed block: cove-demo >>>") != 1 {
		t.Fatalf("duplicate block:\n%s", second)
	}
	if !strings.Contains(second, "Port 222") {
		t.Fatalf("new block missing:\n%s", second)
	}
}
