package kit

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/sbx"
)

// Options controls how Build and Create behave.
type Options struct {
	DryRun bool
	Stdout io.Writer
}

// BuildDir is kitDir/.build — where staged output and the kit zip live.
func BuildDir(kitDir string) string { return filepath.Join(kitDir, ".build") }

// StagingDir is kitDir/.build/kit — the templated tree handed to sbx pack.
func StagingDir(kitDir string) string { return filepath.Join(BuildDir(kitDir), "kit") }

// ZipPath is kitDir/.build/kit.zip — the packed kit.
func ZipPath(kitDir string) string { return filepath.Join(BuildDir(kitDir), "kit.zip") }

// Build templates every regular file under kitDir (excluding the .build/
// subtree), stages the results, asks the runner to pack them, then removes
// staging. Under opts.DryRun it prints the planned work and executes nothing.
func Build(kitDir string, r runner.Runner, lookup func(string) (string, bool), opts Options) error {
	info, err := os.Stat(kitDir)
	if err != nil {
		return fmt.Errorf("kitdir %q: %w", kitDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("kitdir %q is not a directory", kitDir)
	}

	packArgs := sbx.Pack(StagingDir(kitDir), ZipPath(kitDir))

	if opts.DryRun {
		files, err := kitFiles(kitDir)
		if err != nil {
			return err
		}
		fmt.Fprintf(opts.Stdout, "would template %d files\n", len(files))
		fmt.Fprintf(opts.Stdout, "would run: sbx %s\n", strings.Join(packArgs, " "))
		return nil
	}

	if _, err := Stage(kitDir, lookup); err != nil {
		return err
	}
	if err := r.Run("sbx", packArgs...); err != nil {
		return err
	}
	return os.RemoveAll(StagingDir(kitDir))
}

// Stage walks kitDir, templates each regular file (excluding the .build/
// subtree), and writes the result to StagingDir preserving the relative
// path and file mode. Any pre-existing staging dir is removed first. It
// returns the number of files staged.
func Stage(kitDir string, lookup func(string) (string, bool)) (int, error) {
	files, err := kitFiles(kitDir)
	if err != nil {
		return 0, err
	}
	staging := StagingDir(kitDir)
	if err := os.RemoveAll(staging); err != nil {
		return 0, err
	}
	for _, rel := range files {
		src := filepath.Join(kitDir, rel)
		data, err := os.ReadFile(src)
		if err != nil {
			return 0, err
		}
		info, err := os.Stat(src)
		if err != nil {
			return 0, err
		}
		dst := filepath.Join(staging, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return 0, err
		}
		out := Substitute(string(data), lookup)
		if err := os.WriteFile(dst, []byte(out), info.Mode().Perm()); err != nil {
			return 0, err
		}
		if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
			return 0, err
		}
	}
	return len(files), nil
}

// kitFiles returns the sorted relative paths of every regular file under
// kitDir, excluding the kitDir/.build subtree.
func kitFiles(kitDir string) ([]string, error) {
	buildDir := BuildDir(kitDir)
	var files []string
	err := filepath.WalkDir(kitDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == buildDir {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(kitDir, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}
