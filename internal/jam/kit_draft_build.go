package jam

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// Draft build states (KitDraftBuild.State).
const (
	DraftBuildRunning   = "running"
	DraftBuildSucceeded = "succeeded"
	DraftBuildFailed    = "failed"
	DraftBuildUnknown   = "unknown" // no build of the kit since Jam started
)

// KitDraftLogCap is how much of a build's log is kept: its last 1 MiB.
const KitDraftLogCap = 1 << 20

// KitDraftBuilder is implemented by a Launcher that can test-build a kit
// draft (the colima launcher). It builds def's chain with the real harness
// and hardening, the top stage from the draft's tree, tagged tag (never a
// kit version's image or stage tag). Its output goes to log, and it stops
// when ctx is done. It returns the built image's id (sha256:…).
type KitDraftBuilder interface {
	BuildKitDraft(ctx context.Context, def KitDefinition, tag string, log io.Writer) (imageID string, err error)
	// HasKitDraftImage reports whether a built image id is still present.
	HasKitDraftImage(imageID string) (bool, error)
}

// KitDraftImage is a draft build's image: its tag and its id. A draft
// studio runs the id, so a later build moving the tag cannot change it.
type KitDraftImage struct {
	Tag string `json:"tag"`
	ID  string `json:"id"`
}

// KitDraftRef names what a draft studio runs: kit Kit's draft tree Tree as
// of its raise.
type KitDraftRef struct {
	Kit  string `json:"kit"`
	Tree string `json:"tree"`
}

// KitDraftBuild is a kit's latest draft build
// (GET /admin/kits/{name}/draft/build). Log is the log from the offset
// asked for: LogFrom is its first byte's offset (past since when bytes were
// dropped: LogSkipped), and LogEnd is the since of the next read. Current
// reports whether Tree is the draft's tree now; DraftETag is the draft's
// ETag now ("" without a draft). The log is docker's output, kept raw:
// consumers escape it.
type KitDraftBuild struct {
	ID         string    `json:"id,omitempty"`
	Kit        string    `json:"kit"`
	Tree       string    `json:"tree,omitempty"`
	State      string    `json:"state"`
	Tag        string    `json:"tag,omitempty"`
	Digest     string    `json:"digest,omitempty"` // the built image's id, on success
	Error      string    `json:"error,omitempty"`
	By         string    `json:"by,omitempty"` // the operator, or "studio raise <label>"
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	DraftETag  string    `json:"draft_etag,omitempty"`
	Current    bool      `json:"current"`
	Log        string    `json:"log"`
	LogFrom    int64     `json:"log_from"`
	LogEnd     int64     `json:"log_end"`
	LogSkipped bool      `json:"log_skipped,omitempty"`
}

const kitDraftStageSuffix = "-stage"

// KitDraftTag is the image tag a build of kit's draft tree gets:
// cove-kit-draft:<kit>-<tree[:12]>. It is never a kit version's tag. A
// name that cannot make a docker tag (the tag part is at most 128
// characters and must fit the -stage suffix; it cannot start with . or -)
// is refused (400), unnamed.
func KitDraftTag(kit, tree string) (string, error) {
	t := kit + "-" + shortTree(tree)
	if !studio.TagSafeName(kit) || len(t)+len(kitDraftStageSuffix) > 128 || kit[0] == '.' || kit[0] == '-' {
		return "", writeErr(http.StatusBadRequest, "kit name cannot form a draft image tag (at most 109 characters of [A-Za-z0-9_.-], not starting with . or -)")
	}
	return "cove-kit-draft:" + t, nil
}

// KitDraftStageTag is the tag of a draft build's own stage (its Dockerfile
// context built on its parent): never a kit version's stage tag.
func KitDraftStageTag(tag string) string { return tag + kitDraftStageSuffix }

// kitDraftDefinitionLocked is the definition a build of kit name's draft
// tree builds under harness h, its files copied into memory
// (draftDefinitionLocked, then loadDraftFilesLocked). objs is the draft tree
// and its blobs (a draft studio re-stores them should GC take them before
// its instance is recorded). Caller holds kitMu.
func kitDraftDefinitionLocked(store Store, name string, tree kittree.Tree, h harnessinstall.Install) (KitDefinition, KitObjects, error) {
	def, err := draftDefinitionLocked(store, name, tree, h)
	if err != nil {
		return KitDefinition{}, KitObjects{}, err
	}
	objs, err := loadDraftFilesLocked(store, &def, tree)
	if err != nil {
		return KitDefinition{}, KitObjects{}, err
	}
	return def, objs, nil
}

