package jam

import (
	"errors"
	"fmt"
)

// WriteError is a refused control-plane write (role, destination), carrying
// the HTTP status it maps to. Store failures are plain errors.
type WriteError struct {
	Status int
	Msg    string
}

func (e *WriteError) Error() string { return e.Msg }

func writeErr(status int, format string, a ...any) error {
	return &WriteError{Status: status, Msg: fmt.Sprintf(format, a...)}
}

// WriteStatus is err's HTTP status: a *WriteError's own, else fallback (the
// caller's status for a store failure).
func WriteStatus(err error, fallback int) int {
	if we, ok := errors.AsType[*WriteError](err); ok {
		return we.Status
	}
	return projectErrStatus(err, fallback)
}
