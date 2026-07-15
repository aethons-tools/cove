package logging

import (
	"strings"
	"testing"
)

func TestScrubMasksSecretValues(t *testing.T) {
	got := Scrub("Authorization: Bearer sk-ant-oat01-XYZ done", "sk-ant-oat01-XYZ", "")
	if strings.Contains(got, "sk-ant-oat01-XYZ") {
		t.Fatalf("secret value leaked: %q", got)
	}
	if !strings.Contains(got, "«redacted»") {
		t.Fatalf("expected redaction marker; got %q", got)
	}
}
