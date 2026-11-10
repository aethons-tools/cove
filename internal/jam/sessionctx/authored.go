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

// FieldError is one problem with an authored layer or resource list, at a
// field path in the admin API's wire names: core, leaves[2].readWhen,
// resources[0].kind; a list-wide problem names the list (leaves, resources).
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// FieldErrors is every problem found; as an error, the messages joined.
type FieldErrors []FieldError

func (fe FieldErrors) Error() string {
	msgs := make([]string, len(fe))
	for i, e := range fe {
		msgs[i] = e.Message
	}
	return strings.Join(msgs, "; ")
}

// Err is fe as an error, nil when there are none.
func (fe FieldErrors) Err() error {
	if len(fe) == 0 {
		return nil
	}
	return fe
}

func (fe *FieldErrors) add(field, format string, a ...any) {
	*fe = append(*fe, FieldError{Field: field, Message: fmt.Sprintf(format, a...)})
}

// ValidateLayer checks an authored layer (CheckLayer), reporting every
// problem in one error.
func ValidateLayer(l Layer, budget int) error { return CheckLayer(l, budget).Err() }

// CheckLayer checks an authored layer: core within budget; leaves safely
// named, unique, each with a read-when; leaf bodies within MaxLeafBytes. It
// returns every problem found (nil: valid).
func CheckLayer(l Layer, budget int) FieldErrors {
	var fe FieldErrors
	if n := len(strings.TrimSpace(l.Core)); n > budget {
		fe.add("core", "core is %d bytes; the budget is %d — move detail into leaves", n, budget)
	}
	if len(l.Leaves) > MaxLeaves {
		fe.add("leaves", "%d leaves; at most %d", len(l.Leaves), MaxLeaves)
	}
	seen := map[string]bool{}
	total := 0
	for i, lf := range l.Leaves {
		at := fmt.Sprintf("leaves[%d].", i)
		switch {
		case !ValidLeafName(lf.Name):
			fe.add(at+"name", "leaf name %q: want lowercase [a-z0-9._-] ending .md (not CORE.md or INDEX.md)", lf.Name)
		case seen[lf.Name]:
			fe.add(at+"name", "leaf %q appears twice", lf.Name)
		}
		seen[lf.Name] = true
		if strings.TrimSpace(lf.ReadWhen) == "" {
			fe.add(at+"readWhen", "leaf %q needs a read-when", lf.Name)
		} else if len(lf.ReadWhen) > MaxReadWhen {
			fe.add(at+"readWhen", "leaf %q: read-when is %d bytes; at most %d", lf.Name, len(lf.ReadWhen), MaxReadWhen)
		}
		total += len(lf.Body)
	}
	if total > MaxLeafBytes {
		fe.add("leaves", "leaf bodies total %d bytes; at most %d", total, MaxLeafBytes)
	}
	return fe
}

// ValidateResources checks a project's resource list (CheckResources),
// reporting every problem in one error.
func ValidateResources(rs []Resource) error { return CheckResources(rs).Err() }

// CheckResources checks a project's resource list, returning every problem
// found (nil: valid).
func CheckResources(rs []Resource) FieldErrors {
	var fe FieldErrors
	if len(rs) > MaxResources {
		fe.add("resources", "%d resources; at most %d", len(rs), MaxResources)
	}
	seen := map[string]bool{}
	for i, r := range rs {
		at, label := fmt.Sprintf("resources[%d].", i), fmt.Sprintf("resource %q", r.Name)
		if strings.TrimSpace(r.Name) == "" {
			fe.add(at+"name", "resource %d: name is required", i+1)
			label = fmt.Sprintf("resource %d", i+1)
		} else if seen[r.Name] {
			fe.add(at+"name", "%s: name already used", label)
		}
		seen[r.Name] = true
		if !resourceKinds[r.Kind] {
			fe.add(at+"kind", "%s: kind %q; want repo, doc, tracker or url", label, r.Kind)
		}
		if strings.TrimSpace(r.Ref) == "" {
			fe.add(at+"ref", "%s: ref is required", label)
		}
	}
	return fe
}

// DeliveredCoreBytes is how many bytes layer l (named as in Layer*, e.g.
// LayerRole; a project's with its resources rendered by ProjectLayer) adds to
// a session's always-on core below its heading: the authored core plus one
// pointer line per leaf, before any truncation. 0 for an empty layer.
func DeliveredCoreBytes(layer string, l Layer) int {
	if l.Empty() {
		return 0
	}
	n := 0
	if c := strings.TrimSpace(l.Core); c != "" {
		n = len(c) + 1
	}
	for _, lf := range l.Leaves {
		n += len(leafPointer(layer, lf))
	}
	return n
}

// leafPointer is a leaf's line in the core.
func leafPointer(layer string, lf Leaf) string {
	return fmt.Sprintf("- %s/%s — read when %s\n", layer, lf.Name, oneLine(lf.ReadWhen))
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
