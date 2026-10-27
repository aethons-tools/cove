package jam

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
	"github.com/aethons-tools/cove/internal/textesc"
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
	Manager    string    `json:"manager"` // the manager it builds on ("": no build, none named)
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
// context; a parent version gone or unreadable fails the copy. It returns the draft tree and its blobs. Caller holds kitMu.
func loadDraftFilesLocked(store Store, def *KitDefinition, tree kittree.Tree) (KitObjects, error) {
	var objs KitObjects
	last := len(def.Chain) - 1
	for i := range def.Chain {
		link := &def.Chain[i]
		t := tree
		if i < last {
			v, ok := LoadKitVersion(store, link.Ref.ID, link.Ref.Version)
			switch {
			case !ok:
				return KitObjects{}, fmt.Errorf("kit %s@v%d is gone", link.Ref.ID, link.Ref.Version)
			case v.TreeHash == "" && v.KitYML != nil: // pre-tree: no files
				link.Files = nil
				continue
			case v.Err != nil:
				return KitObjects{}, v.Err
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
	id, kit, manager, tree, tag, buildDigest, by string
	seq                                          uint64 // start order: the latest has the highest
	started                                      time.Time
	log                                          *buildLog
	cancel                                       context.CancelFunc
	done                                         chan struct{}

	state, imageID, errText string
	finished                time.Time
}

// KitDraftBuilds runs kit draft test builds and draft studios' images
// (docs/usage/jam/kits-drafts.md#test-build) on named managers. At most one
// build runs per (kit, manager) at a time, each in a goroutine under a
// context derived from the serve's (cancelled on shutdown). Only the latest
// build per (kit, manager) is kept, in memory: its state, image and log are
// lost on restart.
type KitDraftBuilds struct {
	ctx     context.Context
	store   Store
	builder ManagedDraftBuilder
	log     *slog.Logger
	now     func() time.Time
	logCap  int

	mu     sync.Mutex
	jobs   map[string]*draftBuildJob // draftJobKey(kit, manager) → its latest build
	seq    uint64                    // the last job's seq
	closed bool                      // Wait has begun: no build starts
	wg     sync.WaitGroup            // Add under mu
}

// NewKitDraftBuilds runs builds over builder's managers under ctx (the
// serve's build context: cancelling it stops every build).
func NewKitDraftBuilds(ctx context.Context, store Store, builder ManagedDraftBuilder, log *slog.Logger) *KitDraftBuilds {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &KitDraftBuilds{ctx: ctx, store: store, builder: builder, log: log, now: time.Now, logCap: KitDraftLogCap, jobs: map[string]*draftBuildJob{}}
}

// draftJobKey keys a job by its kit and manager.
func draftJobKey(kit, manager string) string { return kit + "\x00" + manager }

// errNoDraftManager: no available manager can build kit drafts.
var errNoDraftManager = fmt.Errorf("%w: no manager can build kit drafts", ErrManagerUnavailable)

// buildManager is the manager a Start builds on: manager, or "" for the
// first draft-capable one. A named one that cannot build now fails as
// managerErr says.
func (b *KitDraftBuilds) buildManager(manager string) (string, error) {
	ms := b.builder.DraftManagers()
	if manager == "" {
		if len(ms) == 0 {
			return "", errNoDraftManager
		}
		return ms[0], nil
	}
	if slices.Contains(ms, manager) {
		return manager, nil
	}
	if err := b.managerErr(manager); err != nil {
		return "", err
	}
	return manager, nil // it came up meanwhile
}

// managerErr is why named manager, absent from DraftManagers, cannot
// build kit drafts: the builder's classification (unknown:
// ErrUnknownManager; away or incapable: ErrManagerUnavailable), asking no
// manager anything. nil only when it is draft-capable on a re-read (it came
// up meanwhile); a classification of nil while it is still absent is
// ErrManagerUnavailable, naming it.
func (b *KitDraftBuilds) managerErr(manager string) error {
	if err := b.builder.DraftManagerErr(manager); err != nil {
		return err
	}
	if slices.Contains(b.builder.DraftManagers(), manager) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrManagerUnavailable, manager)
}

// latestLocked is kit's most recently started build (only a running one
// when running), on any manager; nil when none. Caller holds b.mu.
func (b *KitDraftBuilds) latestLocked(kit string, running bool) *draftBuildJob {
	var latest *draftBuildJob
	for _, j := range b.jobs {
		if j.kit != kit || (running && j.state != DraftBuildRunning) {
			continue
		}
		if latest == nil || j.seq > latest.seq {
			latest = j
		}
	}
	return latest
}

func errDraftBuildRunning(name, manager string) error {
	return writeErr(http.StatusConflict, "a draft build of %s is already running on %s", name, manager)
}

// Start starts a test build of kit name's draft at ETag ifMatch, under the
// default harness, for by, on manager ("" = the first draft-capable one).
// The draft is read and its files copied under kitMu. Refusals: an unknown
// manager (ErrUnknownManager) or one that cannot build now
// (ErrManagerUnavailable), 428/412 for If-Match, 400 for problems or a name
// that cannot form a tag, 404 for no kit or no draft, 409 when the kit
// already has a build running on that manager (checked before any file is
// copied), 503 once shutdown has begun.
func (b *KitDraftBuilds) Start(name, ifMatch, by, manager string) (KitDraftBuild, error) {
	manager, err := b.buildManager(manager)
	if err != nil {
		return KitDraftBuild{}, err
	}
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
	if err := b.canStart(name, manager); err != nil {
		return KitDraftBuild{}, err
	}
	def, _, err := kitDraftDefinitionLocked(b.store, name, tree, harnessinstall.Default())
	if err != nil {
		return KitDraftBuild{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.canStartLocked(name, manager); err != nil {
		return KitDraftBuild{}, err
	}
	return b.viewLocked(b.startLocked(def, manager, d.Tree, tag, by), 0), nil
}

// canStart is canStartLocked under b.mu.
func (b *KitDraftBuilds) canStart(name, manager string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.canStartLocked(name, manager)
}

// canStartLocked refuses a start once shutdown has begun (503) or while kit
// name has a build running on manager (409). Caller holds b.mu, the lock
// wg.Add is taken under, so no build is added once Wait has begun.
func (b *KitDraftBuilds) canStartLocked(name, manager string) error {
	if b.closed || b.ctx.Err() != nil {
		return writeErr(http.StatusServiceUnavailable, "Jam is shutting down")
	}
	if j := b.jobs[draftJobKey(name, manager)]; j != nil && j.state == DraftBuildRunning {
		return errDraftBuildRunning(name, manager)
	}
	return nil
}

// startLocked starts a build of def (kit def.Ref.ID's draft tree tree) on
// manager as the kit's latest there. Caller holds b.mu and has checked
// canStartLocked.
func (b *KitDraftBuilds) startLocked(def KitDefinition, manager, tree, tag, by string) *draftBuildJob {
	ctx, cancel := context.WithCancel(b.ctx)
	b.seq++
	j := &draftBuildJob{
		id: newDraftBuildID(), kit: def.Ref.ID, manager: manager, seq: b.seq, tree: tree, tag: tag, buildDigest: def.Ref.Digest, by: by,
		started: b.now().UTC(), log: &buildLog{cap: b.logCap}, cancel: cancel, done: make(chan struct{}),
		state: DraftBuildRunning,
	}
	b.jobs[draftJobKey(j.kit, manager)] = j
	b.wg.Add(1)
	go b.run(ctx, j, def)
	b.log.Info("kit draft build started", "kit", j.kit, "manager", manager, "tree", shortTree(tree), "build", j.id, "by", by)
	return j
}

// run is one build's goroutine: it builds, writes the outcome as the log's
// last line, and records the state.
func (b *KitDraftBuilds) run(ctx context.Context, j *draftBuildJob, def KitDefinition) {
	defer b.wg.Done()
	defer close(j.done)
	defer j.cancel()
	id, err := b.builder.BuildKitDraftOn(ctx, j.manager, def, j.tag, j.log)
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
	b.log.Info("kit draft build finished", "kit", j.kit, "manager", j.manager, "tree", shortTree(j.tree), "build", j.id, "state", state)
}

// Status is kit name's latest build on manager ("" = the most recently
// started on any), its log from offset since; state unknown when none ran
// since Jam started. 400 for an unsafe name or a negative since, 404 for an
// unknown kit, or a named manager with no build that the builder does not
// know (ErrUnknownManager).
func (b *KitDraftBuilds) Status(name, manager string, since int64) (KitDraftBuild, error) {
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
	j := b.latestLocked(name, false)
	if manager != "" {
		j = b.jobs[draftJobKey(name, manager)]
	}
	b.mu.Unlock()
	if j == nil && manager != "" && !slices.Contains(b.builder.DraftManagers(), manager) {
		if err := b.builder.DraftManagerErr(manager); errors.Is(err, ErrUnknownManager) {
			return KitDraftBuild{}, err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if j == nil {
		v := KitDraftBuild{Kit: name, Manager: manager, State: DraftBuildUnknown}
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
		ID: j.id, Kit: j.kit, Manager: j.manager, Tree: j.tree, State: j.state, Tag: j.tag, Digest: j.imageID, Error: j.errText,
		By: j.by, StartedAt: j.started, FinishedAt: j.finished,
	}
	if d, ok := b.store.GetKitDraft(j.kit); ok {
		v.DraftETag, v.Current = DraftETag(d.Tree), d.Tree == j.tree
	}
	v.Log, v.LogFrom, v.LogEnd = j.log.read(since, j.state != DraftBuildRunning)
	v.LogSkipped = v.LogFrom > since
	return v
}

// Cancel stops kit name's running build on manager ("" = the most
// recently started running one) and waits (until ctx is done) for it to
// end; it is then failed, "cancelled" — unless it completed before the
// cancel landed, when it ends succeeded. 404 when none is running. When ctx
// ends first the error is ctx's, with the build as it is (its id).
func (b *KitDraftBuilds) Cancel(ctx context.Context, name, manager string) (KitDraftBuild, error) {
	if !studio.TagSafeName(name) {
		return KitDraftBuild{}, unsafeKitName()
	}
	b.mu.Lock()
	j := b.latestLocked(name, true)
	if manager != "" {
		j = b.jobs[draftJobKey(name, manager)]
	}
	running := j != nil && j.state == DraftBuildRunning
	b.mu.Unlock()
	if !running {
		if manager != "" && j == nil && !slices.Contains(b.builder.DraftManagers(), manager) {
			if err := b.builder.DraftManagerErr(manager); errors.Is(err, ErrUnknownManager) {
				return KitDraftBuild{}, err
			}
		}
		if manager != "" {
			return KitDraftBuild{}, writeErr(http.StatusNotFound, "kit %s has no draft build running on %s", name, textesc.Escape(manager, false))
		}
		return KitDraftBuild{}, writeErr(http.StatusNotFound, "kit %s has no draft build running", name)
	}
	j.cancel()
	var err error
	select {
	case <-j.done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.viewLocked(j, 0), err
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

// String names a draft studio's kit where a kit version would read
// kit@vN: "<kit> (draft @<tree[:7]>)", never @v0.
func (r KitDraftRef) String() string { return fmt.Sprintf("%s (draft @%.7s)", r.Kit, r.Tree) }

// DraftKitDefinition is what a draft studio of kit name runs: the draft's
// tree as of now, read under kitMu and checked as a test build checks it
// (problems refuse, 400), under the raising role's harness h. Its files are
// not copied yet: Ensure copies them only once it knows it must build.
func DraftKitDefinition(store Store, name string, h harnessinstall.Install) (KitDefinition, KitDraftRef, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	d, err := draftOf(store, name)
	if err != nil {
		return KitDefinition{}, KitDraftRef{}, err
	}
	tree, err := storedDraftTree(store, d)
	if err != nil {
		return KitDefinition{}, KitDraftRef{}, err
	}
	def, err := draftDefinitionLocked(store, name, tree, h)
	return def, KitDraftRef{Kit: name, Tree: d.Tree}, err
}

// Ensure is a draft studio's image: def (DraftKitDefinition: kit ref.Kit's
// draft tree ref.Tree under the raising role's harness), built through the
// same jobs as a test build, for by
// (docs/usage/jam/kits-drafts.md#draft-studio), and the manager holding it,
// which the studio must run on. The candidates are managers (the raise's
// placement; empty = any) that can build drafts now, in managers' order
// (else the builder's). It also returns the tree and its blobs, which the
// raise re-stores should GC take them before its instance is recorded.
//   - The first candidate whose latest build is of exactly that definition
//     (tree and digest, so the harness too), succeeded, with its image
//     still present there, is reused.
//   - Otherwise, on the first candidate: a build of it already running is
//     waited for, or a new build starts and is waited for, until ctx is
//     done. The build runs on regardless, for a later raise to reuse.
//
// No candidate is ErrManagerUnavailable. A running build of another tree or
// harness on the first candidate is 409 and shutdown is 503, both before
// any file is copied. A failed build is 400, with the build's failure line.
func (b *KitDraftBuilds) Ensure(ctx context.Context, def KitDefinition, ref KitDraftRef, by string, managers []string) (KitDraftImage, string, KitObjects, error) {
	tag, err := KitDraftTag(ref.Kit, ref.Tree)
	if err != nil {
		return KitDraftImage{}, "", KitObjects{}, err
	}
	candidates := b.builder.DraftManagers()
	if len(managers) > 0 {
		var in []string
		for _, m := range managers {
			if slices.Contains(candidates, m) && !slices.Contains(in, m) {
				in = append(in, m)
			}
		}
		candidates = in
	}
	if len(candidates) == 0 {
		if len(managers) > 0 {
			// None requested can build: the first one's own refusal.
			if err := b.managerErr(managers[0]); err != nil {
				return KitDraftImage{}, "", KitObjects{}, err
			}
			candidates = []string{managers[0]} // it came up meanwhile
		} else {
			return KitDraftImage{}, "", KitObjects{}, errNoDraftManager
		}
	}
	same := func(j *draftBuildJob) bool { return j != nil && j.tree == ref.Tree && j.buildDigest == def.Ref.Digest }
	for _, m := range candidates {
		b.mu.Lock()
		j := b.jobs[draftJobKey(ref.Kit, m)]
		id := ""
		if same(j) && j.state == DraftBuildSucceeded {
			id = j.imageID
		}
		b.mu.Unlock()
		if id == "" {
			continue
		}
		if ok, herr := b.builder.HasKitDraftImageOn(m, id); herr == nil && ok {
			objs, err := b.draftObjects(ref)
			return KitDraftImage{Tag: tag, ID: id}, m, objs, err
		}
	}
	manager := candidates[0]
	if err := b.canEnsure(ref.Kit, manager, same); err != nil {
		return KitDraftImage{}, "", KitObjects{}, err
	}
	j, objs, err := b.waitOrStart(def, ref, manager, tag, by, same)
	if err != nil {
		return KitDraftImage{}, "", KitObjects{}, err
	}
	select {
	case <-j.done:
	case <-ctx.Done():
		return KitDraftImage{}, "", KitObjects{}, ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if j.state != DraftBuildSucceeded {
		line, _, _ := strings.Cut(j.errText, "\n")
		return KitDraftImage{}, "", KitObjects{}, writeErr(http.StatusBadRequest, "draft %s: the test build failed (see at-jam kit draft build %s --show --manager %s): %s", ref.Kit, ref.Kit, manager, textesc.Escape(line, false))
	}
	return KitDraftImage{Tag: j.tag, ID: j.imageID}, manager, objs, nil
}

// canEnsure is Ensure's refusal before it copies any file: a running build
// of kit on manager other than same (409), or shutdown (503) when none runs.
func (b *KitDraftBuilds) canEnsure(kit, manager string, same func(*draftBuildJob) bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if j := b.jobs[draftJobKey(kit, manager)]; j != nil && j.state == DraftBuildRunning {
		if !same(j) {
			return errDraftBuildRunning(kit, manager)
		}
		return nil
	}
	return b.canStartLocked(kit, manager)
}

// waitOrStart copies def's files under kitMu, then (under b.mu, rechecked)
// returns the running build of the same definition on manager, or starts
// one there. It returns the draft tree's objects too.
func (b *KitDraftBuilds) waitOrStart(def KitDefinition, ref KitDraftRef, manager, tag, by string, same func(*draftBuildJob) bool) (*draftBuildJob, KitObjects, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	tree, err := b.draftTreeLocked(ref)
	if err != nil {
		return nil, KitObjects{}, err
	}
	objs, err := loadDraftFilesLocked(b.store, &def, tree)
	if err != nil {
		return nil, KitObjects{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if j := b.jobs[draftJobKey(ref.Kit, manager)]; j != nil && j.state == DraftBuildRunning && same(j) {
		return j, objs, nil
	}
	if err := b.canStartLocked(ref.Kit, manager); err != nil {
		return nil, KitObjects{}, err
	}
	return b.startLocked(def, manager, ref.Tree, tag, by), objs, nil
}

// draftObjects is draft tree ref.Tree and its blobs, read under kitMu.
func (b *KitDraftBuilds) draftObjects(ref KitDraftRef) (KitObjects, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	tree, err := b.draftTreeLocked(ref)
	if err != nil {
		return KitObjects{}, err
	}
	blobs, err := copyBlobs(b.store, tree)
	if err != nil {
		return KitObjects{}, err
	}
	return KitObjects{Trees: []kittree.Tree{tree}, Blobs: blobs}, nil
}

// draftTreeLocked is draft tree ref.Tree; 409 when it is gone (the draft
// was edited or discarded, and collected, since DraftKitDefinition read
// it). Caller holds kitMu.
func (b *KitDraftBuilds) draftTreeLocked(ref KitDraftRef) (kittree.Tree, error) {
	t, ok := b.store.KitTree(ref.Tree)
	if !ok {
		return kittree.Tree{}, writeErr(http.StatusConflict, "draft %s changed during the raise (retry)", ref.Kit)
	}
	return t, nil
}
