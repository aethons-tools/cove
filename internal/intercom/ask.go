package intercom

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// Message parts and asks (intercom structured asks; see
// docs/superpowers/specs/2026-10-21-intercom-asks-design.md). A squawk may
// carry typed parts beside its body: inline (a type and small data) or, once
// attachments land, a reference to a stored blob. The one part type known
// today is an ask event, which the store folds into the asks read-model as
// it appends; Body stays a readable one-line rendering for older readers.

// Part is one typed part of a squawk.
type Part struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"` // inline
	Ref  string          `json:"ref,omitempty"`  // a stored blob (attachments; refused for now)
	Name string          `json:"name,omitempty"`
	Size int64           `json:"size,omitempty"`
}

// AskPartType is the inline part carrying an AskEvent.
const AskPartType = "application/vnd.jam.ask+json"

// TurnEndPartType marks a squawk a cove auto-posted from a session's turn
// end (its final text): no data, at most one. Wake-on skips a session's
// auto-posts, so replies never bounce between sessions.
const TurnEndPartType = "application/vnd.jam.turn-end+json"

// IsTurnEnd reports whether m was auto-posted from a turn end.
func IsTurnEnd(m Squawk) bool {
	for _, p := range m.Parts {
		if p.Type == TurnEndPartType && p.Ref == "" {
			return true
		}
	}
	return false
}

// AskOption is one answer an ask offers.
type AskOption struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// AskEventKind is what an ask event does.
type AskEventKind string

const (
	AskEventAsk      AskEventKind = "ask"
	AskEventAmend    AskEventKind = "amend"
	AskEventWithdraw AskEventKind = "withdraw"
	AskEventAnswer   AskEventKind = "answer"
)

// AskEvent is an ask part's data. AskID is empty on the "ask" event (the
// ask's id is its squawk's); Version is the version an answer names, and is
// set by the store on ask and amend.
type AskEvent struct {
	Event    AskEventKind `json:"event"`
	AskID    string       `json:"ask_id,omitempty"`
	Version  int          `json:"version,omitempty"`
	Question string       `json:"question,omitempty"`
	Options  []AskOption  `json:"options,omitempty"`
	Of       []ident.ID   `json:"of,omitempty"`
	Choice   string       `json:"choice,omitempty"`
	Comment  string       `json:"comment,omitempty"`
	Reason   string       `json:"reason,omitempty"`
}

// AskState is an ask's lifecycle state.
type AskState string

const (
	AskOpen      AskState = "open"
	AskAnswered  AskState = "answered"
	AskWithdrawn AskState = "withdrawn"
)

// AskOther is the choice id of the always-offered "Other…" answer, which
// needs a comment.
const AskOther = "other"

// AskAnswer is how an ask was answered.
type AskAnswer struct {
	Version int       `json:"version"`
	Choice  string    `json:"choice"`
	Comment string    `json:"comment,omitempty"`
	By      ident.ID  `json:"by"`
	At      time.Time `json:"at"`
}

// AskVersion is an earlier wording of an amended ask.
type AskVersion struct {
	Version  int         `json:"version"`
	Question string      `json:"question"`
	Options  []AskOption `json:"options"`
}

// Ask is an ask's current state, folded from its events.
type Ask struct {
	ID       string       `json:"id"`
	Channel  ident.ID     `json:"channel"`
	Asker    ident.ID     `json:"asker"`
	Of       []ident.ID   `json:"of,omitempty"`
	Question string       `json:"question"`
	Options  []AskOption  `json:"options"`
	Version  int          `json:"version"`
	State    AskState     `json:"state"`
	Answer   *AskAnswer   `json:"answer,omitempty"`
	Reason   string       `json:"reason,omitempty"`
	History  []AskVersion `json:"history,omitempty"`
	LastSeq  int64        `json:"last_seq"`
}

// Ask event refusals: no such ask (in that channel); it is answered or
// withdrawn; the answer names a version it no longer has; the author may not
// make this change.
var (
	ErrAskNotFound  = errors.New("intercom: no such ask")
	ErrAskClosed    = errors.New("intercom: ask already closed")
	ErrAskChanged   = errors.New("intercom: ask changed")
	ErrAskForbidden = errors.New("intercom: not allowed on this ask")
)

// Limits on an ask's text.
const (
	maxAskQuestion = 2048
	maxAskLabel    = 200
	maxAskDetail   = 1024
	maxAskComment  = 4096
	maxAskReason   = 1024
	minAskOptions  = 2
	maxAskOptions  = 8
)

var optionID = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// AskPart is ev as an inline part.
func AskPart(ev AskEvent) Part {
	data, _ := json.Marshal(ev) // a plain struct: never fails
	return Part{Type: AskPartType, Data: data}
}

// AskOf is m's ask event, if it carries one.
func AskOf(m Squawk) (AskEvent, bool) {
	for _, p := range m.Parts {
		if p.Type == AskPartType {
			var ev AskEvent
			if json.Unmarshal(p.Data, &ev) == nil {
				return ev, true
			}
		}
	}
	return AskEvent{}, false
}

