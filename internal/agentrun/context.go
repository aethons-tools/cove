package agentrun

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		if filepath.Clean(p) == "CORE.md" {
			return fmt.Errorf("session context: a file may not replace CORE.md")
		}
		files[p] = body
	}
	parent := filepath.Dir(dir)
	versions := filepath.Join(parent, "context.d")
	name := short(b.Fingerprint)
	if name == "" {
		name = "nofp"
	} else if !isHex(name) {
		// The fingerprint comes from Jam over the network; it names a directory.
		return fmt.Errorf("session context: refusing fingerprint %q (not hex)", name)
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
// running without context never reads a previous raise's, and restores the
// image's SANDBOX.md, which is then the agent's only copy of the sandbox rules.
func clearContext(dir string) {
	_ = os.RemoveAll(dir + ".new")
	_ = os.RemoveAll(dir)
	_ = os.RemoveAll(filepath.Join(filepath.Dir(dir), "context.d"))
	restoreSandboxDoc(dir)
}

// sandboxDocSeed is the image's copy of SANDBOX.md — the plain at-cove sandbox
// guide the base image seeds into /agent-data (COV-246). A var so tests can
// point it away from the real seed.
var sandboxDocSeed = "/home/agent/.init-agent-data/SANDBOX.md"

// sandboxDoc is the CLAUDE.md-imported SANDBOX.md beside the context dir
// (/agent-data/SANDBOX.md for the default dir).
func sandboxDoc(contextDir string) string {
	return filepath.Join(filepath.Dir(contextDir), "SANDBOX.md")
}

// suppressSandboxDoc blanks SANDBOX.md once a session context is written: a
// Jam session's sandbox rules are built-in boilerplate of its context, so the
// image's copy would only duplicate them (and its local-at-cove "how to change
// the kit" would contradict the Jam path). The @SANDBOX.md import stays valid,
// like the empty default COLLABORATOR.md. A kit base that seeds no SANDBOX.md
// gets none created. The entrypoint's per-boot refresh restores the image copy;
// this runs again at every raise.
func suppressSandboxDoc(contextDir string) error {
	p := sandboxDoc(contextDir)
	if _, err := os.Lstat(p); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return replaceFile(p, nil)
}

// restoreSandboxDoc puts the image's SANDBOX.md back (best effort; nothing to
// do when the image seeds none).
func restoreSandboxDoc(contextDir string) {
	b, err := os.ReadFile(sandboxDocSeed)
	if err != nil {
		return
	}
	_ = replaceFile(sandboxDoc(contextDir), b)
}

// replaceFile writes b to p via a sibling temp file and a rename, so a symlink
// planted at p is replaced, never written through.
func replaceFile(p string, b []byte) error {
	tmp := p + ".new"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// short abbreviates a fingerprint for logs.
func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return s != ""
}
