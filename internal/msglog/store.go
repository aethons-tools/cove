package msglog

import "time"

// Store is the message-log capability the consumers depend on. The file *Log
// and the Postgres backend (internal/msglog/msglogpg) both satisfy it.
type Store interface {
	Append(m Message) (Message, error)
	ReadInbox(t Target) []Message
	ReadThread(rootID string) []Message
	List(f Filter) []Message
	// SeenIDs returns the ids of messages whose id starts with prefix, in append
	// order — the bounded query the msgport engine uses to rebuild its ingress
	// dedupe set at startup without materializing the whole log. Ids are
	// identifiers only, not an ordering key — see Seq for that.
	SeenIDs(prefix string) []string
	// ListSince returns messages with Seq > afterSeq, in append (Seq) order,
	// capped at limit (limit <= 0 = unbounded). afterSeq <= 0 starts from the
	// beginning. Seq, not id, is the order key: message ids are opaque
	// identifiers (including ingress ids like "in:linear:<uuid>" from other
	// namespaces) and are not comparable across sources.
	ListSince(afterSeq int64, limit int) []Message
	// ReadInboxSince returns messages addressed to t with Seq > afterSeq, in
	// append (Seq) order, capped at limit (<= 0 = unbounded). afterSeq <= 0 =
	// the whole inbox.
	ReadInboxSince(t Target, afterSeq int64, limit int) []Message
	// ReadInboxBefore returns messages addressed to t with Seq < beforeSeq, the
	// `limit` nearest below beforeSeq, in append (ascending Seq) order (limit <=
	// 0 = unbounded). beforeSeq <= 0 means "from the end" (the last `limit`).
	// Pairs with ReadInboxSince for backward paging: next-backward from a page
	// is ReadInboxBefore(t, page.first.Seq, limit).
	ReadInboxBefore(t Target, beforeSeq int64, limit int) []Message
	// SeqOf returns the append-order Seq assigned to the message with the given
	// id, or (0, false) if no such message exists. It is the boundary resolver
	// from an externally-facing id (e.g. a wire cursor) to the internal Seq
	// ordering key.
	SeqOf(id string) (int64, bool)
	// TailSeq returns the last-appended message's Seq, or (0, false) when the
	// log is empty.
	TailSeq() (int64, bool)
	Close() error
}

// Prepare validates m and assigns an ID and At when unset, returning the message
// ready to persist. Both backends call it so id/at assignment and validation
// live in one place.
func Prepare(m Message) (Message, error) {
	if err := m.validate(); err != nil {
		return Message{}, err
	}
	if m.At.IsZero() {
		m.At = time.Now()
	}
	if m.ID == "" {
		m.ID = newID(m.At)
	}
	return m, nil
}
