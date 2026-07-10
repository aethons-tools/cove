package scheduler

import (
	"strings"
	"testing"
)

func TestAssembleBrief(t *testing.T) {
	iss := Issue{Identifier: "AET-42", Title: "Do the thing", Description: "Make it work.", Class: "implement"}
	comments := []Comment{{Author: "brent", Body: "please prioritize"}, {Author: "agent", Body: "on it"}}
	got := assembleBrief(iss, comments)

	for _, want := range []string{
		"# AET-42 — Do the thing",
		"**Class:** implement",
		"Make it work.",
		"**brent:** please prioritize",
		"**agent:** on it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q\n---\n%s", want, got)
		}
	}
}

func TestAssembleBriefNoComments(t *testing.T) {
	got := assembleBrief(Issue{Identifier: "AET-1", Title: "T", Class: "plan"}, nil)
	if strings.Contains(got, "## Thread") {
		t.Errorf("expected no Thread section when there are no comments:\n%s", got)
	}
}
