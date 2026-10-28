package adminui

import (
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// memberRow is a project member: their user, their tracker handle,
// and their delivery here in the edit form's line syntax.
type memberRow struct {
	UserID       ident.ID
	Name         string
	Handle       string
	Delivery     []jam.DeliveryProfile
	DeliverySpec string
}

// targetView is one escalation target; Unknown marks one that names nobody on
// the project's roster (flagged, never blocked).
type targetView struct {
	Text    string
	Unknown bool
}

type tierView struct {
	Targets []targetView
	Timeout string
}

// chainView is one escalation chain with its tiers in the editor's syntax.
type chainView struct {
	Category string // "" = the default chain
	Tiers    []tierView
	Spec     string // one targets@timeout per line
}

func lines[T any](xs []T, f func(T) string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = f(x)
	}
	return strings.Join(out, "\n")
}

// knownTargets is the set of escalation targets that name someone in a
// project: user:<name|id> for each member (human: is the pre-registry alias),
// channel:<name> for each room.
func knownTargets(members []jam.Member, rooms []jam.RoomView) map[string]bool {
	known := map[string]bool{}
	for _, m := range members {
		for _, k := range []string{"user:", "human:"} {
			known[k+m.User.Name], known[k+string(m.User.ID)] = true, true
		}
	}
	for _, r := range rooms {
		known["channel:"+r.Name] = true
	}
	return known
}

func chain(category string, tiers []jam.EscalationTier, known map[string]bool) chainView {
	c := chainView{Category: category, Spec: lines(tiers, jam.FormatEscalationTierSpec)}
	for _, t := range tiers {
		tv := tierView{Timeout: fmtDur(t.Timeout)}
		for _, x := range t.Targets {
			tv.Targets = append(tv.Targets, targetView{Text: x, Unknown: !known[x]})
		}
		c.Tiers = append(c.Tiers, tv)
	}
	return c
}

// splitSpecLines returns the non-blank, trimmed lines of a textarea.
func splitSpecLines(s string) []string {
	var out []string
	for l := range strings.Lines(s) {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// deliveryFromSpec parses a member's delivery lines (service:address; a
// Discord user id is an account, bound on the user's page).
func deliveryFromSpec(spec string) ([]jam.DeliveryProfile, error) {
	var out []jam.DeliveryProfile
	for _, l := range splitSpecLines(spec) {
		p, err := jam.ParseDeliverySpec(l)
		if err != nil {
			return nil, badRequest("delivery " + `"` + l + `": ` + err.Error())
		}
		if p.UserID != "" {
			return nil, badRequest("delivery " + `"` + l + `": a Discord user id is an account — add it on the user's page`)
		}
		out = append(out, p)
	}
	return out, nil
}

// putMember adds or updates a member of project with delivery (the edit
// form's lines). add is "Add member": for someone already a member, an empty
// delivery keeps theirs (the per-member edit replaces it).
func putMember(store jam.Store, project, user, deliverySpec string, add bool) error {
	uid, err := jam.ResolveRegistryRef(store, ident.User, strings.TrimSpace(user))
	if err != nil {
		return registryErr(err)
	}
	delivery, err := deliveryFromSpec(deliverySpec)
	if err != nil {
		return err
	}
	p, _ := store.GetProject(project)
	if add && delivery == nil {
		if ms, ok := store.GetMembership(p.ID, uid); ok {
			delivery = ms.Delivery
		}
	}
	return registryErr(store.PutMembership(jam.Membership{ProjectID: p.ID, UserID: uid, Delivery: delivery}))
}

// removeMember removes user (a name or id) from project.
func removeMember(store jam.Store, project, user string) error {
	uid, err := jam.ResolveRegistryRef(store, ident.User, user)
	if err != nil {
		return registryErr(err)
	}
	p, _ := store.GetProject(project)
	return registryErr(store.RemoveMember(p.ID, uid))
}

// putRoom adds or rebinds a room of project.
func putRoom(store jam.Store, project, name, connection, ref string) error {
	b := jam.RoomBody{Name: strings.TrimSpace(name), Connection: strings.TrimSpace(connection), Ref: strings.TrimSpace(ref)}
	if b.Name == "" || b.Connection == "" || b.Ref == "" {
		return badRequest("room name, connection and ref are required")
	}
	_, _, err := jam.PutRoom(store, project, b)
	return registryErr(err)
}

// setEscalation sets project's chain for category ("" = the default) from
// the editor's lines (targets@timeout, tier 0 first).
func setEscalation(store jam.Store, project, category, tiersSpec string) error {
	var tiers []jam.EscalationTier
	for _, l := range splitSpecLines(tiersSpec) {
		t, err := jam.ParseEscalationTierSpec(l)
		if err != nil {
			return badRequest(err.Error())
		}
		tiers = append(tiers, t)
	}
	if len(tiers) == 0 {
		return badRequest("a chain needs at least one tier (targets@timeout); use Clear to remove it")
	}
	return store.SetEscalationPolicy(project, strings.TrimSpace(category), tiers)
}
