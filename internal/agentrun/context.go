package agentrun

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// writeContext writes b under dir — CORE.md plus every file — by building
// dir+".new" and swapping it in, so the agent never sees a half-written tree
// and nothing from a previous bundle survives. Every path must be local.
func writeContext(dir string, b sessionctx.Bundle) error {
	files := map[string]string{"CORE.md": b.Core}
	for p, body := range b.Files {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("session context: refusing non-local path %q", p)
		}
		files[p] = body
	}
	staging := dir + ".new"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	for p, body := range files {
		full := filepath.Join(staging, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(staging, dir)
}

// clearContext removes any bundle left under dir (best effort), so a session
// running without context never reads a previous raise's: SANDBOX.md treats an
// existing CORE.md as "this is a Jam session".
func clearContext(dir string) {
	_ = os.RemoveAll(dir + ".new")
	_ = os.RemoveAll(dir)
}

// short abbreviates a fingerprint for logs.
func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
