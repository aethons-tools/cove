// Package backend defines the VM-backend abstraction: provision a VM from an
// assembled build context, reach its sshd, query state, destroy it.
package backend

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/runner"
)

type State int

const (
	StateAbsent State = iota
	StateStopped
	StateRunning
)

type WorkspaceMode int

const (
	Isolated WorkspaceMode = iota
	Shared
)

// WorkspaceMount expresses how the workspace is realized. HostPath is set iff
// Mode == Shared.
type WorkspaceMount struct {
	Mode     WorkspaceMode
	HostPath string
}

// Endpoint is a reachable sshd address.
type Endpoint struct {
	Host string
	Port int
	User string
}

// BaseSpec tells the backend how to resolve and verify the base image the sealed
// hardening layer is applied FROM (COV-34). The backend selects the ref (a kit
// image/Dockerfile it builds, or image.base, or the default blessed
// cove-base-image), runs the provenance gate, and passes the result as the
// hardening build's BASE arg.
type BaseSpec struct {
	KitDir          string // holds image/Dockerfile, if the kit ships one
	Base            string // config.yml image.base; "" if unset
	AllowUnverified bool   // --allow-unverified-base: downgrade a failed gate to a warning
}

// CreateContext is everything a backend needs to provision a VM from a
// pre-built image. Create is run-only (COV-38): it consumes the image `install`
// built (Image, sourced from install.json) and does `docker run` — no build, no
// base resolution, no gate (those live in Backend.Install).
type CreateContext struct {
	Name  string
	Image string // the pre-built image tag (from install.json) — kept for display/state
	// Digest is the built image's own sha256 (image ID) captured at install
	// (COV-78). When set, Create runs it to pin the exact built image rather than
	// the mutable tag; empty for a legacy manifest, where Create falls back to Image.
	Digest    string
	Workspace WorkspaceMount
	// DNS pins the container's resolver IPs (docker run --dns), from the kit's
	// image.dns. Empty emits no --dns flag, so the container inherits Docker's
	// default resolver (the VM/host resolver, honoring split-DNS VPNs).
	DNS []string
}

// InstallContext is everything Backend.Install needs to build + gate + tag a
// kit's hardened image (COV-38). It is the one place a base is resolved and the
// provenance gate runs.
type InstallContext struct {
	Kit      string   // identity for the built image tag (naming.Image → atcove-<Kit>)
	BuildDir string   // the assembled .build context to build
	Base     BaseSpec // base resolution + provenance gate inputs (owns AllowUnverified)
}

// InstalledImage is Backend.Install's result: the built, tagged image and the
// base ref it resolved to. The CLI freezes both into install.json.
type InstalledImage struct {
	Ref    string // the built, tagged image (the stable atcove-<kit> identity)
	Digest string // the built image's own sha256 (image ID), captured post-build (COV-78)
	// BaseDigest is the resolved base ref/digest the image was built FROM — the
	// currency-gate provenance input, distinct from Digest (the built image itself).
	BaseDigest string
}

// VolumeSet records the named volumes an instance was created with, so teardown
// removes exactly those rather than re-deriving them from the container name
// (COV-76). State (/agent-data) is always present; Workspace is empty for a
// shared (bind-mount) workspace, which has no volume to remove.
type VolumeSet struct {
	State     string // the /agent-data volume name
	Workspace string // the workspace volume name; empty for a shared workspace
}

// Instance identifies a provisioned VM. Create returns it; the CLI records it in
// the kit's state file, and connect/destroy/status drive the backend from it
// (rather than from the kit config), so a live sandbox is independent of kit edits.
type Instance struct {
	Backend     string
	Container   string // backend handle (docker container name)
	Image       string // built image reference (tag), kept for display/diagnostics
	ImageDigest string // the built image's own sha256 the run was pinned to (COV-78); empty for a legacy run
	Workspace   WorkspaceMount
	Volumes     VolumeSet // the named volumes Create made; consumed by Destroy (COV-76)
}

// Backend provisions and manages VMs of one technology. Dial/GetStatus take the
// container handle from a recorded Instance; Destroy takes the whole Instance so
// it can also clean up the image.
type Backend interface {
	// Install builds + gates + tags a kit's hardened image — the single
	// docker-build path (COV-38). It resolves the base (running the provenance
	// gate), builds the assembled context FROM it, and tags the result.
	Install(ctx InstallContext) (InstalledImage, error)
	// Create runs a pre-built image (COV-38): `docker run` only, consuming the
	// image `install` produced. It does not build, resolve a base, or run the gate.
	Create(ctx CreateContext) (Instance, error)
	Dial(container string) (Endpoint, func(), error)
	// Destroy removes the instance's container and (for the interactive instance)
	// its image. When keepVolumes is true the named volumes survive — `recreate`
	// relies on this so the saved login on /agent-data persists. When false, a
	// real `destroy` also removes the instance's named volumes.
	Destroy(inst Instance, keepVolumes bool) error
	// RemoveImage removes a kit's compiled image (`docker rmi`). It is the inverse
	// of Install and is invoked ONLY by `at-cove uninstall` — the container
	// lifecycle (Destroy/recreate) deliberately keeps the image, which is an
	// `install` artifact, not a per-create build (COV-63). Kept separate from
	// Destroy so the image is torn down solely when the user uninstalls the kit.
	RemoveImage(image string) error
	GetStatus(container string) (State, error)
}

// DispatchOps is the ephemeral-container surface `at-cove work` needs, beyond
// the persistent Create/Destroy lifecycle. A Backend may implement it. Work
// consumes the image `at-cove install` pre-built (COV-38); there is no build op
// here — RunEphemeral runs that installed image directly.
type DispatchOps interface {
	RunEphemeral(image, digest, name, label string, dns []string) (Instance, error) // fresh labeled --rm no-volume container; sshd published; pins digest when set (COV-78); dns pins container resolvers (empty inherits Docker's default)
	Dial(container string) (Endpoint, func(), error)
	RemoveContainer(name string) error // docker rm -f; no image/volume removal
	// ScavengeLabeled force-removes labeled containers whose age (relative to now)
	// exceeds olderThan. Returns the count removed.
	ScavengeLabeled(label string, olderThan time.Duration, now time.Time) (int, error)
}

// SessionEgress applies a session's per-class egress delta to a running
// container (COV-39 §5). It is a privileged delivery op — implemented via host
// `docker exec` as root — kept in its own interface so both the ephemeral
// (DispatchOps) and persistent (Backend) paths can type-assert it independently,
// without either lifecycle interface having to grow the method.
type SessionEgress interface {
	// ApplySessionEgress execs the sealed apply-session-domains.sh helper inside
	// container (as root) with domains piped on stdin, one per line; the helper
	// overwrites /etc/squid/allowed_domains.session.txt and runs `squid -k
	// reconfigure`. An empty domains clears the session file (the container
	// reverts to the baked sealed + kit lists — root-only for a no-class or
	// exited session). Domains flow on stdin only, never on argv.
	ApplySessionEgress(container string, domains []string) error
}

// Factory constructs a Backend bound to a Runner.
type Factory func(r runner.Runner) Backend

var registry = map[string]Factory{}

// Register adds a backend factory under name (called from backend init()s).
func Register(name string, f Factory) { registry[name] = f }

// Get returns the factory for name, or an error listing supported backends.
func Get(name string) (Factory, error) {
	if f, ok := registry[name]; ok {
		return f, nil
	}
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("unknown backend %q; supported: %s", name, strings.Join(names, ", "))
}