// draftDefinitionLocked is the definition of kit name's draft tree under
// harness h, its files not yet copied: the tree checked as a publish checks
// it (problems refuse, 400) and its parents resolved as a push resolves
// them. Ref is {name, v0, ChainDigest}; the self link has Version 0 and no
// files. It reads no blob beyond what the check parses, so a caller can
// refuse (a running build, a reusable image) before copying any. Caller
// holds kitMu.
func draftDefinitionLocked(store Store, name string, tree kittree.Tree, h harnessinstall.Install) (KitDefinition, error) {
	sk, problems := draftProblems(store, name, tree)
	if len(problems) > 0 {
		return KitDefinition{}, writeErr(http.StatusBadRequest, "draft %s has problems: %s", name, strings.Join(problems, "; "))
	}
	chain, err := validateKit(store, name, sk)
	if err != nil {
		return KitDefinition{}, err
	}
	return chainDefinition(KitRef{ID: name}, sk, chain, h), nil
}

// loadDraftFilesLocked copies every link's files of def (the draft's tree
// for the last, each parent's version tree for the rest) into memory, so an
// edit, a GC or a version removal landing while the build runs cannot pull a
// blob out from under it. A pre-tree parent has no files, and so no build
// context. It returns the draft tree and its blobs. Caller holds kitMu.
func loadDraftFilesLocked(store Store, def *KitDefinition, tree kittree.Tree) (KitObjects, error) {
	var objs KitObjects
	last := len(def.Chain) - 1
	for i := range def.Chain {
		link := &def.Chain[i]
		t := tree
		if i < last {
			v, ok := LoadKitVersion(store, link.Ref.ID, link.Ref.Version)
			if !ok || v.TreeHash == "" {
				link.Files = nil
				continue
			}
			t = v.Tree
		}
		blobs, err := copyBlobs(store, t)
		if err != nil {
			return KitObjects{}, err
		}
		link.Files = kittree.NewFS(t, kittree.MapBlobs(blobs))
		if i == last {
			objs = KitObjects{Trees: []kittree.Tree{t}, Blobs: blobs}
		}
	}
	return objs, nil
}

// copyBlobs reads every blob tree t names. Caller holds kitMu.
func copyBlobs(store KitReader, t kittree.Tree) (map[string][]byte, error) {
	blobs := make(map[string][]byte, len(t.Entries))
	for _, e := range t.Entries {
		b, ok := store.KitBlob(e.SHA256)
		if !ok {
			return nil, fmt.Errorf("kit tree %.12s: a file has no stored content", t.Hash())
		}
		blobs[e.SHA256] = b
	}
	return blobs, nil
}

// buildLog is a build's log: its last cap bytes in memory, offsets counted
// from the build's first byte. Safe for concurrent writes (a command's
// stdout and stderr). The buffer trims to cap once it passes twice cap, so
// a long log costs amortised O(1) per byte; a read never returns more than
// cap.
type buildLog struct {
	mu      sync.Mutex
	buf     []byte
	dropped int64 // bytes trimmed from the head
	cap     int
}

func (l *buildLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	if len(l.buf) > 2*l.cap {
		over := len(l.buf) - l.cap
		l.buf = append(l.buf[:0:0], l.buf[over:]...)
		l.dropped += int64(over)
	}
	return len(p), nil
}

// read is the log from offset since: its text, the offset that text starts
// at (past since when bytes were dropped; clamped to the end), and the end
// offset (the next read's since). While final is false a trailing
// incomplete UTF-8 sequence is held back for the next read.
func (l *buildLog) read(since int64, final bool) (text string, from, end int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	end = l.dropped + int64(len(l.buf))
	head := max(l.dropped, end-int64(l.cap))
	from = min(max(since, head), end)
	chunk := l.buf[from-l.dropped:]
	if !final {
		chunk = completeRunes(chunk)
	}
	return string(chunk), from, from + int64(len(chunk))
}

