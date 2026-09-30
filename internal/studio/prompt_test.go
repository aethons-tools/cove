// internal/studio/prompt_test.go
package studio

import (
	"strings"
	"testing"
)

func TestComposePromptOrdersAndOmitsEmpty(t *testing.T) {
	got := ComposePrompt(PromptLayers{Kit: "KIT", Launch: "LAUNCH"})
	if !strings.HasPrefix(got, JamBoilerplate) {
		t.Fatal("boilerplate must come first")
	}
	ik, il := strings.Index(got, "KIT"), strings.Index(got, "LAUNCH")
	if ik == -1 || il == -1 || ik > il {
		t.Fatalf("Kit must precede Launch; got %q", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("empty layers (Project/Role) must be omitted, no blank runs; got %q", got)
	}
}

func TestComposePromptAllLayers(t *testing.T) {
	got := ComposePrompt(PromptLayers{Kit: "K", Project: "P", Role: "R", Launch: "L"})
	order := []string{JamBoilerplate, "K", "P", "R", "L"}
	last := -1
	for _, s := range order {
		i := strings.Index(got, s)
		if i <= last {
			t.Fatalf("layer %q out of order in %q", s, got)
		}
		last = i
	}
}
