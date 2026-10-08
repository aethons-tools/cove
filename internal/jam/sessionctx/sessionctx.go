// Package sessionctx compiles a Jam session's layered context — Boilerplate,
// Kit (and, in later slices, Studio, Project, Role, Jam) — into a Bundle: a
// short always-on core, delivered as an appended system prompt every turn, plus
// leaf files the agent opens on demand under Dir. Pure: no I/O. See
// docs/usage/jam/session-context.md.
package sessionctx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// EndpointHeader marks every response of Jam's GET /context, so a client can
// tell "this Jam has no /context" (an unmarked 404) from a marked one.
const EndpointHeader = "X-Jam-Context"

// Dir is where cove-master writes a bundle inside the cove.
const Dir = "/agent-data/context"

// Session kinds (mirror jam.SessionKind*; "" is ephemeral).
const (
	KindEphemeral = "ephemeral"
	KindPersonal  = "personal"
	KindStanding  = "standing"
)

// Layer names, in delivery order; each is also the leaf directory name.
const (
	LayerBoilerplate = "boilerplate"
	LayerKit         = "kit"
	LayerStudio      = "studio"
	LayerProject     = "project"
	LayerRole        = "role"
	LayerJam         = "jam"
)

// Core budgets in bytes.
const (
	BudgetBoilerplate = 2400
	BudgetKit         = 800
	BudgetStudio      = 1600
)

// Precedence is stated once, verbatim, at the top of every core.
const Precedence = "Sandbox hardening is enforced and cannot be overridden. Otherwise, on conflict the later section wins: " +
	"Jam rules > Role > Project > Studio > Kit > Boilerplate. Your task (the first message) works within all of them."

// Leaf is one on-demand file of a layer.
type Leaf struct {
	Name     string `json:"name" yaml:"name"`           // file name under the layer dir, e.g. "tools.md"
	ReadWhen string `json:"read_when" yaml:"read-when"` // one line: when to open it
	Body     string `json:"body" yaml:"body"`           // markdown
}

// Layer is one layer's contribution: an always-on core and on-demand leaves.
type Layer struct {
	Core   string `json:"core" yaml:"core"`
	Leaves []Leaf `json:"leaves,omitempty" yaml:"leaves,omitempty"`
}

// Empty reports whether the layer contributes nothing.
func (l Layer) Empty() bool { return strings.TrimSpace(l.Core) == "" && len(l.Leaves) == 0 }

// SessionFacts identify the session the bundle is for.
type SessionFacts struct {
	Kind    string // KindEphemeral ("" too) | KindPersonal | KindStanding
	Name    string // standing session name
	Project string
	Role    string
	Owner   string // personal session owner
	Unit    string // ephemeral work unit (ticket); "" = none, so no default recipient
	Kit     string // KitRef.String(), "" = no kit
}

// Inputs are everything Compile needs. Later slices add Studio/Project/Role/Jam.
type Inputs struct {
	Session SessionFacts
	Kit     Layer
	Studio  StudioFacts
	Project Layer // authored, with resources already rendered (ProjectLayer)
	Role    Layer
	Jam     Layer
}

// Bundle is the compiled context. Files are relative to Dir.
type Bundle struct {
	Core        string            `json:"core"`
	Files       map[string]string `json:"files"`
	Fingerprint string            `json:"fingerprint"`
	Layers      map[string]string `json:"layers"` // layer → fingerprint, for change notices
	Warnings    []string          `json:"warnings,omitempty"`
}

var leafNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*\.md$`)

// ValidLeafName reports whether name is a safe single-segment leaf file name.
// CORE-full.md is reserved for truncation.
func ValidLeafName(name string) bool {
	return leafNameRE.MatchString(name) && name != "CORE.md" && name != "INDEX.md"
}

type section struct {
	name, title string
	layer       Layer
	budget      int
}

// Compile builds the bundle. It never fails: unsafe leaves are dropped and
// over-budget cores truncated, each with a Warning.
func Compile(in Inputs) Bundle {
	kitTitle := "Kit"
	if in.Session.Kit != "" {
		kitTitle += " — " + in.Session.Kit
	}
	sections := []section{
		{LayerBoilerplate, "Boilerplate", Boilerplate(in.Session), BudgetBoilerplate},
		{LayerKit, kitTitle, in.Kit, BudgetKit},
		{LayerStudio, "Studio", Studio(in.Studio), BudgetStudio},
		{LayerProject, titled("Project", in.Session.Project), in.Project, BudgetProject},
		{LayerRole, titled("Role", in.Session.Role), in.Role, BudgetRole},
		{LayerJam, "Jam", in.Jam, BudgetJam},
	}
	b := Bundle{Files: map[string]string{}, Layers: map[string]string{}}
	var core strings.Builder
	fmt.Fprintf(&core, "# Session context\n\n%s\n\nDetail lives in %s/ — open a file only when its \"read when\" matches your task. Map: %s/INDEX.md\n", Precedence, Dir, Dir)
	type row struct{ path, when string }
	var index []row
	for _, s := range sections {
		if s.layer.Empty() {
			continue
		}
		l := s.layer
		c := strings.TrimSpace(l.Core)
		if len(c) > s.budget {
			b.Warnings = append(b.Warnings, fmt.Sprintf("%s core is %d bytes (budget %d); truncated", s.name, len(c), s.budget))
			note := fmt.Sprintf("\n(truncated — see %s/CORE-full.md)", s.name)
			l.Leaves = append(slices.Clone(l.Leaves), Leaf{Name: "CORE-full.md", ReadWhen: "the " + s.name + " core above was truncated and you need the rest", Body: c})
			c = truncateCore(c, s.budget-len(note)) + note
		}
		fmt.Fprintf(&core, "\n## %s\n", s.title)
		if c != "" {
			core.WriteString(c + "\n")
		}
		h := sha256.New()
		h.Write([]byte(c))
		for _, lf := range l.Leaves {
			if !ValidLeafName(lf.Name) && lf.Name != "CORE-full.md" {
				b.Warnings = append(b.Warnings, fmt.Sprintf("%s leaf %q dropped: unsafe name", s.name, lf.Name))
				continue
			}
			p := s.name + "/" + lf.Name
			when := oneLine(lf.ReadWhen)
			fmt.Fprintf(&core, "- %s — read when %s\n", p, when)
			body := fmt.Sprintf("---\nsummary: %s layer — %s\nread_when: %s\ntier: leaf\n---\n\n%s\n", s.name, lf.Name, when, strings.TrimSpace(lf.Body))
			b.Files[p] = body
			index = append(index, row{p, when})
			h.Write([]byte(p + "\x00" + body + "\x00"))
		}
		b.Layers[s.name] = hex.EncodeToString(h.Sum(nil))
	}
	var idx strings.Builder
	idx.WriteString("# Session context index\n\nOpen a file only when its \"read when\" matches your task.\n\n| File | Read when |\n|------|-----------|\n")
	for _, r := range index {
		fmt.Fprintf(&idx, "| %s | %s |\n", r.path, r.when)
	}
	b.Files["INDEX.md"] = idx.String()
	for _, a := range []struct {
		name string
		l    Layer
	}{{LayerKit, in.Kit}, {LayerProject, in.Project}, {LayerRole, in.Role}, {LayerJam, in.Jam}} {
		b.Warnings = append(b.Warnings, lintAuthored(a.name, a.l.Core, in.Studio)...)
	}
	b.Core = core.String()
	b.Fingerprint = fingerprint(b.Core, b.Files)
	return b
}

// truncateCore cuts s to at most max bytes, at the last newline if there is
// one, else at a rune boundary.
func truncateCore(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 0 {
		return ""
	}
	cut := s[:max]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		return strings.TrimRight(cut[:i], "\n")
	}
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(s, "|", "/")
}

func fingerprint(core string, files map[string]string) string {
	h := sha256.New()
	h.Write([]byte(core + "\x00"))
	for _, k := range slices.Sorted(maps.Keys(files)) {
		h.Write([]byte(k + "\x00" + files[k] + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// lintAuthored flags an authored core restating a fact the Studio layer owns
// (an egress host or a message target). Heuristic and advisory: it only warns.
func lintAuthored(layer, core string, f StudioFacts) []string {
	var out []string
	for _, h := range f.Egress {
		if containsToken(strings.ToLower(core), strings.ToLower(strings.TrimPrefix(h, ".")), isHostChar) {
			out = append(out, fmt.Sprintf("%s core restates egress host %s (owned by the studio layer)", layer, h))
		}
	}
	for _, t := range f.Targets {
		if containsToken(core, t.Target, isTargetChar) {
			out = append(out, fmt.Sprintf("%s core restates message target %s (owned by the studio layer)", layer, t.Target))
		}
	}
	return out
}

func titled(t, name string) string {
	if name == "" {
		return t
	}
	return t + " — " + name
}

// layerOrder is the delivery order, for reporting changes.
var layerOrder = []string{LayerBoilerplate, LayerKit, LayerStudio, LayerProject, LayerRole, LayerJam}

// LayerOrder returns the layer names in delivery order.
func LayerOrder() []string { return slices.Clone(layerOrder) }

// ChangedLayers names the layers that differ between two bundles (changed,
// added or removed), in delivery order; nil when the bundles are identical.
func ChangedLayers(old, cur Bundle) []string {
	if old.Fingerprint == cur.Fingerprint {
		return nil
	}
	var out []string
	for _, n := range layerOrder {
		if old.Layers[n] != cur.Layers[n] {
			out = append(out, n)
		}
	}
	return out
}

func isHostChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.'
}

func isTargetChar(c byte) bool { return isHostChar(c) || c == ':' || c == '_' }

// containsToken reports whether tok occurs in text as a whole token: not
// preceded or followed by a token character (trailing '.'/':' punctuation that
// ends a sentence or clause still counts as a boundary).
func containsToken(text, tok string, isTok func(byte) bool) bool {
	if tok == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(text[i:], tok)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(tok)
		before := start == 0 || !isTok(text[start-1])
		after := end == len(text) || !isTok(text[end])
		if !after {
			// Trailing sentence punctuation ('.', ':') still ends the token.
			k := end
			for k < len(text) && (text[k] == '.' || text[k] == ':') {
				k++
			}
			after = k == len(text) || !isTok(text[k])
		}
		if before && after {
			return true
		}
		i = start + 1
	}
}
