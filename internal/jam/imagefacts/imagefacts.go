// Package imagefacts reads a built studio image's facts on the launcher host
// for Jam's setup inspection: `docker image inspect` (env, labels, id) and the
// /agent-data seed via `docker create` + `docker cp … -` + `docker rm`. The
// container is never started. Every command is argv through runner.Runner,
// pinned to the launcher's docker context.
package imagefacts

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/runner"
)

// SeedDir is where an image keeps the seed copied into /agent-data at boot.
const SeedDir = "/home/agent/.init-agent-data"

// Caps on a seed listing.
const (
	MaxSeedBytes   = 1 << 20  // total regular-file bytes
	MaxSeedEntries = 200      // archive entries
	MaxSeedText    = 64 << 10 // a file shown as text
	cacheSize      = 16       // images whose facts are kept
	// maxTarBytes bounds the docker cp stream: the content cap plus a header
	// and padding block per entry.
	maxTarBytes = MaxSeedBytes + (MaxSeedEntries+2)*1024
)

const seedUnreadable = "seed unreadable: docker error on the launcher host"

// seedNone notes an image without a seed dir: a cacheable fact, not an error.
const seedNone = "image seeds nothing (no " + SeedDir + ")"

// noSeedDirRE matches docker cp's stderr for a source path that is absent.
var noSeedDirRE = regexp.MustCompile(`Could not find the file|No such container:path`)

// DockerArgs pins a docker subcommand to the launcher's context
// ((*colima.Colima).DockerArgs).
type DockerArgs interface {
	DockerArgs(args ...string) []string
}

// Reader reads image facts, caching them per image id (LRU). Concurrent
// uncached reads of one image id share a single seed read.
type Reader struct {
	r        runner.Runner
	d        DockerArgs
	mu       sync.Mutex
	cache    map[string]jam.ImageFacts
	order    []string // least recently used first
	inflight map[string]*seedCall
}

// seedCall is one in-flight seed read; done closes once f is final.
// waiters counts callers blocked on it (guarded by Reader.mu).
type seedCall struct {
	done    chan struct{}
	f       jam.ImageFacts
	waiters int
}

var _ jam.ImageFactsReader = (*Reader)(nil)

// New returns a Reader running docker through r, pinned by d.
func New(r runner.Runner, d DockerArgs) *Reader {
	return &Reader{r: r, d: d, cache: map[string]jam.ImageFacts{}, inflight: map[string]*seedCall{}}
}

