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
