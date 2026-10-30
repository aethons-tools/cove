package sessionctx

import (
	"fmt"
	"strings"
)

// Boilerplate is the always-present first layer: who this session is, the
// sandbox and turn model, and the intercom rules for its kind. It owns the
// facts the old per-kind preambles (standing.Prompt, personalPrompt) carried,
// and — for a Jam session — the sandbox rules the image's fallback SANDBOX.md
// gives a session without a bundle (COV-246). That file's guard tells a Jam session
// (CORE.md present) to ignore it, so these are the rules the session follows.
func Boilerplate(f SessionFacts) Layer {
	const ownChannel = "`send` without `to` posts to your own channel: whoever is in it (people who joined it, or were called in with `call_in`) hears you. Pass `to` to reach anyone else (`list_targets` shows who you may message). "
	var who, comms, turns string
	turns = "- Turns: each turn is one `claude -p` run. " + legacyBackground
	structured := f.TurnProtocol == TurnProtocolStructured
	if structured {
		turns = structuredTurns
	}
	switch f.Kind {
	case KindStanding:
		who = fmt.Sprintf("the standing session %q for role %s in project %s", f.Name, f.Role, f.Project)
		comms = ownChannel + "A message to you wakes you. You run until an operator removes you."
		if !structured {
			turns += " Ending your turn is how you wait."
		}
	case KindPersonal:
		who = fmt.Sprintf("a personal session for %s (role %s, project %s)", f.Owner, f.Role, f.Project)
		comms = fmt.Sprintf("You were started by %[1]s: `send` without `to` posts to your own channel, which %[1]s is in, and their reply wakes you. "+
			"Pass `to` to reach anyone else (`list_targets`). You stay open until %[1]s releases you.", f.Owner)
		if !structured {
			turns += " Ending your turn is how you wait."
		}
	default:
		who = fmt.Sprintf("an ephemeral worker session for role %s in project %s", f.Role, f.Project)
		if !structured {
			turns += " Ending your turn is how you wait."
		}
		if f.Unit != "" {
			comms = "`send` without `to` posts to your ticket."
			turns += "\n- Your ticket: keep its state current with `report` (in-review with the PR link, needs-input, blocked, done), and call `end` as your last action once it is finished."
			if structured {
				turns += " " + structuredTicket
			}
		} else {
			comms = strings.TrimSpace(ownChannel)
		}
	}
	lines := []string{
		"You are " + who + ", running in a Jam studio.",
		"- Sandbox: isolated filesystem; network egress is allow-listed through an in-cove proxy (`http(s)_proxy=http://127.0.0.1:3128`) and nftables drops the rest. A connection or proxy error to a host means it is not allowed — not a transient fault: don't retry or hunt for mirrors. Only /home/agent/workspace and /agent-data (your `CLAUDE_CONFIG_DIR`: settings, history, login) persist; installed packages and env tweaks reset when the cove is rebuilt.",
		turns,
		"- Intercom: `send` messages people, `read` fetches your inbox, `commit` marks messages handled. " + comms,
		"- Mission: when your focus changes (a new task, a ticket picked up), set a one-line `mission` saying what you are on — people see it beside your name.",
		"- Changing the sandbox (a domain, a tool) is human-gated: you cannot rebuild your own cove and must never weaken the hardening. How: boilerplate/changing-the-kit.md; what no one can change from inside: boilerplate/hardening-limits.md.",
	}
	if f.Previews {
		lines = append(lines, "- Previews: `expose(port, name)` makes a port you serve reachable by your collaborators at a URL — post the link in your channel; `unexpose`/`exposures` manage them. A studio restart clears them: expose again.")
	}
	core := strings.Join(lines, "\n")
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

// TurnProtocolStructured is SessionFacts.TurnProtocol for a cove that speaks
// the structured turn end (agentrun.TurnProtocol).
const TurnProtocolStructured = 2

// legacyBackground is the legacy turns line's word on background processes.
const legacyBackground = "Background processes you start outlive the turn but freeze while the studio is paused as you wait — finish work within the turn."

// structuredTurns replaces the legacy turns line for a cove that speaks the
// structured turn end; structuredTicket is a ticket session's extra.
const (
	structuredTurns  = "- Turns: each turn is one `claude -p` run; ending your turn is how you wait. New messages arrive in your prompt. End your turn with your reply — your final text is posted for you (to your own channel unless `turn_end` names another recipient); end with no text when there is nothing to say, and don't also `send` it. Text you write before a tool call is posted to your own channel as you go, so don't `send` it either. It doesn't wake other sessions: to get one to act, `send` to it. Ending your turn marks the messages you were shown as handled; `turn_end` changes that, what wakes you, or what happens next (compact, end). Background processes you start outlive your turn but freeze while the studio is paused as you wait. To keep something running (a preview), use `keep_start`: the studio isn't paused while it runs, it wakes you if it exits, and it stops with your session; agree with the person how long it should run and set a `wake_at` to ask them then."
	structuredTicket = "Your final text is a comment on your ticket: end with no text when your `report` already says it."
)

const changingTheKit = `# Changing the kit

You cannot rebuild your own cove or widen its access. When you need something the sandbox lacks:

1. Work out the exact change: an egress domain, a tool and version, an env var.
2. Message a human (who started you, or a project contact) with the change and why. Name where it goes: the role's studio kit ` + "`kit.yml`" + ` (` + "`egress:`" + `, ` + "`build-args:`" + `, ` + "`base:`" + `), or the role's egress policy (` + "`jam egress set`" + `), which an operator manages.
3. It takes effect after the kit's draft is published (` + "`jam kit draft publish <kit>`" + `, or ` + "`jam kit push`" + `) and the cove is re-raised; an egress-policy change reaches a running cove within a reconcile pass.

A one-off install or ` + "`export`" + ` in this session is fine for now but will not survive a rebuild.`

const hardeningLimits = `# What no one changes from inside

Your cove is built kit base → harness → hardening. The hardening layer is applied last, is a security boundary, and always wins; neither you nor the kit can alter what it owns:

- **Egress**: the proxy and the nftables rules. Your effective allow-list is your role's egress policy (` + "`jam egress set`" + `, operator-managed) inside the kit's ceiling; it is applied as root before you start and may be re-applied while you run. You cannot change it from inside.
- **sshd, the entrypoint and the git credential helper.**
- **The managed agent settings** (` + "`/etc/claude-code/managed-settings.json`" + `).
- **The sealed environment**: ` + "`CLAUDE_CONFIG_DIR`" + ` (always /agent-data) and the proxy variables.

The docs and skills under /agent-data come from the kit's image, not the hardening, and this session context comes from Jam.

If you believe the hardening itself must change, stop and explain why to a human — it is not something a kit change or an egress policy can grant.`
