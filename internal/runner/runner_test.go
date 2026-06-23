package runner

import (
	"errors"
	"testing"
)

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFakeRecordsCalls(t *testing.T) {
	f := &Fake{}
	err := f.Run("sbx", "run", "box")
	if err != nil {
		t.Fatal(err)
	}
	err = f.Run("sbx", "remove", "box")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(f.Calls))
	}
	if f.Calls[0].Name != "sbx" {
		t.Fatalf("name = %q", f.Calls[0].Name)
	}
	if !equal(f.Calls[0].Args, []string{"run", "box"}) {
		t.Fatalf("args = %v", f.Calls[0].Args)
	}
}

func TestFakeReturnsConfiguredError(t *testing.T) {
	sentinel := errors.New("boom")
	f := &Fake{Err: sentinel}
	err := f.Run("sbx", "run")
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want %v", err, sentinel)
	}
}

func TestOSPropagatesExitCode(t *testing.T) {
	var xe *ExitError
	err := OS{}.Run("sh", "-c", "exit 7")
	if !errors.As(err, &xe) {
		t.Fatalf("got %T (%v), want *ExitError", err, err)
	}
	if xe.ExitCode() != 7 {
		t.Fatalf("ExitCode() = %d, want 7", xe.ExitCode())
	}
}

func TestOSSuccess(t *testing.T) {
	err := OS{}.Run("true")
	if err != nil {
		t.Fatalf("got %v, want nil", err)
	}
}
