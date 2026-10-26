// Package backend defines the docker-backend surfaces Jam's launcher drives:
// run, dial, pause and remove studio containers, their named volumes, their
// egress and SSH CA, and the managed-kit image build.
package backend

import (
	"context"
	"io"
)

type State int

const (
	StateAbsent State = iota
	StateStopped
	StateRunning
)

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

// Instance identifies a studio container RunEphemeral started.
type Instance struct {
	Backend     string
	Container   string // backend handle (docker container name)
	Image       string // built image reference (tag), kept for display/diagnostics
	ImageDigest string // the built image's own sha256 the run was pinned to (COV-78); empty for a legacy run
}

// DispatchOps is the ephemeral-container surface Jam's launcher raises studios
// with: a labeled --rm container from a built image (pinned by digest when
// known), dialled over its published sshd, paused when idle, removed at
// teardown. There is no build op here — KitImageBuilder builds the image.
type DispatchOps interface {
	RunEphemeral(image, digest, name, label string, dns, addHosts []string, docker bool, mounts ...Mount) (Instance, error) // fresh labeled --rm container; sshd published; pins digest when set (COV-78); dns pins container resolvers (empty inherits Docker's default); addHosts maps names to the host gateway (COV-138); docker runs it under Sysbox with a -docker cache volume (COV-117); mounts attaches named volumes, which --rm never removes (COV-249)
	Dial(container string) (Endpoint, func(), error)
	RemoveContainer(name string) error // docker rm -f; no image/volume removal
	Pause(name string) error           // docker pause; freeze an idle container (cgroup freezer)
	Unpause(name string) error         // docker unpause; thaw it
}

// Mount is a named docker volume mounted into an ephemeral container at
// Target. Named volumes outlive the container: `--rm` removes only anonymous
// volumes and `docker rm -f` (no -v) none, so a later run with the same Mount
// re-attaches the same state (a Jam standing session's /agent-data and
// workspace, COV-249). Only VolumeOps.RemoveVolumes deletes them.
type Mount struct {
	Volume string // docker volume name (created on first use)
	Target string // absolute mount path in the container
}

// VolumeOps manages labeled named volumes — a standing session's state
// volumes (COV-249). Kept its own interface so DispatchOps doesn't grow it.
type VolumeOps interface {
	// CreateVolume creates the named volume with labels ("key=value" each);
	// creating an existing volume is a no-op.
	CreateVolume(name string, labels ...string) error
	// RemoveVolumes deletes the named volumes; an absent volume is not an
	// error, an in-use one is.
	RemoveVolumes(names ...string) error
	// ListVolumes returns the volumes carrying label key, name → its value,
	// narrowed to those also carrying every match label ("key=value").
	ListVolumes(key string, match ...string) (map[string]string, error)
}

// RoleEgress replaces a running container's active egress policy list with a
// role's domains, which must fit the kit's baked ceiling (enforced in-box by the
// sealed apply-role-egress.sh). Privileged: host docker exec as root; domains on
// stdin only. Jam's launcher applies it at raise, before the agent starts, and
// again on a running cove when the role's policy changes.
type RoleEgress interface {
	// ApplyRoleEgress execs the sealed helper inside container (as root) with
	// domains piped on stdin, one per line. An empty domains is a set-but-empty
	// policy (nothing beyond the sealed base + the kit's infra domains). The
	// helper rejects — changing nothing — any domain outside the ceiling.
	ApplyRoleEgress(container string, domains []string) error
	// ResetRoleEgress execs the sealed helper inside container (as root) in its
	// --kit-default mode, with empty stdin: the active list reverts to the kit's
	// baked default (its ceiling). Used when a role's policy is cleared.
	ResetRoleEgress(container string) error
}

