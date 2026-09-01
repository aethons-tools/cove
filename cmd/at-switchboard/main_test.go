package main

import (
	"strings"
	"testing"
)

func TestRun_MissingToken_IsUsageError(t *testing.T) {
	var out, errb strings.Builder
	env := func(k string) string { return "" } // nothing set
	code := run([]string{"run"}, env, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "DISCORD_BOT_TOKEN") {
		t.Fatalf("stderr = %q", errb.String())
	}
	if strings.Contains(errb.String(), "secret") { // never leak values
		t.Fatalf("stderr should not mention secret values: %q", errb.String())
	}
}
