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
		`harbor\.attach\.v1`, `x-harbor-launch-secret`, `cove\.manager\.v1`}},
	{"in-image paths and build args", []string{`lib/cove\b`, `cove-egress`, `sshd_config\.d/cove\.conf`,
		`cove-git-credential`, `/dev/shm/cove-`, `cove-master\.log`, `\bAT_JAM_`, `\bCOVE_BASE_IMAGE\b`, `\bCOVE_SSHENV\b`}},
	{"images", []string{`\bcove-base\b`, `\bcove-image\b`, `\bCOVE_IMAGE\b`}},
	{"tags, labels, containers", []string{`cove-kit`, `harbor\.cove`, `atcove-cove`, `"atcove"`, `/etc/cove/mcp-servers`}},
	{"directories and markers", []string{`\.config/at-(cove|jam|harbor)`, `state/at-jam`, `[<>]{3} at-jam`,
		`atCoveConfigDir`, `atJamConfigDir`, `atJamStateDir`, `legacyConfigDir`}},
	{"binaries and release", []string{`at-jam`, `at-mint`, `\bat-cove`, `at-harbor`, `cove-master`, `covemaster`,
		`at-studio-manager`, `\batcove\b`, `cove-jam`, `cove_(\{\{|\$\{|\*)`, `\bCOVE_[A-Z]`, `\bAT_(JAM|COVE|HARBOR|MINT|LOG)_`, `atMint`}},
	// jam-studio-manager became jam-colima (docs/superpowers/specs/2026-11-05-jam-colima-design.md §1).
	{"studio manager binary", []string{`jam-studio-manager`, `\bstudio-manager\.md`}},
}

// renamedAllow is where an old name may survive: the design history, the
// Harbor-rename table, the migrations' legacy names, the frozen legacy
// installer, the upgrade notes, and the guards. only lists the token sources allowed there
// (nil: every token).
var renamedAllow = []struct {
	prefix string
	only   []string
}{
	{"docs/superpowers/", nil},
	{"docs/modules/ROOT/pages/usage/jam/renamed-from-harbor.adoc", nil},
	{"docs/modules/ROOT/pages/usage/jam/renamed-from-at.adoc", nil},
	{"docs/modules/ROOT/pages/usage/jam/INDEX.adoc", []string{`at-jam`, `at-mint`, `at-studio-manager`, `harbor\.cove`}}, // the routing row to renamed-from-at.adoc names what it renames
	{"docs/modules/ROOT/pages/usage/jam/coves.adoc", []string{`cove-master`}},                                            // its link to the 2026-09-13-cove-master-client.md design spec (history keeps its name)
	{"internal/legacynames/", nil},
	{"renamed_test.go", nil},
	{"deleted_test.go", nil},
	{"testdata/install-legacy", nil}, // the frozen pre-rename installers
	{"install.test.sh", nil},         // drives the frozen pre-rename installers
	{"install.sh", []string{`at-jam`, `at-mint`, `\bat-cove`, `at-studio-manager`, `jam-studio-manager`}}, // the stale-binary notices
	{"docs/modules/ROOT/pages/usage/jam-colima.adoc", []string{`jam-studio-manager`}},                     // its "Upgrading from jam-studio-manager" section
	{"docs/modules/ROOT/pages/usage/INDEX.adoc", []string{`jam-studio-manager`}},                          // the routing row to that section
	{".gitignore", []string{`at-jam`, `at-mint`, `cove-master`, `covemaster`, `covemasterbin`}},           // keeps stale local builds out of git
	{".github/workflows/release.yml", []string{`\bcove-base\b`}},                                          // the one-time re-cut copies the blessed bases out of the pre-rename repo
	// The VS Code extension's port of the CLI's legacy-block strip, and its test.
	{"web/vscode/src/ssh/config.ts", []string{`[<>]{3} at-jam`, `at-jam`}},
	{"web/vscode/test/sshconfig.test.ts", []string{`[<>]{3} at-jam`, `at-jam`}},
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
