package jam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Placement is where a raise may land: Managers in preference order ("" / nil = any, registration order),
// and how long to look for one that accepts (0 = do not wait).
type Placement struct {
	Managers []string
	Timeout  time.Duration
}

// Placed is the result of a successful raise on a manager.
type Placed struct {
	Manager  string
	Location string
}

// PlacingLauncher is optionally implemented by a Launcher that spreads raises across named managers (fleet.Router).
type PlacingLauncher interface {
	Launcher
	RaiseOn(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (Placed, error)
	PrepareKitOn(ctx context.Context, manager string, def KitDefinition) (KitStatus, error)
	ImageTagOn(manager string, ref KitRef) string
	HasManager(name string) bool
}

// KitNotReadyError: manager needs def prepared before it can raise. errors.Is(err, ErrKitNotReady) holds.
type KitNotReadyError struct {
	Manager string
}

// Error implements the error interface.
func (e *KitNotReadyError) Error() string {
	return "manager " + e.Manager + ": " + ErrKitNotReady.Error()
}

// Unwrap implements the error unwrapping interface to support errors.Is.
func (e *KitNotReadyError) Unwrap() error {
	return ErrKitNotReady
}

// NoManagerError: no manager accepted before the timeout.
type NoManagerError struct {
	Reasons []string
}

// Error implements the error interface.
func (e *NoManagerError) Error() string {
	return "no manager could host it: " + strings.Join(e.Reasons, "; ")
}

// Error variables for placement-related errors.
var (
	ErrManagerUnavailable = errors.New("manager unavailable")
	// ErrRaiseInterrupted: the manager connection dropped mid-raise, so whether
	// the studio started is unknown; the raise is not retried elsewhere.
	ErrRaiseInterrupted = errors.New("manager connection lost mid-raise; outcome unknown")
	ErrUnknownManager   = errors.New("unknown manager")
	ErrPinExcluded      = errors.New("standing session is pinned to a manager the request excludes")
)

// ParsePlacement builds a Placement from a request's managers and its
// placement_timeout, a Go duration string ("" = 0, do not wait). A timeout that
// does not parse or is negative is an error.
func ParsePlacement(managers []string, timeout string) (Placement, error) {
	p := Placement{Managers: managers}
	if timeout == "" {
		return p, nil
	}
	d, err := time.ParseDuration(timeout)
	if err != nil {
		return Placement{}, fmt.Errorf("placement_timeout: %w", err)
	}
	if d < 0 {
		return Placement{}, fmt.Errorf("placement_timeout: %q is negative", timeout)
	}
	p.Timeout = d
	return p, nil
}

// ManagerErrStatus is the shared admin mapper for manager errors: the HTTP status of a named-manager failure: 404 for
// ErrUnknownManager, 503 for ErrManagerUnavailable; ok=false for any other.
func ManagerErrStatus(err error) (code int, ok bool) {
	switch {
	case errors.Is(err, ErrUnknownManager):
		return http.StatusNotFound, true
	case errors.Is(err, ErrManagerUnavailable):
		return http.StatusServiceUnavailable, true
	}
	return 0, false
}
