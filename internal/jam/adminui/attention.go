package adminui

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/studio"
)

// attnKind is what kind of trouble an attention item is. broken is red; stale
// and config are amber.
type attnKind string

const (
	attnBroken attnKind = "broken" // a studio is lost, terminating or failing egress re-apply
	attnStale  attnKind = "stale"  // a studio runs an out-of-date image or connector
	attnConfig attnKind = "config" // configuration that names something that isn't there
)

// attnItem is one thing that needs an operator's attention. Scope is "" for
// Jam, else the project's name; Tab is the scope tab that owns it; Subject
// keys it for row flags (an agent id, "project/role", "kit:<name>",
// "dest:<name>", "escalation:<category>:<target>").
type attnItem struct {
	Kind    attnKind
	Scope   string
	Tab     string
	Subject string
	Href    string
	Why     string
}

// attention lists everything that needs attention, Jam's items first, then
// each project's in name order — the one source for the rail, tab and row
// badges and the Needs attention cards.
func attention(store jam.Store, img jam.ImageResolver) []attnItem {
	var out []attnItem

	// Jam: kits whose current version doesn't parse; destinations in a
	// connector conflict.
	for _, k := range store.ListKits() {
		if _, err := studio.ParseStudioKit([]byte(k.Versions[k.Current])); err != nil {
			out = append(out, attnItem{Kind: attnConfig, Tab: "specs", Subject: "kit:" + k.Name, Href: kitURL(k.Name),
				Why: fmt.Sprintf("kit %s v%d does not parse", k.Name, k.Current)})
		}
	}
	for _, d := range store.ListDestinations() {
		if dd, ok := buildDestDetail(store, d.Name); ok && len(dd.Conflicts) > 0 {
			out = append(out, attnItem{Kind: attnConfig, Tab: "specs", Subject: "dest:" + d.Name, Href: destURL(d.Name),
				Why: "destination " + d.Name + " conflicts with another in " + strings.Join(dd.ConflictRoles(), ", ")})
		}
	}

	// Projects: studios (one item per agent, its worst trouble), roles naming
	// a missing kit or model-spec, escalation targets naming nobody.
	byProject := map[string][]attnItem{}
	for _, c := range jam.CoveSummaries(store, img) {
		p := orDefaultProject(c.Project)
		inst, _ := store.GetInstance(c.ID)
		var broken, stale []string
		switch jam.Phase(c.Phase) {
		case jam.PhaseLost, jam.PhaseTerminating:
			broken = append(broken, c.Phase)
		}
		if inst.EgressFailures > 0 {
			broken = append(broken, fmt.Sprintf("egress re-apply failing (%d)", inst.EgressFailures))
		}
		if c.Image == "stale" {
			stale = append(stale, "image stale")
		}
		if c.Connector == "stale" {
			stale = append(stale, "connector stale")
		}
		item := attnItem{Scope: p, Tab: "agents", Subject: c.ID, Href: agentURL(c.ID) + "?project=" + url.QueryEscape(p)}
		switch {
		case len(broken) > 0:
			item.Kind, item.Why = attnBroken, c.ID+": "+strings.Join(append(broken, stale...), ", ")
		case len(stale) > 0:
			item.Kind, item.Why = attnStale, c.ID+": "+strings.Join(stale, ", ")
		default:
			continue
		}
		byProject[p] = append(byProject[p], item)
	}
	for _, p := range store.ListProjects() {
		for _, r := range store.ListRoles(p) {
			var why []string
			if r.Kit != "" {
				if _, ok := store.GetKit(r.Kit); !ok {
					why = append(why, "kit "+r.Kit+" not found")
				}
			}
			if r.ModelSpec != "" {
				if _, ok := store.GetModelSpec(r.ModelSpec); !ok {
					why = append(why, "model-spec "+r.ModelSpec+" not found")
				}
			}
			if why != nil {
				byProject[p] = append(byProject[p], attnItem{Kind: attnConfig, Scope: p, Tab: "roles", Subject: p + "/" + r.Name,
					Href: roleURL(p, r.Name), Why: "role " + r.Name + ": " + strings.Join(why, ", ")})
			}
		}
		proj, ok := store.GetProject(p)
		if !ok {
			continue
		}
		known := knownTargets(jam.MembersOf(store, proj.ID), jam.ListRooms(store, proj))
		chains := map[string][]jam.EscalationTier{"": proj.Escalation}
		maps.Copy(chains, proj.EscalationByCategory)
		for _, cat := range slices.Sorted(maps.Keys(chains)) {
			for _, t := range chain(cat, chains[cat], known).Tiers {
				for _, tg := range t.Targets {
					if !tg.Unknown {
						continue
					}
					where := "default chain"
					if cat != "" {
						where = "chain " + cat
					}
					byProject[p] = append(byProject[p], attnItem{Kind: attnConfig, Scope: p, Tab: "escalation",
						Subject: "escalation:" + cat + ":" + tg.Text, Href: projectSectionURL(p, sectionEscalation),
						Why: "escalation target " + tg.Text + " (" + where + ") names nobody here"})
				}
			}
		}
	}
	for _, p := range slices.Sorted(maps.Keys(byProject)) {
		out = append(out, byProject[p]...)
	}
	return out
}

// attnBadge is a count of items with the worst kind's color and a breakdown
// for its hover title ("1 broken · 2 out of date").
type attnBadge struct {
	N     int
	Red   bool
	Title string
}

func badgeOf(items []attnItem) attnBadge {
	var broken, stale, config int
	for _, it := range items {
		switch it.Kind {
		case attnBroken:
			broken++
		case attnStale:
			stale++
		case attnConfig:
			config++
		}
	}
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{{broken, "broken"}, {stale, "out of date"}, {config, "config"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
		}
	}
	return attnBadge{N: len(items), Red: broken > 0, Title: strings.Join(parts, " · ")}
}

// inScope keeps the items of one scope ("" = Jam), optionally of one tab.
func inScope(items []attnItem, scope, tab string) []attnItem {
	var out []attnItem
	for _, it := range items {
		if it.Scope == scope && (tab == "" || it.Tab == tab) {
			out = append(out, it)
		}
	}
	return out
}
