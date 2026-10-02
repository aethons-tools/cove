package sessionevents

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxRecordBytes bounds one JSONL record: a 1 MiB raw line can roughly double
// when JSON-escaped as raw_text, plus the envelope.
const maxRecordBytes = 4 << 20

var safeActorRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// FileStore keeps one append-only JSONL file per (actor, stream) under
// dir/<actor>/<stream>.jsonl. Single-writer (the serve process).
type FileStore struct {
	dir  string
	mu   sync.Mutex
	high map[string]uint64 // path → last appended seq (lazy)
}

func OpenFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session-events-dir: %w", err)
	}
	return &FileStore{dir: dir, high: map[string]uint64{}}, nil
}

func (s *FileStore) path(actorID, streamID string) (string, error) {
	if !safeActorRe.MatchString(actorID) || strings.Trim(actorID, ".") == "" {
		return "", fmt.Errorf("sessionevents: unsafe actor id %q", actorID)
	}
	if !ValidStreamID(streamID) {
		return "", fmt.Errorf("sessionevents: invalid stream id %q", streamID)
	}
	return filepath.Join(s.dir, actorID, streamID+".jsonl"), nil
}

func readAll(path string) ([]Event, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxRecordBytes)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // a torn final line from a crash: skip, never fail the read
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

func (s *FileStore) highLocked(path string) (uint64, error) {
	if hw, ok := s.high[path]; ok {
		return hw, nil
	}
	evs, err := readAll(path)
	if err != nil {
		return 0, err
	}
	var hw uint64
	for _, e := range evs {
		if e.Seq > hw {
			hw = e.Seq
		}
	}
	s.high[path] = hw
	return hw, nil
}

func (s *FileStore) Append(ev Event) error {
	p, err := s.path(ev.ActorID, ev.StreamID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hw, err := s.highLocked(p)
	if err != nil {
		return err
	}
	if ev.Seq <= hw {
		return nil
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.high[p] = ev.Seq
	return nil
}

func (s *FileStore) HighWater(actorID, streamID string) (uint64, error) {
	p, err := s.path(actorID, streamID)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.highLocked(p)
}

func (s *FileStore) List(f Filter) ([]Event, error) {
	p, err := s.path(f.ActorID, f.StreamID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	evs, err := readAll(p)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range evs {
		if e.Seq > f.AfterSeq {
			out = append(out, e)
			if f.Limit > 0 && len(out) == f.Limit {
				break
			}
		}
	}
	return out, nil
}

func (s *FileStore) Streams(actorID string) ([]StreamInfo, error) {
	if !safeActorRe.MatchString(actorID) || strings.Trim(actorID, ".") == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.dir, actorID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []StreamInfo
	for _, de := range entries {
		id, ok := strings.CutSuffix(de.Name(), ".jsonl")
		if !ok || !ValidStreamID(id) {
			continue
		}
		evs, err := readAll(filepath.Join(s.dir, actorID, de.Name()))
		if err != nil || len(evs) == 0 {
			continue
		}
		out = append(out, StreamInfo{StreamID: id, FirstAt: evs[0].ReceivedAt, LastAt: evs[len(evs)-1].ReceivedAt,
			LastSeq: evs[len(evs)-1].Seq, Events: len(evs)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstAt.After(out[j].FirstAt) })
	return out, nil
}

// DeleteBefore removes whole stream files whose last event was received
// before t (a stream is the retention unit for the file backend).
func (s *FileStore) DeleteBefore(t time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := filepath.Glob(filepath.Join(s.dir, "*", "*.jsonl"))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range files {
		evs, err := readAll(p)
		if err != nil || len(evs) == 0 || !evs[len(evs)-1].ReceivedAt.Before(t) {
			continue
		}
		if err := os.Remove(p); err != nil {
			return n, err
		}
		delete(s.high, p)
		n += len(evs)
	}
	return n, nil
}
