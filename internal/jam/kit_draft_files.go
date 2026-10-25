package jam

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitDraftSave answers a draft write: the draft's new tree and ETag, the
// files this write changed (vs the draft before it), and the problems that
// now block publishing.
type KitDraftSave struct {
	Kit      string        `json:"kit"`
	Tree     string        `json:"tree"`
	ETag     string        `json:"etag"`
	Changed  []DraftChange `json:"changed"`
	Problems []string      `json:"problems"`
}

// draftPathErr is CheckPath's refusal located at the draft; it never names
// the path.
func draftPathErr(name string, err error) error {
	return writeErr(http.StatusBadRequest, "draft %s: %s", name, err)
}

// draftFileTooLarge is the 413 for a file body over kittree.MaxFile; p has
// passed CheckPath.
func draftFileTooLarge(name, p string) error {
	return writeErr(http.StatusRequestEntityTooLarge, "draft %s: %s", name, kittree.FileTooLarge(p))
}

// checkDraftFileArgs refuses a kit name that is not tag-safe and an unsafe
// path, before any text names either.
func checkDraftFileArgs(name, p string) error {
	if !studio.TagSafeName(name) {
		return unsafeKitName()
	}
	if err := kittree.CheckPath(p); err != nil {
		return draftPathErr(name, err)
	}
	return nil
}

// storedDraftTree is d's tree; its absence is a store fault (a 500).
func storedDraftTree(store Store, d KitDraft) (kittree.Tree, error) {
	t, ok := store.KitTree(d.Tree)
	if !ok {
		return kittree.Tree{}, fmt.Errorf("kit %s draft: tree %.12s is not stored", d.Kit, d.Tree)
	}
	return t, nil
}

// lockedDraftTree is kit name's draft, its ETag checked against ifMatch, and
// its tree. Caller holds kitMu.
func lockedDraftTree(store Store, name, ifMatch string) (KitDraft, kittree.Tree, error) {
	d, err := draftOf(store, name)
	if err != nil {
		return KitDraft{}, kittree.Tree{}, err
	}
	if err := checkDraftETag(d, ifMatch); err != nil {
		return KitDraft{}, kittree.Tree{}, err
	}
	t, err := storedDraftTree(store, d)
	if err != nil {
		return KitDraft{}, kittree.Tree{}, err
	}
	return d, t, nil
}

// saveDraftLocked records next as d's tree (objs carrying it and its new
// blobs) for actor at now, collects what only the old tree held, and answers
// the save. next is validated under the file rules first (400), so no
// unvalidated tree is ever stored; an unchanged tree writes nothing. Caller
// holds kitMu and has checked d's ETag (lockedDraftTree); prev is d's
// current tree.
func saveDraftLocked(store Store, log *slog.Logger, d KitDraft, prev, next kittree.Tree, objs KitObjects, actor string, now time.Time) (KitDraftSave, error) {
	if err := next.ValidateFiles(); err != nil {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s", d.Kit, err)
	}
	hash := next.Hash()
	if hash != d.Tree {
		d.Tree, d.UpdatedAt, d.Actor = hash, now, actor
		if err := store.PutKitDraft(d, objs); err != nil {
			return KitDraftSave{}, err
		}
		gcKitObjects(store, log)
	}
	_, problems := draftProblems(store, d.Kit, next)
	return KitDraftSave{Kit: d.Kit, Tree: hash, ETag: DraftETag(hash), Changed: draftChanges(prev, next), Problems: problems}, nil
}

// freshBlobs looks a blob up among a write's new blobs, then in the store.
func freshBlobs(store Store, fresh map[string][]byte) kittree.Blobs {
	return func(sha string) ([]byte, bool) {
		if b, ok := fresh[sha]; ok {
			return b, true
		}
		return store.KitBlob(sha)
	}
}

