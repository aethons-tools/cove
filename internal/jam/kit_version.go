package jam

import (
	"container/list"
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
		v.Err = fmt.Errorf("kit %s@v%d is not stored as a tree yet (Jam migrates kits at serve start)", name, version)
		return v, true
	}
	return KitVersion{}, false
}

// kitParseCap bounds kitParses.
const kitParseCap = 256

// kitParses caches successful tree parses by tree hash: trees are immutable,
// so a hash always parses the same. Failures are not cached. It holds the
// kitParseCap most recently used trees; GCKitObjects drops the trees it
// deletes (forgetKitParses).
var kitParses = newParseLRU(kitParseCap)

func parseTreeCached(sha string, t kittree.Tree, blobs kittree.Blobs) (studio.StudioKit, error) {
	if sk, ok := kitParses.get(sha); ok {
		return cloneKit(sk), nil
	}
	sk, err := studio.ParseTree(t, blobs)
	if err != nil {
		return studio.StudioKit{}, err
	}
	kitParses.put(sha, sk)
	return cloneKit(sk), nil
}

// forgetKitParses drops trees from the parse cache.
func forgetKitParses(trees []string) {
	for _, sha := range trees {
		kitParses.drop(sha)
	}
}

// parseLRU is a mutex-guarded least-recently-used map of tree hash to
// parsed kit, at most cap entries.
type parseLRU struct {
	mu    sync.Mutex
	cap   int
	order *list.List               // front = most recently used; values are tree hashes
	items map[string]*list.Element // tree hash → its element
	kits  map[string]studio.StudioKit
}

func newParseLRU(n int) *parseLRU {
	return &parseLRU{cap: n, order: list.New(), items: map[string]*list.Element{}, kits: map[string]studio.StudioKit{}}
}

func (c *parseLRU) get(sha string) (studio.StudioKit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[sha]
	if !ok {
		return studio.StudioKit{}, false
	}
	c.order.MoveToFront(e)
	return c.kits[sha], true
}

func (c *parseLRU) put(sha string, sk studio.StudioKit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[sha]; ok {
		c.order.MoveToFront(e)
		c.kits[sha] = sk
		return
	}
	c.items[sha] = c.order.PushFront(sha)
	c.kits[sha] = sk
	for c.order.Len() > c.cap {
		old := c.order.Back()
		c.order.Remove(old)
		delete(c.items, old.Value.(string))
		delete(c.kits, old.Value.(string))
	}
}

func (c *parseLRU) drop(sha string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[sha]; ok {
		c.order.Remove(e)
		delete(c.items, sha)
		delete(c.kits, sha)
	}
}

func (c *parseLRU) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
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
	return sk
}
