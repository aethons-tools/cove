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
	if k, err := sk.Base.Kind(); err != nil || k != BaseDefault {
		t.Fatalf("default kit base must be empty (→ blessed default); kind=%v err=%v", k, err)
	}
}

func TestCeilingExcludesTrailingDotFQDNs(t *testing.T) {
	// Trailing-dot FQDNs (e.g., api.anthropic.com.) must be excluded from the ceiling.
	ceiling, _ := Ceiling([]string{"anthropic.com.", "api.anthropic.com.", "github.com"})
	if slices.Contains(ceiling, "anthropic.com.") || slices.Contains(ceiling, "api.anthropic.com.") {
		t.Fatalf("ceiling must exclude trailing-dot Anthropic entries: %v", ceiling)
	}
	if !slices.Contains(ceiling, "github.com") {
		t.Fatalf("ceiling must include non-Anthropic entry github.com: %v", ceiling)
	}
	// Negative cases: notanthropic.com should NOT be excluded
	_, excluded2 := Ceiling([]string{"notanthropic.com"})
	if len(excluded2) != 0 {
		t.Fatalf("notanthropic.com should not be excluded: excluded=%v", excluded2)
	}
	// Uppercase variants should be excluded (case-insensitive)
	_, excluded3 := Ceiling([]string{"ANTHROPIC.COM", "claude.com"})
	if !slices.Contains(excluded3, "ANTHROPIC.COM") || !slices.Contains(excluded3, "claude.com") {
		t.Fatalf("uppercase ANTHROPIC.COM and claude.com must be excluded: excluded=%v", excluded3)
	}
}
