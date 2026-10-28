package adminui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func labels(ns []TreeNode) string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Label)
	}
	return strings.Join(out, ",")
}

// find is the node at path, depth-first.
func find(ns []TreeNode, path string) (TreeNode, bool) {
	for _, n := range ns {
		if n.Path == path {
			return n, true
		}
		if c, ok := find(n.Children, path); ok {
			return c, true
		}
	}
	return TreeNode{}, false
}

func treeStore(t *testing.T) (jam.Store, jam.User) {
	t.Helper()
	st := jam.NewMemStore()
	if err := st.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	alice, err := st.CreateUser(jam.User{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetProject("acme")
	if err := st.AddMember(p.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRole("acme", jam.Role{Name: "coder"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddDestination(jam.Destination{Name: "gh", Route: "/gh", Upstream: "https://api.github.com"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutModelSpec(jam.ModelSpec{Name: "m1"}); err != nil {
		t.Fatal(err)
	}
	return st, alice
}

func TestTreeShape(t *testing.T) {
	st, alice := treeStore(t)
	nodes := buildTree(st, attention(st, nil))
	if got := labels(nodes); got != "Tracking,Projects,Users,Kits,Destinations,Model Specs" {
		t.Fatalf("top level = %s", got)
	}
	acme, ok := find(nodes, "config/projects/acme")
	if !ok || !acme.Group || labels(acme.Children) != "Members,Agents,Intercom,Escalation" {
		t.Fatalf("acme = %+v", acme)
	}
	m, ok := find(nodes, "config/projects/acme/members/"+string(alice.ID))
	if !ok || m.Label != "alice" || m.Group {
		t.Errorf("member = %+v", m)
	}
	for _, p := range []string{"config/projects/acme/agents/coder", "config/users/" + string(alice.ID), "config/destinations/gh", "config/model-specs/m1", "config/tracking"} {
		if _, ok := find(nodes, p); !ok {
			t.Errorf("no node %s", p)
		}
	}
	for _, p := range []string{"config/projects", "config/projects/acme/members", "config/projects/acme/agents", "config/users", "config/kits", "config/destinations", "config/model-specs"} {
		if n, _ := find(nodes, p); !n.Group {
			t.Errorf("%s is not a group", p)
		}
	}
	js, _ := json.Marshal(TreeResponse{Nodes: nodes})
	if strings.Contains(string(js), `"children":null`) {
		t.Errorf("a nil children list: %s", js)
	}
}

func TestTreeBadges(t *testing.T) {
	st, _ := treeStore(t)
	// A kit removed after the role bound it (RemoveKit doesn't check users).
	if _, err := st.PushKit("nope", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRole("acme", jam.Role{Name: "coder", Kit: "nope"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveKit("nope"); err != nil {
		t.Fatal(err)
	}
	items := append(attention(st, nil), attnItem{Kind: attnBroken, Scope: "acme", Tab: "agents", Subject: "ses_1"})
	nodes := buildTree(st, items)
	for _, p := range []string{"config/projects/acme/agents/coder", "config/projects/acme/agents", "config/projects/acme", "config/projects"} {
		if n, _ := find(nodes, p); n.Badge != 1 || n.BadgeRed {
			t.Errorf("%s badge = %d red=%v, want 1 amber", p, n.Badge, n.BadgeRed)
		}
	}
	for _, p := range []string{"config/users", "config/tracking", "config/projects/acme/members"} {
		if n, _ := find(nodes, p); n.Badge != 0 {
			t.Errorf("%s badge = %d, want 0", p, n.Badge)
		}
	}
	if b := appTabBadges(items); b["config"].N != 1 || b["agents"].N != 1 {
		t.Errorf("tab badges = %+v", b)
	}
}

func TestTreePathsEscape(t *testing.T) {
	st := jam.NewMemStore()
	if err := st.CreateProject("a b%c"); err != nil {
		t.Skipf("store refuses the name: %v", err)
	}
	nodes := buildTree(st, nil)
	if _, ok := find(nodes, "config/projects/a%20b%25c"); !ok {
		t.Errorf("escaped project path missing: %+v", nodes[1].Children)
	}
	if p, ok := treePath(attnItem{Kind: attnConfig, Subject: "kit:x/y"}); !ok || p != "config/kits/x%2Fy" {
		t.Errorf("kit path = %q %v", p, ok)
	}
	if p, ok := treePath(attnItem{Kind: attnConfig, Scope: "a b", Tab: "agents", Subject: "a b/r 1"}); !ok || p != "config/projects/a%20b/agents/r%201" {
		t.Errorf("role path = %q %v", p, ok)
	}
	if _, ok := treePath(attnItem{Kind: attnStale, Scope: "a", Tab: "agents", Subject: "ses_1"}); ok {
		t.Errorf("a runtime item has a tree node")
	}
}
