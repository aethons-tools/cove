package adminui

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// newImage reports one current image tag for every role, so a studio raised
// on another tag is stale.
type newImage struct{}

func (newImage) CurrentImage(project, role string) (jam.CurrentImage, error) {
	return jam.CurrentImage{HasKit: true, Tag: "img:new"}, nil
}

// attnFixture: project acme with one studio per trouble (lost, terminating,
// egress failing, stale image, healthy), a role naming a missing kit and one
// naming a missing model-spec, an escalation target naming nobody; Jam with
// an unparseable kit and two destinations whose git routes conflict in one
// role; project beta with nothing wrong.
func attnFixture(t *testing.T) jam.Store {
	t.Helper()
	st := jam.NewMemStore()
	for _, p := range []string{"acme", "beta"} {
		if err := st.CreateProject(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, i := range []jam.Instance{
		{ActorID: "s-lost", Project: "acme", Role: "dev", Phase: jam.PhaseLost},
		{ActorID: "s-term", Project: "acme", Role: "dev", Phase: jam.PhaseTerminating},
		{ActorID: "s-egress", Project: "acme", Role: "dev", Phase: jam.PhaseLive, EgressFailures: 2},
		{ActorID: "s-stale", Project: "acme", Role: "dev", Phase: jam.PhaseLive, ImageTag: "img:old"},
		{ActorID: "s-ok", Project: "acme", Role: "dev", Phase: jam.PhaseLive, ImageTag: "img:new"},
		{ActorID: "s-beta", Project: "beta", Role: "w", Phase: jam.PhaseLive, ImageTag: "img:new"},
	} {
		if err := st.PutInstance(i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.PushKit("bad", "listen: :443"); err != nil { // not a studio kit
		t.Fatal(err)
	}
	for _, d := range []jam.Destination{
		{Name: "git-a", Route: "/git-a/", Upstream: "https://github.com", Git: true},
		{Name: "git-b", Route: "/git-b/", Upstream: "https://github.com", Git: true},
	} {
		if err := st.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	// A kit removed after a role bound it (RemoveKit doesn't check users).
	if _, err := st.PushKit("ghost", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []jam.Role{
		{Name: "dev", Scope: jam.Scope{Destinations: []string{"git-a", "git-b"}}},
		{Name: "nokit", Kit: "ghost"},
		{Name: "nospec", ModelSpec: "ghost-spec"},
	} {
		if err := st.PutRole("acme", r); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RemoveKit("ghost"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRole("beta", jam.Role{Name: "w"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEscalationPolicy("acme", "", []jam.EscalationTier{{Targets: []string{"user:ghost"}, Timeout: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAttention(t *testing.T) {
	items := attention(attnFixture(t), newImage{})
	got := map[string]attnItem{}
	for _, it := range items {
		if _, dup := got[it.Scope+"|"+it.Tab+"|"+it.Subject]; dup {
			t.Errorf("duplicate item %+v", it)
		}
		got[it.Scope+"|"+it.Tab+"|"+it.Subject] = it
	}
	for key, want := range map[string]struct {
		kind attnKind
		why  string
	}{
		"acme|agents|s-lost":                     {attnBroken, "lost"},
		"acme|agents|s-term":                     {attnBroken, "terminating"},
		"acme|agents|s-egress":                   {attnBroken, "egress re-apply failing (2)"},
		"acme|agents|s-stale":                    {attnStale, "image stale"},
		"acme|agents|acme/nokit":                 {attnConfig, "kit ghost not found"},
		"acme|agents|acme/nospec":                {attnConfig, "model-spec ghost-spec not found"},
		"acme|escalation|escalation::user:ghost": {attnConfig, "user:ghost"},
		"|specs|kit:bad":                         {attnConfig, "does not parse"},
		"|specs|dest:git-a":                      {attnConfig, "conflicts"},
		"|specs|dest:git-b":                      {attnConfig, "conflicts"},
	} {
		it, ok := got[key]
		if !ok {
			t.Errorf("missing %s", key)
			continue
		}
		if it.Kind != want.kind || !strings.Contains(it.Why, want.why) || it.Href == "" {
			t.Errorf("%s = %+v, want kind %s, why containing %q, an href", key, it, want.kind, want.why)
		}
		delete(got, key)
	}
	for key := range got {
		t.Errorf("unexpected item %s", key) // s-ok, s-beta and beta are healthy
	}
	if items[0].Scope != "" || items[len(items)-1].Scope != "acme" {
		t.Errorf("Jam's items come first, then each project's")
	}
}

func TestBadgeOf(t *testing.T) {
	if b := badgeOf(nil); b.N != 0 {
		t.Errorf("no items: %+v", b)
	}
	b := badgeOf([]attnItem{{Kind: attnStale}, {Kind: attnConfig}, {Kind: attnStale}})
	if b.N != 3 || b.Red || b.Title != "2 out of date · 1 config" {
		t.Errorf("amber badge = %+v", b)
	}
	b = badgeOf([]attnItem{{Kind: attnStale}, {Kind: attnBroken}})
	if b.N != 2 || !b.Red || b.Title != "1 broken · 1 out of date" {
		t.Errorf("red badge = %+v", b)
	}
}

// A target named twice in one chain is one item.
func TestAttentionEscalationDedupe(t *testing.T) {
	st := jam.NewMemStore()
	if err := st.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEscalationPolicy("acme", "", []jam.EscalationTier{
		{Targets: []string{"user:ghost"}, Timeout: time.Minute},
		{Targets: []string{"user:ghost"}, Timeout: time.Minute},
	}); err != nil {
		t.Fatal(err)
	}
	if items := attention(st, nil); len(items) != 1 || items[0].Subject != "escalation::user:ghost" {
		t.Errorf("items = %+v, want one escalation::user:ghost", items)
	}
}
