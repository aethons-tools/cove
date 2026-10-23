package push

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/relay"
)

// Delivery tuning (spec §7).
const (
	Service = "push"
	// TTL is how long a push service holds a notification for an offline
	// device, in seconds.
	TTL = 86400
	// MaxAge is the oldest squawk still pushed: after an outage, old news
	// is marked delivered without buzzing anyone.
	MaxAge = 10 * time.Minute
	// FailThreshold consecutive transient failures raise ConditionKey.
	FailThreshold = 5
)

// ConditionKey is the health condition raised while sends keep failing.
var ConditionKey = condition.Key("push", "relay")

// ErrBadSubscription is a subscription whose keys can never encrypt a
// message: the sender refuses it before any request, and the surface
// deletes it.
var ErrBadSubscription = errors.New("push: unusable subscription keys")

// SendOptions are one send's Web Push parameters.
type SendOptions struct {
	TTL          int    // seconds
	Topic        string // collapses queued pushes per conversation
	Subscriber   string // the VAPID subject, as configured (mailto: or https:)
	VAPIDPrivate string // base64url; never logged
	VAPIDPublic  string // base64url
}

// Sender encrypts payload for sub and posts it to sub's push service,
// reporting the HTTP status (err: no answer, or ErrBadSubscription).
type Sender interface {
	Send(ctx context.Context, payload []byte, sub jam.PushSubscription, opts SendOptions) (status int, err error)
}

// Surface is the push relay.Surface: egress only. Each relay.Delivery is
// one device of one recipient (see AddressFor); BodyPrefix carries the
// recipient's user id (push never renders a body prefix).
type Surface struct {
	Store jam.PushSubscriptionStore
	// Sender posts to the push services (WebPushSender in production).
	Sender Sender
	// Payload is m's notification for user; ok=false: user is no longer
	// one of its recipients (nothing is sent).
	Payload func(m intercom.Squawk, user ident.ID) (Payload, bool)
	// Opts carries the VAPID subject and keys; TTL and Topic are set per send.
	Opts  SendOptions
	Now   func() time.Time   // nil: time.Now
	Conds *condition.Tracker // may be nil
	Log   *slog.Logger       // may be nil
}

// AddressFor is a device's delivery address: "push:" + the first 16 hex
// digits of sha256(endpoint), so the engine tracks each device separately
// without the endpoint (a capability URL) in its bookkeeping.
func AddressFor(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return "push:" + hex.EncodeToString(sum[:])[:16]
}

// TopicFor is the Topic header for a channel: the first 32 hex digits of
// sha256(channel id) — the push service keeps only the newest queued push
// per conversation.
func TopicFor(channel ident.ID) string {
	sum := sha256.Sum256([]byte(channel))
	return hex.EncodeToString(sum[:])[:32]
}

// Service implements relay.Surface.
func (s *Surface) Service() string { return Service }

// Poll implements relay.Surface: push has no ingress.
func (s *Surface) Poll(context.Context, string, string) ([]relay.Event, string, error) {
	return nil, "", nil
}

// Close implements relay.Surface.
func (s *Surface) Close() error { return nil }

func (s *Surface) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Surface) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Deliver pushes m to one device (spec §7). Only a transient failure — a
// network error, 429 or 5xx — returns an error (the engine retries it);
// everything else is final, so the egress low-water mark keeps advancing:
// a squawk older than MaxAge, a device gone since Surfaces or no longer the
// recipient's, an endpoint off the allowlist or with unusable keys (deleted),
// 404/410 (deleted), any other refusal (logged).
func (s *Surface) Deliver(ctx context.Context, d relay.Delivery, m intercom.Squawk) (string, error) {
	if s.now().Sub(m.At) > MaxAge {
		return "", nil
	}
	user := ident.ID(d.BodyPrefix)
	sub, ok := s.subscription(user, d.Address)
	if !ok {
		return "", nil
	}
	if err := jam.ValidPushEndpoint(sub.Endpoint); err != nil {
		s.log().Warn("push: stored endpoint not allowed; deleting it", "device", d.Address, "error", err.Error())
		return "", s.drop(sub)
	}
	p, ok := s.Payload(m, user)
	if !ok {
		return "", nil
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", nil // a Payload always marshals; never stall the log on it
	}
	opts := s.Opts
	opts.TTL, opts.Topic = TTL, TopicFor(m.Channel)
	status, err := s.Sender.Send(ctx, body, sub, opts)
	switch {
	case errors.Is(err, ErrBadSubscription):
		s.log().Warn("push: subscription keys unusable; deleting it", "device", d.Address, "error", err.Error())
		return "", s.drop(sub)
	case err != nil:
		return "", s.transient(fmt.Errorf("push: send: %w", err))
	case status >= 200 && status < 300:
		s.healthy()
		if err := s.Store.TouchPushSubscription(sub.Endpoint, s.now().UTC()); err != nil {
			s.log().Warn("push: record delivery failed", "device", d.Address, "error", err.Error())
		}
		return "", nil
	case status == http.StatusNotFound || status == http.StatusGone:
		s.healthy()
		return "", s.drop(sub)
	case status == http.StatusTooManyRequests || status >= 500:
		return "", s.transient(fmt.Errorf("push: push service answered %d", status))
	default:
		s.healthy()
		s.log().Warn("push: send refused", "device", d.Address, "status", status)
		return "", nil
	}
}

// subscription is user's device at address (ok=false: gone, or moved to
// another user since Surfaces listed it).
func (s *Surface) subscription(user ident.ID, address string) (jam.PushSubscription, bool) {
	for _, sub := range s.Store.PushSubscriptions(user) {
		if AddressFor(sub.Endpoint) == address {
			return sub, true
		}
	}
	return jam.PushSubscription{}, false
}

// drop deletes a dead subscription; a failed delete is retried (the error
// keeps the delivery pending).
func (s *Surface) drop(sub jam.PushSubscription) error {
	if err := s.Store.DeletePushSubscription(sub.Endpoint); err != nil {
		return fmt.Errorf("push: delete subscription: %w", err)
	}
	return nil
}

// transient counts one transient failure toward ConditionKey and returns err.
func (s *Surface) transient(err error) error {
	s.Conds.Fail(condition.Condition{
		Key:      ConditionKey,
		Severity: condition.Warning,
		Summary:  "push notifications are failing to send",
		Detail:   err.Error(),
		Fix:      "check the Jam host's outbound HTTPS to the push services (fcm.googleapis.com, *.push.services.mozilla.com, *.push.apple.com, *.notify.windows.com); undelivered pushes are retried and dropped after 10 minutes",
	}, FailThreshold)
	return err
}

// healthy resets the failure count and resolves ConditionKey.
func (s *Surface) healthy() {
	s.Conds.Ok(ConditionKey)
}
