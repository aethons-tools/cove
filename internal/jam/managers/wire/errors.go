package wire

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aethons-tools/cove/internal/jam"
)

// Error is an error that crossed the wire: a message and, for the sentinels
// Jam acts on, a code.
type Error struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

var sentinels = []struct {
	code string
	err  error
}{
	{"kit-not-ready", jam.ErrKitNotReady},
	{"image-not-present", jam.ErrImageNotPresent},
	{"image-unreadable", jam.ErrImageUnreadable},
}

// Is matches the sentinel the Error's code stands for.
func (e *Error) Is(target error) bool {
	for _, s := range sentinels {
		if s.code == e.Code {
			return target == s.err
		}
	}
	return false
}

// EncodeError turns err into its wire form; nil for nil.
func EncodeError(err error) *Error {
	if err == nil {
		return nil
	}
	e := &Error{Message: err.Error()}
	for _, s := range sentinels {
		if errors.Is(err, s.err) {
			e.Code = s.code
			break
		}
	}
	return e
}

// Result is the payload of an OpResult frame.
type Result struct {
	Error *Error          `json:"error,omitempty"`
	Body  json.RawMessage `json:"body,omitempty"`
}

// EncodeResult encodes a reply: the error if err is non-nil, else body.
func EncodeResult(body any, err error) ([]byte, error) {
	if err != nil {
		return json.Marshal(Result{Error: EncodeError(err)})
	}
	var raw json.RawMessage
	if body != nil {
		b, merr := json.Marshal(body)
		if merr != nil {
			return nil, fmt.Errorf("wire: encode result: %w", merr)
		}
		raw = b
	}
	return json.Marshal(Result{Body: raw})
}

// DecodeResult decodes a reply into body (which may be nil). A remote error is
// returned as a *Error.
func DecodeResult(data []byte, body any) error {
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("wire: decode result: %w", err)
	}
	if r.Error != nil {
		return r.Error
	}
	if body == nil || len(r.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Body, body); err != nil {
		return fmt.Errorf("wire: decode result body: %w", err)
	}
	return nil
}
