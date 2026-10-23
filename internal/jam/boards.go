package jam

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam/boardrender"
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

	mu      sync.Mutex
	subs    map[chan ident.ID]bool
	renders map[string][]byte // PNG snapshots by board/seq/size
	locks   map[ident.ID]*sync.Mutex
	render  chan struct{} // bounds concurrent renders
}

// lock serializes writes to board id in this process: an agent's apply
// (read, compute, merge) never interleaves with another write.
func (b *Boards) lock(id ident.ID) func() {
	b.mu.Lock()
	l, ok := b.locks[id]
	if !ok {
		l = &sync.Mutex{}
		b.locks[id] = l
	}
	b.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// NewBoards returns boards over lg's store, with ic's access rules.
func NewBoards(lg intercom.Store, ic *Intercom) *Boards {
	return &Boards{lg: lg, ic: ic, subs: map[chan ident.ID]bool{}, renders: map[string][]byte{}, locks: map[ident.ID]*sync.Mutex{}, render: make(chan struct{}, 2)}
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
		// A member — or the board's creator, who may have posted it into a
		// room it isn't a member of.
		ok = b.ic.isMemberOf(ch, viewer) || bd.CreatedBy == viewer
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
		return bd, true // a member of (or the creator in) a live channel posts there
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
	defer b.lock(id)()
	return b.merge(id, docs)
}

// merge merges docs into board id; the caller holds its lock.
func (b *Boards) merge(id ident.ID, docs []json.RawMessage) (int64, []json.RawMessage, error) {
	if len(docs) > intercom.MaxMergeBatch {
		return 0, nil, fmt.Errorf("%w: over %d elements in one merge", intercom.ErrInvalid, intercom.MaxMergeBatch)
	}
	els := make([]intercom.Element, len(docs))
	for i, d := range docs {
		e, err := intercom.ElementOf(d)
		if err != nil {
			return 0, nil, err
		}
		els[i] = e
	}
	before, _ := b.lg.GetBoard(id)
	seq, lost, err := b.lg.MergeElements(id, els)
	if err != nil {
		return 0, nil, err
	}
	out := make([]json.RawMessage, len(lost))
	for i, e := range lost {
		out[i] = e.Doc
	}
	if seq != before.Seq {
		b.publish(id) // something was accepted
	}
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
	seen := map[ident.ID]bool{}
	add := func(bd intercom.Board) {
		if !seen[bd.ID] {
			if _, _, ok := b.CanRead(viewer, bd.ID); ok {
				seen[bd.ID] = true
				out = append(out, bd)
			}
		}
	}
	for _, chID := range b.ic.store.ChannelsOf(viewer) {
		for _, bd := range b.lg.ListBoards(chID) {
			add(bd)
		}
	}
	for _, bd := range b.lg.BoardsCreatedBy(viewer) { // e.g. in a room a session posts to
		add(bd)
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

// ThumbSize is a board card thumbnail's longer side.
const ThumbSize = 480

// maxBoardRequest caps a board request body.
const maxBoardRequest = 8 << 20

// HandleCreate is the create route: {channel (a person) | to (a session), title}
// → 201 {board_id, channel}.
func (b *Boards) HandleCreate(w http.ResponseWriter, r *http.Request, p Poster, now time.Time) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBoardRequest)
	var req struct {
		Channel string `json:"channel"`
		To      string `json:"to"`
		Title   string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	target := req.Channel
	if p.Session != nil {
		target = req.To
	}
	bd, err := b.Create(p, target, req.Title, now)
	if !boardError(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		BoardID ident.ID `json:"board_id"`
		Channel ident.ID `json:"channel"`
	}{bd.ID, bd.Channel})
}

// HandleElements is the elements route: GET ?since= → {seq, elements};
// POST {elements} → {seq, lost}. 404 alike for missing and forbidden.
func (b *Boards) HandleElements(w http.ResponseWriter, r *http.Request, viewer, id ident.ID) {
	switch r.Method {
	case http.MethodGet:
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		els, seq, err := b.Elements(viewer, id, since)
		if !boardError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Seq      int64             `json:"seq"`
			Elements []json.RawMessage `json:"elements"`
		}{seq, nonNil(els)})
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxBoardRequest)
		var req struct {
			Elements []json.RawMessage `json:"elements"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		seq, lost, err := b.Merge(viewer, id, req.Elements)
		if !boardError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Seq  int64             `json:"seq"`
			Lost []json.RawMessage `json:"lost"`
		}{seq, nonNil(lost)})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func nonNil(v []json.RawMessage) []json.RawMessage {
	if v == nil {
		return []json.RawMessage{}
	}
	return v
}

// boardError writes err's status, reporting whether there was none.
func boardError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, intercom.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrSendDenied), errors.Is(err, ErrSendUnresolved), errors.Is(err, ErrRemoved), errors.Is(err, intercom.ErrNoBoard):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, "board request failed", http.StatusInternalServerError)
	}
	return false
}

// allDocs is every element doc of board id, deleted ones included (an op
// may need to see a tombstone's version).
func (b *Boards) allDocs(id ident.ID) []json.RawMessage {
	els, _ := b.lg.BoardElements(id, 0)
	out := make([]json.RawMessage, len(els))
	for i, e := range els {
		out[i] = e.Doc
	}
	return out
}

// Read is board id's compact view for an agent, with its seq.
func (b *Boards) Read(viewer, id ident.ID) ([]CompactElement, int64, error) {
	if _, _, ok := b.CanRead(viewer, id); !ok {
		return nil, 0, ErrSendDenied
	}
	els, seq := b.lg.BoardElements(id, 0)
	docs := make([]json.RawMessage, len(els))
	for i, e := range els {
		docs[i] = e.Doc
	}
	return CompactBoard(docs), seq, nil
}

// Apply applies an agent's ops to board id atomically: the new seq and the
// ids added. A bad op is intercom.ErrInvalid with its reason; nothing changes.
func (b *Boards) Apply(viewer, id ident.ID, ops []BoardOp) (int64, []string, error) {
	if _, ok := b.CanWrite(viewer, id); !ok {
		return 0, nil, ErrSendDenied
	}
	defer b.lock(id)()
	changed, added, err := ApplyBoardOps(b.allDocs(id), ops)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", intercom.ErrInvalid, err)
	}
	seq, lost, err := b.merge(id, changed)
	if err == nil && len(lost) > 0 {
		// Only another Jam process writing at once gets here: say so rather
		// than report a half-applied batch as done.
		return seq, added, fmt.Errorf("%w: the board changed while applying; read it and retry", intercom.ErrInvalid)
	}
	return seq, added, err
}

// Snapshot renders board id to PNG for viewer (longer side ≤ maxSide),
// cached per (board, seq, size).
func (b *Boards) Snapshot(viewer, id ident.ID, maxSide int) ([]byte, int64, error) {
	if _, _, ok := b.CanRead(viewer, id); !ok {
		return nil, 0, ErrSendDenied
	}
	els, seq := b.lg.BoardElements(id, 0)
	key := fmt.Sprintf("%s/%d/%d", id, seq, maxSide)
	b.mu.Lock()
	if png, ok := b.renders[key]; ok {
		b.mu.Unlock()
		return png, seq, nil
	}
	b.mu.Unlock()
	b.render <- struct{}{} // at most a couple of renders at once
	defer func() { <-b.render }()
	docs := make([]json.RawMessage, len(els))
	for i, e := range els {
		docs[i] = e.Doc
	}
	png, err := boardrender.Render(docs, nil, maxSide)
	if err != nil {
		return nil, 0, err
	}
	b.mu.Lock()
	if len(b.renders) > 256 {
		clear(b.renders) // a small cache: renders are cheap to redo
	}
	b.renders[key] = png
	b.mu.Unlock()
	return png, seq, nil
}

// ServeSnapshot writes the snapshot as image/png (?size= max side; default
// the full size), 404 alike for missing and forbidden.
func (b *Boards) ServeSnapshot(w http.ResponseWriter, r *http.Request, viewer, id ident.ID) {
	// Two sizes only (a thumbnail, or full), so sizes can't dodge the cache.
	size := boardrender.DefaultMaxSide
	if s, _ := strconv.Atoi(r.URL.Query().Get("size")); s > 0 && s <= ThumbSize {
		size = ThumbSize
	}
	png, seq, err := b.Snapshot(viewer, id, size)
	if !boardError(w, err) {
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Board-Seq", strconv.FormatInt(seq, 10))
	w.Header().Set("Cache-Control", "private, no-cache")
	_, _ = w.Write(png)
}

// MayCreateIn reports whether person p may start a board in channel ch: they
// may post there and it is live.
func (b *Boards) MayCreateIn(p, ch ident.ID) bool {
	c, ok := b.ic.store.GetChannel(ch)
	if !ok || c.Status != StatusLive {
		return false
	}
	_, err := b.ic.PlanChannel(Poster{ID: p}, ch)
	return err == nil
}
