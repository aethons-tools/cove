// Package relay is Jam's squawk log adapter engine: a resident engine
// (one per external Service) that bridges the durable squawk Log
// (internal/intercom) to a Service. Its egress loop tails the Log and delivers
// outbound squawks onto a surface (exactly-once); its ingress loop polls the
// Service and appends foreign events (human replies) back into the Log
// (idempotently). This package is the pure engine + its contract: it imports
// ONLY internal/intercom + stdlib and is proven hermetically against fakes. The
// concrete Linear/Discord Surface implementations, the Jam-backed
// Directory, and the cmd wiring are later slices — this package is wired to
// nothing.
package relay

import (
	"context"
	"errors"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// Delivery is one External-bound message already resolved to a concrete
// Service surface.
type Delivery struct {
	Service      string // "linear" | "discord" — the owning Service
	Address      string // the surface to post onto (a Linear ticket identifier, a Discord channel id)
	BodyPrefix   string // literal string prepended to m.Body at delivery ("" = none); Surfaces sets it, Deliver renders it
	SenderName   string // the From actor's display identity (Discord webhook username override; a "<name>:" prefix on Linear)
	SenderAvatar string
}

// Event is one Service-native foreign artifact (a human reply), pre-routing.
type Event struct {
	ForeignID      string // stable Service id (Linear comment id / Discord snowflake) — the dedup anchor
	Surface        string // Service-native surface it occurred on (ticket/channel id)
	Author         string // Service-native author handle
	AuthorID       string // Service-native immutable author id (Discord user snowflake); "" if unknown
	AuthorBot      bool   // the author is a bot (Discord author.bot); a bot is never a roster human
	Body           string
	ReplyToForeign string // Service id of the artifact replied to ("" = top-level)
	At             time.Time
	ContentType    string // intercom content type of Body; "" = the default (markdown)
}

// Surface is the egress+ingress+lifecycle contract for ONE Service; the
// concrete client (over *linear.Client, the Discord webhook client) is wired
// at cmd.
type Surface interface {
	// Service selects which External Log targets this engine owns; keys markers/cursors.
	Service() string
	// EGRESS: render+deliver one already-resolved message; return the Service-native id (receipt).
	// m.ID is passed as the Service-side dedup key (Discord nonce / Linear body footer).
	Deliver(ctx context.Context, d Delivery, m intercom.Squawk) (foreignID string, err error)
	// INGRESS: foreign events strictly after `since`, plus the opaque watermark to persist next.
	Poll(ctx context.Context, project, since string) (events []Event, next string, err error)
	Close() error
}

// EgressMark is the BOUNDED per-Service delivery bookkeeping (not stored in
// intercom — it stays pure).
type EgressMark struct {
	LastSeq int64                      // low-water: every Log squawk with Seq <= this is fully delivered for this Service
	Pending map[string]map[string]bool // msgID → set of delivered surfaces (Delivery.Address; the draining in-flight window)
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

// Routed is where a foreign event goes in the channel log: its channel, its
// author (a user, or the account they post as), the squawk it replies to,
// and the surface it came from (so egress never renders it back there).
type Routed struct {
	Channel   ident.ID
	From      ident.ID
	ReplyTo   string
	Origin    ident.ID // the connection
	OriginRef string   // the ref on it (a ticket key, a Discord channel id)
}

// Directory maps between the channel log and Services (the concrete impl,
// over the jam channel registry, is at cmd).
type Directory interface {
	// Projects this Service polls.
	Projects(service string) []string
	// Surfaces lists where m goes on this Service: its channel's surfaces
	// there, minus the one it came from. Deterministic for a given m.
	Surfaces(service string, m intercom.Squawk) []Delivery
	// Route maps a polled foreign event into the log; ok=false when it
	// belongs nowhere (logged, never silently appended).
	Route(service, project string, e Event) (Routed, bool)
	// Post appends a routed foreign event (m carries id, author, body, …) to
	// its channel, with the channel's audience. An error wrapping ErrPermanent
	// will fail the same way every time (the channel was archived, the event is
	// empty or already in the log): the engine skips the event instead of
	// retrying it.
	Post(r Routed, m intercom.Squawk) error
}

// ErrPermanent marks a Post failure retrying can't fix.
var ErrPermanent = errors.New("relay: permanent")

// Config tunes the Engine's two poll loops; zero values pick defaults (e.g.
// 2s / 15s).
type Config struct {
	EgressPoll, IngressPoll time.Duration
	// EgressEnabled gates whether Run starts the egress loop at all; the
	// ingress loop always runs. Defaults to false (ingress-only) so a new
	// Service can be wired up shadow-reading before it's trusted to deliver.
	EgressEnabled bool
}
