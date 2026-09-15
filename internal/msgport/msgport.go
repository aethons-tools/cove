// Package msgport is harbor's message-log adapter engine: a resident engine
// (one per external Service) that bridges the durable message Log
// (internal/msglog) to a Service. Its egress loop tails the Log and delivers
// outbound messages onto a surface (exactly-once); its ingress loop polls the
// Service and appends foreign events (human replies) back into the Log
// (idempotently). This package is the pure engine + its contract: it imports
// ONLY internal/msglog + stdlib and is proven hermetically against fakes. The
// concrete Linear/Discord Surface implementations, the harbor-backed
// Directory, and the cmd wiring are later slices — this package is wired to
// nothing.
package msgport

import (
	"context"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

// Delivery is one External-bound message already resolved to a concrete
// Service surface.
type Delivery struct {
	Service      string // "linear" | "discord" — the owning Service
	Address      string // the surface to post onto (a Linear ticket identifier, a Discord channel id)
	BodyPrefix   string // literal string prepended to m.Body at delivery ("" = none); Resolve sets it, Deliver renders it
	SenderName   string // the From actor's display identity (Discord webhook username override; a "<name>:" prefix on Linear)
	SenderAvatar string
}

// Event is one Service-native foreign artifact (a human reply), pre-routing.
type Event struct {
	ForeignID      string // stable Service id (Linear comment id / Discord snowflake) — the dedup anchor
	Surface        string // Service-native surface it occurred on (ticket/channel id)
	Author         string // Service-native author handle
	Body           string
	ReplyToForeign string // Service id of the artifact replied to ("" = top-level)
	At             time.Time
}

// Surface is the egress+ingress+lifecycle contract for ONE Service; the
// concrete client (over *linear.Client, the Discord webhook client) is wired
// at cmd.
type Surface interface {
	// Service selects which External Log targets this engine owns; keys markers/cursors.
	Service() string
	// EGRESS: render+deliver one already-resolved message; return the Service-native id (receipt).
	// m.ID is passed as the Service-side dedup key (Discord nonce / Linear body footer).
	Deliver(ctx context.Context, d Delivery, m msglog.Message) (foreignID string, err error)
	// INGRESS: foreign events strictly after `since`, plus the opaque watermark to persist next.
	Poll(ctx context.Context, project, since string) (events []Event, next string, err error)
	Close() error
}

// EgressMark is the BOUNDED per-Service delivery bookkeeping (not stored in
// msglog — it stays pure).
type EgressMark struct {
	LastSeq int64                      // low-water: every Log message with Seq <= this is fully delivered for this Service
	Pending map[string]map[string]bool // msgID → set of delivered target.String() (the draining in-flight window)
}

// Markers persists EgressMark per Service. In-memory in tests; a cmd-layer
// store in Slice 1.
type Markers interface {
	// Egress returns the zero EgressMark when unset for service.
	Egress(service string) EgressMark
	SetEgress(service string, m EgressMark) error
}

// Cursors persists the opaque per-(service,project) ingress watermark (an
// efficiency bound on Poll).
type Cursors interface {
	// Ingress returns "" when unset for (service, project).
	Ingress(service, project string) string
	SetIngress(service, project, cursor string) error
}

// Directory does all actor<->Service mapping (the concrete impl, over
// harbor.Roster/Instance, is at cmd).
type Directory interface {
	// Projects this Service should egress/ingress for.
	Projects(service string) []string
	// Resolve maps an External Log target (+ the sender) to a concrete
	// Delivery ON THIS SERVICE; ok=false when this Service doesn't
	// own/can't reach the target (another Service handles it).
	Resolve(service, project string, to, from msglog.Target) (Delivery, bool)
	// Route maps a polled foreign Event to an inbound Log message's
	// From/To/ReplyTo; ok=false when unroutable (logged as an unrouted
	// event, never silently appended).
	Route(service, project string, e Event) (from msglog.Target, to []msglog.Target, replyTo string, ok bool)
}

// Config tunes the Engine's two poll loops; zero values pick defaults (e.g.
// 2s / 15s).
type Config struct {
	EgressPoll, IngressPoll time.Duration
	// EgressEnabled gates whether Run starts the egress loop at all; the
	// ingress loop always runs. Defaults to false (ingress-only) so a new
	// Service can be wired up shadow-reading before it's trusted to deliver.
	EgressEnabled bool
}
