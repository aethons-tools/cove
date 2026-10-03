package sessionctx

import (
	"fmt"
	"strings"
)

// StudioDestination is one granted destination as a session should see it:
// what it reaches and how to use it. Never credentials or env values.
type StudioDestination struct {
	Name, Upstream string
	EnvKeys        []string // sorted env var names the connector sets
	Git            bool     // https://github.com/ is routed through it
	Note           string   // Destination.Note
}

// StudioTarget is one message target the session may `send` to.
type StudioTarget struct {
	Target string // "human:<name>" | "channel:<name>"
	Who    string // e.g. "your owner", "project contact @alice", "channel"
}

// StudioFacts are what a session can actually reach, gathered at raise.
type StudioFacts struct {
	Destinations []StudioDestination
	Egress       []string // effective allow-list (sorted)
	EgressKnown  bool     // false = the image's default list (not known to Jam)
	Targets      []StudioTarget
}

func (f StudioFacts) empty() bool {
	return len(f.Destinations) == 0 && len(f.Egress) == 0 && !f.EgressKnown && len(f.Targets) == 0
}

// Studio renders the generated Studio layer. It never exceeds BudgetStudio:
// when the full form is too long, each list moves to its own leaf and the core
// keeps a one-line summary per list.
func Studio(f StudioFacts) Layer {
	if f.empty() {
		return Layer{}
	}
	dests, egress, targets := studioDestinations(f), studioEgress(f), studioTargets(f)
	full := strings.Join(nonEmpty(dests, egress, targets), "\n")
	if len(full) <= BudgetStudio {
		return Layer{Core: full}
	}
	var l Layer
	var rest []string // the core lines after the destinations summary
	if len(f.Egress) > 0 {
		rest = append(rest, fmt.Sprintf("Egress: %d allowed hosts, plus the sealed base and Jam's own routes.", len(f.Egress)))
		l.Leaves = append(l.Leaves, Leaf{Name: "egress.md", ReadWhen: "a host fails to connect and you need to know whether it is allowed", Body: egress})
	} else {
		rest = append(rest, egress)
	}
	if targets != "" {
		rest = append(rest, fmt.Sprintf("Message targets: %d (`list_targets` shows them).", len(f.Targets)))
		l.Leaves = append(l.Leaves, Leaf{Name: "targets.md", ReadWhen: "you need who a message target is", Body: targets})
	}
	core := rest
	if dests != "" {
		room := BudgetStudio - len(strings.Join(rest, "\n")) - 1
		core = append([]string{destinationSummary(f.Destinations, room)}, rest...)
		l.Leaves = append([]Leaf{{Name: "destinations.md", ReadWhen: "you need how to reach a granted service (env, git routing, notes)", Body: dests}}, l.Leaves...)
	}
	l.Core = strings.Join(core, "\n")
	return l
}

// destinationSummary names as many destinations as fit in room bytes, then
// counts the rest, always pointing at the full leaf.
func destinationSummary(ds []StudioDestination, room int) string {
	head := fmt.Sprintf("Destinations via Jam (%d): ", len(ds))
	for n := len(ds); n >= 0; n-- {
		names := make([]string, n)
		for i := range n {
			names[i] = codeSafe(ds[i].Name)
		}
		line := head + strings.Join(names, ", ")
		if n < len(ds) {
			line += fmt.Sprintf(" … %d more in studio/destinations.md.", len(ds)-n)
		} else {
			line += " — details in studio/destinations.md."
		}
		if len(line) <= room || n == 0 {
			return line
		}
	}
	return head
}

func studioDestinations(f StudioFacts) string {
	if len(f.Destinations) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Destinations via Jam (credentials are injected for you; never ask for them):")
	for _, d := range f.Destinations {
		fmt.Fprintf(&b, "\n- `%s` → %s", codeSafe(d.Name), oneLine(d.Upstream))
		if len(d.EnvKeys) > 0 {
			fmt.Fprintf(&b, "; env %s", strings.Join(d.EnvKeys, ", "))
		}
		if d.Git {
			b.WriteString("; https://github.com/ is routed through Jam")
		}
		if n := oneLine(d.Note); n != "" {
			b.WriteString(" — " + n)
		}
	}
	return b.String()
}

func studioEgress(f StudioFacts) string {
	switch {
	case len(f.Egress) > 0:
		hosts := make([]string, len(f.Egress))
		for i, h := range f.Egress {
			hosts[i] = egressDisplay(h)
		}
		return "Egress (other hosts are blocked): " + strings.Join(hosts, ", ") + ", plus the sealed base and Jam's own routes."
	case f.EgressKnown:
		return "Egress: no hosts beyond the sealed base and Jam's own routes."
	default:
		return "Egress: the image's default allow-list, plus Jam's own routes."
	}
}

func studioTargets(f StudioFacts) string {
	if len(f.Targets) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Message targets:")
	for _, t := range f.Targets {
		fmt.Fprintf(&b, "\n- `%s` — %s", codeSafe(t.Target), oneLine(t.Who))
	}
	return b.String()
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// codeSafe flattens an admin-authored name for an inline code span: one line,
// no backticks, so it can't open a heading or break the span.
func codeSafe(s string) string { return strings.ReplaceAll(oneLine(s), "`", "'") }

// egressDisplay spells a squid wildcard (".example.com") as what it allows.
func egressDisplay(h string) string {
	if rest, ok := strings.CutPrefix(h, "."); ok && rest != "" {
		return "*." + rest + " (and " + rest + ")"
	}
	return oneLine(h)
}