// withAsk returns m with its ask part replaced by ev.
func withAsk(m Squawk, ev AskEvent) Squawk {
	parts := slices.Clone(m.Parts)
	for i, p := range parts {
		if p.Type == AskPartType {
			parts[i] = AskPart(ev)
		}
	}
	m.Parts = parts
	return m
}

// validateParts checks m's parts: inline only (no attachments yet), known
// types, at most one ask whose data decodes, at most one turn-end mark.
func validateParts(parts []Part) error {
	asks, refs, boards, turnEnds := 0, 0, 0, 0
	for _, p := range parts {
		if p.Ref != "" {
			if err := validateRef(p); err != nil {
				return err
			}
			if refs++; refs > MaxAttachments {
				return fmt.Errorf("%w: more than %d attachments", ErrInvalid, MaxAttachments)
			}
			continue
		}
		if p.Type == BoardPartType {
			if boards++; boards > 1 {
				return fmt.Errorf("%w: more than one board part", ErrInvalid)
			}
			var d struct {
				BoardID ident.ID `json:"board_id"`
			}
			if err := json.Unmarshal(p.Data, &d); err != nil || d.BoardID.Kind() != ident.Board {
				return fmt.Errorf("%w: board part", ErrInvalid)
			}
			continue
		}
		if p.Type == TurnEndPartType {
			if turnEnds++; turnEnds > 1 || len(p.Data) > 0 {
				return fmt.Errorf("%w: turn-end part", ErrInvalid)
			}
			continue
		}
		switch {
		case p.Type != AskPartType:
			return fmt.Errorf("%w: unsupported part type %q", ErrInvalid, p.Type)
		}
		var ev AskEvent
		if err := json.Unmarshal(p.Data, &ev); err != nil {
			return fmt.Errorf("%w: ask part: %v", ErrInvalid, err)
		}
		if asks++; asks > 1 {
			return fmt.Errorf("%w: more than one ask part", ErrInvalid)
		}
	}
	return nil
}

func validateWording(question string, options []AskOption) error {
	if strings.TrimSpace(question) == "" || len(question) > maxAskQuestion {
		return fmt.Errorf("%w: question must be 1–%d bytes", ErrInvalid, maxAskQuestion)
	}
	if len(options) < minAskOptions || len(options) > maxAskOptions {
		return fmt.Errorf("%w: an ask offers %d–%d options", ErrInvalid, minAskOptions, maxAskOptions)
	}
	seen := map[string]bool{}
	for _, o := range options {
		switch {
		case !optionID.MatchString(o.ID) || o.ID == AskOther:
			return fmt.Errorf("%w: option id %q", ErrInvalid, o.ID)
		case seen[o.ID]:
			return fmt.Errorf("%w: duplicate option id %q", ErrInvalid, o.ID)
		case strings.TrimSpace(o.Label) == "" || len(o.Label) > maxAskLabel:
			return fmt.Errorf("%w: option %q label must be 1–%d bytes", ErrInvalid, o.ID, maxAskLabel)
		case len(o.Detail) > maxAskDetail:
			return fmt.Errorf("%w: option %q detail over %d bytes", ErrInvalid, o.ID, maxAskDetail)
		}
		seen[o.ID] = true
	}
	return nil
}

