package jam

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitOriginDraft is the KitVersionMeta.Origin of a version published from a draft.
const KitOriginDraft = "draft"

// KitDraftPublishBody is the POST /admin/kits/{name}/draft/publish request.
type KitDraftPublishBody struct {
	Pin bool `json:"pin,omitempty"`
}

// KitDraftPublish answers a publish: the new version and its tree, the
// draft's base, the followers pinned ("web v5 (app@v3)"), and, for a
// config-managed kit (or a managed follower pinned), Drift with
// DraftDriftWarning. Warning also says when the draft could not be closed.
type KitDraftPublish struct {
	Kit     string   `json:"kit"`
	Version int      `json:"version"`
	Tree    string   `json:"tree"`
	Base    int      `json:"base"`
	Pinned  []string `json:"pinned,omitempty"`
	Drift   bool     `json:"drift,omitempty"`
	Warning string   `json:"warning,omitempty"`
}

// pendingKitVersion is one kit's next version, planned and validated under
// kitMu (planKitTreeLocked) and not yet written: its ref, its meta, its tree
// and the objects that carry it.
type pendingKitVersion struct {
	ref  KitRef
	meta KitVersionMeta
	tree kittree.Tree
	objs KitObjects
}

// PublishKitDraft makes kit name's draft tree its next version (Origin
// "draft") and closes the draft, under kitMu. The draft's ETag (ifMatch) is
// checked; its tree must have no problems (400) and differ from current's
// (409, the draft kept). A draft based on an older version than current is
// published as it is: nothing is merged. With pin, every kit following name
// is pinned to the version it follows now (pinFollowersLocked); without,
// followers go stale as after any push. The pins and the new version are one
// PutKitDocs, so a failure leaves nothing pinned and nothing published. The
// store has no write that also drops the draft, so the draft is closed right
// after, still under kitMu; should that fail, the answer carries the new
// version and the error.
func PublishKitDraft(store Store, log *slog.Logger, name string, pin bool, ifMatch string) (KitDraftPublish, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	d, tree, err := lockedDraftTree(store, name, ifMatch)
	if err != nil {
		return KitDraftPublish{}, err
	}
	sk, problems := draftProblems(store, name, tree)
	if len(problems) > 0 {
		return KitDraftPublish{}, writeErr(http.StatusBadRequest, "draft %s has problems: %s", name, strings.Join(problems, "; "))
	}
	k, _ := store.GetKit(name)
	if k.Trees[k.Current] == d.Tree {
		return KitDraftPublish{}, writeErr(http.StatusConflict, "draft %s has no changes", name)
	}
	ref, meta, _, err := planKitTreeLocked(store, name, tree, sk, KitOriginDraft)
	if err != nil {
		return KitDraftPublish{}, err
	}
	pending := []pendingKitVersion{{ref: ref, meta: meta, tree: tree, objs: KitObjects{Trees: []kittree.Tree{tree}}}}
	res := KitDraftPublish{Kit: name, Base: d.Base, Drift: kitManaged(store, name)}
	if pin {
		pins, err := pinFollowersLocked(store, name, k.Current)
		if err != nil {
			return KitDraftPublish{}, err
		}
		for _, p := range pins {
			res.Pinned = append(res.Pinned, fmt.Sprintf("%s v%d (%s@v%d)", p.ref.ID, p.ref.Version, name, k.Current))
			res.Drift = res.Drift || kitManaged(store, p.ref.ID)
		}
		pending = append(pending, pins...)
	}
	if err := writeKitVersionsLocked(store, pending); err != nil {
		return KitDraftPublish{}, fmt.Errorf("kit %s: publish: %w", name, err)
	}
	res.Version, res.Tree = ref.Version, d.Tree
	if res.Drift {
		res.Warning = DraftDriftWarning
	}
	if err := store.RemoveKitDraft(name); err != nil {
		// The version is written: the publish succeeded, so it answers
		// 200 with a fixed warning; the store error goes to the log only.
		if log != nil {
			log.Warn("kit draft: published but not closed", "kit", name, "version", ref.Version, "err", err.Error())
		}
		res.Warning = joinWarnings(res.Warning, fmt.Sprintf("published v%d; the draft could not be closed — discard it", ref.Version))
	}
	gcKitObjects(store, log)
	return res, nil
}

