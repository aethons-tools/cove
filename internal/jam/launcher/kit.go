package launcher

import "github.com/aethons-tools/cove/internal/jam"

// The kit-reference types are canonical in package jam (the jam.Launcher seam is
// parametrized by them and launcher imports jam, so they must sit on the jam
// side to avoid an import cycle). These aliases let the launcher's own
// kit-prepare machinery name them without the jam. qualifier — launcher.KitRef
// and jam.KitRef are the one type. See
// docs/superpowers/specs/2026-09-29-cove-launcher-abstraction-design.md.
type (
	KitRef        = jam.KitRef
	KitDefinition = jam.KitDefinition
	KitStatus     = jam.KitStatus
	KitState      = jam.KitState
)

const (
	KitPreparing = jam.KitPreparing
	KitReady     = jam.KitReady
)

// ErrKitNotReady re-exports jam.ErrKitNotReady (same sentinel value), so a Raise
// returning it is matchable with errors.Is against either name.
var ErrKitNotReady = jam.ErrKitNotReady
