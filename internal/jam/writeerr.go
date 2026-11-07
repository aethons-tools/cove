package jam

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// WriteError is a refused control-plane write (role, destination), carrying
// the HTTP status it maps to. Store failures are plain errors. Fields, when
// set, is every problem with the written value, each at its field path.
type WriteError struct {
	Status int
	Msg    string
	Fields sessionctx.FieldErrors
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

// contextFieldsErr is a 400 refusing scope's ("project", "role", "jam")
// authored context for every problem in fe; nil when fe is empty.
func contextFieldsErr(scope string, fe sessionctx.FieldErrors) error {
	if len(fe) == 0 {
		return nil
	}
	return &WriteError{Status: http.StatusBadRequest, Msg: contextMessage(scope, fe), Fields: fe}
}

// contextMessage is fe as one line, each problem prefixed "<scope> context: "
// (or "<scope> resources: " for a resources field).
func contextMessage(scope string, fe sessionctx.FieldErrors) string {
	msgs := make([]string, len(fe))
	for i, e := range fe {
		what := " context: "
		if strings.HasPrefix(e.Field, "resources") {
			what = " resources: "
		}
		msgs[i] = scope + what + e.Message
	}
	return strings.Join(msgs, "; ")
}
