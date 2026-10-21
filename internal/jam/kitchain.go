package jam

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitLink is one resolved kit in a chain: root first, the kit itself last.
type KitLink struct {
	Ref    KitRef // ID, Version (0 for a kit not yet pushed), Digest = the link's own BuildDigest
	Kit    studio.StudioKit
	Follow bool // reached by a following (unpinned) reference
}

// MaxKitChain is the longest chain allowed, counting the kit itself.
const MaxKitChain = 4

// ErrKitChain is wrapped by every chain-resolution failure.
var ErrKitChain = errors.New("kit chain")

// chainError carries a fixed, user-facing message and matches ErrKitChain.
type chainError struct{ msg string }

func (e chainError) Error() string        { return e.msg }
func (e chainError) Is(target error) bool { return target == ErrKitChain }

func chainErr(format string, a ...any) error { return chainError{fmt.Sprintf(format, a...)} }

// ResolveKitChain resolves sk's parents from the registry (current, or @vN) up
// to its root. self names sk (for cycle detection; "" when unnamed). The result
// is root first, sk last; sk's own link has Version 0. A missing or invalid
// parent, a cycle (including a kit naming itself) and a chain longer than
// MaxKitChain fail with an error matching ErrKitChain.
func ResolveKitChain(store Store, self string, sk studio.StudioKit) ([]KitLink, error) {
	h := harnessinstall.Default()
	label := self
	if label == "" {
		label = "this kit"
	}
	rev := []KitLink{{Ref: KitRef{ID: self, Digest: studio.BuildDigest(sk, h)}, Kit: sk}}
	path := []string{label} // child → parent names, for the cycle and depth messages
	cur, curName := sk, label
	for cur.Base.HasParent() {
		name, pin, err := cur.Base.ParentRef()
		if err != nil {
			return nil, chainErr("kit %s: %v", curName, err)
		}
		if slices.Contains(path, name) {
			return nil, chainErr("cycle: %s", strings.Join(append(slices.Clone(path), name), " → "))
		}
		if len(rev) >= MaxKitChain {
			return nil, chainErr("kit chain too deep (max %d): %s", MaxKitChain, strings.Join(append(slices.Clone(path), name), " → "))
		}
		k, ok := store.GetKit(name)
		version := pin
		if version == 0 {
			version = k.Current
		}
		if !ok || version == 0 {
			return nil, chainErr("kit %s (parent of %s) is missing", name, curName)
		}
		text, ok := store.KitConfig(name, version)
		if !ok {
			return nil, chainErr("kit %s has no version v%d (parent of %s)", name, version, curName)
		}
		parent, err := studio.ParseStudioKit([]byte(text))
		if err != nil {
			return nil, chainErr("kit %s@v%d (parent of %s) is invalid: %v", name, version, curName, err)
		}
		rev = append(rev, KitLink{Ref: KitRef{ID: name, Version: version, Digest: studio.BuildDigest(parent, h)}, Kit: parent, Follow: pin == 0})
		path = append(path, name)
		cur, curName = parent, name
	}
	slices.Reverse(rev)
	return rev, nil
}

// chainKits is the studio kits of a chain, in order.
func chainKits(chain []KitLink) []studio.StudioKit {
	out := make([]studio.StudioKit, len(chain))
	for i, l := range chain {
		out[i] = l.Kit
	}
	return out
}

// KitsReferencing lists, sorted, the kits whose CURRENT version names parent as
// its base kit: follow lists those that follow its current version, pinned
// those that pin a version. A kit whose current config does not parse is
// skipped.
func KitsReferencing(store Store, parent string) (follow, pinned []string) {
	for _, k := range store.ListKits() {
		if k.Current == 0 {
			continue
		}
		text, ok := store.KitConfig(k.Name, k.Current)
		if !ok {
			continue
		}
		sk, err := studio.ParseStudioKit([]byte(text))
		if err != nil || !sk.Base.HasParent() {
			continue
		}
		name, pin, err := sk.Base.ParentRef()
		if err != nil || name != parent {
			continue
		}
		if pin == 0 {
			follow = append(follow, k.Name)
		} else {
			pinned = append(pinned, k.Name)
		}
	}
	slices.Sort(follow)
	slices.Sort(pinned)
	return follow, pinned
}

// recordedChainKit is the merged kit of a recorded image chain, each link read
// at its exact (immutable) version. A link no longer in the registry fails.
func recordedChainKit(store Store, chain []KitRef) (studio.StudioKit, error) {
	kits := make([]studio.StudioKit, 0, len(chain))
	for _, r := range chain {
		text, ok := store.KitConfig(r.ID, r.Version)
		if !ok {
			return studio.StudioKit{}, fmt.Errorf("kit %s (in the image's chain) is missing", r)
		}
		sk, err := studio.ParseStudioKit([]byte(text))
		if err != nil {
			return studio.StudioKit{}, fmt.Errorf("kit %s (in the image's chain) is invalid: %w", r, err)
		}
		kits = append(kits, sk)
	}
	return studio.Merge(kits), nil
}

// chainRefs is what an Instance records of the chain its image was built
// from: each link's id and version, root first. nil for an unchained kit.
func chainRefs(chain []KitLink) []KitRef {
	if len(chain) < 2 {
		return nil
	}
	out := make([]KitRef, len(chain))
	for i, l := range chain {
		out[i] = KitRef{ID: l.Ref.ID, Version: l.Ref.Version}
	}
	return out
}

// chainCacheKey is a cheap fingerprint of the parents a kit config resolves
// to right now: each parent's id@version (a following parent at its current
// version) and a hash of its stored config, root last. "" for an unchained
// kit. It parses configs only to read their base reference — no merge, no
// build digest — so the current-image cache can key on it per poll. A chain
// that does not resolve returns a marker no healthy chain produces, so the
// caller misses the cache and the full resolution reports the real error.
func chainCacheKey(store Store, config string) string {
	sk, err := studio.ParseStudioKit([]byte(config))
	if err != nil {
		return "!"
	}
	var b strings.Builder
	for depth := 1; sk.Base.HasParent(); depth++ {
		name, pin, err := sk.Base.ParentRef()
		if err != nil || depth >= MaxKitChain {
			return "!"
		}
		version := pin
		if version == 0 {
			k, ok := store.GetKit(name)
			if !ok {
				return "!"
			}
			version = k.Current
		}
		text, ok := store.KitConfig(name, version)
		if !ok {
			return "!"
		}
		sum := sha256.Sum256([]byte(text))
		fmt.Fprintf(&b, "%s@v%d:%x;", name, version, sum[:8])
		if sk, err = studio.ParseStudioKit([]byte(text)); err != nil {
			return "!"
		}
	}
	return b.String()
}
