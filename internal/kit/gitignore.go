package kit

import (
	"os"
	"path/filepath"
)

// gitignoreContent is the kit .gitignore at-cove manages: the generated build
// context and the (deferred) source-control-excluded local override layer.
const gitignoreContent = `# Managed by at-cove — generated build context and local overrides.
.build/
.local/
`

// EnsureGitignore writes <kitDir>/.gitignore when it is absent, so a tracked kit
// never accidentally commits the generated .build/ (or .local/). An existing
// file is left untouched — the kit owner may customize it.
func EnsureGitignore(kitDir string) error {
	p := filepath.Join(kitDir, ".gitignore")
	switch _, err := os.Stat(p); {
	case err == nil:
		return nil // present; don't clobber
	case os.IsNotExist(err):
		return os.WriteFile(p, []byte(gitignoreContent), 0o644)
	default:
		return err
	}
}
