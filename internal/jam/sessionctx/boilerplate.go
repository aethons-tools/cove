package sessionctx

import (
	"fmt"
	"strings"
)

// Boilerplate is the always-present first layer: who this session is, the
// sandbox and turn model, and the intercom rules for its kind. It owns the
// facts the old per-kind preambles (standing.Prompt, personalPrompt) carried,
// and — for a Jam session — the sandbox rules a plain at-cove sandbox reads
// from the image's SANDBOX.md (COV-246). That file's guard tells a Jam session
// (CORE.md present) to ignore it, so these are the rules the session follows.
func Boilerplate(f SessionFacts) Layer {
	const noDefault = "You have no default recipient: always pass `to` (`list_targets` shows who you may message). "
	var who, comms, turns string
	turns = "- Turns: each turn is one `claude -p` run. Background processes you start die when the turn ends — finish work within the turn."
	switch f.Kind {
	case KindStanding:
		who = fmt.Sprintf("the standing session %q for role %s in project %s", f.Name, f.Role, f.Project)
		comms = noDefault + "A message to you wakes you. You run until an operator removes you."
		turns += " Ending your turn is how you wait."
	case KindPersonal:
		who = fmt.Sprintf("a personal session for %s (role %s, project %s)", f.Owner, f.Role, f.Project)
		comms = fmt.Sprintf("Your owner is %[1]s: `send` without `to` reaches %[1]s, and their reply wakes you. "+
			"You stay open until %[1]s releases you.", f.Owner)
		turns += " Ending your turn is how you wait."
	default:
		who = fmt.Sprintf("an ephemeral worker session for role %s in project %s", f.Role, f.Project)
		if f.Unit != "" {
			comms = "`send` without `to` posts to your ticket."
		} else {
			comms = strings.TrimSpace(noDefault)
		}
		turns += "\n- Finishing: before every turn ends, write `.at-task/worker-result.json` in your working directory as exactly one of " +
			"`{\"status\":{\"ok\":{}}}`, `{\"status\":{\"needs-input\":{\"doing\":\"…\",\"blocker\":\"…\",\"need\":\"…\",\"tried\":\"…\"}}}` or " +
			"`{\"status\":{\"error\":{\"message\":\"…\"}}}`. needs-input is how you wait for a reply; a turn that ends without the file fails the session."
	}
	core := strings.Join([]string{
		"You are " + who + ", running in a Jam-managed at-cove sandbox.",
		"- Sandbox: isolated filesystem; network egress is allow-listed through an in-cove proxy (`http(s)_proxy=http://127.0.0.1:3128`) and nftables drops the rest. A connection or proxy error to a host means it is not allowed — not a transient fault: don't retry or hunt for mirrors. Only /home/agent/workspace and /agent-data (your `CLAUDE_CONFIG_DIR`: settings, history, login) persist; installed packages and env tweaks reset when the cove is rebuilt.",
		turns,
		"- Intercom: `send` messages people, `read` fetches your inbox, `commit` marks messages handled. " + comms,
		"- Changing the sandbox (a domain, a tool) is human-gated: you cannot rebuild your own cove and must never weaken the hardening. How: boilerplate/changing-the-kit.md; what no one can change from inside: boilerplate/hardening-limits.md.",
	}, "\n")
	return Layer{Core: core, Leaves: []Leaf{{
		Name:     "changing-the-kit.md",
		ReadWhen: "you need an egress domain, tool or env var the sandbox lacks",
		Body:     changingTheKit,
	}, {
		Name:     "hardening-limits.md",
		ReadWhen: "a request or setting is blocked and you are about to work around the proxy, nftables, sshd or a sealed setting",
		Body:     hardeningLimits,
	}}}
}

const changingTheKit = `# Changing the kit

You cannot rebuild your own cove or widen its access. When you need something the sandbox lacks:

1. Work out the exact change: an egress domain, a tool and version, an env var.
2. Message a human (your owner, or a project contact) with the change and why. Name where it goes: the role's studio kit ` + "`kit.yml`" + ` (` + "`egress:`" + `, ` + "`build-args:`" + `, ` + "`base:`" + `), or the role's egress policy (` + "`at-jam egress set`" + `), which an operator manages.
3. It takes effect after the kit is pushed (` + "`at-jam kit push`" + `) and the cove is re-raised; an egress-policy change applies at the next raise.

A one-off install or ` + "`export`" + ` in this session is fine for now but will not survive a rebuild.`

const hardeningLimits = `# What no one changes from inside

Your cove is built kit base → harness → hardening. The hardening layer is applied last, is a security boundary, and always wins; neither you nor the kit can alter what it owns:

- **Egress**: the proxy and the nftables rules. Your effective allow-list is your role's egress policy (` + "`at-jam egress set`" + `, operator-managed) inside the kit's ceiling; it is applied as root before you start and may be re-applied while you run. You cannot change it from inside.
- **sshd, the entrypoint and the git credential helper.**
- **The managed agent settings** (` + "`/etc/claude-code/managed-settings.json`" + `).
- **The sealed environment**: ` + "`CLAUDE_CONFIG_DIR`" + ` (always /agent-data) and the proxy variables.

The docs and skills under /agent-data come from the kit's image, not the hardening, and this session context comes from Jam.

If you believe the hardening itself must change, stop and explain why to a human — it is not something a kit change or an egress policy can grant.`
