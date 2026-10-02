package sessionctx

import (
	"fmt"
	"strings"
)

// Boilerplate is the always-present first layer: who this session is, the
// sandbox and turn model, and the intercom rules for its kind. It owns the
// facts the old per-kind preambles (standing.Prompt, personalPrompt) carried.
func Boilerplate(f SessionFacts) Layer {
	var who, comms string
	switch f.Kind {
	case KindStanding:
		who = fmt.Sprintf("the standing session %q for role %s in project %s", f.Name, f.Role, f.Project)
		comms = "You have no default recipient: always pass `to` (`list_targets` shows who you may message). " +
			"A message to you wakes you. You run until an operator removes you."
	case KindPersonal:
		who = fmt.Sprintf("a personal session for %s (role %s, project %s)", f.Owner, f.Role, f.Project)
		comms = fmt.Sprintf("Your owner is %[1]s: `send` without `to` reaches %[1]s, and their reply wakes you. "+
			"You stay open until %[1]s releases you.", f.Owner)
	default:
		who = fmt.Sprintf("an ephemeral worker session for role %s in project %s", f.Role, f.Project)
		comms = "`send` without `to` posts to your ticket. If you need input, ask there and end your turn; a reply wakes you."
	}
	core := strings.Join([]string{
		"You are " + who + ", running in a Jam-managed at-cove sandbox.",
		"- Sandbox: isolated filesystem; network egress is allow-listed through a proxy. A connection or proxy error to a host means it is not allowed — not a transient fault: don't retry or hunt for mirrors. Only /home/agent/workspace and /agent-data persist; installed packages and env tweaks reset when the cove is rebuilt.",
		"- Turns: each turn is one `claude -p` run. Background processes you start die when the turn ends — finish work within the turn. Ending your turn is how you wait.",
		"- Intercom: `send` messages people, `read` fetches your inbox, `commit` marks messages handled. " + comms,
		"- Changing the sandbox (a domain, a tool) is human-gated. Hardening limits: /agent-data/reference/sandbox-hardening-limits.md.",
	}, "\n")
	return Layer{Core: core, Leaves: []Leaf{{
		Name:     "changing-the-kit.md",
		ReadWhen: "you need an egress domain, tool or env var the sandbox lacks",
		Body:     changingTheKit,
	}}}
}

const changingTheKit = `# Changing the kit

You cannot rebuild your own cove or widen its access. When you need something the sandbox lacks:

1. Work out the exact change: an egress domain, a tool and version, an env var.
2. Message a human (your owner, or a project contact) with the change and why. Name where it goes: the role's studio kit ` + "`kit.yml`" + ` (` + "`egress:`" + `, ` + "`build-args:`" + `, ` + "`base:`" + `), or the role's egress policy (` + "`at-jam egress set`" + `), which an operator manages.
3. It takes effect after the kit is pushed (` + "`at-jam kit push`" + `) and the cove is re-raised; an egress-policy change applies at the next raise.

A one-off install or ` + "`export`" + ` in this session is fine for now but will not survive a rebuild.`
