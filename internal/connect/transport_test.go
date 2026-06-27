package connect

import (
	"sort"
	"testing"

	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/sshargs"
)

func TestSendEnvLaunch(t *testing.T) {
	f := &runner.Fake{}
	tr := SendEnv{R: f}
	tgt := sshargs.Target{Host: "h", User: "agent", Port: 22, IdentityFile: "/id", KnownHostsFile: "/kh"}
	err := tr.Launch(tgt, map[string]string{"GITHUB_TOKEN": "tok", "X": "y"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "ssh" {
		t.Fatalf("expected one ssh call, got %+v", f.Calls)
	}
	// Values travel in the child env, never on argv.
	env := append([]string(nil), f.Calls[0].Env...)
	sort.Strings(env)
	if len(env) != 2 || env[0] != "GITHUB_TOKEN=tok" || env[1] != "X=y" {
		t.Fatalf("env = %v", env)
	}
	for _, a := range f.Calls[0].Args {
		if a == "tok" || a == "y" {
			t.Fatalf("secret value leaked onto argv: %v", f.Calls[0].Args)
		}
	}
	// SendEnv flags present for both names; remote command launches claude.
	joined := f.Calls[0].Args
	if !hasPair(joined, "SendEnv=GITHUB_TOKEN") || !hasPair(joined, "SendEnv=X") {
		t.Fatalf("SendEnv flags missing: %v", joined)
	}
	if joined[len(joined)-1] != "exec claude" {
		t.Fatalf("remote cmd = %q", joined[len(joined)-1])
	}
}

func TestStdinScriptNoValueOnArgv(t *testing.T) {
	f := &runner.Fake{}
	tr := StdinScript{R: f}
	tgt := sshargs.Target{Host: "h", User: "agent", Port: 22, IdentityFile: "/id", KnownHostsFile: "/kh"}
	if err := tr.Launch(tgt, map[string]string{"GITHUB_TOKEN": "tok"}); err != nil {
		t.Fatal(err)
	}
	// Two ssh calls: write-to-tmpfs, then interactive source+launch.
	if len(f.Calls) != 2 {
		t.Fatalf("expected 2 ssh calls, got %d", len(f.Calls))
	}
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if a == "tok" {
				t.Fatalf("secret value leaked onto argv: %v", c.Args)
			}
		}
	}
}

func hasPair(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}
