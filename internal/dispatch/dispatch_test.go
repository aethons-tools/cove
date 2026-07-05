package dispatch

import (
	"errors"
	"testing"
)

func TestServeReturnsNotImplemented(t *testing.T) {
	err := Serve()
	if err == nil {
		t.Fatal("Serve() = nil; want a not-implemented error")
	}
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Serve() = %v; want errors.Is(err, ErrNotImplemented)", err)
	}
}
