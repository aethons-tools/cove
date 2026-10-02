package sessionctx

import (
	"strings"
	"testing"
)

func TestBoilerplatePerKind(t *testing.T) {
	cases := []struct {
		f       SessionFacts
		want    []string
		mustNot []string
	}{
		{SessionFacts{Kind: KindStanding, Name: "alice-bot", Project: "acme", Role: "reviewer"},
			[]string{`the standing session "alice-bot" for role reviewer in project acme`, "always pass `to`", "until an operator removes you"},
			[]string{"owner"}},
		{SessionFacts{Kind: KindPersonal, Owner: "alice", Project: "acme", Role: "pair"},
			[]string{"a personal session for alice", "`send` without `to` reaches alice", "until alice releases"},
			nil},
		{SessionFacts{Kind: "", Project: "acme", Role: "worker", Unit: "AET-9"},
			[]string{"an ephemeral worker session for role worker in project acme", "without `to` posts to your ticket"},
			[]string{"owner"}},
	}
	for _, c := range cases {
		l := Boilerplate(c.f)
		for _, w := range c.want {
			if !strings.Contains(l.Core, w) {
				t.Errorf("%s: missing %q in\n%s", c.f.Kind, w, l.Core)
			}
		}
		for _, w := range c.mustNot {
			if strings.Contains(l.Core, w) {
				t.Errorf("%s: must not contain %q", c.f.Kind, w)
			}
		}
		if len(l.Core) > BudgetBoilerplate {
			t.Errorf("%s: boilerplate %d bytes > budget %d", c.f.Kind, len(l.Core), BudgetBoilerplate)
		}
		for _, common := range []string{"one `claude -p` run", "Background processes", "allow-listed", "/home/agent/workspace", "/agent-data/reference/sandbox-hardening-limits.md"} {
			if !strings.Contains(l.Core, common) {
				t.Errorf("%s: missing common %q", c.f.Kind, common)
			}
		}
		if len(l.Leaves) != 1 || l.Leaves[0].Name != "changing-the-kit.md" || !strings.Contains(l.Leaves[0].Body, "at-jam kit push") {
			t.Errorf("%s: want the changing-the-kit leaf, got %+v", c.f.Kind, l.Leaves)
		}
	}
}

func TestCompiledBundleNeverTruncatesBoilerplate(t *testing.T) {
	for _, k := range []string{KindEphemeral, KindPersonal, KindStanding} {
		b := Compile(Inputs{Session: SessionFacts{Kind: k, Name: strings.Repeat("n", 64), Owner: strings.Repeat("o", 64), Project: strings.Repeat("p", 64), Role: strings.Repeat("r", 64)}})
		if len(b.Warnings) != 0 {
			t.Errorf("%s: %v", k, b.Warnings)
		}
	}
}

// An ephemeral session is not resident: a turn that ends without a
// worker-result fails it, so its boilerplate carries that contract and never
// says that ending a turn is how to wait.
func TestBoilerplateEphemeralResultContract(t *testing.T) {
	l := Boilerplate(SessionFacts{Kind: KindEphemeral, Project: "p", Role: "r", Unit: "AET-9"})
	for _, want := range []string{".at-task/worker-result.json", `{"status":{"ok":{}}}`, `"needs-input"`, `{"status":{"error":`} {
		if !strings.Contains(l.Core, want) {
			t.Errorf("ephemeral boilerplate missing %q:\n%s", want, l.Core)
		}
	}
	if strings.Contains(l.Core, "Ending your turn is how you wait") {
		t.Error("ephemeral sessions must not be told that ending the turn waits")
	}
	for _, k := range []string{KindPersonal, KindStanding} {
		if c := Boilerplate(SessionFacts{Kind: k, Owner: "o", Name: "n"}).Core; !strings.Contains(c, "Ending your turn is how you wait") || strings.Contains(c, "worker-result.json") {
			t.Errorf("%s: resident turn model wrong:\n%s", k, c)
		}
	}
}

// Without a unit there is no ticket: `send` without `to` is rejected.
func TestBoilerplateEphemeralWithoutUnitNeedsTo(t *testing.T) {
	c := Boilerplate(SessionFacts{Kind: KindEphemeral, Project: "p", Role: "r"}).Core
	if strings.Contains(c, "ticket") || !strings.Contains(c, "always pass `to`") {
		t.Fatalf("unit-less ephemeral must be told to always pass `to`:\n%s", c)
	}
}
