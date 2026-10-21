package jam

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// Whiteboards (docs/usage/jam/whiteboards.md): a board lives in a channel and
// takes its access from it — readable by whoever may read the channel (a
// person who can see it, a session in it), writable by whoever may post
// there; an archived channel's boards are read-only. Merges go to the
// intercom store (per element) and are published on the hub, which /me's
// live push forwards. Board edits never post to chat or wake a session.
type Boards struct {
	lg intercom.Store
	ic *Intercom

	mu   sync.Mutex
	subs map[chan ident.ID]bool
}

// NewBoards returns boards over lg's store, with ic's access rules.
func NewBoards(lg intercom.Store, ic *Intercom) *Boards {
	return &Boards{lg: lg, ic: ic, subs: map[chan ident.ID]bool{}}
}

// Create starts a board titled title and posts its card: a person names the
// channel id in target; a session addresses it like a send ("" = home).
func (b *Boards) Create(p Poster, target, title string, now time.Time) (intercom.Board, error) {
	if title == "" || len(title) > 200 {
		return intercom.Board{}, fmt.Errorf("%w: a title is 1–200 bytes", intercom.ErrInvalid)
	}
	var pl Planned
	var err error
	if p.Session != nil {
		pl, err = b.ic.Plan(p, target, now)
	} else {
		pl, err = b.ic.PlanChannel(p, ident.ID(target))
	}
	if err != nil {
		return intercom.Board{}, err
	}
	bd := intercom.Board{ID: ident.New(ident.Board), Channel: pl.Channel.ID, CreatedBy: p.ID, Title: title, Created: now.UTC()}
	if err := b.lg.CreateBoard(bd); err != nil {
		return intercom.Board{}, err
	}
	data, _ := json.Marshal(struct {
		BoardID ident.ID `json:"board_id"`
	}{bd.ID})
	card := intercom.Squawk{From: p.ID, Body: "started a whiteboard: " + title, ContentType: intercom.ContentPlain,
		Parts: []intercom.Part{{Type: intercom.BoardPartType, Data: data}}}
	if _, err := b.ic.Post(pl, card); err != nil {
		return intercom.Board{}, err
	}
	return bd, nil
}

// CanRead is board id if viewer may read it.
func (b *Boards) CanRead(viewer, id ident.ID) (intercom.Board, Channel, bool) {
	bd, ok := b.lg.GetBoard(id)
	if !ok {
		return intercom.Board{}, Channel{}, false
	}
	ch, ok := b.ic.store.GetChannel(bd.Channel)
	if !ok {
		return intercom.Board{}, Channel{}, false
	}
	if isSessionID(viewer) {
		ok = b.ic.isMemberOf(ch, viewer)
	} else {
		ok = b.ic.CanSee(viewer, ch)
	}
	return bd, ch, ok
}

// CanWrite is board id if viewer may edit it: they may read it and post in
// its live channel.
func (b *Boards) CanWrite(viewer, id ident.ID) (intercom.Board, bool) {
	bd, ch, ok := b.CanRead(viewer, id)
	if !ok || ch.Status != StatusLive {
		return intercom.Board{}, false
	}
	if isSessionID(viewer) {
		return bd, true // a member of a live channel posts there
	}
	if _, err := b.ic.PlanChannel(Poster{ID: viewer}, ch.ID); err != nil {
		return intercom.Board{}, false
	}
	return bd, true
}

// Merge merges docs (Excalidraw elements) into board id as viewer: the new
// seq, and the stored winners of docs that lost. ErrSendDenied when viewer
// may not write; intercom.ErrInvalid for a malformed doc (nothing stored).
func (b *Boards) Merge(viewer, id ident.ID, docs []json.RawMessage) (int64, []json.RawMessage, error) {
	if _, ok := b.CanWrite(viewer, id); !ok {
		return 0, nil, ErrSendDenied
	}
	els := make([]intercom.Element, len(docs))
	for i, d := range docs {
		e, err := intercom.ElementOf(d)
		if err != nil {
			return 0, nil, err
		}
		els[i] = e
	}
	seq, lost, err := b.lg.MergeElements(id, els)
	if err != nil {
		return 0, nil, err
	}
	out := make([]json.RawMessage, len(lost))
	for i, e := range lost {
		out[i] = e.Doc
	}
	b.publish(id)
	return seq, out, nil
}

// Elements are board id's element docs changed after since, and its seq.
func (b *Boards) Elements(viewer, id ident.ID, since int64) ([]json.RawMessage, int64, error) {
	if _, _, ok := b.CanRead(viewer, id); !ok {
		return nil, 0, ErrSendDenied
	}
	els, seq := b.lg.BoardElements(id, since)
	out := make([]json.RawMessage, len(els))
	for i, e := range els {
		out[i] = e.Doc
	}
	return out, seq, nil
}

// ForChannelsOf lists the boards viewer may read, in the channels they're in.
func (b *Boards) ForChannelsOf(viewer ident.ID) []intercom.Board {
	var out []intercom.Board
	for _, chID := range b.ic.store.ChannelsOf(viewer) {
		for _, bd := range b.lg.ListBoards(chID) {
			if _, _, ok := b.CanRead(viewer, bd.ID); ok {
				out = append(out, bd)
			}
		}
	}
	return out
}

// Subscribe receives the ids of boards that changed; a slow subscriber
// misses pushes (it catches up by seq).
func (b *Boards) Subscribe() (<-chan ident.ID, func()) {
	ch := make(chan ident.ID, 16)
	b.mu.Lock()
	b.subs[ch] = true
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *Boards) publish(id ident.ID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- id:
		default:
		}
	}
}
