package jam

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// kitTreeOf reads files as kit name's version, the way every push does: the
// root .dockerignore applied (ignored files are never stored), then the one
// ingest pipeline, studio.ParseFiles (the tree limits, no packed-context
// field, kit.yml parsed with its note files read from the tree). A refusal of
// the files as a tree is located at the kit ("kit <name>: …"); kit.yml's own
// errors are returned as they are. Errors are the operator's input (callers
// map them to 400).
func kitTreeOf(name string, files []kittree.File) (kittree.Tree, KitObjects, studio.StudioKit, error) {
	files, err := kittree.Ignore(files)
	if err != nil {
		return kittree.Tree{}, KitObjects{}, studio.StudioKit{}, fmt.Errorf("kit %s: %w", name, err)
	}
	tree, blobs, sk, err := studio.ParseFiles(files)
	if err != nil {
		if _, isTree := errors.AsType[*studio.TreeError](err); isTree {
			err = fmt.Errorf("kit %s: %w", name, err)
		}
		return kittree.Tree{}, KitObjects{}, studio.StudioKit{}, err
	}
	return tree, KitObjects{Trees: []kittree.Tree{tree}, Blobs: blobs}, sk, nil
}

// PushKitFiles records files as kit name's next version: the one push path
// of the tar API, the JSON API, the UI and the default kit. The kit is
// validated against its parent chain under kitMu (validateKit). A tree equal
// to the current version's makes no version (unchanged=true). Every refusal
// is a 400 WriteError.
func PushKitFiles(store Store, name string, files []kittree.File) (KitRef, bool, error) {
	tree, objs, sk, err := kitTreeOf(name, files)
	if err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	return pushKitTreeLocked(store, name, tree, objs, sk)
}

// pushKitTreeLocked is PushKitFiles past kitTreeOf; the caller holds kitMu.
func pushKitTreeLocked(store Store, name string, tree kittree.Tree, objs KitObjects, sk studio.StudioKit) (KitRef, bool, error) {
	chain, err := validateKit(store, name, sk)
	if err != nil {
		return KitRef{}, false, err
	}
	digest := studio.ChainDigest(chainKits(chain), harnessinstall.Default())
	hash := tree.Hash()
	if k, ok := store.GetKit(name); ok && k.Current != 0 && k.Trees[k.Current] == hash {
		return KitRef{ID: name, Version: k.Current, Digest: digest}, true, nil
	}
	v, err := store.PushKitVersion(name, hash, objs, KitVersionMeta{PushedAt: time.Now().UTC(), Parent: chainParent(chain)}, nil)
	if err != nil {
		return KitRef{}, false, fmt.Errorf("studio kit %q: push: %w", name, err)
	}
	return KitRef{ID: name, Version: v, Digest: digest}, false, nil
}

// ReplaceKitYML pushes kit name's current files with kit.yml replaced by
// config: the UI's "push a new version", which edits kit.yml only. A kit
// with no tree yet is a one-file push. The current files are read and the
// version pushed under one kitMu hold, so a push landing in between cannot
// be silently reverted.
func ReplaceKitYML(store Store, name, config string) (KitRef, bool, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	out := []kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: []byte(config)}}
	if cur, ok := LoadKitVersion(store, name, 0); ok && cur.TreeHash != "" {
		for _, e := range cur.Tree.Entries {
			if e.Path == kittree.KitFile {
				out[0].Mode = e.Mode
				continue
			}
			b, ok := store.KitBlob(e.SHA256)
			if !ok {
				return KitRef{}, false, fmt.Errorf("kit %s: %s has no stored content", name, e.Path)
			}
			out = append(out, kittree.File{Path: e.Path, Mode: e.Mode, Data: b})
		}
	}
	tree, objs, sk, err := kitTreeOf(name, out)
	if err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	return pushKitTreeLocked(store, name, tree, objs, sk)
}
