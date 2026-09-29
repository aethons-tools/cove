package launcher

import (
	"errors"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kit"
)

// KitRef is the light, hot-path reference to a kit: what a Raise carries so the
// launcher can answer "do I have this?" without the full config crossing the
// wire. It is an alias for jam.KitRef — the canonical type lives in jam so
// RaiseSpec.Kit and the jam.Launcher seam can name it without an import cycle
// (launcher imports jam) — so launcher.KitRef and jam.KitRef are one type.
//
// See docs/superpowers/specs/2026-09-29-cove-launcher-abstraction-design.md
// ("Kit reference + lazy prepare").
type KitRef = jam.KitRef

// KitDefinition is the chunky payload: a KitRef plus the full kit config. The
// supervisor sends it only on a miss (ErrKitNotReady), via PrepareKit.
type KitDefinition struct {
	Ref    KitRef
	Config kit.Config
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
