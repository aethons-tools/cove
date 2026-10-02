package sessionctx

import (
	"fmt"
	"strings"
	"testing"
)

func ghFacts() StudioFacts {
	return StudioFacts{
		Destinations: []StudioDestination{
			{Name: "anthropic", Upstream: "https://api.anthropic.com", EnvKeys: []string{"ANTHROPIC_BASE_URL"}},
			{Name: "github-api", Upstream: "https://api.github.com", EnvKeys: []string{"GH_ENTERPRISE_TOKEN", "GH_HOST"}, Note: "pass -R $GH_HOST/<owner>/<repo>"},
			{Name: "git", Upstream: "https://github.com", Git: true},
		},
		Egress:      []string{"github.com", "proxy.golang.org"},
		EgressKnown: true,
		Targets:     []StudioTarget{{Target: "human:alice", Who: "your owner"}, {Target: "channel:ops", Who: "channel"}},
	}
}

func TestStudioCoreListsWhatTheSessionCanReach(t *testing.T) {
	l := Studio(ghFacts())
	for _, want := range []string{
		"`github-api` → https://api.github.com",
		"env GH_ENTERPRISE_TOKEN, GH_HOST",
		"pass -R $GH_HOST/<owner>/<repo>",
		"`git` → https://github.com",
		"https://github.com/ is routed through Jam",
		"github.com, proxy.golang.org",
		"plus the sealed base and Jam's own routes",
		"`human:alice` — your owner",
	} {
		if !strings.Contains(l.Core, want) {
			t.Errorf("core missing %q:\n%s", want, l.Core)
		}
	}
	if len(l.Leaves) != 0 {
		t.Errorf("a small studio must not spill: %+v", l.Leaves)
	}
}

func TestStudioEmptyIsEmpty(t *testing.T) {
	if l := Studio(StudioFacts{}); !l.Empty() {
		t.Fatalf("no facts → empty layer, got %+v", l)
	}
}

func TestStudioEgressWording(t *testing.T) {
	if c := Studio(StudioFacts{EgressKnown: true}).Core; !strings.Contains(c, "no hosts beyond the sealed base and Jam's own routes") {
		t.Errorf("empty policy wording wrong:\n%s", c)
	}
	if c := Studio(StudioFacts{Targets: []StudioTarget{{Target: "human:a", Who: "x"}}}).Core; !strings.Contains(c, "the image's default allow-list") {
		t.Errorf("unknown egress wording wrong:\n%s", c)
	}
}

func TestStudioNoteIsOneLine(t *testing.T) {
	f := ghFacts()
	f.Destinations[1].Note = "line one\nline | two"
	c := Studio(f).Core
	if !strings.Contains(c, "line one line / two") {
		t.Fatalf("note must be flattened to one line:\n%s", c)
	}
}

func TestStudioSpillsInsteadOfTruncating(t *testing.T) {
	var f StudioFacts
	for i := range 40 {
		f.Destinations = append(f.Destinations, StudioDestination{Name: fmt.Sprintf("dest-%02d", i), Upstream: "https://upstream.example.com/path", EnvKeys: []string{"SOME_LONG_ENV_KEY"}})
	}
	for i := range 80 {
		f.Egress = append(f.Egress, fmt.Sprintf("host-%02d.example.org", i))
	}
	f.EgressKnown = true
	for i := range 30 {
		f.Targets = append(f.Targets, StudioTarget{Target: fmt.Sprintf("human:person-%02d", i), Who: "project contact"})
	}
	l := Studio(f)
	if len(l.Core) > BudgetStudio {
		t.Fatalf("core %d bytes > budget %d", len(l.Core), BudgetStudio)
	}
	leaves := map[string]string{}
	for _, lf := range l.Leaves {
		leaves[lf.Name] = lf.Body
	}
	for name, want := range map[string]string{"destinations.md": "dest-39", "egress.md": "host-79.example.org", "targets.md": "human:person-29"} {
		if !strings.Contains(leaves[name], want) {
			t.Errorf("leaf %s must hold the full list (missing %q)", name, want)
		}
	}
	b := Compile(Inputs{Session: SessionFacts{Kind: KindStanding, Name: "n", Project: "p", Role: "r"}, Studio: f})
	if len(b.Warnings) != 0 {
		t.Fatalf("a generated layer must never truncate: %v", b.Warnings)
	}
}

func TestCompilePlacesStudioAfterKit(t *testing.T) {
	b := Compile(Inputs{Session: SessionFacts{Kind: KindStanding, Name: "n", Project: "p", Role: "r", Kit: "web@v1"}, Kit: Layer{Core: "K"}, Studio: ghFacts()})
	k, s := strings.Index(b.Core, "## Kit"), strings.Index(b.Core, "## Studio")
	if k < 0 || s < 0 || s < k {
		t.Fatalf("want Kit then Studio:\n%s", b.Core)
	}
}

// Spill mode must fit whatever the name lengths: the core lists names only
// while they fit, then counts the rest.
func TestStudioSpillFitsManyLongNames(t *testing.T) {
	var f StudioFacts
	for i := range 200 {
		f.Destinations = append(f.Destinations, StudioDestination{Name: fmt.Sprintf("github-enterprise-mirror-%03d", i), Upstream: "https://ghe.example.com"})
	}
	f.Egress, f.EgressKnown = []string{"a.example.com"}, true
	l := Studio(f)
	if len(l.Core) > BudgetStudio {
		t.Fatalf("core %d bytes > budget %d", len(l.Core), BudgetStudio)
	}
	if !strings.Contains(l.Core, "more in studio/destinations.md") {
		t.Fatalf("core must point at the leaf for the rest:\n%s", l.Core)
	}
	b := Compile(Inputs{Session: SessionFacts{Kind: KindStanding, Name: "n", Project: "p", Role: "r"}, Studio: f})
	if len(b.Warnings) != 0 {
		t.Fatalf("a generated layer must never truncate: %v", b.Warnings)
	}
}
