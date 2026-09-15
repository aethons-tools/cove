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
	// dedupe set at startup without materializing the whole log.
	SeenIDs(prefix string) []string
	// ListSince returns messages with id > afterID, in append order, capped at
	// limit (limit <= 0 = unbounded). afterID == "" starts from the beginning.
	// Append order == lexical id order relies on distinct At nanoseconds (the
	// "%020d-<rand>" id in newID); a sub-nanosecond tie under the single writer
	// is the only case where id order could differ from insertion order.
	ListSince(afterID string, limit int) []Message
	// ReadInboxSince returns messages addressed to t with id > afterID, in append
	// order, capped at limit (<= 0 = unbounded). afterID == "" = the whole inbox.
	ReadInboxSince(t Target, afterID string, limit int) []Message
	// ReadInboxBefore returns messages addressed to t with id < beforeID, the
	// `limit` nearest below beforeID, in append (ascending) order (limit <= 0 =
	// unbounded). beforeID == "" means "from the end" (the last `limit`). Pairs
	// with ReadInboxSince for backward paging: next-backward from a page is
	// ReadInboxBefore(t, page.first, limit).
	ReadInboxBefore(t Target, beforeID string, limit int) []Message
	// TailID returns the highest-id (last-appended) message's id, or ("", false)
	// when the log is empty.
	TailID() (string, bool)
	// SeqOf returns the append-order Seq assigned to the message with the given
	// id, or (0, false) if no such message exists.
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
