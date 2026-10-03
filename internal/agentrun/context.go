package agentrun

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// writeContext writes b into a fresh version dir <parent>/context.d/<fp12> and
// atomically points dir (a symlink) at it, then removes superseded versions, so
// a reader always sees one complete tree. Every path must be local.
func writeContext(dir string, b sessionctx.Bundle) error {
	files := map[string]string{"CORE.md": b.Core}
	for p, body := range b.Files {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("session context: refusing non-local path %q", p)
		}
		files[p] = body
	}
	parent := filepath.Dir(dir)
	versions := filepath.Join(parent, "context.d")
	name := short(b.Fingerprint)
	if name == "" {
		name = "nofp"
	}
	target := filepath.Join(versions, name)
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	for p, body := range files {
		full := filepath.Join(target, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return err
		}
	}
	// A pre-symlink plain directory (older cove-master) is removed once.
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	tmp := dir + ".new"
	_ = os.RemoveAll(tmp)
	if err := os.Symlink(filepath.Join("context.d", name), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return err
	}
	entries, _ := os.ReadDir(versions)
	for _, e := range entries {
		if e.Name() != name {
			_ = os.RemoveAll(filepath.Join(versions, e.Name()))
		}
	}
	return nil
}

// clearContext removes any bundle left under dir (best effort), so a session
// running without context never reads a previous raise's: SANDBOX.md treats an
// existing CORE.md as "this is a Jam session".
func clearContext(dir string) {
	_ = os.RemoveAll(dir + ".new")
	_ = os.RemoveAll(dir)
	_ = os.RemoveAll(filepath.Join(filepath.Dir(dir), "context.d"))
}

// short abbreviates a fingerprint for logs.
func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