// completeRunes is b without a trailing incomplete UTF-8 sequence.
func completeRunes(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

// draftBuildJob is one build. The fields after done are guarded by
// KitDraftBuilds.mu; the rest are set before the job is shared.
type draftBuildJob struct {
	id, kit, tree, tag, buildDigest, by string
	started                             time.Time
	log                                 *buildLog
	cancel                              context.CancelFunc
	done                                chan struct{}

	state, imageID, errText string
	finished                time.Time
}

// KitDraftBuilds runs kit draft test builds and draft studios' images
// (docs/usage/jam/kits-drafts.md#test-build). At most one build runs per
// kit at a time, each in a goroutine under a context derived from the
// serve's (cancelled on shutdown). Only the latest build per kit is kept,
// in memory: its state, image and log are lost on restart.
type KitDraftBuilds struct {
	ctx     context.Context
	store   Store
	builder KitDraftBuilder
	log     *slog.Logger
	now     func() time.Time
	logCap  int

	mu     sync.Mutex
	jobs   map[string]*draftBuildJob // kit → its latest build
	closed bool                      // Wait has begun: no build starts
	wg     sync.WaitGroup            // Add under mu
}

// NewKitDraftBuilds runs builds over builder under ctx (the serve's build
// context: cancelling it stops every build).
func NewKitDraftBuilds(ctx context.Context, store Store, builder KitDraftBuilder, log *slog.Logger) *KitDraftBuilds {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &KitDraftBuilds{ctx: ctx, store: store, builder: builder, log: log, now: time.Now, logCap: KitDraftLogCap, jobs: map[string]*draftBuildJob{}}
}

func errDraftBuildRunning(name string) error {
	return writeErr(http.StatusConflict, "kit %s already has a draft build running", name)
}

// Start starts a test build of kit name's draft at ETag ifMatch, under the
// default harness, for by. The draft is read and its files copied under
// kitMu. Refusals: 428/412 for If-Match, 400 for problems or a name that
// cannot form a tag, 404 for no kit or no draft, 409 when the kit already
// has a build running (checked before any file is copied), 503 once
// shutdown has begun.
func (b *KitDraftBuilds) Start(name, ifMatch, by string) (KitDraftBuild, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	d, tree, err := lockedDraftTree(b.store, name, ifMatch)
	if err != nil {
		return KitDraftBuild{}, err
	}
	tag, err := KitDraftTag(name, d.Tree)
	if err != nil {
		return KitDraftBuild{}, err
	}
	if err := b.canStart(name); err != nil {
		return KitDraftBuild{}, err
	}
	def, _, err := kitDraftDefinitionLocked(b.store, name, tree, harnessinstall.Default())
	if err != nil {
		return KitDraftBuild{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.canStartLocked(name); err != nil {
		return KitDraftBuild{}, err
	}
	return b.viewLocked(b.startLocked(def, d.Tree, tag, by), 0), nil
}

// canStart is canStartLocked under b.mu.
func (b *KitDraftBuilds) canStart(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.canStartLocked(name)
}

// canStartLocked refuses a start once shutdown has begun (503) or while kit
// name has a build running (409). Caller holds b.mu, the lock wg.Add is
// taken under, so no build is added once Wait has begun.
func (b *KitDraftBuilds) canStartLocked(name string) error {
	if b.closed || b.ctx.Err() != nil {
		return writeErr(http.StatusServiceUnavailable, "Jam is shutting down")
	}
	if j := b.jobs[name]; j != nil && j.state == DraftBuildRunning {
		return errDraftBuildRunning(name)
	}
	return nil
}

// startLocked starts a build of def (kit def.Ref.ID's draft tree tree) as
// the kit's latest. Caller holds b.mu and has checked canStartLocked.
func (b *KitDraftBuilds) startLocked(def KitDefinition, tree, tag, by string) *draftBuildJob {
	ctx, cancel := context.WithCancel(b.ctx)
	j := &draftBuildJob{
		id: newDraftBuildID(), kit: def.Ref.ID, tree: tree, tag: tag, buildDigest: def.Ref.Digest, by: by,
		started: b.now().UTC(), log: &buildLog{cap: b.logCap}, cancel: cancel, done: make(chan struct{}),
		state: DraftBuildRunning,
	}
	b.jobs[j.kit] = j
	b.wg.Add(1)
	go b.run(ctx, j, def)
	b.log.Info("kit draft build started", "kit", j.kit, "tree", shortTree(tree), "build", j.id, "by", by)
	return j
}

// run is one build's goroutine: it builds, writes the outcome as the log's
// last line, and records the state.
func (b *KitDraftBuilds) run(ctx context.Context, j *draftBuildJob, def KitDefinition) {
	defer b.wg.Done()
	defer close(j.done)
	defer j.cancel()
	id, err := b.builder.BuildKitDraft(ctx, def, j.tag, j.log)
	state, errText := DraftBuildSucceeded, ""
	switch {
	case err == nil:
		fmt.Fprintf(j.log, "\nbuilt %s (%s)\n", j.tag, id)
	case b.ctx.Err() != nil:
		state, errText, id = DraftBuildFailed, "cancelled: Jam is shutting down", ""
	case ctx.Err() != nil:
		state, errText, id = DraftBuildFailed, "cancelled", ""
	default:
		state, errText, id = DraftBuildFailed, err.Error(), ""
	}
	if state == DraftBuildFailed {
		fmt.Fprintf(j.log, "\nbuild failed: %s\n", errText)
	}
	b.mu.Lock()
	j.state, j.imageID, j.errText, j.finished = state, id, errText, b.now().UTC()
	b.mu.Unlock()
	b.log.Info("kit draft build finished", "kit", j.kit, "tree", shortTree(j.tree), "build", j.id, "state", state)
}

// Status is kit name's latest build, its log from offset since; state
// unknown when none ran since Jam started. 400 for an unsafe name or a
// negative since, 404 for an unknown kit.
func (b *KitDraftBuilds) Status(name string, since int64) (KitDraftBuild, error) {
	if !studio.TagSafeName(name) {
		return KitDraftBuild{}, unsafeKitName()
	}
	if since < 0 {
		return KitDraftBuild{}, writeErr(http.StatusBadRequest, "since must be a log offset (0 or more)")
	}
	if _, ok := b.store.GetKit(name); !ok {
		return KitDraftBuild{}, writeErr(http.StatusNotFound, "no such kit")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	j := b.jobs[name]
	if j == nil {
		v := KitDraftBuild{Kit: name, State: DraftBuildUnknown}
		if d, ok := b.store.GetKitDraft(name); ok {
			v.DraftETag = DraftETag(d.Tree)
		}
		return v, nil
	}
	return b.viewLocked(j, since), nil
}

// viewLocked is j as the API shows it, its log from since. Caller holds b.mu.
func (b *KitDraftBuilds) viewLocked(j *draftBuildJob, since int64) KitDraftBuild {
	v := KitDraftBuild{
		ID: j.id, Kit: j.kit, Tree: j.tree, State: j.state, Tag: j.tag, Digest: j.imageID, Error: j.errText,
		By: j.by, StartedAt: j.started, FinishedAt: j.finished,
	}
	if d, ok := b.store.GetKitDraft(j.kit); ok {
		v.DraftETag, v.Current = DraftETag(d.Tree), d.Tree == j.tree
	}
	v.Log, v.LogFrom, v.LogEnd = j.log.read(since, j.state != DraftBuildRunning)
	v.LogSkipped = v.LogFrom > since
	return v
}

// Cancel stops kit name's running build and waits (until ctx is done) for
// it to end; it is then failed, "cancelled". 404 when none is running.
func (b *KitDraftBuilds) Cancel(ctx context.Context, name string) (KitDraftBuild, error) {
	if !studio.TagSafeName(name) {
		return KitDraftBuild{}, unsafeKitName()
	}
	b.mu.Lock()
	j := b.jobs[name]
	running := j != nil && j.state == DraftBuildRunning
	b.mu.Unlock()
	if !running {
		return KitDraftBuild{}, writeErr(http.StatusNotFound, "kit %s has no draft build running", name)
	}
	j.cancel()
	select {
	case <-j.done:
	case <-ctx.Done():
		return KitDraftBuild{}, ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.viewLocked(j, 0), nil
}

// Wait waits up to timeout for every build goroutine to end (after the
// serve context is cancelled); false when some still run. From then on no
// build starts.
func (b *KitDraftBuilds) Wait(timeout time.Duration) bool {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// newDraftBuildID is a build's id: kdb_ and 16 random hex digits.
func newDraftBuildID() string {
	var r [8]byte
	_, _ = rand.Read(r[:])
	return "kdb_" + hex.EncodeToString(r[:])
}
