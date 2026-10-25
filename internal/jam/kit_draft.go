package jam

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitDraftStatus is GET /admin/kits/{name}/draft: the draft, its files, its
// changes from its base version, and what blocks publishing it.
type KitDraftStatus struct {
	Kit       string        `json:"kit"`
	Base      int           `json:"base"`    // the version it was opened from
	Current   int           `json:"current"` // the kit's current version now; ≠ Base when one landed since
	Tree      string        `json:"tree"`
	ETag      string        `json:"etag"` // quoted tree hash: send it back as If-Match
	OpenedAt  time.Time     `json:"opened_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Actor     string        `json:"actor"` // who last wrote it
	Entries   []DraftEntry  `json:"entries"`
	Changes   []DraftChange `json:"changes"`           // vs Base
	Problems  []string      `json:"problems"`          // empty: publishable
	Managed   bool          `json:"managed,omitempty"` // config-managed: publishing drifts
}

// DraftEntry is one file of a draft.
type DraftEntry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"` // "0644" or "0755"
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// DraftChange is one file that differs: kind added, removed, modified or mode.
type DraftChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// KitDraftOpenBody is the POST /admin/kits/{name}/draft request; From 0 = current.
type KitDraftOpenBody struct {
	From int `json:"from,omitempty"`
}

// DraftDriftWarning is the fixed warning a publish of a config-managed kit
// answers with (KitDraftPublish.Warning), shown by the UI and the CLI.
const DraftDriftWarning = "this will drift from the config repo; config write it to keep the repo in sync"

// DraftETag is a draft's ETag: its tree hash, quoted.
func DraftETag(tree string) string { return `"` + tree + `"` }

// unsafeKitNameText is the fixed refusal of a kit name that is not
// tag-safe: the name itself is never echoed.
const unsafeKitNameText = "kit name is not tag-safe (allowed: [A-Za-z0-9_.-])"

func unsafeKitName() error { return writeErr(http.StatusBadRequest, unsafeKitNameText) }

// draftOf is kit name's open draft: 400 for a name that is not tag-safe
// (checked before any text names it), 404 for an unknown kit or no draft.
func draftOf(store Store, name string) (KitDraft, error) {
	if !studio.TagSafeName(name) {
		return KitDraft{}, unsafeKitName()
	}
	if _, ok := store.GetKit(name); !ok {
		return KitDraft{}, writeErr(http.StatusNotFound, "no such kit")
	}
	d, ok := store.GetKitDraft(name)
	if !ok {
		return KitDraft{}, writeErr(http.StatusNotFound, "kit %s has no draft", name)
	}
	return d, nil
}

// checkDraftETag refuses a write without If-Match (428) or against a tree
// that is no longer the draft's (412). It matches exactly "<hash>" or the
// bare hash: a weak tag, *, or an unbalanced or doubled quote never does.
func checkDraftETag(d KitDraft, ifMatch string) error {
	tag := strings.TrimSpace(ifMatch)
	if tag == "" {
		return writeErr(http.StatusPreconditionRequired, "draft %s: If-Match is required (send the etag from the draft status)", d.Kit)
	}
	if inner, quoted := strings.CutPrefix(tag, `"`); quoted {
		tag, quoted = strings.CutSuffix(inner, `"`)
		if !quoted {
			tag = "" // unbalanced: never a tree hash
		}
	}
	if tag != d.Tree {
		return writeErr(http.StatusPreconditionFailed, "draft %s changed since you loaded it (reload)", d.Kit)
	}
	return nil
}

// draftProblems reads a draft tree as a push would and lists what blocks
// publishing it, in fixed text: the parse (kit.yml missing, located at the
// kit as a push locates it; schema, packed-context fields, note files), else
// validateKit (name, chain, budgets). The parsers stop at their first error.
// sk is the parsed kit when there are none; problems is never nil.
func draftProblems(store Store, name string, tree kittree.Tree) (studio.StudioKit, []string) {
	sk, err := studio.ParseTree(tree, store.KitBlob)
	if errors.Is(err, studio.ErrNoKitFile) {
		err = fmt.Errorf("kit %s: %w", name, err)
	}
	if err == nil {
		_, err = validateKit(store, name, sk)
	}
	if err != nil {
		return studio.StudioKit{}, []string{err.Error()}
	}
	return sk, []string{}
}

func draftEntries(t kittree.Tree) []DraftEntry {
	out := make([]DraftEntry, 0, len(t.Entries))
	for _, e := range t.Entries {
		out = append(out, DraftEntry{Path: e.Path, Mode: fmt.Sprintf("%04o", e.Mode), Size: e.Size, SHA256: e.SHA256})
	}
	return out
}

func draftChanges(from, to kittree.Tree) []DraftChange {
	cs, _ := kittree.Changes(from, to)
	out := make([]DraftChange, 0, len(cs))
	for _, c := range cs {
		out = append(out, DraftChange{Path: c.Path, Kind: c.Kind})
	}
	return out
}

// kitManaged reports whether the config repo manages kit name (a managed mark).
func kitManaged(store Store, name string) bool {
	key := ConfigKey{Kind: ConfigKindKit, Name: name}
	return slices.ContainsFunc(store.ListConfigMarks(), func(m ConfigMark) bool { return m.Key() == key })
}

// OpenKitDraft opens kit name's draft from version from (0 = current) for
// actor at now, under kitMu. 400 for an unsafe name, a negative from or a
// version with no stored tree (broken, pre-tree); 404 for an unknown kit or
// version; 409 when the kit already has a draft.
func OpenKitDraft(store Store, name string, from int, actor string, now time.Time) (KitDraft, error) {
	if !studio.TagSafeName(name) {
		return KitDraft{}, unsafeKitName()
	}
	if from < 0 {
		return KitDraft{}, writeErr(http.StatusBadRequest, "from must be a version number")
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	k, ok := store.GetKit(name)
	if !ok {
		return KitDraft{}, writeErr(http.StatusNotFound, "no such kit")
	}
	if from == 0 {
		from = k.Current
	}
	if !k.HasVersion(from) {
		return KitDraft{}, writeErr(http.StatusNotFound, "kit %s has no version v%d", name, from)
	}
	tree, ok := k.Trees[from]
	if _, stored := store.KitTree(tree); !ok || !stored {
		return KitDraft{}, writeErr(http.StatusBadRequest, "kit %s v%d is not stored as a tree", name, from)
	}
	if _, open := store.GetKitDraft(name); open {
		return KitDraft{}, writeErr(http.StatusConflict, "kit %s already has a draft", name)
	}
	d := KitDraft{Kit: name, Base: from, Tree: tree, OpenedAt: now, UpdatedAt: now, Actor: actor}
	if err := store.PutKitDraft(d, KitObjects{}); err != nil {
		return KitDraft{}, err
	}
	return d, nil
}

// KitDraftStatusOf is kit name's draft status, read under kitMu as one
// snapshot (a concurrent save and GC cannot drop its tree mid-read).
func KitDraftStatusOf(store Store, name string) (KitDraftStatus, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	d, err := draftOf(store, name)
	if err != nil {
		return KitDraftStatus{}, err
	}
	tree, err := storedDraftTree(store, d)
	if err != nil {
		return KitDraftStatus{}, err
	}
	k, _ := store.GetKit(name)
	base, _ := store.KitTree(k.Trees[d.Base])
	_, problems := draftProblems(store, name, tree)
	return KitDraftStatus{
		Kit: name, Base: d.Base, Current: k.Current, Tree: d.Tree, ETag: DraftETag(d.Tree),
		OpenedAt: d.OpenedAt, UpdatedAt: d.UpdatedAt, Actor: d.Actor,
		Entries: draftEntries(tree), Changes: draftChanges(base, tree), Problems: problems,
		Managed: kitManaged(store, name),
	}, nil
}

// DiscardKitDraft drops kit name's draft (If-Match checked) under kitMu and
// collects what only it held. It returns the dropped draft (for the log).
func DiscardKitDraft(store Store, log *slog.Logger, name, ifMatch string) (KitDraft, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	d, err := draftOf(store, name)
	if err != nil {
		return KitDraft{}, err
	}
	if err := checkDraftETag(d, ifMatch); err != nil {
		return KitDraft{}, err
	}
	if err := store.RemoveKitDraft(name); err != nil {
		return KitDraft{}, err
	}
	gcKitObjects(store, log)
	return d, nil
}
