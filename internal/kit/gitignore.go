package kit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// managedIgnores are the kit-relative paths at-cove keeps out of version
// control: the generated build context, the runtime state (lockfile + state.json),
// and the (deferred) source-control-excluded local override layer.
var managedIgnores = []string{".build/", ".local/", ".state/"}

// EnsureGitignore makes <kitDir>/.gitignore exist and contain every managed
// entry, so a tracked kit never commits generated/runtime artifacts. It creates
// the file when absent and appends only the missing managed entries to an
// existing file (preserving the owner's customizations). Idempotent.
func EnsureGitignore(kitDir string) error {
	p := filepath.Join(kitDir, ".gitignore")
	existing, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	have := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, m := range managedIgnores {
		if !have[m] {
			missing = append(missing, m)
		}
	}

	if len(existing) == 0 {
		var b strings.Builder
		b.WriteString("# Managed by at-cove — generated and runtime artifacts.\n")
		for _, m := range managedIgnores {
			b.WriteString(m + "\n")
		}
		return os.WriteFile(p, []byte(b.String()), 0o644)
	}
	if len(missing) == 0 {
		return nil
	}
	var b strings.Builder
	b.Write(existing)
	if !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("# Added by at-cove\n")
	for _, m := range missing {
		b.WriteString(m + "\n")
	}
	return os.WriteFile(p, []byte(b.String()), 0o644)
}