// KitDraftFile is file p of kit name's draft: its bytes, its entry and the
// draft's ETag, read under kitMu.
func KitDraftFile(store Store, name, p string) ([]byte, kittree.Entry, string, error) {
	if err := checkDraftFileArgs(name, p); err != nil {
		return nil, kittree.Entry{}, "", err
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	d, err := draftOf(store, name)
	if err != nil {
		return nil, kittree.Entry{}, "", err
	}
	t, err := storedDraftTree(store, d)
	if err != nil {
		return nil, kittree.Entry{}, "", err
	}
	e, ok := t.Lookup(p)
	if !ok {
		return nil, kittree.Entry{}, "", writeErr(http.StatusNotFound, "draft %s has no file %s", name, p)
	}
	b, ok := store.KitBlob(e.SHA256)
	if !ok {
		return nil, kittree.Entry{}, "", fmt.Errorf("kit %s draft: %s has no stored content", name, p)
	}
	return b, e, DraftETag(d.Tree), nil
}

// PutKitDraftFile writes file p (data, mode 0644 or 0755; 0 keeps the
// file's mode, else 0644) into kit name's draft, under kitMu and against
// ifMatch. The draft's .dockerignore applies: p excluded refuses, and a
// .dockerignore drops the files it newly excludes. The file rules hold
// (kittree.Tree.ValidateFiles, in saveDraftLocked); kit.yml may be missing or invalid.
func PutKitDraftFile(store Store, log *slog.Logger, name, p string, mode fs.FileMode, data []byte, ifMatch, actor string, now time.Time) (KitDraftSave, error) {
	if err := checkDraftFileArgs(name, p); err != nil {
		return KitDraftSave{}, err
	}
	if mode != 0 && mode != 0o644 && mode != 0o755 {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: mode must be 0644 or 0755", name)
	}
	if int64(len(data)) > kittree.MaxFile {
		return KitDraftSave{}, draftFileTooLarge(name, p)
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	d, cur, err := lockedDraftTree(store, name, ifMatch)
	if err != nil {
		return KitDraftSave{}, err
	}
	if mode == 0 {
		mode = 0o644
		if e, ok := cur.Lookup(p); ok {
			mode = e.Mode
		}
	}
	one, blobs, err := kittree.BuildFiles([]kittree.File{{Path: p, Mode: mode, Data: data}})
	if err != nil {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s", name, err)
	}
	next, err := kittree.IgnoreTree(cur.With(one.Entries[0]), freshBlobs(store, blobs))
	if err != nil {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s", name, err)
	}
	if _, kept := next.Lookup(p); !kept {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s: excluded by .dockerignore", name, p)
	}
	return saveDraftLocked(store, log, d, cur, next, KitObjects{Trees: []kittree.Tree{next}, Blobs: blobs}, actor, now)
}

// RemoveKitDraftFile removes file p from kit name's draft, under kitMu and
// against ifMatch: 404 when the draft has no such file.
func RemoveKitDraftFile(store Store, log *slog.Logger, name, p, ifMatch, actor string, now time.Time) (KitDraftSave, error) {
	if err := checkDraftFileArgs(name, p); err != nil {
		return KitDraftSave{}, err
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	d, cur, err := lockedDraftTree(store, name, ifMatch)
	if err != nil {
		return KitDraftSave{}, err
	}
	if _, ok := cur.Lookup(p); !ok {
		return KitDraftSave{}, writeErr(http.StatusNotFound, "draft %s has no file %s", name, p)
	}
	next := cur.Without(p)
	return saveDraftLocked(store, log, d, cur, next, KitObjects{Trees: []kittree.Tree{next}}, actor, now)
}

// ReplaceKitDraft replaces kit name's draft tree with files (a sync): their
// root .dockerignore applied as on a push, the file rules held, kit.yml
// optional. The input is checked before kitMu is taken; the ETag under it.
func ReplaceKitDraft(store Store, log *slog.Logger, name string, files []kittree.File, ifMatch, actor string, now time.Time) (KitDraftSave, error) {
	if !studio.TagSafeName(name) {
		return KitDraftSave{}, unsafeKitName()
	}
	files, err := kittree.Ignore(files)
	if err != nil {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s", name, err)
	}
	next, blobs, err := kittree.BuildFiles(files)
	if err != nil {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s", name, err)
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	d, cur, err := lockedDraftTree(store, name, ifMatch)
	if err != nil {
		return KitDraftSave{}, err
	}
	return saveDraftLocked(store, log, d, cur, next, KitObjects{Trees: []kittree.Tree{next}, Blobs: blobs}, actor, now)
}
