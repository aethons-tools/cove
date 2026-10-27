// Package legacynames holds the pre-rename names (jam-only slice 3,
// docs/usage/jam/renamed-from-at.md) that Jam's one-time migrations and its
// refusals must still recognise. Nothing else may spell an old name: the
// renamed guard (renamed_test.go) allows them only here.
package legacynames

import (
	"path"
	"regexp"
	"strings"

	"github.com/aethons-tools/cove/internal/naming"
)

const (
	// ContainerPrefix starts a pre-rename studio container's name: atcove-cove-<id>.
	ContainerPrefix = "atcove-cove-"
	// Label is the docker label on every pre-rename studio container.
	Label = "harbor.cove"
	// StateLabel is a pre-rename state volume's owner label (value: actor id).
	StateLabel = "harbor.cove.state"
	// JamLabel is a pre-rename state volume's Jam label (value: the Jam id).
	JamLabel = "harbor.cove.jam"
	// ConfigDir is the pre-rename CLI config directory under $XDG_CONFIG_HOME.
	ConfigDir = "at-jam"
	// StateDir is the pre-rename state directory under $XDG_STATE_HOME.
	StateDir = "at-jam"
	// LauncherDir held the launcher's id_ed25519 and known_hosts.d.
	LauncherDir = "at-cove"
	// SSHBlockBegin and SSHBlockEnd prefix a pre-rename ~/.ssh/config block's markers.
	SSHBlockBegin = "# >>> at-jam "
	SSHBlockEnd   = "# <<< at-jam "
	// JamBinary and MintBinary are the pre-rename CLI and minter binaries a
	// credential command may still call (serve start warns).
	JamBinary  = "at-jam"
	MintBinary = "at-mint"
	// BaseImageRepo is the pre-rename base image repository.
	BaseImageRepo = "ghcr.io/aethons-tools/cove-base-image"
	// newBaseImageRepo is its new name (basedigest.Image after Task 4).
	newBaseImageRepo = "ghcr.io/aethons-tools/jam-studio-base"
)

// KitRename is one name a stored kit may still use and the name Jam passes now.
type KitRename struct{ Old, New string }

// KitRenames are the build-arg and ENV names (whole words) and the base image
// repository (a literal) a kit's kit.yml or Dockerfile used before the rename.
var KitRenames = []KitRename{
	{"AT_JAM_STUDIO_BASE_IMAGE", "JAM_STUDIO_BASE_IMAGE"},
	{"AT_JAM_STUDIO_TARGET_ARCH", "JAM_STUDIO_TARGET_ARCH"},
	{"COVE_BASE_IMAGE", "JAM_STUDIO_BASE_IMAGE"},
	{"COVE_SSHENV", "JAM_SSHENV"},
	{BaseImageRepo, newBaseImageRepo},
}

var kitNameRe = regexp.MustCompile(`\b(AT_JAM_STUDIO_BASE_IMAGE|AT_JAM_STUDIO_TARGET_ARCH|COVE_BASE_IMAGE|COVE_SSHENV)\b|` +
	regexp.QuoteMeta(BaseImageRepo))

// Container is actorID's pre-rename container name.
func Container(actorID string) string { return ContainerPrefix + naming.SafeID(actorID) }

// NewKitName is old's new name (old itself when it is not a KitRenames name).
func NewKitName(old string) string {
	for _, r := range KitRenames {
		if r.Old == old {
			return r.New
		}
	}
	return old
}

// RewriteKitText is s with every pre-rename kit name replaced; changed
// reports whether anything was.
func RewriteKitText(s string) (out string, changed bool) {
	out = kitNameRe.ReplaceAllStringFunc(s, NewKitName)
	return out, out != s
}

// FindKitName is the first pre-rename kit name in s, if any.
func FindKitName(s string) (string, bool) {
	m := kitNameRe.FindString(s)
	return m, m != ""
}

// KitFileScanned reports whether a kit file is one the rename rewrites and
// push refuses on: kit.yml and Dockerfiles.
func KitFileScanned(p string) bool {
	b := path.Base(p)
	return b == "kit.yml" || b == "Dockerfile" || b == "Containerfile" ||
		strings.HasPrefix(b, "Dockerfile.") || strings.HasSuffix(b, ".Dockerfile")
}
