package notify

import (
	"context"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam/push"
	"github.com/aethons-tools/cove/internal/relay"
)

// Surface is the notify relay.Surface: egress only. Each relay.Delivery's
// Address is one recipient's user id.
type Surface struct {
	Hub *Hub
	// Event is m's notification for user; ok=false: user is no longer one of
	// its recipients (nothing is published).
	Event func(m intercom.Squawk, user ident.ID) (Event, bool)
	Now   func() time.Time // nil: time.Now
}

// Service implements relay.Surface.
func (s *Surface) Service() string { return Service }

// Poll implements relay.Surface: notify has no ingress.
func (s *Surface) Poll(context.Context, string, string) ([]relay.Event, string, error) {
	return nil, "", nil
}

// Close implements relay.Surface.
func (s *Surface) Close() error { return nil }

// Deliver publishes m's notification to user's open streams. A notification
// is best effort and never stalls the log, so it always returns "", nil: a
// squawk older than push.MaxAge, a user with no open stream (checked before
// building the event) and a user who is no longer a recipient are dropped.
func (s *Surface) Deliver(_ context.Context, d relay.Delivery, m intercom.Squawk) (string, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if now().Sub(m.At) > push.MaxAge {
		return "", nil
	}
	user := ident.ID(d.Address)
	if s.Hub.Subscribers(user) == 0 {
		return "", nil
	}
	if e, ok := s.Event(m, user); ok {
		s.Hub.Publish(user, e)
	}
	return "", nil
}
