package wire

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestResultErrorCodes(t *testing.T) {
	for _, sent := range []error{jam.ErrKitNotReady, jam.ErrImageNotPresent, jam.ErrImageUnreadable} {
		err := fmt.Errorf("x: %w", sent)
		data, e := EncodeResult(nil, err)
		if e != nil {
			t.Fatal(e)
		}
		var res RaiseResult
		got := DecodeResult(data, &res)
		if !errors.Is(got, sent) {
			t.Fatalf("%v: not Is", sent)
		}
		if got.Error() != err.Error() {
			t.Fatalf("message %q, want %q", got.Error(), err.Error())
		}
		var re *Error
		if !errors.As(got, &re) {
			t.Fatal("not *Error")
		}
	}
	data, _ := EncodeResult(nil, errors.New("boom"))
	got := DecodeResult(data, &RaiseResult{})
	if got == nil || got.Error() != "boom" {
		t.Fatalf("got %v", got)
	}
	for _, s := range []error{jam.ErrKitNotReady, jam.ErrImageNotPresent, jam.ErrImageUnreadable} {
		if errors.Is(got, s) {
			t.Fatalf("plain error matches %v", s)
		}
	}
	data, err := EncodeResult(RaiseResult{Location: "loc"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ok RaiseResult
	if err := DecodeResult(data, &ok); err != nil || ok.Location != "loc" {
		t.Fatalf("%v %+v", err, ok)
	}
	if EncodeError(nil) != nil {
		t.Fatal("nil error must encode to nil")
	}
}
