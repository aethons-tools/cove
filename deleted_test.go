package cove

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// deletedPackages are the binaries and packages jam-only slice 2 removed
// (docs/superpowers/specs/2026-10-26-jam-only-design.md §2). None may come back
// as a tracked file or an import.
var deletedPackages = []string{
	"cmd/at-cove", "cmd/at-switchboard", "cmd/at-task",
	"internal/atswitchboard", "internal/attask", "internal/awake",
	"internal/dispatch/exec", "internal/dispatch/github", "internal/dispatch/gitlab",
	"internal/dispatch/worker", "internal/dispatchrun", "internal/dockere2e",
	"internal/sshconfig", "internal/state", "internal/switchboard",
}

// deletedMentions are tokens that only make sense while a deleted binary or
// its build plumbing exists: an invocation, a staging script, an install path.
var deletedMentions = []string{
	"cmd/at-cove", "cmd/at-task", "cmd/at-switchboard",
	"internal/attask", "internal/atswitchboard", "internal/switchboard",
	"internal/dispatchrun", "stage-attask",
	"bin/at-task", "bin/at-switchboard",
	"at-task prepare", "at-task complete", "at-task clone-workspace",
	"at-cove dispatch", "at-cove work", "at-cove teammate", "at-cove chat",
	"at-cove create", "at-cove install", "at-cove update", "at-cove view",
	"at-cove ssh-proxy",
}

// mentionExempt are paths whose mentions are history: the design history, the frozen
// legacy installer fixture, and this file.
func mentionExempt(rel string) bool {
	for _, p := range []string{
		"docs/superpowers/",
		"testdata/install-legacy.sh",
		"deleted_test.go",
		".gitignore", // names the stale staged dirs so they are never committed
	} {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// gitLsFiles lists the tracked files under the given pathspecs (all of them
// when none are given), skipping the test when git or the checkout is absent.
func gitLsFiles(t *testing.T, pathspecs ...string) []string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"ls-files", "--"}, pathspecs...)...).Output()
	if err != nil {
		t.Skipf("git ls-files unavailable: %v", err)
	}
	var files []string
	for _, rel := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if rel != "" {
			files = append(files, rel)
		}
	}
	return files
}

// isDeleted reports whether import path ip is (or is under) a deleted package.
func isDeleted(ip string) (string, bool) {
	const mod = "github.com/aethons-tools/cove/"
	for _, p := range deletedPackages {
		if ip == mod+p || strings.HasPrefix(ip, mod+p+"/") {
			return p, true
		}
	}
	return "", false
}

func TestDeletedPackagesStayDeleted(t *testing.T) {
	for _, p := range deletedPackages {
		if files := gitLsFiles(t, p+"/"); len(files) > 0 {
			t.Errorf("%s is tracked again (%s); slice 2 deleted it", p, files[0])
		}
	}
	// Every package of the module and everything it reaches, test and
	// integration-tagged files included: a deleted package is neither a
	// package of the module nor a dependency of one.
	for _, tags := range []string{"", "integration"} {
		cmd := exec.Command("go", "list", "-e", "-deps", "-test", "-tags", tags, "-f", "{{.ImportPath}}", "./...")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list (tags %q): %v", tags, err)
		}
		for _, ip := range strings.Fields(string(out)) {
			if p, ok := isDeleted(ip); ok {
				t.Errorf("go list (tags %q) reaches deleted package %s (%s)", tags, p, ip)
			}
		}
	}
}

func TestNoMentionsOfDeletedBinaries(t *testing.T) {
	for _, rel := range gitLsFiles(t) {
		if mentionExempt(rel) {
			continue
		}
		b, err := os.ReadFile(rel)
		if err != nil {
			continue // listed but deleted in the working tree
		}
		s := string(b)
		for _, m := range deletedMentions {
			if strings.Contains(s, m) {
				t.Errorf("%s mentions %q (a deleted binary or its plumbing)", rel, m)
			}
		}
	}
}

// TestDeprecatedKitDirsStayDeleted: the at-cove (.at-cove/) and repo-kit
// (.at-jam/) directories are deprecated, so none may be tracked anywhere —
// tooling that still writes them (a generated .gitignore) must not get them
// committed.
func TestDeprecatedKitDirsStayDeleted(t *testing.T) {
	for _, rel := range gitLsFiles(t) {
		for _, seg := range strings.Split(rel, "/") {
			if seg == ".at-cove" || seg == ".at-jam" {
				t.Errorf("%s is tracked; %s/ is deprecated", rel, seg)
				break
			}
		}
	}
}