// SSHCA installs Jam's SSH user-CA public key in a running studio, so sshd
// (TrustedUserCAKeys, baked by the hardening layer) accepts certs that CA issues
// for the agent user. Privileged: host docker exec as root; the key on stdin.
// Jam's launcher calls it at raise, before the agent starts.
type SSHCA interface {
	// InstallSSHUserCA execs the sealed install-ssh-ca.sh inside container (as
	// root) with pub — one ssh-ed25519 authorized_keys line — on stdin. The
	// helper validates the line and atomically replaces /etc/ssh/jam_user_ca.pub.
	InstallSSHUserCA(container string, pub []byte) error
}

// KitImageBuilder is the managed-cove build+inventory surface a Jam launcher
// drives on a substrate (COV-217). The launcher owns "prepare a managed kit's
// image", which each substrate builds its own way — Colima builds locally via
// docker in its pinned context, so the build and the RunEphemeral that later runs
// the image share one daemon. Kept its own interface so the launcher type-asserts
// it and DispatchOps doesn't grow build methods. It builds an
// already-assembled context FROM a base the caller has already resolved and
// gated (ResolveKitBase*), so it never runs the provenance gate again.
type KitImageBuilder interface {
	// BuildKitImage builds the assembled context in buildDir into the tagged image,
	// FROM base (the Dockerfile's BASE arg; must be non-empty), injecting buildArgs
	// as additional --build-arg pairs. buildArgs values must never carry secrets.
	// noCache bypasses docker's layer cache. Returns the built image's own sha256 digest.
	BuildKitImage(buildDir, tag, base string, buildArgs map[string]string, noCache bool) (digest string, err error)
	// HasKitImage reports whether the tagged image exists on this substrate. A
	// miss is (false, nil) — the normal signal that drives PrepareKit — not an error.
	HasKitImage(tag string) (bool, error)
	// ResolveKitBase resolves + gates a role-named kit's declared base into the ref
	// to pass BuildKitImage as its base. An empty declaredBase resolves to the
	// substrate's blessed default. The provenance gate is ON — a brokered kit gets
	// no --allow-unverified escape hatch, so an unblessed base errors here rather
	// than building. All studio kits resolve + gate their declared base here (gate
	// ON, no allow-unverified); an empty declared base resolves to the blessed default.
	ResolveKitBase(declaredBase string) (resolvedBase string, err error)
	// ResolveKitBaseTar streams a studio kit's build context (a tar, gzip
	// auto-detected) to `docker build -`, tags the built image, and gates it —
	// returning the ref to pass BuildKitImage as its base. Docker reads the context
	// on stdin and preserves file modes natively (so a +x script keeps its exec
	// bit). The gate is ON (same as ResolveKitBase) — the built base must descend
	// from a blessed cove-base-image or this errors. The context's Dockerfile builds
	// FROM the blessed base via the injected COVE_BASE_IMAGE build arg.
	ResolveKitBaseTar(ctx io.Reader) (resolvedBase string, err error)
	// ResolveKitBaseTarOn builds one stage of a kit chain: like ResolveKitBaseTar,
	// but the context builds FROM parentRef (the parent stage, injected as
	// COVE_BASE_IMAGE / AT_JAM_STUDIO_BASE_IMAGE) instead of the blessed default.
	// The gate is ON and unchanged — the built stage must descend from a blessed
	// cove-base-image (the parent does, so a child built on it does too). Only
	// once the gate passes is the result tagged stageTag, which is returned; a
	// rejected stage leaves no stageTag behind, so HasKitBase never vouches for an
	// ungated image.
	ResolveKitBaseTarOn(ctx io.Reader, parentRef, stageTag string) (resolvedBase string, err error)
	// HasKitBase reports whether a chain stage tagged tag (by ResolveKitBaseTarOn,
	// after its gate passed) exists on this substrate. A miss is (false, nil).
	HasKitBase(tag string) (bool, error)
}

// LoggedKitBuilder is optionally implemented by a KitImageBuilder that can
// run its builds under a context, their output copied to a log: the Jam
// launcher's kit draft test build (docs/usage/jam/kits-drafts.md#test-build).
// The returned builder builds exactly as the receiver does — same argv,
// same gate — only logged and cancellable.
type LoggedKitBuilder interface {
	WithBuildLog(ctx context.Context, log io.Writer) KitImageBuilder
}
