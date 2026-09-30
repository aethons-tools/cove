package studio

import (
	"slices"
	"testing"
)

func TestCeilingExcludesAnthropic(t *testing.T) {
	ceiling, excluded := Ceiling([]string{"github.com", ".anthropic.com", "claude.ai", "pkg.go.dev", "api.anthropic.com"})
	if slices.Contains(ceiling, ".anthropic.com") || slices.Contains(ceiling, "claude.ai") || slices.Contains(ceiling, "api.anthropic.com") {
		t.Fatalf("ceiling must exclude Anthropic roots: %v", ceiling)
	}
	if !slices.Equal(ceiling, []string{"github.com", "pkg.go.dev"}) {
		t.Fatalf("ceiling = %v, want sorted non-Anthropic entries", ceiling)
	}
	if !slices.Equal(excluded, []string{".anthropic.com", "api.anthropic.com", "claude.ai"}) {
		t.Fatalf("excluded = %v, want the sorted Anthropic entries", excluded)
	}
}

func TestDefaultStudioKitIsAnthropicFree(t *testing.T) {
	sk := DefaultStudioKit()
	if err := sk.Validate(); err != nil {
		t.Fatalf("default kit invalid: %v", err)
	}
	_, excluded := Ceiling(sk.Egress)
	if len(excluded) != 0 {
		t.Fatalf("default kit egress must already be Anthropic-free, excluded=%v", excluded)
	}
	if sk.Base.Ref != "" || sk.Base.Dockerfile != "" {
		t.Fatal("default kit base must be empty (→ blessed default)")
	}
}
