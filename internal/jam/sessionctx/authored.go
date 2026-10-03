package sessionctx

import (
	"fmt"
	"strings"
)

// Authored-layer limits, enforced when an operator writes a layer.
const (
	BudgetProject = 1200
	BudgetRole    = 1200
	BudgetJam     = 800
	MaxLeafBytes  = 64 << 10 // per layer, all leaf bodies
	MaxResources  = 50
	MaxLeaves     = 20  // each leaf adds a pointer line to the core
	MaxReadWhen   = 160 // bytes per read-when line
)

// ResourcesLeaf is the project leaf generated from its Resources.
const ResourcesLeaf = "resources.md"

var resourceKinds = map[string]bool{"repo": true, "doc": true, "tracker": true, "url": true}

// Resource is one project resource (a repo, doc, tracker or URL) sessions
// should know about; rendered into the project layer's resources.md leaf.
type Resource struct {
	Name string `json:"name" yaml:"name"`
	Kind string `json:"kind" yaml:"kind"` // repo | doc | tracker | url
	Ref  string `json:"ref" yaml:"ref"`
	Note string `json:"note,omitempty" yaml:"note,omitempty"`
}

// ValidateLayer checks an authored layer: core within budget; leaves safely
// named, unique, each with a read-when; leaf bodies within MaxLeafBytes.
func ValidateLayer(l Layer, budget int) error {
	if n := len(strings.TrimSpace(l.Core)); n > budget {
		return fmt.Errorf("core is %d bytes; the budget is %d — move detail into leaves", n, budget)
	}
	if len(l.Leaves) > MaxLeaves {
		return fmt.Errorf("%d leaves; at most %d", len(l.Leaves), MaxLeaves)
	}
	seen := map[string]bool{}
	total := 0
	for _, lf := range l.Leaves {
		if !ValidLeafName(lf.Name) {
			return fmt.Errorf("leaf name %q: want lowercase [a-z0-9._-] ending .md (not CORE.md or INDEX.md)", lf.Name)
		}
		if seen[lf.Name] {
			return fmt.Errorf("leaf %q appears twice", lf.Name)
		}
		seen[lf.Name] = true
		if strings.TrimSpace(lf.ReadWhen) == "" {
			return fmt.Errorf("leaf %q needs a read-when", lf.Name)
		}
		if len(lf.ReadWhen) > MaxReadWhen {
			return fmt.Errorf("leaf %q: read-when is %d bytes; at most %d", lf.Name, len(lf.ReadWhen), MaxReadWhen)
		}
		total += len(lf.Body)
	}
	if total > MaxLeafBytes {
		return fmt.Errorf("leaf bodies total %d bytes; at most %d", total, MaxLeafBytes)
	}
	return nil
}

// ValidateResources checks a project's resource list.
func ValidateResources(rs []Resource) error {
	if len(rs) > MaxResources {
		return fmt.Errorf("%d resources; at most %d", len(rs), MaxResources)
	}
	for i, r := range rs {
		switch {
		case strings.TrimSpace(r.Name) == "":
			return fmt.Errorf("resource %d: name is required", i+1)
		case !resourceKinds[r.Kind]:
			return fmt.Errorf("resource %q: kind %q; want repo, doc, tracker or url", r.Name, r.Kind)
		case strings.TrimSpace(r.Ref) == "":
			return fmt.Errorf("resource %q: ref is required", r.Name)
		}
	}
	return nil
}

// ProjectLayer is the project's authored layer plus its resources, rendered
// as a resources.md leaf with a pointer line in the core.
func ProjectLayer(l Layer, rs []Resource) Layer {
	if len(rs) == 0 {
		return l
	}
	var b strings.Builder
	b.WriteString("| Name | Kind | Ref | Note |\n|------|------|-----|------|\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", oneLine(r.Name), oneLine(r.Kind), oneLine(r.Ref), oneLine(r.Note))
	}
	out := Layer{Core: strings.TrimSpace(l.Core + fmt.Sprintf("\n%d project resource(s) (repos, docs, trackers) are listed in project/resources.md.", len(rs)))}
	out.Leaves = append(append([]Leaf(nil), l.Leaves...), Leaf{Name: ResourcesLeaf, ReadWhen: "you need the project's repos, docs or trackers", Body: b.String()})
	return out
}
