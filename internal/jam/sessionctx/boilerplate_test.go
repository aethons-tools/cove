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
			[]string{`the standing session "alice-bot" for role reviewer in project acme`, "`send` without `to` posts to your own channel", "until an operator removes you"},
			[]string{"owner"}},
		{SessionFacts{Kind: KindPersonal, Owner: "alice", Project: "acme", Role: "pair"},
			[]string{"a personal session for alice", "`send` without `to` posts to your own channel, which alice is in", "until alice releases"},
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
		for _, common := range []string{"one `claude -p` run", "Background processes", "allow-listed", "/home/agent/workspace", "boilerplate/hardening-limits.md"} {
			if !strings.Contains(l.Core, common) {
				t.Errorf("%s: missing common %q", c.f.Kind, common)
			}
		}
		if len(l.Leaves) != 2 || l.Leaves[0].Name != "changing-the-kit.md" || !strings.Contains(l.Leaves[0].Body, "at-jam kit push") ||
			l.Leaves[1].Name != "hardening-limits.md" {
			t.Errorf("%s: want the changing-the-kit and hardening-limits leaves, got %+v", c.f.Kind, l.Leaves)
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

// Every session waits when its turn ends; a ticket session is also told how
// it finishes (report, end) and never about worker-result.json, which the Jam
// path no longer reads.
func TestBoilerplateTicketTurnContract(t *testing.T) {
	l := Boilerplate(SessionFacts{Kind: KindEphemeral, Project: "p", Role: "r", Unit: "AET-9"})
	for _, want := range []string{"Ending your turn is how you wait", "`report`", "`end`"} {
		if !strings.Contains(l.Core, want) {
			t.Errorf("ticket boilerplate missing %q:\n%s", want, l.Core)
		}
	}
	for _, k := range []string{KindEphemeral, KindPersonal, KindStanding} {
		if c := Boilerplate(SessionFacts{Kind: k, Owner: "o", Name: "n", Unit: "AET-9"}).Core; !strings.Contains(c, "Ending your turn is how you wait") || strings.Contains(c, "worker-result") {
			t.Errorf("%s: turn model wrong:\n%s", k, c)
		}
	}
}

// Without a unit there is no ticket: `send` without `to` posts to the
// session's own channel.
func TestBoilerplateEphemeralWithoutUnitHasOwnChannel(t *testing.T) {
	c := Boilerplate(SessionFacts{Kind: KindEphemeral, Project: "p", Role: "r"}).Core
	if strings.Contains(c, "ticket") || !strings.Contains(c, "posts to your own channel") {
		t.Fatalf("unit-less ephemeral must be told about its own channel:\n%s", c)
	}
}

// The sandbox rules a plain at-cove sandbox reads from the image's SANDBOX.md
// are Jam built-in boilerplate (COV-246): every compiled Jam session carries
// them, self-contained — never pointing at the kit-overridable
// /agent-data/reference docs — because the image's SANDBOX.md tells a Jam
// session to ignore it.
func TestCompiledContextCarriesSandboxRules(t *testing.T) {
	for _, k := range []string{KindEphemeral, KindPersonal, KindStanding} {
		b := Compile(Inputs{Session: SessionFacts{Kind: k, Name: "n", Owner: "o", Project: "p", Role: "r"}})
		for _, want := range []string{
			"allow-listed", "http(s)_proxy=http://127.0.0.1:3128", "nftables", "not a transient fault",
			"/home/agent/workspace", "/agent-data", "CLAUDE_CONFIG_DIR", "reset when the cove is rebuilt",
			"human-gated", "never weaken the hardening",
			"boilerplate/changing-the-kit.md", "boilerplate/hardening-limits.md",
		} {
			if !strings.Contains(b.Core, want) {
				t.Errorf("%s: compiled core missing sandbox rule %q:\n%s", k, want, b.Core)
			}
		}
		if strings.Contains(b.Core, "/agent-data/reference/") || strings.Contains(b.Core, "SANDBOX.md") {
			t.Errorf("%s: sandbox rules must be self-contained, not point at image docs:\n%s", k, b.Core)
		}
		lim := b.Files["boilerplate/hardening-limits.md"]
		for _, want := range []string{"nftables", "sshd", "entrypoint", "credential helper", "CLAUDE_CONFIG_DIR", "managed-settings.json", "at-jam egress set"} {
			if !strings.Contains(lim, want) {
				t.Errorf("%s: hardening-limits leaf missing %q:\n%s", k, want, lim)
			}
		}
		if !strings.Contains(b.Files["boilerplate/changing-the-kit.md"], "will not survive a rebuild") {
			t.Errorf("%s: changing-the-kit leaf lost the one-off install rule", k)
		}
	}
}
