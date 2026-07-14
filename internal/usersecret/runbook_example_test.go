package usersecret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReferenceRunbookSecretsExampleLoads guards against doc drift: every
// ```yaml block in the reference kit's RUNBOOK that declares a `minters:` section
// (i.e. the machine-side secrets.yml example) must actually parse through Load.
// A bare-scalar Source field (e.g. `app-key: /path` instead of
// `app-key: { value: /path }`) would silently ship a non-loadable example.
func TestReferenceRunbookSecretsExampleLoads(t *testing.T) {
	data, err := os.ReadFile("../../kits/reference-worker/RUNBOOK.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := yamlBlocks(string(data))
	found := 0
	for i, b := range blocks {
		if !strings.Contains(b, "minters:") {
			continue
		}
		found++
		dir := t.TempDir()
		p := filepath.Join(dir, "secrets.yml")
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p, filepath.Join(dir, "none.yml")); err != nil {
			t.Errorf("RUNBOOK secrets.yml example block #%d does not Load: %v\n---\n%s", i, err, b)
		}
	}
	if found == 0 {
		t.Fatal("no ```yaml block containing `minters:` found in RUNBOOK.md — did the example move?")
	}
}

// yamlBlocks returns the bodies of all ```yaml fenced code blocks in md.
func yamlBlocks(md string) []string {
	var out []string
	lines := strings.Split(md, "\n")
	inBlock := false
	var cur []string
	for _, ln := range lines {
		if !inBlock {
			if strings.TrimSpace(ln) == "```yaml" {
				inBlock, cur = true, nil
			}
			continue
		}
		if strings.TrimSpace(ln) == "```" {
			out = append(out, strings.Join(cur, "\n"))
			inBlock = false
			continue
		}
		cur = append(cur, ln)
	}
	return out
}
