package sessionctx

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateLayer(t *testing.T) {
	ok := Layer{Core: "c", Leaves: []Leaf{{Name: "a.md", ReadWhen: "w", Body: "b"}}}
	if err := ValidateLayer(ok, 10); err != nil {
		t.Fatalf("valid layer refused: %v", err)
	}
	for name, tc := range map[string]struct {
		l    Layer
		want string
	}{
		"over budget":  {Layer{Core: strings.Repeat("x", 11)}, "11 bytes"},
		"bad name":     {Layer{Leaves: []Leaf{{Name: "../x.md", ReadWhen: "w"}}}, `"../x.md"`},
		"dup name":     {Layer{Leaves: []Leaf{{Name: "a.md", ReadWhen: "w"}, {Name: "a.md", ReadWhen: "w"}}}, "twice"},
		"no read-when": {Layer{Leaves: []Leaf{{Name: "a.md"}}}, "read-when"},
		"huge leaves":  {Layer{Leaves: []Leaf{{Name: "a.md", ReadWhen: "w", Body: strings.Repeat("x", MaxLeafBytes+1)}}}, "leaf"},
	} {
		if err := ValidateLayer(tc.l, 10); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want mention of %q", name, err, tc.want)
		}
	}
}

func TestValidateResources(t *testing.T) {
	if err := ValidateResources([]Resource{{Name: "cove", Kind: "repo", Ref: "aethons-tools/cove"}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]Resource{
		{{Name: "", Kind: "repo", Ref: "x"}},
		{{Name: "x", Kind: "wiki", Ref: "x"}},
		{{Name: "x", Kind: "url", Ref: ""}},
		make([]Resource, MaxResources+1),
	} {
		if err := ValidateResources(bad); err == nil {
			t.Errorf("want an error for %+v", bad[0])
		}
	}
}

func TestProjectLayerRendersResources(t *testing.T) {
	l := ProjectLayer(Layer{Core: "Ship the thing."}, []Resource{{Name: "cove", Kind: "repo", Ref: "aethons-tools/cove", Note: "main\nrepo"}})
	if !strings.Contains(l.Core, "Ship the thing.") || !strings.Contains(l.Core, "1 project resource") {
		t.Fatalf("core: %q", l.Core)
	}
	if len(l.Leaves) != 1 || l.Leaves[0].Name != "resources.md" || !strings.Contains(l.Leaves[0].Body, "| cove | repo | aethons-tools/cove | main repo |") {
		t.Fatalf("leaves: %+v", l.Leaves)
	}
	if got := ProjectLayer(Layer{}, nil); !got.Empty() {
		t.Fatalf("nothing in, nothing out: %+v", got)
	}
}

func TestCompileAuthoredOrderAndLint(t *testing.T) {
	b := Compile(Inputs{
		Session: SessionFacts{Kind: KindStanding, Name: "n", Project: "acme", Role: "dev", Kit: "web@v1"},
		Kit:     Layer{Core: "K"},
		Studio:  StudioFacts{Egress: []string{"example.org"}, EgressKnown: true},
		Project: Layer{Core: "PROJECT"},
		Role:    Layer{Core: "ROLE uses example.org"},
		Jam:     Layer{Core: "JAM"},
	})
	order := []string{"## Kit", "## Studio", "## Project — acme", "## Role — dev", "## Jam"}
	last := -1
	for _, h := range order {
		i := strings.Index(b.Core, h)
		if i < 0 || i < last {
			t.Fatalf("want %v in order:\n%s", order, b.Core)
		}
		last = i
	}
	if !strings.Contains(strings.Join(b.Warnings, "\n"), "role core restates egress host example.org") {
		t.Fatalf("authored layers are linted too: %v", b.Warnings)
	}
}

// The leaf list is printed in the core, so it is bounded too.
func TestValidateLayerBoundsLeafList(t *testing.T) {
	var many Layer
	for i := range MaxLeaves + 1 {
		many.Leaves = append(many.Leaves, Leaf{Name: fmt.Sprintf("l%02d.md", i), ReadWhen: "w"})
	}
	if err := ValidateLayer(many, 100); err == nil || !strings.Contains(err.Error(), "leaves") {
		t.Errorf("too many leaves: %v", err)
	}
	long := Layer{Leaves: []Leaf{{Name: "a.md", ReadWhen: strings.Repeat("w", MaxReadWhen+1)}}}
	if err := ValidateLayer(long, 100); err == nil || !strings.Contains(err.Error(), "read-when") {
		t.Errorf("long read-when: %v", err)
	}
}
