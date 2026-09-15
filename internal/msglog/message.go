// Package msglog is harbor's durable, append-only message Log: one envelope
// (Message{From, To[], Body, …}) for all comms, over a JSONL file mirrored in
// memory. A Target's Reach (Internal/External) decides whether it's delivered
// in-band (a cove reads its inbox) or later rendered onto a human surface by an
// adapter. This package is stdlib-only and imports nothing from internal/harbor;
// harbor consumes it. Single-node (the serve process is the sole writer).
package msglog

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Target addresses a participant or conduit.
//
//	actor:<coveID>  — an internal harbor cove (Reach Internal; delivered in-band)
//	human:<name>    — an external human (Reach External; rendered by an adapter)
//	channel:<name>  — a shared conduit (a ticket thread, a chat channel)
type Target struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

func (t Target) String() string { return t.Kind + ":" + t.Ref }

func validKind(k string) bool { return k == "actor" || k == "human" || k == "channel" }

func (t Target) valid() bool { return validKind(t.Kind) && t.Ref != "" }

// Message is one immutable Log entry.
type Message struct {
	Seq     int64     `json:"seq"` // monotonic append order; assigned at Append, 0 before
	ID      string    `json:"id"`
	From    Target    `json:"from"`
	To      []Target  `json:"to"`
	Body    string    `json:"body"`
	At      time.Time `json:"at"`
	Project string    `json:"project,omitempty"`
	ReplyTo string    `json:"reply_to,omitempty"`
}

func (m Message) validate() error {
	if m.Body == "" {
		return fmt.Errorf("msglog: empty body")
	}
	if !m.From.valid() {
		return fmt.Errorf("msglog: invalid from %q", m.From.String())
	}
	if len(m.To) == 0 {
		return fmt.Errorf("msglog: empty to")
	}
	for _, t := range m.To {
		if !t.valid() {
			return fmt.Errorf("msglog: invalid to %q", t.String())
		}
	}
	return nil
}

// Reach classifies a Target for delivery.
type Reach int

const (
	Internal Reach = iota // delivered in-band (a cove reads its inbox); never touches an adapter
	External              // rendered onto a surface by an adapter
)

// Classify is the structural reach rule, correct for every target kind that
// exists today: actor → Internal, human → External, channel → External (every
// current channel is a Linear ticket, an external surface). Per-channel
// internal/external resolution — for a future agent-only channel — is deferred
// to when such a channel type exists (it will take a directory lookup then).
func Classify(t Target) Reach {
	if t.Kind == "actor" {
		return Internal
	}
	return External
}

// newID returns a time-sortable, unique id: zero-padded UnixNano + a random
// suffix, so append order equals lexical id order.
func newID(at time.Time) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%020d-%s", at.UnixNano(), hex.EncodeToString(b))
}
