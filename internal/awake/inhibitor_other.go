//go:build !darwin

package awake

// New returns a no-op Inhibitor on platforms without a sleep-prevention
// implementation. connect runs unchanged; the host's own power settings apply.
func New() Inhibitor { return noop{} }

type noop struct{}

func (noop) Inhibit() (func(), error) { return func() {}, nil }
