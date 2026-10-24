package jam

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

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

// chainUnresolved is chainCacheKey's "cannot fingerprint" marker.
const chainUnresolved = "!"

// chainCacheKey is a cheap fingerprint of the parents a kit config resolves
// to right now: each parent's id@version (a following parent at its current
// version) and a hash of its stored config, root last. "" for an unchained
// kit. It parses configs only to read their base reference — no merge, no
// build digest — so the current-image cache can key on it per poll. A chain
// that does not resolve returns chainUnresolved: the caller must bypass the
// cache entirely (no get, no put) and let the full resolution report the error.
func chainCacheKey(store Store, config string) string {
	sk, err := studio.ParseStudioKit([]byte(config))
	if err != nil {
		return chainUnresolved
	}
	var b strings.Builder
	for depth := 1; sk.Base.HasParent(); depth++ {
		name, pin, err := sk.Base.ParentRef()
		if err != nil || depth >= MaxKitChain {
			return chainUnresolved
		}
		version := pin
		if version == 0 {
			k, ok := store.GetKit(name)
			if !ok {
				return chainUnresolved
			}
			version = k.Current
		}
		text, ok := store.KitConfig(name, version)
		if !ok {
			return chainUnresolved
		}
		sum := sha256.Sum256([]byte(text))
		fmt.Fprintf(&b, "%s@v%d:%x;", name, version, sum[:8])
		if sk, err = studio.ParseStudioKit([]byte(text)); err != nil {
			return chainUnresolved
		}
	}
	return b.String()
}

// RemoveKitChecked removes a kit unless a role binds it or another kit's
// current version names it as its parent (409 with the offenders listed). The
// check and the removal run under kitMu, so a concurrent child push cannot
// slip in between. It then collects the kit objects nothing references; a
// collection failure is logged to log (nil = not logged) and never fails the
// removal: the next serve start collects again.
func RemoveKitChecked(store Store, log *slog.Logger, name string) error {
	kitMu.Lock()
	defer kitMu.Unlock()
	if project, role, ok := store.RoleReferencingKit(name); ok {
		return writeErr(http.StatusConflict, "kit %q is referenced by role %s/%s", name, project, role)
	}
	follow, pinned := KitsReferencing(store, name)
	if children := append(follow, pinned...); len(children) > 0 {
		slices.Sort(children)
		return writeErr(http.StatusConflict, "kit %s is the parent of %s", name, strings.Join(children, ", "))
	}
	if err := store.RemoveKit(name); err != nil {
		return err
	}
	gcKitObjects(store, log)
	return nil
}

// PinKitChecked rolls a kit's current pointer to version, unless a kit
// following it (unpinned) would silently move with it (409). Children pinned to
// a version don't block. The check and the pin run under kitMu.
func PinKitChecked(store Store, name string, version int) error {
	kitMu.Lock()
	defer kitMu.Unlock()
	if k, ok := store.GetKit(name); !ok || !k.HasVersion(version) {
		return store.PinKit(name, version) // the store's own not-found/bad-version error
	}
	if follow, _ := KitsReferencing(store, name); len(follow) > 0 {
		return writeErr(http.StatusConflict, "kit %s is followed by %s (pin those to a %s version, or point them elsewhere)",
			name, strings.Join(follow, ", "), name)
	}
	return store.PinKit(name, version)
}

// KitChainLink is one parent in a GET /admin/kits/{name} chain.
type KitChainLink struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Follow  bool   `json:"follow"`
}

// kitParentSpec renders a kit config's base reference as "name" or "name@vN";
// "" when it has no parent or the config does not parse.
func kitParentSpec(config string) string {
	sk, err := studio.ParseStudioKit([]byte(config))
	if err != nil || !sk.Base.HasParent() {
		return ""
	}
	return sk.Base.Kit
}

// KitChainView is the chain (root first, excluding the kit itself) and the
// effective kit, as YAML, of the stored config of kit name. An unchained kit's
// effective view is its own kit, unmerged, so it renders exactly as stored. A
// config or chain that does not resolve is returned as an error.
func KitChainView(store Store, name, config string) (links []KitChainLink, effective string, err error) {
	sk, err := studio.ParseStudioKit([]byte(config))
	if err != nil {
		return nil, "", err
	}
	chain, err := ResolveKitChain(store, name, sk)
	if err != nil {
		return nil, "", err
	}
	for _, l := range chain[:len(chain)-1] {
		links = append(links, KitChainLink{Name: l.Ref.ID, Version: l.Ref.Version, Follow: l.Follow})
	}
	shown := sk
	if len(chain) > 1 {
		shown = studio.Merge(chainKits(chain))
	}
	shown.LegacyName = "" // the registry names the kit
	y, err := yaml.Marshal(shown)
	if err != nil {
		return nil, "", err
	}
	return links, string(y), nil
}
