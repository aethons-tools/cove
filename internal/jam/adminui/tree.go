package adminui

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
)

// The Config tree (docs/superpowers/specs/2026-10-28-admin-spa-design.md §3):
// the admin app's navigation for configuration — Tracking, the projects
// (members, agents and roles, intercom, escalation), users and the specs.
// Paths are under the app's mount; each segment is path-escaped.

// treePath is the tree node an attention item belongs to. Only configuration
// trouble is on the tree; a studio's runtime trouble badges the Agents tab
// and an open condition Health.
func treePath(it attnItem) (string, bool) {
	if it.Kind != attnConfig {
		return "", false
	}
	if k, ok := strings.CutPrefix(it.Subject, "kit:"); ok {
		return "config/kits/" + url.PathEscape(k), true
	}
	if d, ok := strings.CutPrefix(it.Subject, "dest:"); ok {
		return "config/destinations/" + url.PathEscape(d), true
	}
	if it.Scope == "" {
		return "", false
	}
	project := "config/projects/" + url.PathEscape(it.Scope)
	switch it.Tab {
	case "agents":
		if role, ok := strings.CutPrefix(it.Subject, it.Scope+"/"); ok {
			return project + "/agents/" + url.PathEscape(role), true
		}
	case "escalation":
		return project + "/escalation", true
	}
	return project, true
}

// buildTree lays out the Config tree with each node's badge: the config items
// at or under it (items is every attention item).
func buildTree(store jam.Store, items []attnItem) []TreeNode {
	esc := url.PathEscape
	leaf := func(path, label string) TreeNode { return TreeNode{Path: path, Label: label, Children: []TreeNode{}} }
	group := func(path, label string, children ...TreeNode) TreeNode {
		if children == nil {
			children = []TreeNode{}
		}
		return TreeNode{Path: path, Label: label, Group: true, Children: children}
	}

	var projects []TreeNode
	for _, name := range slices.Sorted(slices.Values(store.ListProjects())) {
		base := "config/projects/" + esc(name)
		var members, roles []TreeNode
		if p, ok := store.GetProject(name); ok {
			ms := jam.MembersOf(store, p.ID)
			slices.SortFunc(ms, func(a, b jam.Member) int { return cmp.Compare(a.User.Name, b.User.Name) })
			for _, m := range ms {
				members = append(members, leaf(base+"/members/"+esc(string(m.User.ID)), m.User.Name))
			}
		}
		rs := store.ListRoles(name)
		slices.SortFunc(rs, func(a, b jam.Role) int { return cmp.Compare(a.Name, b.Name) })
		for _, r := range rs {
			roles = append(roles, leaf(base+"/agents/"+esc(r.Name), r.Name))
		}
		projects = append(projects, group(base, name,
			group(base+"/members", "Members", members...),
			group(base+"/agents", "Agents", roles...),
			leaf(base+"/intercom", "Intercom"),
			leaf(base+"/escalation", "Escalation"),
		))
	}
	var users []TreeNode
	us := store.ListUsers()
	slices.SortFunc(us, func(a, b jam.User) int { return cmp.Compare(a.Name, b.Name) })
	for _, u := range us {
		users = append(users, leaf("config/users/"+esc(string(u.ID)), u.Name))
	}
	named := func(path, label string, names []string) TreeNode {
		var ch []TreeNode
		for _, n := range slices.Sorted(slices.Values(names)) {
			ch = append(ch, leaf(path+"/"+esc(n), n))
		}
		return group(path, label, ch...)
	}
	var kits, dests, specs []string
	for _, k := range store.ListKits() {
		kits = append(kits, k.Name)
	}
	for _, d := range store.ListDestinations() {
		dests = append(dests, d.Name)
	}
	for _, m := range store.ListModelSpecs() {
		specs = append(specs, m.Name)
	}
	nodes := []TreeNode{
		leaf("config/tracking", "Tracking"),
		group("config/projects", "Projects", projects...),
		group("config/users", "Users", users...),
		named("config/kits", "Kits", kits),
		named("config/destinations", "Destinations", dests),
		named("config/model-specs", "Model Specs", specs),
	}
	var paths []string
	var onTree []attnItem
	for _, it := range items {
		if p, ok := treePath(it); ok {
			paths, onTree = append(paths, p), append(onTree, it)
		}
	}
	badgeTree(nodes, paths, onTree)
	return nodes
}

// badgeTree sets each node's badge from the items whose path (paths[i] is
// items[i]'s) is the node's or under it.
func badgeTree(nodes []TreeNode, paths []string, items []attnItem) {
	for i := range nodes {
		n := &nodes[i]
		var mine []attnItem
		for j, p := range paths {
			if p == n.Path || strings.HasPrefix(p, n.Path+"/") {
				mine = append(mine, items[j])
			}
		}
		b := badgeOf(mine)
		n.Badge, n.BadgeRed, n.BadgeTitle = b.N, b.Red, b.Title
		if len(mine) > 0 {
			badgeTree(n.Children, paths, items)
		}
	}
}

func treeHandler(src frameSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, TreeResponse{Nodes: buildTree(src.store, src.items())})
	}
}
