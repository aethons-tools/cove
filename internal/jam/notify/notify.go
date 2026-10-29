// Package notify carries "what the phone would buzz for" to a user's open
// Jam streams: Event is push's Payload plus the session to open, Hub fans an
// Event out to one user's subscribers without ever blocking a publisher, and
// Surface is the relay.Surface (service "notify", egress only) that feeds the
// Hub from the channel log.
package notify

import "github.com/aethons-tools/cove/internal/jam/push"

// Service is the relay service name of Surface.
const Service = "notify"

// SubBuffer is how many events a subscriber may fall behind before Publish
// drops the newest.
const SubBuffer = 16

// Event is one notification for one user: push's payload plus the session
// to open.
type Event struct {
	push.Payload
	Session string `json:"session"` // "" = none (a chat, or no live session)
}
