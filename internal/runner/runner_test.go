package runner

import (
	"errors"
	"strings"
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

func TestOSOutputCaptures(t *testing.T) {
	out, err := OS{}.Output("sh", "-c", "printf hello")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" {
		t.Fatalf("Output = %q, want %q", out, "hello")
	}
}

func TestFakeOutputReturnsQueuedResults(t *testing.T) {
	f := &Fake{Outputs: []FakeResult{{Stdout: "one"}, {Stdout: "two"}}}
	a, _ := f.Output("docker", "port", "x")
	b, _ := f.Output("docker", "inspect", "x")
	if a != "one" || b != "two" {
		t.Fatalf("got %q,%q want one,two", a, b)
	}
	if len(f.Calls) != 2 || f.Calls[0].Name != "docker" {
		t.Fatalf("calls not recorded: %+v", f.Calls)
	}
}

func TestFakeRunEnvRecordsEnv(t *testing.T) {
	f := &Fake{}
	if err := f.RunEnv([]string{"K=V"}, "ssh", "host"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || len(f.Calls[0].Env) != 1 || f.Calls[0].Env[0] != "K=V" {
		t.Fatalf("env not recorded: %+v", f.Calls)
	}
}

func TestFakeRunStdinRecordsCall(t *testing.T) {
	f := &Fake{}
	if err := f.RunStdin(strings.NewReader("export X=1\n"), "ssh", "host", "cat > /x"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "ssh" || f.Calls[0].Args[0] != "host" {
		t.Fatalf("call not recorded: %+v", f.Calls)
	}
}

func TestOSRunStdinFeedsStdin(t *testing.T) {
	// `cat` echoes stdin to stdout; with stdout not captured this just must not error.
	if err := (OS{}).RunStdin(strings.NewReader("hi"), "cat"); err != nil {
		t.Fatal(err)
	}
}
