package intercom

import (
	"errors"
	"fmt"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// The channel log (intercom slice 2): every squawk belongs to one channel and
// comes from one participant (a session, user or account id); who hears it —
// its audience — is decided by the poster at append and recorded as
// deliveries, so an inbox is simply the deliveries to a participant. It
// continues the legacy log (LegacyStore): seqs carry on from the legacy tail,
// and ids from both resolve. The channel registry and its policy live in
// internal/jam; this package only stores.

// Squawk is one immutable channel-log entry.
type Squawk struct {
	Seq     int64     `json:"seq"` // monotonic append order (across the legacy log too); assigned at Append
	ID      string    `json:"id"`
	Channel ident.ID  `json:"channel"`
	From    ident.ID  `json:"from"`
	Body    string    `json:"body"`
	At      time.Time `json:"at"`
	ReplyTo string    `json:"reply_to,omitempty"`
	// ContentType is ContentMarkdown (the default) or ContentPlain.
	ContentType string `json:"content_type"`
	// Origin and OriginRef name the surface an ingested squawk came from (a
	// connection and its ref), so egress never renders it back there; empty
	// for a squawk posted in Jam.
	Origin    ident.ID `json:"origin,omitempty"`
	OriginRef string   `json:"origin_ref,omitempty"`
}

// ErrInvalid is a squawk Prepare refuses (no body, channel or author, or a
// bad content type); ErrDuplicateID an id already in either log. Both are
// permanent: appending the same squawk again fails the same way.
var (
	ErrInvalid     = errors.New("intercom: invalid squawk")
	ErrDuplicateID = errors.New("intercom: duplicate squawk id")
)

// ChannelStat is one channel a participant has deliveries in, and the seq of
// the latest.
type ChannelStat struct {
	Channel ident.ID
	LastSeq int64
}

// Store is the channel log. Reads return ascending seq order; a limit <= 0
// is unbounded; a *Before seq <= 0 reads from the end.
type Store interface {
	// Append stores m (Prepare'd) and one delivery per audience member, in one
	// step. An id already in either log is refused.
	Append(m Squawk, audience []ident.ID) (Squawk, error)
	InboxSince(p ident.ID, afterSeq int64, limit int) []Squawk
	InboxBefore(p ident.ID, beforeSeq int64, limit int) []Squawk
	ChannelSince(ch ident.ID, afterSeq int64, limit int) []Squawk
	ChannelBefore(ch ident.ID, beforeSeq int64, limit int) []Squawk
	// InboxChannels lists the channels p has deliveries in, sorted by id.
	InboxChannels(p ident.ID) []ChannelStat
	// ListSince reads the whole (new) log after a seq: the relays' feed.
	ListSince(afterSeq int64, limit int) []Squawk
	// ReadThread is the root (ID == rootID) and its direct replies.
	ReadThread(rootID string) []Squawk
	// SeqOf resolves an id from either log; SeenIDs lists ids with a prefix
	// from both (ingress dedupe).
	SeqOf(id string) (int64, bool)
	SeenIDs(prefix string) []string
	// TailSeq is the last seq appended to either log.
	TailSeq() (int64, bool)
	// CutoverSeq is the first seq of the new log: legacy squawks are below it.
	CutoverSeq() int64
	Close() error
}

// Prepare validates m and assigns an ID, At and ContentType when unset; both
// backends call it.
func Prepare(m Squawk) (Squawk, error) {
	switch {
	case m.Body == "":
		return Squawk{}, fmt.Errorf("%w: empty body", ErrInvalid)
	case m.Channel == "":
		return Squawk{}, fmt.Errorf("%w: no channel", ErrInvalid)
	case m.From == "":
		return Squawk{}, fmt.Errorf("%w: no author", ErrInvalid)
	case !ValidContentType(m.ContentType):
		return Squawk{}, fmt.Errorf("%w: unsupported content type %q", ErrInvalid, m.ContentType)
	}
	if m.At.IsZero() {
		m.At = time.Now()
	}
	if m.ID == "" {
		m.ID = newID(m.At)
	}
	if m.ContentType == "" {
		m.ContentType = ContentMarkdown
	}
	return m, nil
}
