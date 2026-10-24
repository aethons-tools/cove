package jam

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/go-git/go-billy/v5"

	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitReader is what reading a stored kit version needs: a Store, a
// planner's or exporter's memState, a kit set's overlay.
type KitReader interface {
	GetKit(name string) (Kit, bool)
	KitTree(sha string) (kittree.Tree, bool)
	KitBlob(sha string) ([]byte, bool)
}

// KitFS is the read-only view of stored tree sha; ok=false for an unknown tree.
func KitFS(r KitReader, sha string) (billy.Filesystem, bool) {
	t, ok := r.KitTree(sha)
	if !ok {
		return nil, false
	}
	return kittree.NewFS(t, r.KitBlob), true
}

// KitVersion is one stored kit version as every reader sees it.
type KitVersion struct {
	Name     string
	Version  int
	TreeHash string           // "" for a pre-tree version
	Tree     kittree.Tree     // zero for a pre-tree version
	KitYML   []byte           // kit.yml; a pre-tree version's stored config text
	Files    billy.Filesystem // the version's files; nil for a pre-tree version
	Kit      studio.StudioKit // parsed (note files read, BuildContext set); zero when Err is set
	Err      error            // the version does not parse, or is broken
}

// Key is the version's content identity, for caches and "unchanged" checks:
// "tree:<hash>", or "text:<sha256>" of a pre-tree version's config.
func (v KitVersion) Key() string {
	if v.TreeHash != "" {
		return "tree:" + v.TreeHash
	}
	sum := sha256.Sum256(v.KitYML)
	return "text:" + hex.EncodeToString(sum[:])
}

// LoadKitVersion reads kit name's version (0 = current). ok=false: no such
// kit or version. A broken version, an unstored tree or a kit.yml that does
// not parse is returned with Err set (KitYML still set when it was read).
func LoadKitVersion(r KitReader, name string, version int) (KitVersion, bool) {
	k, ok := r.GetKit(name)
	if !ok {
		return KitVersion{}, false
	}
	if version == 0 {
		version = k.Current
	}
	v := KitVersion{Name: name, Version: version}
	if reason, broken := k.Broken[version]; broken {
		v.Err = fmt.Errorf("kit %s@v%d could not be migrated to a tree: %s", name, version, reason)
		return v, true
	}
	if sha, ok := k.Trees[version]; ok {
		v.TreeHash = sha
		t, ok := r.KitTree(sha)
		if !ok {
			v.Err = fmt.Errorf("kit %s@v%d: tree %.12s is not stored", name, version, sha)
			return v, true
		}
		blobs := kittree.Blobs(r.KitBlob)
		v.Tree, v.Files = t, kittree.NewFS(t, blobs)
		if e, ok := t.Lookup(kittree.KitFile); ok {
			v.KitYML, _ = blobs(e.SHA256)
		}
		v.Kit, v.Err = parseTreeCached(sha, t, blobs)
		return v, true
	}
	if text, ok := k.Versions[version]; ok {
		v.KitYML = []byte(text)
		v.Kit, v.Err = studio.ParseStudioKit([]byte(text))
		return v, true
	}
	return KitVersion{}, false
}

// kitParses caches successful tree parses by tree hash: trees are immutable,
// so a hash always parses the same. Failures are not cached. Unbounded: trees
// are few.
var kitParses sync.Map // tree sha256 → studio.StudioKit

func parseTreeCached(sha string, t kittree.Tree, blobs kittree.Blobs) (studio.StudioKit, error) {
	if sk, ok := kitParses.Load(sha); ok {
		return cloneKit(sk.(studio.StudioKit)), nil
	}
	sk, err := studio.ParseTree(t, blobs)
	if err != nil {
		return studio.StudioKit{}, err
	}
	kitParses.Store(sha, sk)
	return cloneKit(sk), nil
}

// cloneKit deep-copies sk's slices and maps, so a caller cannot reach the cache.
func cloneKit(sk studio.StudioKit) studio.StudioKit {
	sk.Egress = slices.Clone(sk.Egress)
	sk.BuildArgs = maps.Clone(sk.BuildArgs)
	sk.Secrets = maps.Clone(sk.Secrets)
	sk.Notes = slices.Clone(sk.Notes)
	if sk.MCPServers != nil {
		servers := make(map[string]kit.MCPServer, len(sk.MCPServers))
		for name, s := range sk.MCPServers {
			s.Headers = maps.Clone(s.Headers)
			s.Args = slices.Clone(s.Args)
			servers[name] = s
		}
		sk.MCPServers = servers
	}
	sk.Base.ContextFiles = cloneContextTree(sk.Base.ContextFiles)
	sk.Base.StoredContextFiles = cloneContextTree(sk.Base.StoredContextFiles)
	return sk
}

func cloneContextTree(t studio.ContextTree) studio.ContextTree {
	if t == nil {
		return nil
	}
	out := make(studio.ContextTree, len(t))
	for name, n := range t {
		if n.File != nil {
			f := *n.File
			n.File = &f
		}
		n.Dir = cloneContextTree(n.Dir)
		out[name] = n
	}
	return out
}
