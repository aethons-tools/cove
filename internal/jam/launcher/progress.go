package launcher

import (
	"bytes"
	"regexp"
	"strconv"
	"sync"

	"github.com/aethons-tools/cove/internal/jam"
)

// Progress receives one long-running launcher job's steps: a kit prepare
// (jam.ActivityPrepareKit) or a kit draft build (jam.ActivityBuildKitDraft).
// Step may be called from a build's output goroutines; Done ends the job.
type Progress interface {
	Step(step, detail string)
	Done()
}

// TrackFunc starts reporting a job of kind for kit (Config.Track).
type TrackFunc func(kind string, kit jam.KitRef) Progress

type noProgress struct{}

func (noProgress) Step(string, string) {}
func (noProgress) Done()               {}

// track starts a job's progress on the configured hook; a no-op without one.
func (l *Launcher) track(kind string, kit jam.KitRef) Progress {
	if l.cfg.Track == nil {
		return noProgress{}
	}
	if p := l.cfg.Track(kind, kit); p != nil {
		return p
	}
	return noProgress{}
}

// buildStepRe matches a `docker build --progress=plain` step header:
// "#7 [3/12] RUN …", or with a stage name, "#12 [harness 2/5] RUN …".
var buildStepRe = regexp.MustCompile(`^#\d+ \[(?:(\S+) )?(\d+)/(\d+)\]`)

// parseBuildStep reads a step header line: its stage ("" for an unnamed
// one) and step k of m; false for any other line.
func parseBuildStep(line string) (stage string, k, m int, ok bool) {
	sm := buildStepRe.FindStringSubmatch(line)
	if sm == nil {
		return "", 0, 0, false
	}
	k, err1 := strconv.Atoi(sm[2])
	m, err2 := strconv.Atoi(sm[3])
	if err1 != nil || err2 != nil || k < 1 || k > m {
		return "", 0, 0, false
	}
	return sm[1], k, m, true
}

// maxBuildLine bounds a buffered output line: a longer one is dropped.
const maxBuildLine = 64 << 10

// buildSteps is an io.Writer over a build's docker output that reports
// ("building", "step k/m") to p: the highest step seen of the stage
// building now (a new stage starts over). Safe for concurrent writes.
type buildSteps struct {
	p Progress

	mu       sync.Mutex
	buf      []byte
	dropping bool // the current line overflowed maxBuildLine
	stage    string
	k        int
}

func newBuildSteps(p Progress) *buildSteps { return &buildSteps{p: p} }

func (b *buildSteps) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			b.add(data)
			break
		}
		b.add(data[:i])
		if !b.dropping {
			b.line(string(b.buf))
		}
		b.buf, b.dropping = b.buf[:0], false
		data = data[i+1:]
	}
	return n, nil
}

// add buffers part of the current line, dropping it once too long. b.mu is held.
func (b *buildSteps) add(part []byte) {
	if b.dropping {
		return
	}
	if len(b.buf)+len(part) > maxBuildLine {
		b.buf, b.dropping = b.buf[:0], true
		return
	}
	b.buf = append(b.buf, part...)
}

// line reports one complete output line's step, if it advances. b.mu is held.
func (b *buildSteps) line(s string) {
	stage, k, m, ok := parseBuildStep(s)
	if !ok {
		return
	}
	if stage == b.stage && k <= b.k {
		return
	}
	b.stage, b.k = stage, k
	b.p.Step("building", "step "+strconv.Itoa(k)+"/"+strconv.Itoa(m))
}
