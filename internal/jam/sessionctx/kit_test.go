package sessionctx

import (
	"strings"
	"testing"
)

func TestKitLayer(t *testing.T) {
	l := KitLayer("Work on cove.", []Leaf{{Name: "release.md", ReadWhen: "w", Body: "B"}}, map[string]string{"GO_VERSION": "1.27.1", "HADOLINT_VERSION": "v2.15.1", "CC_SKILLS_GOLANG_VERSION": "2.0.0", "PROFILE": "ci"})
	if l.Core != "Work on cove." || len(l.Leaves) != 2 || l.Leaves[0].Name != "release.md" || l.Leaves[1].Name != ToolsLeaf {
		t.Fatalf("layer = %+v", l)
	}
	tools := l.Leaves[1].Body
	for _, want := range []string{"| cc-skills-golang | 2.0.0 |", "| go | 1.27.1 |", "| hadolint | v2.15.1 |", "| PROFILE | ci |"} {
		if !strings.Contains(tools, want) {
			t.Errorf("tools.md missing %q:\n%s", want, tools)
		}
	}
	if strings.Index(tools, "cc-skills-golang") > strings.Index(tools, "| go |") {
		t.Error("rows must be sorted")
	}
	if got := KitLayer("", nil, nil); !got.Empty() {
		t.Fatalf("nothing in, nothing out: %+v", got)
	}
}
