// Package wire is the codec Jam and a remote Studio Manager share over the
// manager gRPC stream (managerpb): frame chunking, op names, JSON payloads,
// the result envelope and the kit-definition encoding. It names no container
// runtime; a studio's location is opaque.
package wire

import (
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// Wire limits and timings.
const (
	MaxFrameData    = 1 << 20
	MaxMessageBytes = 256 << 20
	HeartbeatEvery  = 10 * time.Second
	DeadAfter       = 30 * time.Second
	HelloTimeout    = 10 * time.Second
)

// Request ops (Jam to manager).
const (
	OpRaise            = "raise"
	OpTeardown         = "teardown"
	OpProbe            = "probe"
	OpPause            = "pause"
	OpUnpause          = "unpause"
	OpApplyEgress      = "apply-egress"
	OpPrepareKit       = "prepare-kit"
	OpPurgeState       = "purge-state"
	OpStateOwners      = "state-owners"
	OpImageTag         = "image-tag"
	OpBuildKitDraft    = "build-kit-draft"
	OpHasKitDraftImage = "has-kit-draft-image"
	OpImageFacts       = "image-facts"
)

// Control ops.
const (
	OpResult = "result" // manager to Jam; id is the request's
	OpLog    = "log"    // manager to Jam; one raw draft-log chunk per frame, never assembled
	OpCancel = "cancel" // Jam to manager; empty
)

// Payloads (JSON in Frame.data).

type RaiseRequest struct {
	Spec  jam.RaiseSpec
	Creds jam.LaunchCreds
}
type RaiseResult struct{ Location string }

type InstanceRequest struct{ Instance jam.Instance }
type ProbeResult struct{ Liveness jam.Liveness }

type EgressRequest struct {
	Instance jam.Instance
	Policy   *jam.EgressPolicy
}

// KitRequest carries a kit definition; the result is a jam.KitStatus.
type KitRequest struct{ Def KitDef }

type ActorRequest struct{ ActorID string }
type OwnersResult struct{ ActorIDs []string }

type ImageTagRequest struct{ Ref jam.KitRef }
type TagResult struct{ Tag string }

type DraftBuildRequest struct {
	Def KitDef
	Tag string
}
type DraftBuildResult struct{ ImageID string }

type DraftImageRequest struct{ ImageID string }
type HasResult struct{ Has bool }

// ImageFactsRequest's result is a jam.ImageFacts.
type ImageFactsRequest struct{ Image string }

// Empty is the payload of ops with nothing to say.
type Empty struct{}
