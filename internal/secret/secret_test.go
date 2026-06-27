package secret

import (
	"testing"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

func TestResolveTrimsAndMaps(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "key"}}}
	env, err := Resolve(f, []Spec{
		{Name: "GITHUB_TOKEN", Command: []string{"op", "read", "x"}},
		{Name: "ANTHROPIC_API_KEY", Command: []string{"pass", "y"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["GITHUB_TOKEN"] != "tok" || env["ANTHROPIC_API_KEY"] != "key" {
		t.Fatalf("env = %v", env)
	}
	if f.Calls[0].Name != "op" || f.Calls[0].Args[0] != "read" {
		t.Fatalf("call0 = %+v", f.Calls[0])
	}
}

func TestResolveFailsClosed(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Err: &runner.ExitError{Code: 1}}}}
	_, err := Resolve(f, []Spec{{Name: "GITHUB_TOKEN", Command: []string{"op", "read", "x"}}})
	if err == nil {
		t.Fatal("expected error when a resolver command fails")
	}
}
