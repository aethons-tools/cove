package jam

import (
	"context"
	"errors"
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
	ErrUnknownManager     = errors.New("unknown manager")
	ErrPinExcluded        = errors.New("standing session is pinned to a manager the request excludes")
)
