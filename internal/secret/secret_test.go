package secret

import (
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestResolveTrimsAndMaps(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "key"}}}
	env, err := Resolve(f, nil, []Spec{
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
	_, err := Resolve(f, nil, []Spec{{Name: "GITHUB_TOKEN", Command: []string{"op", "read", "x"}}})
	if err == nil {
		t.Fatal("expected error when a resolver command fails")
	}
}

func TestResolveLiteralValueRunsNoCommand(t *testing.T) {
	f := &runner.Fake{}
	env, err := Resolve(f, nil, []Spec{{Name: "GITHUB_TOKEN", Value: "ghp_x", Literal: true}})
	if err != nil {
		t.Fatal(err)
	}
	if env["GITHUB_TOKEN"] != "ghp_x" {
		t.Fatalf("env = %v", env)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("a literal secret must not run a command; calls=%+v", f.Calls)
	}
}

func TestResolvePassesExtraEnv(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok"}}}
	_, err := Resolve(f, map[string]string{"COVE_RUN_REPO": "acme/x"}, []Spec{{Name: "T", Command: []string{"mint"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || !contains(f.Calls[0].Env, "COVE_RUN_REPO=acme/x") {
		t.Fatalf("resolver env = %v; want COVE_RUN_REPO", f.Calls[0].Env)
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
