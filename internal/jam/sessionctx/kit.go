package sessionctx

import (
	"fmt"
	"slices"
	"strings"
)

// ToolsLeaf is the kit leaf generated from the kit's build-args.
const ToolsLeaf = "tools.md"

// KitLayer is the kit's layer: its prompt as the core, its notes as leaves,
// plus a generated tools.md listing the build-args the image was built with
// (X_VERSION keys render as the tool "x").
func KitLayer(prompt string, notes []Leaf, buildArgs map[string]string) Layer {
	l := Layer{Core: prompt, Leaves: slices.Clone(notes)}
	if len(buildArgs) == 0 {
		return l
	}
	type row struct{ tool, value string }
	var rows []row
	for k, v := range buildArgs {
		tool := k
		if p, ok := strings.CutSuffix(k, "_VERSION"); ok && p != "" {
			tool = strings.ReplaceAll(strings.ToLower(p), "_", "-")
		}
		rows = append(rows, row{tool, v})
	}
	slices.SortFunc(rows, func(a, b row) int { return strings.Compare(a.tool, b.tool) })
	var b strings.Builder
	b.WriteString("Tools and versions this kit's image was built with (its build-args):\n\n| Tool | Version / value |\n|------|-----------------|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s |\n", oneLine(r.tool), oneLine(r.value))
	}
	l.Leaves = append(l.Leaves, Leaf{Name: ToolsLeaf, ReadWhen: "you need which tool versions the image has", Body: b.String()})
	return l
}
