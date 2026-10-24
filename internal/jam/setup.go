package jam

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aethons-tools/cove/internal/jam/linediff"
)

// Setup section names, in render order (spec §1).
const (
	SectionIdentity     = "identity"
	SectionContext      = "context"
	SectionEgress       = "egress"
	SectionDestinations = "destinations"
	SectionEnv          = "env"
	SectionModel        = "model"
	SectionKit          = "kit"
	SectionSeed         = "seed"
	SectionMCP          = "mcp"
	SectionPolicy       = "policy"
)

// Setup section sources.
const (
	SourceSnapshot    = "snapshot"    // what Jam recorded delivering
	SourceRecomputed  = "recomputed"  // a studio part with no snapshot, rendered from config now
	SourceImage       = "image"       // read from the built image on the launcher host
	SourceConfig      = "config"      // config as of now
	SourceUnavailable = "unavailable" // could not be produced; Reason says why
)

var setupSectionOrder = []string{SectionIdentity, SectionContext, SectionEgress, SectionDestinations, SectionEnv,
	SectionModel, SectionKit, SectionSeed, SectionMCP, SectionPolicy}

// SetupSection is one section of a StudioSetup. Body is plain text (already
// redacted); Diff, when Diffed, is the line diff of this studio's body (-)
// against a raise now (+).
type SetupSection struct {
	Name         string          `json:"name"`
	Source       string          `json:"source"`
	Stale        bool            `json:"stale,omitempty"`
	At           time.Time       `json:"at,omitzero"`
	Reason       string          `json:"reason,omitempty"`
	Body         string          `json:"body"`
	Diffed       bool            `json:"diffed,omitempty"`
	Diff         []linediff.Line `json:"diff,omitempty"`
	DiffTooLarge bool            `json:"diff_too_large,omitempty"`
}

// StudioSetup is what an agent is set up with: for a role ("role p/r", what a
// raise now would get) or a studio ("studio <id>", what it got).
type StudioSetup struct {
	Subject  string         `json:"subject"`
	Note     string         `json:"note,omitempty"`
	Sections []SetupSection `json:"sections"`
}

// SetupSectionNames lists every section, in render order.
func SetupSectionNames() []string { return slices.Clone(setupSectionOrder) }

// ParseSetupSections reads a ?sections= / --section list: comma-separated,
// deduped, returned in render order; empty = all. An unknown name is an error
// naming it.
func ParseSetupSections(csv string) ([]string, error) {
	want := map[string]bool{}
	for n := range strings.SplitSeq(csv, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !slices.Contains(setupSectionOrder, n) {
			return nil, fmt.Errorf("unknown section %q (want %s)", n, strings.Join(setupSectionOrder, ", "))
		}
		want[n] = true
	}
	if len(want) == 0 {
		return SetupSectionNames(), nil
	}
	var out []string
	for _, n := range setupSectionOrder {
		if want[n] {
			out = append(out, n)
		}
	}
	return out, nil
}

// unavailable is a section that could not be produced, with a fixed reason.
func unavailable(name, reason string) SetupSection {
	return SetupSection{Name: name, Source: SourceUnavailable, Reason: reason}
}

// RenderSetupText writes the CLI's text form: one block per section headed
// "== <name> (<source>[, stale][, <at>])", the reason in parentheses, the
// body, then the diff when one was asked for.
func RenderSetupText(w io.Writer, s StudioSetup) {
	fmt.Fprintf(w, "# %s\n", s.Subject)
	if s.Note != "" {
		fmt.Fprintf(w, "(%s)\n", s.Note)
	}
	for _, sec := range s.Sections {
		head := sec.Source
		if sec.Stale {
			head += ", stale"
		}
		if !sec.At.IsZero() {
			head += ", " + sec.At.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "\n== %s (%s)\n", sec.Name, head)
		if sec.Reason != "" {
			fmt.Fprintf(w, "(%s)\n", sec.Reason)
		}
		if sec.Body != "" {
			io.WriteString(w, ensureNL(sec.Body))
		}
		switch {
		case !sec.Diffed:
		case sec.DiffTooLarge:
			fmt.Fprintln(w, "-- diff too large to compute")
		case !diffChanged(sec.Diff):
			fmt.Fprintln(w, "-- same as a raise now")
		default:
			fmt.Fprintln(w, "-- diff (- this studio, + a raise now)")
			for _, l := range sec.Diff {
				fmt.Fprintf(w, "%s %s\n", l.Op, l.Text)
			}
		}
	}
}

func diffChanged(lines []linediff.Line) bool {
	return slices.ContainsFunc(lines, func(l linediff.Line) bool { return l.Op == "+" || l.Op == "-" })
}

// writeTable writes an aligned table (two spaces between columns).
func writeTable(w io.Writer, head []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(head, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

// flat keeps a value on one line, so it cannot fake a row or a header.
func flat(s string) string { return strings.NewReplacer("\n", `\n`, "\r", `\r`).Replace(s) }

func ensureNL(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func joinOr(ss []string, none string) string {
	if len(ss) == 0 {
		return none
	}
	return strings.Join(ss, ", ")
}

// shortDigest is the first 12 characters of a digest or id ("unknown" for none).
func shortDigest(d string) string {
	switch {
	case d == "":
		return "unknown"
	case len(d) > 12:
		return d[:12]
	}
	return d
}
