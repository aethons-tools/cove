package jam

import (
	"errors"
	"fmt"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitRef is the light, hot-path reference to a kit: what the supervisor puts on
// a Raise so the launcher can answer "do I have this?" without the full config
// crossing the wire. It is a stable content key — kit versions are monotonic and
// immutable — so a launcher can cache prepared artifacts by it. Digest is the
// image's build-digest (studio.BuildDigest: the kit's build inputs AND the
// harness layer of the raising role's model-spec); "" = unset.
//
// These kit-reference types live in package jam (not internal/jam/launcher)
// because the jam.Launcher interface is parametrized by them: the concrete
// launcher imports jam, so the types must sit on the jam side of that seam to
// avoid an import cycle.
//
// See docs/superpowers/specs/2026-09-29-cove-launcher-abstraction-design.md
// ("Kit reference + lazy prepare").
type KitRef struct {
	ID      string
	Version int
	Digest  string
}

// String is the stable key used in logs and the launcher's prepared-kit
// inventory, e.g. "managed@v3".
func (r KitRef) String() string { return fmt.Sprintf("%s@v%d", r.ID, r.Version) }

// KitDefinition is the chunky payload: a KitRef plus the full studio-kit
// definition. The supervisor sends it only on a miss (ErrKitNotReady), via
// PrepareKit.
type KitDefinition struct {
	Ref KitRef
	Kit studio.StudioKit
	// Harness is the harness layer the image installs — the raising role's
	// model-spec (CLI type, exact version, plugins). Ref.Digest is keyed on
	// it too (studio.BuildDigest).
	Harness harnessinstall.Install
}

// KitState is the readiness of a kit on a launcher. Colima prepares
// synchronously, but the state exists so a future remote launcher can report
// KitPreparing while a build runs.
type KitState int

const (
	KitPreparing KitState = iota
	KitReady
)

// KitStatus is what PrepareKit reports. Err is populated when a prepare failed
// (never a secret).
type KitStatus struct {
	State KitState
	Err   string
}

// ErrKitNotReady is returned by a Launcher's Raise when it has not prepared the
// requested kit (never built, or evicted). The supervisor responds by fetching
// the full definition and calling PrepareKit, then retrying the Raise. Match it
// with errors.Is.
var ErrKitNotReady = errors.New("launcher: kit not prepared")