// ApplyAsk folds squawk m — which carries an ask event, and has its ID,
// Channel, From, At and Seq set — onto prev (nil for a new ask), returning
// the new state; prev is never modified. On error nothing should be stored.
func ApplyAsk(prev *Ask, m Squawk) (Ask, error) {
	ev, ok := AskOf(m)
	if !ok {
		return Ask{}, fmt.Errorf("%w: no ask part", ErrInvalid)
	}
	if ev.Event == AskEventAsk {
		if prev != nil {
			return Ask{}, fmt.Errorf("%w: ask %q exists", ErrInvalid, m.ID)
		}
		if err := validateWording(ev.Question, ev.Options); err != nil {
			return Ask{}, err
		}
		return Ask{ID: m.ID, Channel: m.Channel, Asker: m.From, Of: slices.Clone(ev.Of), Question: ev.Question,
			Options: slices.Clone(ev.Options), Version: 1, State: AskOpen, LastSeq: m.Seq}, nil
	}
	if prev == nil || prev.Channel != m.Channel {
		return Ask{}, fmt.Errorf("%w: %q", ErrAskNotFound, ev.AskID)
	}
	a := *prev
	a.Options, a.History, a.Of = slices.Clone(prev.Options), slices.Clone(prev.History), slices.Clone(prev.Of)
	if a.State != AskOpen {
		if a.State == AskAnswered && a.Answer != nil {
			return Ask{}, fmt.Errorf("%w: answered %q", ErrAskClosed, a.Answer.Choice)
		}
		return Ask{}, fmt.Errorf("%w: %s", ErrAskClosed, a.State)
	}
	switch ev.Event {
	case AskEventAmend, AskEventWithdraw:
		if m.From != a.Asker {
			return Ask{}, fmt.Errorf("%w: only the asker may %s it", ErrAskForbidden, ev.Event)
		}
		if ev.Event == AskEventWithdraw {
			if len(ev.Reason) > maxAskReason {
				return Ask{}, fmt.Errorf("%w: reason over %d bytes", ErrInvalid, maxAskReason)
			}
			a.State, a.Reason = AskWithdrawn, ev.Reason
			break
		}
		question, options := a.Question, a.Options
		if ev.Question != "" {
			question = ev.Question
		}
		if len(ev.Options) > 0 {
			options = slices.Clone(ev.Options)
		}
		if err := validateWording(question, options); err != nil {
			return Ask{}, err
		}
		a.History = append(a.History, AskVersion{Version: a.Version, Question: a.Question, Options: a.Options})
		a.Question, a.Options = question, options
		a.Version++
	case AskEventAnswer:
		if m.From.Kind() != ident.User || len(a.Of) > 0 && !slices.Contains(a.Of, m.From) {
			return Ask{}, fmt.Errorf("%w: not yours to answer", ErrAskForbidden)
		}
		if ev.Version != a.Version {
			return Ask{}, fmt.Errorf("%w: answered version %d, now %d", ErrAskChanged, ev.Version, a.Version)
		}
		if len(ev.Comment) > maxAskComment {
			return Ask{}, fmt.Errorf("%w: comment over %d bytes", ErrInvalid, maxAskComment)
		}
		if ev.Choice == AskOther {
			if strings.TrimSpace(ev.Comment) == "" {
				return Ask{}, fmt.Errorf("%w: other needs a comment", ErrInvalid)
			}
		} else if a.option(ev.Choice) == nil {
			return Ask{}, fmt.Errorf("%w: no option %q", ErrInvalid, ev.Choice)
		}
		a.State = AskAnswered
		a.Answer = &AskAnswer{Version: a.Version, Choice: ev.Choice, Comment: ev.Comment, By: m.From, At: m.At}
	default:
		return Ask{}, fmt.Errorf("%w: unknown ask event %q", ErrInvalid, ev.Event)
	}
	a.LastSeq = m.Seq
	return a, nil
}

func (a Ask) option(id string) *AskOption {
	for i := range a.Options {
		if a.Options[i].ID == id {
			return &a.Options[i]
		}
	}
	return nil
}

// ChoiceLabel is the label of choice id ("other" for the Other answer).
func (a Ask) ChoiceLabel(id string) string {
	if o := a.option(id); o != nil {
		return o.Label
	}
	return id
}

// AskBody is the one-line plain rendering of event ev on ask a (its state
// before ev), by byLabel for an answer: what an older reader sees.
func AskBody(ev AskEvent, a Ask, byLabel string) string {
	options := func(q string, opts []AskOption) string {
		labels := make([]string, len(opts))
		for i, o := range opts {
			labels[i] = fmt.Sprintf("(%s) %s", o.ID, o.Label)
		}
		return q + " — " + strings.Join(labels, ", ")
	}
	switch ev.Event {
	case AskEventAsk:
		return "asked: " + options(ev.Question, ev.Options)
	case AskEventAmend:
		q, opts := a.Question, a.Options
		if ev.Question != "" {
			q = ev.Question
		}
		if len(ev.Options) > 0 {
			opts = ev.Options
		}
		return "edited ask: " + options(q, opts)
	case AskEventWithdraw:
		if ev.Reason != "" {
			return "withdrew ask: " + a.Question + " (" + ev.Reason + ")"
		}
		return "withdrew ask: " + a.Question
	case AskEventAnswer:
		s := byLabel + " chose " + a.ChoiceLabel(ev.Choice) + " on: " + a.Question
		if ev.Comment != "" {
			s += " — " + ev.Comment
		}
		return s
	}
	return "ask " + string(ev.Event)
}

// AskFilter selects asks; a zero field matches all.
type AskFilter struct {
	Channel, Asker ident.ID
	State          AskState
}

// Match reports whether a passes f.
func (f AskFilter) Match(a Ask) bool {
	return (f.Channel == "" || a.Channel == f.Channel) && (f.Asker == "" || a.Asker == f.Asker) && (f.State == "" || a.State == f.State)
}

// FoldAsk is a store's step for appending m (Prepare'd, its ID set): when m
// carries an ask event it is validated against the ask's current state (get)
// and m is returned with its part normalized (the ask's id, the version an
// ask or amend makes), with the new state to store once m's seq is known
// (set LastSeq). isAsk is false for any other squawk. On error, append
// nothing.
func FoldAsk(m Squawk, get func(id string) (Ask, bool)) (out Squawk, a Ask, isAsk bool, err error) {
	ev, ok := AskOf(m)
	if !ok {
		return m, Ask{}, false, nil
	}
	var prev *Ask
	if ev.Event == AskEventAsk {
		ev.AskID = m.ID
	} else if cur, ok := get(ev.AskID); ok {
		prev = &cur
	}
	if a, err = ApplyAsk(prev, m); err != nil {
		return Squawk{}, Ask{}, true, err
	}
	if ev.Event == AskEventAsk || ev.Event == AskEventAmend {
		ev.Version = a.Version
	}
	return withAsk(m, ev), a, true, nil
}
