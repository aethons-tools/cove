package cove

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

// renamedGroup is one jam-rename task's old names
// (docs/superpowers/specs/2026-10-26-jam-only-design.md §3). Once a group is
// listed, no tracked file outside renamedAllow may contain any of its tokens,
// in its content or its path.
type renamedGroup struct {
	name   string
	tokens []string // Go regexp sources
}

var renamedGroups = []renamedGroup{
	{"go identifiers", []string{`AtCove`, `CoveMaster`, `coveMaster`, `covemasterbin`,
		`internal/covemaster\b`, `(?i)package covemaster`, `\bcovemaster\.[A-Z]`, `CoveContainer`}},
	{"env and wire", []string{`\bAT_(COVE|HARBOR|MINT|LOG)_`,
		`\bAT_JAM_(ADMIN|IDENTITY|RUNTIME|LAUNCH|BASE|CONNECTOR|SHUTDOWN|SIGNAL|TOKEN|TEST|X)`,
		`\bCOVE_(DOCKER|SHADOW_DIRS|PLUGIN_|RUN_|KIT_DOMAINS_FILE|SESSION_DOMAINS_FILE|EGRESS_CEILING_FILE|ENV_FILE|SSH_USER_CA_FILE|BROWSER|BOARD_SCREENSHOT|RUNNER_TEST_HELPER)`,
		`harbor\.attach\.v1`, `x-harbor-launch-secret`}},
	{"in-image paths and build args", []string{`lib/cove\b`, `cove-egress`, `sshd_config\.d/cove\.conf`,
		`cove-git-credential`, `/dev/shm/cove-`, `cove-master\.log`, `\bAT_JAM_`, `\bCOVE_BASE_IMAGE\b`, `\bCOVE_SSHENV\b`}},
}

// renamedAllow is where an old name may survive: the design history, the
// Harbor-rename table, the migrations' legacy names, the frozen legacy
// installer, and the guards. only lists the token sources allowed there
// (nil: every token).
var renamedAllow = []struct {
	prefix string
	only   []string
}{
	{"docs/superpowers/", nil},
	{"docs/usage/jam/renamed-from-harbor.md", nil},
	{"internal/legacynames/", nil},
	{"renamed_test.go", nil},
	{"deleted_test.go", nil},
	{"testdata/install-legacy.sh", nil},
}

// docsPending skips the docs until Task 9 of the slice-3 plan renames them.
func docsPending(rel string) bool {
	return strings.HasPrefix(rel, "docs/") || rel == "README.md" || rel == "AGENTS.md" || rel == "CLAUDE.md" ||
		strings.Contains(rel, "/.init-agent-data/")
}

func renamedAllowed(rel, token string) bool {
	for _, a := range renamedAllow {
		if !strings.HasPrefix(rel, a.prefix) {
			continue
		}
		if a.only == nil {
			return true
		}
		for _, o := range a.only {
			if o == token {
				return true
			}
		}
	}
	return false
}

func TestNoPreRenameNames(t *testing.T) {
	type tok struct {
		group, src string
		re         *regexp.Regexp
	}
	var toks []tok
	for _, g := range renamedGroups {
		for _, s := range g.tokens {
			toks = append(toks, tok{g.name, s, regexp.MustCompile(s)})
		}
	}
	for _, rel := range gitLsFiles(t) {
		if docsPending(rel) {
			continue
		}
		b, err := os.ReadFile(rel)
		if err != nil || bytes.IndexByte(b, 0) >= 0 {
			b = nil // gone from the tree, or binary: check its path only
		}
		for _, k := range toks {
			if renamedAllowed(rel, k.src) {
				continue
			}
			if k.re.MatchString(rel) {
				t.Errorf("%s: path keeps the pre-rename name %q (%s)", rel, k.src, k.group)
			}
			if loc := k.re.FindIndex(b); loc != nil {
				line := 1 + bytes.Count(b[:loc[0]], []byte("\n"))
				t.Errorf("%s:%d: %q survives the jam rename (%s)", rel, line, b[loc[0]:loc[1]], k.group)
			}
		}
	}
}
