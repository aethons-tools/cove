package dispatch

import "errors"

// ErrNotImplemented is returned by skeleton entry points that have no logic yet.
var ErrNotImplemented = errors.New("at-dispatch: not implemented yet — see docs/orchestration/")

// Serve will run the dispatcher (scheduler + webhook receiver). It is a stub
// until the orchestration design is implemented.
func Serve() error {
	return ErrNotImplemented
}