var (
	containerIDRE = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
	imageIDRE     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type inspectOut struct {
	ID     string `json:"Id"`
	Config struct {
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// ImageFacts inspects image (one `docker image inspect` per call: it names the
// id the cache is keyed on) and reads its seed once per id. A failed inspect
// is ErrImageNotPresent; a failed seed read keeps env and labels, sets
// SeedErr, and is not cached. runner.Runner takes no context, so ctx bounds
// only a wait on another caller's in-flight read of the same image.
func (x *Reader) ImageFacts(ctx context.Context, image string) (jam.ImageFacts, error) {
	if image == "" || strings.HasPrefix(image, "-") {
		return jam.ImageFacts{}, jam.ErrImageNotPresent
	}
	out, err := x.output("image", "inspect", "--format", "{{json .}}", image)
	if err != nil {
		return jam.ImageFacts{}, jam.ErrImageNotPresent
	}
	var in inspectOut
	if err := json.Unmarshal(out, &in); err != nil || !imageIDRE.MatchString(in.ID) {
		return jam.ImageFacts{}, jam.ErrImageUnreadable
	}
	if f, ok := x.get(in.ID); ok {
		return f, nil
	}
	return x.read(ctx, jam.ImageFacts{ID: in.ID, Env: in.Config.Env, Labels: in.Config.Labels}), nil
}

// unreadable is f with its seed marked unreadable.
func unreadable(f jam.ImageFacts) jam.ImageFacts {
	f.Seed, f.SeedNote, f.SeedErr = nil, seedUnreadable, true
	return f
}

// read fills f's seed, joining an in-flight read of the same id if there is
// one (until ctx ends). A cacheable result is cached before the in-flight
// entry is dropped, so a later caller always finds one or the other. The
// cleanup is deferred: a panic in the read (recovered, logged by type only)
// yields SeedErr and never leaves the image wedged.
func (x *Reader) read(ctx context.Context, f jam.ImageFacts) (out jam.ImageFacts) {
	x.mu.Lock()
	if c, ok := x.inflight[f.ID]; ok {
		c.waiters++
		x.mu.Unlock()
		defer func() {
			x.mu.Lock()
			c.waiters--
			x.mu.Unlock()
		}()
		select {
		case <-c.done:
			return c.f
		case <-ctx.Done():
			return unreadable(f)
		}
	}
	if cached, ok := x.cache[f.ID]; ok {
		x.mu.Unlock()
		return cached
	}
	c := &seedCall{done: make(chan struct{}), f: unreadable(f)}
	x.inflight[f.ID] = c
	x.mu.Unlock()
	defer func() {
		if p := recover(); p != nil {
			slog.Error("imagefacts: seed read panicked", "type", fmt.Sprintf("%T", p))
			c.f = unreadable(f)
		}
		x.mu.Lock()
		if !c.f.SeedErr {
			x.putLocked(c.f)
		}
		delete(x.inflight, f.ID)
		x.mu.Unlock()
		close(c.done)
		out = c.f
	}()
	f.Seed, f.SeedNote, f.SeedErr = x.seed(f.ID)
	c.f = f
	return f
}

// output runs one docker command, capturing stdout (stdin empty, stderr dropped).
func (x *Reader) output(args ...string) ([]byte, error) {
	var out bytes.Buffer
	err := x.r.RunIO(strings.NewReader(""), &out, io.Discard, "docker", x.d.DockerArgs(args...)...)
	return out.Bytes(), err
}

// seed creates a stopped container from id, streams its seed dir out as a
// tar, and always removes the container (with its anonymous volumes). The
// container gets an explicit command so an image without CMD can be created;
// it is never started. seedErr reports a docker failure (create or cp); an
// absent seed dir is the cacheable seedNone, and hitting the stream cap is a
// truncation, not a failure.
func (x *Reader) seed(id string) (files []jam.SeedFile, note string, seedErr bool) {
	out, err := x.output("create", "--pull=never", id, "/bin/true")
	ctr := strings.TrimSpace(string(out))
	if !containerIDRE.MatchString(ctr) {
		return nil, seedUnreadable, true
	}
	// A create that printed an id but failed may still have left a container.
	defer func() { _, _ = x.output("rm", "-f", "-v", ctr) }()
	if err != nil {
		return nil, seedUnreadable, true
	}
	w := &capWriter{max: maxTarBytes}
	stderr := &capWriter{max: 4 << 10}
	err = x.r.RunIO(strings.NewReader(""), w, stderr, "docker", x.d.DockerArgs("cp", ctr+":"+SeedDir, "-")...)
	if err != nil && !w.full {
		if noSeedDirRE.Match(stderr.buf.Bytes()) {
			return nil, seedNone, false
		}
		return nil, seedUnreadable, true
	}
	files, note = parseSeedTar(bytes.NewReader(w.buf.Bytes()))
	if w.full && (note == "" || note == noteIncomplete) {
		note = fmt.Sprintf("seed listing truncated at %d bytes", MaxSeedBytes)
	}
	return files, note, false
}

var errTarFull = errors.New("seed archive over its cap")

const noteIncomplete = "seed listing truncated (incomplete archive)"

// countingReader counts the bytes archive/tar has consumed. tar reads whole
// 512-byte blocks straight from it, unbuffered, so the count is an exact
// stream position.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// capWriter keeps at most max bytes, then fails the write (docker cp stops).
type capWriter struct {
	buf  bytes.Buffer
	max  int
	full bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	room := max(w.max-w.buf.Len(), 0)
	if len(p) <= room {
		return w.buf.Write(p)
	}
	w.buf.Write(p[:room])
	w.full = true
	return room, errTarFull
}

// parseSeedTar lists the regular files of a `docker cp <dir> -` archive,
// paths relative to the copied dir, within the caps; note says why a listing
// stopped early. Symlinks, hardlinks, devices and directories are skipped,
// and no path can climb out of the seed root.
func parseSeedTar(r io.Reader) ([]jam.SeedFile, string) {
	cr := &countingReader{r: r}
	tr := tar.NewReader(cr)
	var files []jam.SeedFile
	var total int64
	note := ""
	for entries := 1; ; entries++ {
		// Drain the previous entry's body so a short one is caught, then note
		// where its padding starts: archive/tar reports an archive cut at a
		// block boundary, or missing its end marker, as a clean io.EOF.
		if _, err := io.Copy(io.Discard, tr); err != nil {
			note = noteIncomplete
			break
		}
		bodyEnd := cr.n
		h, err := tr.Next()
		if err == io.EOF {
			// Complete only if the two zero blocks ending an archive were read.
			if cr.n < (bodyEnd+511)/512*512+1024 {
				note = noteIncomplete
			}
			break
		}
		if err != nil {
			note = noteIncomplete
			break
		}
		if entries > MaxSeedEntries {
			note = fmt.Sprintf("seed listing truncated at %d entries", MaxSeedEntries)
			break
		}
		// Every entry's size counts, before any is skipped, so the next
		// drain never expands a huge or sparse body past the cap.
		if h.Size < 0 || total+h.Size > MaxSeedBytes {
			note = fmt.Sprintf("seed listing truncated at %d bytes", MaxSeedBytes)
			break
		}
		total += h.Size
		if h.Typeflag != tar.TypeReg {
			continue
		}
		p, ok := seedPath(h.Name)
		if !ok {
			continue
		}
		f := jam.SeedFile{Path: p, Size: h.Size}
		if h.Size <= MaxSeedText {
			b, err := io.ReadAll(io.LimitReader(tr, MaxSeedText+1))
			if err == nil && int64(len(b)) == h.Size && utf8.Valid(b) {
				f.Text, f.HasText = string(b), true
			}
		}
		files = append(files, f)
	}
	slices.SortFunc(files, func(a, b jam.SeedFile) int { return strings.Compare(a.Path, b.Path) })
	return files, note
}

// seedPath drops the archive's top directory (the copied dir's own name);
// ".." cannot climb out of it.
func seedPath(name string) (string, bool) {
	clean := strings.TrimPrefix(path.Clean("/"+name), "/")
	_, rest, ok := strings.Cut(clean, "/")
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

func (x *Reader) get(id string) (jam.ImageFacts, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	f, ok := x.cache[id]
	if ok {
		x.order = append(slices.DeleteFunc(x.order, func(s string) bool { return s == id }), id)
	}
	return f, ok
}

func (x *Reader) put(f jam.ImageFacts) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.putLocked(f)
}

// putLocked caches f as most recently used, evicting the least recently used
// id past cacheSize. x.mu must be held.
func (x *Reader) putLocked(f jam.ImageFacts) {
	if _, ok := x.cache[f.ID]; ok {
		x.order = slices.DeleteFunc(x.order, func(s string) bool { return s == f.ID })
	}
	x.order = append(x.order, f.ID)
	if len(x.order) > cacheSize {
		delete(x.cache, x.order[0])
		x.order = x.order[1:]
	}
	x.cache[f.ID] = f
}
