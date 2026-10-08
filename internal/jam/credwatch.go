package jam

import "github.com/aethons-tools/cove/internal/jam/condition"

// CredFailThreshold is how many consecutive resolve failures of one
// credential raise cred.unavailable:<name>.
const CredFailThreshold = 3

// WatchedResolver wraps the broker's credential resolver and turns repeated
// resolve failures into a cred.unavailable:<name> condition (critical),
// cleared by the next success. The condition names the credential only — the
// resolver's error text may carry secret material and is never copied.
type WatchedResolver struct {
	base CredResolver
	t    *condition.Tracker
	fix  func(name string) string
}

// NewWatchedResolver wraps base. fix returns the remedy for a credential; nil means none.
func NewWatchedResolver(base CredResolver, t *condition.Tracker, fix func(name string) string) *WatchedResolver {
	if fix == nil {
		fix = func(string) string { return "" }
	}
	return &WatchedResolver{base: base, t: t, fix: fix}
}

func (w *WatchedResolver) Resolve(name string) (string, error) {
	v, err := w.base.Resolve(name)
	w.note(name, err)
	return v, err
}

// ResolveFor delegates to the base's identity-aware resolve when it has one.
func (w *WatchedResolver) ResolveFor(name, identityHash string) (string, error) {
	var v string
	var err error
	if ir, ok := w.base.(IdentityCredResolver); ok {
		v, err = ir.ResolveFor(name, identityHash)
	} else {
		v, err = w.base.Resolve(name)
	}
	w.note(name, err)
	return v, err
}

// PoolCredential delegates to the base when it is a pool resolver.
func (w *WatchedResolver) PoolCredential(name string) bool {
	if pr, ok := w.base.(PoolCredResolver); ok {
		return pr.PoolCredential(name)
	}
	return false
}

func (w *WatchedResolver) note(name string, err error) {
	key := condition.Key("cred.unavailable", name)
	if err == nil {
		w.t.Ok(key)
		return
	}
	w.t.Fail(condition.Condition{
		Key:      key,
		Severity: condition.Critical,
		Summary:  "credential " + name + " cannot be resolved; brokered requests using it fail (502)",
		Fix:      w.fix(name),
	}, CredFailThreshold)
}
