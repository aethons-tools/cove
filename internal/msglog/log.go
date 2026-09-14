package msglog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// maxLineBytes bounds a single JSONL line read at open (a message body is far
// smaller in practice; this is a safety cap well above any real message).
const maxLineBytes = 4 << 20 // 4 MiB

// Log is a durable, append-only message log: a JSONL file mirrored in memory.
// The serve process is the sole writer (single-node MVP).
type Log struct {
	mu   sync.Mutex
	path string
	f    *os.File
	msgs []Message
	log  *slog.Logger
}

// Open loads (or creates) the log at path — reading every well-formed line into
// the in-memory mirror, tolerating a torn trailing line (logged + skipped) — and
// opens the file for append. A nil logger discards.
func Open(path string, log *slog.Logger) (*Log, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	l := &Log{path: path, log: log}
	if data, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(data)
		sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var m Message
			if err := json.Unmarshal(line, &m); err != nil {
				log.Warn("msglog: skipping malformed line", "error", err.Error())
				continue
			}
			l.msgs = append(l.msgs, m)
		}
		data.Close()
		// A Scanner error (e.g. an over-long final torn line) is tolerated: the
		// prior good messages are kept.
		if err := sc.Err(); err != nil {
			log.Warn("msglog: scan ended early", "error", err.Error())
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("msglog: open %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("msglog: open-append %s: %w", path, err)
	}
	l.f = f
	return l, nil
}

// Close closes the append handle.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Append validates m, assigns an ID/At when unset, writes one JSONL line, and
// mirrors it in memory. Returns the stored message.
func (l *Log) Append(m Message) (Message, error) {
	m, err := Prepare(m)
	if err != nil {
		return Message{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	line, err := json.Marshal(m)
	if err != nil {
		return Message{}, fmt.Errorf("msglog: marshal: %w", err)
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return Message{}, fmt.Errorf("msglog: write: %w", err)
	}
	l.msgs = append(l.msgs, m)
	return m, nil
}

// snapshot returns a copy of the in-memory mirror (deep enough that callers
// can't mutate stored To slices). Reads in read.go build on this.
func (l *Log) snapshot() []Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Message, len(l.msgs))
	for i, m := range l.msgs {
		m.To = append([]Target(nil), m.To...)
		out[i] = m
	}
	return out
}

// SeenIDs returns ids with the given prefix, in append order.
func (l *Log) SeenIDs(prefix string) []string {
	var out []string
	for _, m := range l.snapshot() {
		if strings.HasPrefix(m.ID, prefix) {
			out = append(out, m.ID)
		}
	}
	return out
}

var _ Store = (*Log)(nil)