// joinWarnings joins two warnings with "; ", either possibly empty.
func joinWarnings(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// pinFollowersLocked plans, for each kit that follows name (an unpinned
// base.kit), a new version whose base.kit is rewritten in place to
// name@v<version>, the version it follows now (studio.SetBaseKit keeps every
// other byte), validated as a push would. Nothing is written: the caller
// writes the plans with its own version (writeKitVersionsLocked). A follower
// that cannot be pinned refuses (409), as does one whose current version
// is not stored as a tree yet (KitsReferencing does not see it). A kit
// whose current version is broken has no kit.yml to read: it is not seen
// as a follower. Caller holds kitMu.
func pinFollowersLocked(store Store, name string, version int) ([]pendingKitVersion, error) {
	if f, ok := preTreeFollower(store, name); ok {
		return nil, writeErr(http.StatusConflict, "kit %s follows %s but is not stored as a tree; pin it by hand", f, name)
	}
	follow, _ := KitsReferencing(store, name)
	ref := fmt.Sprintf("%s@v%d", name, version)
	out := make([]pendingKitVersion, 0, len(follow))
	for _, f := range follow {
		cur, _ := LoadKitVersion(store, f, 0)
		yml, err := studio.SetBaseKit(cur.KitYML, ref)
		if err != nil {
			return nil, cannotPin(f, ref, err)
		}
		files, err := currentFilesWithKitYML(store, f, yml)
		if err != nil {
			return nil, err
		}
		tree, objs, sk, err := kitTreeOf(f, files)
		if err != nil {
			return nil, cannotPin(f, ref, err)
		}
		r, meta, unchanged, err := planKitTreeLocked(store, f, tree, sk, "")
		if err != nil {
			return nil, cannotPin(f, ref, err)
		}
		if !unchanged {
			out = append(out, pendingKitVersion{ref: r, meta: meta, tree: tree, objs: objs})
		}
	}
	return out, nil
}

// preTreeFollower is the first kit (by name) whose current version is not
// stored as a tree yet and whose stored kit.yml text follows name (an
// unpinned base.kit).
func preTreeFollower(store Store, name string) (string, bool) {
	kits := store.ListKits()
	slices.SortFunc(kits, func(a, b Kit) int { return strings.Compare(a.Name, b.Name) })
	for _, k := range kits {
		text, preTree := k.Versions[k.Current]
		if _, tree := k.Trees[k.Current]; tree || !preTree {
			continue
		}
		if _, broken := k.Broken[k.Current]; broken {
			continue
		}
		sk, err := studio.ParseStudioKit([]byte(text))
		if err != nil || !sk.Base.HasParent() {
			continue
		}
		if parent, pin, err := sk.Base.ParentRef(); err == nil && parent == name && pin == 0 {
			return k.Name, true
		}
	}
	return "", false
}

// cannotPin is the 409 refusal of pinning follower f to ref, with why (a
// WriteError's own text).
func cannotPin(f, ref string, err error) error {
	msg := err.Error()
	if we, ok := errors.AsType[*WriteError](err); ok {
		msg = we.Msg
	}
	return writeErr(http.StatusConflict, "kit %s: cannot pin its base.kit to %s (%s)", f, ref, msg)
}

// writeKitVersionsLocked records each planned version as its kit's current
// one, every kit's doc and every object in one PutKitDocs: all or nothing.
// Caller holds kitMu, so the docs read here are the ones the plans saw.
func writeKitVersionsLocked(store Store, vs []pendingKitVersion) error {
	docs := make([]Kit, 0, len(vs))
	objs := KitObjects{Blobs: map[string][]byte{}}
	for _, v := range vs {
		k, _ := store.GetKit(v.ref.ID)
		doc := copyKit(k)
		doc.Name = v.ref.ID
		setKitTree(&doc, v.ref.Version, v.tree.Hash(), v.meta, nil)
		docs = append(docs, doc)
		objs.Trees = append(objs.Trees, v.objs.Trees...)
		maps.Copy(objs.Blobs, v.objs.Blobs)
	}
	return store.PutKitDocs(docs, objs)
}
