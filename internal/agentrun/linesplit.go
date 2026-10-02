package agentrun

import "bytes"

// maxEventLine caps one stream-json line forwarded as a session event; the
// rest is counted, not sent (the full line is still in cove-master.log).
const maxEventLine = 1 << 20

// lineSplitter is an io.Writer that cuts claude's stdout into lines and emits
// each (without the newline), keeping at most max bytes and counting the rest.
// Write never fails, so it is safe inside an io.MultiWriter. emit's slice is
// only valid during the call.
type lineSplitter struct {
	max     int
	emit    func(line []byte, dropped uint64)
	buf     []byte
	dropped uint64
}

func (s *lineSplitter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.keep(p)
			break
		}
		s.keep(p[:i])
		s.Flush()
		p = p[i+1:]
	}
	return n, nil
}

func (s *lineSplitter) keep(c []byte) {
	room := s.max - len(s.buf)
	if room < 0 {
		room = 0
	}
	if room > len(c) {
		room = len(c)
	}
	s.buf = append(s.buf, c[:room]...)
	s.dropped += uint64(len(c) - room)
}

// Flush emits any pending partial line (call once the process has exited).
func (s *lineSplitter) Flush() {
	line := bytes.TrimSuffix(s.buf, []byte("\r"))
	if len(line) > 0 || s.dropped > 0 {
		s.emit(line, s.dropped)
	}
	s.buf, s.dropped = s.buf[:0], 0
}
