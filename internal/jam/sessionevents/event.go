// Package sessionevents is Jam's store and live fan-out for managed-cove
// session events — each line of a cove agent's claude stream-json stdout,
// delivered over the Attach stream. It is grpc-free and never imports
// internal/jam. See docs/usage/jam/session-events.md.
package sessionevents

import (
	"bytes"
	"encoding/json"
	"regexp"
	"time"
)

// Event kinds.
const (
	KindEvent = "event"
	KindGap   = "gap"
)

// Stamp is the provenance of the cove session that produced an event.
type Stamp struct {
	Project, Role, Unit, Owner, SessionKind string
	RaisedAt                                time.Time
	// ProjectID and OwnerID are the project's and the personal session's
	// owner's ids; Project and Owner are their names, as labels.
	ProjectID, OwnerID string
}

// Index is the thin, queryable summary derived from a raw stream-json line.
type Index struct {
	Type, Subtype, ToolName, ClaudeSessionID string
	CostUSD                                  float64
	InputTokens, OutputTokens, DurationMS    int64
	IsError                                  bool
}

// Event is one stored session event (or a gap marker).
type Event struct {
	ActorID, StreamID      string
	Seq                    uint64
	Kind                   string // KindEvent | KindGap
	GapFrom, GapTo         uint64 // KindGap only
	Turn                   uint32
	ObservedAt, ReceivedAt time.Time
	TruncatedBytes         uint64
	Raw                    []byte // line bytes (content-equal after a store round trip)
	Stamp                  Stamp
	Index                  Index
}

// Filter selects events from a Store.
type Filter struct {
	ActorID, StreamID string // both required
	AfterSeq          uint64
	Limit             int // <= 0 → unbounded
}

// StreamInfo summarizes one stream of an actor.
type StreamInfo struct {
	StreamID        string
	FirstAt, LastAt time.Time // ReceivedAt of first/last row
	LastSeq         uint64
	Events          int
}

// Store persists session events.
type Store interface {
	// Append persists ev. Appending an (actor, stream, seq) that already exists
	// is a no-op returning nil.
	Append(ev Event) error
	// HighWater is the largest stored seq for the stream (0 if none).
	HighWater(actorID, streamID string) (uint64, error)
	// List returns events in seq order.
	List(f Filter) ([]Event, error)
	// Streams lists an actor's streams, newest (by FirstAt) first.
	Streams(actorID string) ([]StreamInfo, error)
	// DeleteBefore removes data last received before t; returns rows removed.
	DeleteBefore(t time.Time) (int, error)
}

var streamIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidStreamID reports whether s is a cove-generated stream id. It is validated at the
// ingest, export, and UI boundaries; anything else is rejected.
func ValidStreamID(s string) bool { return streamIDRe.MatchString(s) }

type wireEvent struct {
	ActorID         string          `json:"actor_id"`
	StreamID        string          `json:"stream_id"`
	Seq             uint64          `json:"seq"`
	Kind            string          `json:"kind"`
	GapFrom         uint64          `json:"gap_from,omitempty"`
	GapTo           uint64          `json:"gap_to,omitempty"`
	Turn            uint32          `json:"turn"`
	ObservedAt      time.Time       `json:"observed_at"`
	ReceivedAt      time.Time       `json:"received_at"`
	TruncatedBytes  uint64          `json:"truncated_bytes"`
	Project         string          `json:"project"`
	Role            string          `json:"role"`
	Unit            string          `json:"unit"`
	Owner           string          `json:"owner"`
	ProjectID       string          `json:"project_id,omitempty"`
	OwnerID         string          `json:"owner_id,omitempty"`
	SessionKind     string          `json:"session_kind"`
	RaisedAt        time.Time       `json:"raised_at"`
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	ToolName        string          `json:"tool_name"`
	ClaudeSessionID string          `json:"claude_session_id"`
	CostUSD         float64         `json:"cost_usd"`
	InputTokens     int64           `json:"input_tokens"`
	OutputTokens    int64           `json:"output_tokens"`
	DurationMS      int64           `json:"duration_ms"`
	IsError         bool            `json:"is_error"`
	Raw             json.RawMessage `json:"raw,omitempty"`
	RawText         *string         `json:"raw_text,omitempty"`
}

// MarshalJSON encodes the flat wire form: Raw is embedded as a JSON object
// under "raw" when valid JSON, else as a string under "raw_text".
func (e Event) MarshalJSON() ([]byte, error) {
	w := wireEvent{ActorID: e.ActorID, StreamID: e.StreamID, Seq: e.Seq, Kind: e.Kind, GapFrom: e.GapFrom, GapTo: e.GapTo,
		Turn: e.Turn, ObservedAt: e.ObservedAt, ReceivedAt: e.ReceivedAt, TruncatedBytes: e.TruncatedBytes,
		Project: e.Stamp.Project, Role: e.Stamp.Role, Unit: e.Stamp.Unit, Owner: e.Stamp.Owner,
		ProjectID: e.Stamp.ProjectID, OwnerID: e.Stamp.OwnerID,
		SessionKind: e.Stamp.SessionKind, RaisedAt: e.Stamp.RaisedAt,
		Type: e.Index.Type, Subtype: e.Index.Subtype, ToolName: e.Index.ToolName, ClaudeSessionID: e.Index.ClaudeSessionID,
		CostUSD: e.Index.CostUSD, InputTokens: e.Index.InputTokens, OutputTokens: e.Index.OutputTokens,
		DurationMS: e.Index.DurationMS, IsError: e.Index.IsError}
	switch {
	case len(e.Raw) == 0:
	case json.Valid(e.Raw):
		w.Raw = json.RawMessage(e.Raw)
	default:
		s := string(e.Raw)
		w.RawText = &s
	}
	return json.Marshal(w)
}

// UnmarshalJSON decodes the flat wire form produced by MarshalJSON.
func (e *Event) UnmarshalJSON(b []byte) error {
	var w wireEvent
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*e = Event{ActorID: w.ActorID, StreamID: w.StreamID, Seq: w.Seq, Kind: w.Kind, GapFrom: w.GapFrom, GapTo: w.GapTo,
		Turn: w.Turn, ObservedAt: w.ObservedAt, ReceivedAt: w.ReceivedAt, TruncatedBytes: w.TruncatedBytes,
		Stamp: Stamp{Project: w.Project, Role: w.Role, Unit: w.Unit, Owner: w.Owner, SessionKind: w.SessionKind, RaisedAt: w.RaisedAt,
			ProjectID: w.ProjectID, OwnerID: w.OwnerID},
		Index: Index{Type: w.Type, Subtype: w.Subtype, ToolName: w.ToolName, ClaudeSessionID: w.ClaudeSessionID,
			CostUSD: w.CostUSD, InputTokens: w.InputTokens, OutputTokens: w.OutputTokens, DurationMS: w.DurationMS, IsError: w.IsError}}
	switch {
	case len(w.Raw) > 0:
		e.Raw = []byte(w.Raw)
	case w.RawText != nil:
		e.Raw = []byte(*w.RawText)
	}
	return nil
}

// RawEqual reports whether two raw lines are the same event: JSON-equal when
// both are valid JSON (stores may compact or reorder), byte-equal otherwise.
func RawEqual(a, b []byte) bool {
	if json.Valid(a) && json.Valid(b) {
		var x, y any
		json.Unmarshal(a, &x)
		json.Unmarshal(b, &y)
		xb, _ := json.Marshal(x)
		yb, _ := json.Marshal(y)
		return bytes.Equal(xb, yb)
	}
	return bytes.Equal(a, b)
}
