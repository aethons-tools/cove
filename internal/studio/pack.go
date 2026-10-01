package studio

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

// PackContextDir packs the build context rooted at dir into a base64 string
// suitable for Base.Context: a deterministic tar.gz that PRESERVES file modes
// (the unix exec bit survives, so docker restores it when it extracts the
// streamed context — no chmod needed). It requires a root Dockerfile, honors a
// root .dockerignore (moby parity), rejects symlinks and irregular files, and
// enforces the caps (entry count, uncompressed size, and the encoded-size cap on
// the result). Only regular files are packed (their paths carry their
// directories); empty directories are dropped.
func PackContextDir(dir string) (string, error) {
	// Resolve a symlinked root to its real path: WalkDir does NOT descend into a
	// symlinked directory (it would yield an empty zip that only fails later at
	// build), and EvalSymlinks also gives a clear error for a missing dir.
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("context-dir %q: %w", dir, err)
	}
	dir = real
	if fi, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("context-dir %q: a root Dockerfile is required", dir)
	}
	pm, err := loadDockerignore(dir)
	if err != nil {
		return "", fmt.Errorf("context-dir %q: .dockerignore: %w", dir, err)
	}
	var packed []tarEntry
	var entries int
	var total int64
	var sawDockerfile bool
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		// Honor .dockerignore (exact docker-build parity), but always keep the root
		// Dockerfile and the .dockerignore itself. An ignored directory is pruned,
		// so its contents neither pack nor count against the caps.
		if relSlash != "Dockerfile" && relSlash != ".dockerignore" {
			ignored, merr := pm.MatchesOrParentMatches(relSlash)
			if merr != nil {
				return merr
			}
			if ignored {
				if d.IsDir() {
					// Prune only when no `!` exclusion could re-include something
					// beneath this dir; otherwise descend and match each child
					// individually (moby/docker-build parity for `*` + `!sub/x`).
					if !pm.Exclusions() {
						return filepath.SkipDir
					}
					return nil
				}
				return nil
			}
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("entry %q is a symlink (not allowed)", p)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("entry %q is not a regular file", p)
		}
		if relSlash == "Dockerfile" {
			sawDockerfile = true
		}
		entries++
		if entries > maxZipEntries {
			return fmt.Errorf("context-dir has more than %d entries", maxZipEntries)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		// Read into memory bounded by the remaining budget (the actual stream, not
		// the stat size — the file could grow mid-pack), cumulative across entries.
		before := total
		var body bytes.Buffer
		n, cerr := io.Copy(&body, io.LimitReader(f, int64(maxDecompressedZip)-before+1))
		f.Close()
		if cerr != nil {
			return cerr
		}
		if n > int64(maxDecompressedZip)-before {
			return fmt.Errorf("context-dir exceeds the %d-byte uncompressed cap", maxDecompressedZip)
		}
		total = before + n
		// Preserve the unix permission bits so docker restores the exec bit.
		packed = append(packed, tarEntry{name: relSlash, mode: int64(fi.Mode().Perm()), data: body.Bytes()})
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("pack context-dir %q: %w", dir, walkErr)
	}
	if !sawDockerfile {
		return "", fmt.Errorf("pack context-dir %q: no root Dockerfile packed", dir)
	}
	raw, err := buildTarGz(packed)
	if err != nil {
		return "", fmt.Errorf("pack context-dir %q: %w", dir, err)
	}
	b64 := base64.StdEncoding.EncodeToString(raw)
	if len(b64) > maxEncodedZip {
		return "", fmt.Errorf("packed context-dir %q is %d base64 bytes, exceeds the %d-byte cap", dir, len(b64), maxEncodedZip)
	}
	return b64, nil
}

// loadDockerignore reads <dir>/.dockerignore into a matcher for exact
// docker-build parity. A missing file yields a matcher that excludes nothing.
func loadDockerignore(dir string) (*patternmatcher.PatternMatcher, error) {
	f, err := os.Open(filepath.Join(dir, ".dockerignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return patternmatcher.New(nil)
		}
		return nil, err
	}
	defer f.Close()
	patterns, err := ignorefile.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return patternmatcher.New(patterns)
}

// ResolveContextDir packs a client-only base.context-dir into base.context (a
// tar.gz) and clears context-dir, so the resulting kit is an ordinary context
// kit ready to serialize + push. A relative context-dir is resolved against
// baseDir (e.g. the kit file's directory). A no-op when context-dir is unset.
func (sk *StudioKit) ResolveContextDir(baseDir string) error {
	if sk.Base.ContextDir == "" {
		return nil
	}
	dir := sk.Base.ContextDir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(baseDir, dir)
	}
	b64, err := PackContextDir(dir)
	if err != nil {
		return err
	}
	sk.Base.Context = b64
	sk.Base.ContextDir = ""
	return nil
}
