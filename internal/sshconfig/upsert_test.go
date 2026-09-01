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

func TestUpsertManagedBlock_MultiAliasCoexistence(t *testing.T) {
	cfg := UpsertManagedBlock("", "cove-a", "Host cove-a\n    Port 111\n")
	cfg = UpsertManagedBlock(cfg, "cove-b", "Host cove-b\n    Port 222\n")
	bBlock := extractBlock(t, cfg, "cove-b")

	// Re-upsert cove-a with new content; cove-b's block must be untouched.
	cfg = UpsertManagedBlock(cfg, "cove-a", "Host cove-a\n    Port 333\n")

	if strings.Contains(cfg, "Port 111") {
		t.Fatalf("old cove-a block not replaced:\n%s", cfg)
	}
	if !strings.Contains(cfg, "Port 333") {
		t.Fatalf("new cove-a block missing:\n%s", cfg)
	}
	if got := extractBlock(t, cfg, "cove-b"); got != bBlock {
		t.Fatalf("cove-b block was not left byte-for-byte intact:\n--- before ---\n%s\n--- after ---\n%s", bBlock, got)
	}
	for _, alias := range []string{"cove-a", "cove-b"} {
		if n := strings.Count(cfg, "at-cove managed block: "+alias+" >>>"); n != 1 {
			t.Fatalf("expected exactly one block for %s, got %d:\n%s", alias, n, cfg)
		}
	}
}

// extractBlock returns the alias's managed block (markers inclusive) from cfg.
func extractBlock(t *testing.T, cfg, alias string) string {
	t.Helper()
	begin := "# >>> at-cove managed block: " + alias + " >>>"
	end := "# <<< at-cove managed block: " + alias + " <<<"
	bi := strings.Index(cfg, begin)
	if bi < 0 {
		t.Fatalf("block for %s not found:\n%s", alias, cfg)
	}
	rel := strings.Index(cfg[bi:], end)
	if rel < 0 {
		t.Fatalf("block end for %s not found:\n%s", alias, cfg)
	}
	ei := bi + rel + len(end)
	return cfg[bi:ei]
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
