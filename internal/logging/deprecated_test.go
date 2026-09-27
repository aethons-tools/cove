package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestDeprecatedWarnsOncePerOldName(t *testing.T) {
	ResetDeprecations()
	t.Cleanup(ResetDeprecations)
	t.Setenv("AT_LOG_MODE", "")
	var errb bytes.Buffer
	Deprecated(&errb, "at-harbor", "at-jam")
	Deprecated(&errb, "at-harbor", "at-jam")
	got := errb.String()
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("want exactly one warning line, got %q", got)
	}
	for _, want := range []string{`"level":"WARN"`, `"old":"at-harbor"`, `"new":"at-jam"`, RenameDoc} {
		if !strings.Contains(got, want) {
			t.Fatalf("warning missing %q: %q", want, got)
		}
	}
	errb.Reset()
	Deprecated(&errb, "harbor:", "jam:")
	if !strings.Contains(errb.String(), `"old":"harbor:"`) {
		t.Fatalf("a different old name must still warn once; got %q", errb.String())
	}
}

func TestDeprecatedAttendedIsHumanText(t *testing.T) {
	ResetDeprecations()
	t.Cleanup(ResetDeprecations)
	t.Setenv("AT_LOG_MODE", "attended")
	var errb bytes.Buffer
	Deprecated(&errb, "AT_HARBOR_ADMIN_TOKEN", "AT_JAM_ADMIN_TOKEN")
	got := errb.String()
	if !strings.Contains(got, "level=WARN") || !strings.Contains(got, "AT_HARBOR_ADMIN_TOKEN") || !strings.Contains(got, "AT_JAM_ADMIN_TOKEN") {
		t.Fatalf("attended warning should be human text naming both; got %q", got)
	}
}
