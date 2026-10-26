package usersecret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCredentialsDocExampleLoads guards against doc drift: every ```yaml block
// in docs/usage/jam/credentials.md that is a credentials.yml example (it has
// both minters: and credentials:) must parse through LoadFlat.
func TestCredentialsDocExampleLoads(t *testing.T) {
	data, err := os.ReadFile("../../docs/usage/jam/credentials.md")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for i, b := range yamlBlocks(string(data)) {
		if !strings.Contains(b, "minters:") || !strings.Contains(b, "credentials:") {
			continue // e.g. the serve config's name-only demand list
		}
		found++
		p := filepath.Join(t.TempDir(), "credentials.yml")
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFlat(p); err != nil {
			t.Errorf("credentials.md yaml block %d does not load: %v\n%s", i, err, b)
		}
	}
	if found == 0 {
		t.Fatal("no credentials.yml example (a yaml block with minters: and credentials:) in credentials.md")
	}
}

// yamlBlocks returns the bodies of the ```yaml fenced blocks in md.
func yamlBlocks(md string) []string {
	var out, cur []string
	in := false
	for _, line := range strings.Split(md, "\n") {
		switch {
		case !in && strings.TrimSpace(line) == "```yaml":
			in, cur = true, nil
		case in && strings.TrimSpace(line) == "```":
			in = false
			out = append(out, strings.Join(cur, "\n")+"\n")
		case in:
			cur = append(cur, line)
		}
	}
	return out
}
