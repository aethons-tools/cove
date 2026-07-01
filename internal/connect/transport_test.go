package connect

import (
	"sort"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

func rawTarget() sshargs.Target {
	return sshargs.Target{Host: "h", User: "agent", Port: 22, IdentityFile: "/id", KnownHostsFile: "/kh"}
}

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
	// SendEnv flags present for both names; remote command cd's into the
	// workspace then launches claude.
	joined := f.Calls[0].Args
	if !hasPair(joined, "SendEnv=GITHUB_TOKEN") || !hasPair(joined, "SendEnv=X") {
		t.Fatalf("SendEnv flags missing: %v", joined)
	}
	if joined[len(joined)-1] != "cd /home/agent/workspace && exec claude" {
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

func TestSendEnvLaunchRawBash(t *testing.T) {
	f := &runner.Fake{}
	if err := (SendEnv{R: f, Cmd: "bash"}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	args := f.Calls[0].Args
	if args[len(args)-1] != "cd /home/agent/workspace && exec bash" {
		t.Fatalf("raw SendEnv should cd to workspace and launch bash, got %q", args[len(args)-1])
	}
}

func TestStdinScriptRawBash(t *testing.T) {
	f := &runner.Fake{}
	if err := (StdinScript{R: f, Cmd: "bash"}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	// Second call is the interactive launch; its remote script cd's into the
	// workspace then exec's bash.
	remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if !strings.HasSuffix(remote, "cd /home/agent/workspace && exec bash") {
		t.Fatalf("raw StdinScript should cd to workspace and exec bash, got %q", remote)
	}
}

func TestStdinScriptStartsInWorkspace(t *testing.T) {
	f := &runner.Fake{}
	if err := (StdinScript{R: f}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	// The interactive launch (second call) must cd into the workspace before exec.
	remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if !strings.HasSuffix(remote, "cd /home/agent/workspace && exec claude") {
		t.Fatalf("session should start in workspace, got %q", remote)
	}
}

func TestStdinScriptNamesSession(t *testing.T) {
	f := &runner.Fake{}
	if err := (StdinScript{R: f, Name: "mykit"}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if !strings.HasSuffix(remote, `cd /home/agent/workspace && exec claude -n 'mykit cove'`) {
		t.Fatalf("claude launch should be named '<kit> cove': %q", remote)
	}
}

func TestStdinScriptNamesResumedSession(t *testing.T) {
	f := &runner.Fake{}
	if err := (StdinScript{R: f, Name: "mykit", Resume: true}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if !strings.Contains(remote, `exec claude --continue -n 'mykit cove'`) {
		t.Fatalf("resumed claude should carry the session name: %q", remote)
	}
	if !strings.Contains(remote, `done; exec claude -n 'mykit cove'; }`) {
		t.Fatalf("fresh fallback should carry the session name: %q", remote)
	}
}

func TestRawBashIgnoresName(t *testing.T) {
	f := &runner.Fake{}
	if err := (StdinScript{R: f, Cmd: "bash", Name: "mykit"}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if strings.Contains(remote, "mykit") {
		t.Fatalf("raw bash is a program replacement and must not get a claude session name: %q", remote)
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

func TestStdinScriptResumesWhenEnabled(t *testing.T) {
	f := &runner.Fake{}
	if err := (StdinScript{R: f, Resume: true}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if !strings.Contains(remote, "exec claude --continue") {
		t.Fatalf("resume should pass --continue: %q", remote)
	}
	if !strings.Contains(remote, "done; exec claude; }") {
		t.Fatalf("resume must fall back to a fresh claude when no session exists: %q", remote)
	}
	if !strings.Contains(remote, "/projects/*/*.jsonl") {
		t.Fatalf("resume must guard on an existing session transcript: %q", remote)
	}
	// nullglob-immune: a literal, unexpanded pattern is rejected by the -e test
	// rather than treated as a match.
	if !strings.Contains(remote, `[ -e "$f" ]`) {
		t.Fatalf("resume detection must be nullglob-immune: %q", remote)
	}
}

func TestStdinScriptFreshDoesNotResume(t *testing.T) {
	f := &runner.Fake{}
	if err := (StdinScript{R: f}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if strings.Contains(remote, "--continue") {
		t.Fatalf("fresh launch must not pass --continue: %q", remote)
	}
	if !strings.HasSuffix(remote, "cd /home/agent/workspace && exec claude") {
		t.Fatalf("fresh launch should exec plain claude: %q", remote)
	}
}

func TestSendEnvResumesWhenEnabled(t *testing.T) {
	f := &runner.Fake{}
	if err := (SendEnv{R: f, Resume: true}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	remote := f.Calls[0].Args[len(f.Calls[0].Args)-1]
	if !strings.Contains(remote, "exec claude --continue") || !strings.Contains(remote, "done; exec claude; }") {
		t.Fatalf("SendEnv resume should pass --continue with fallback: %q", remote)
	}
}

func TestRawNeverResumes(t *testing.T) {
	f := &runner.Fake{}
	if err := (StdinScript{R: f, Cmd: "bash", Resume: true}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if strings.Contains(remote, "--continue") {
		t.Fatalf("raw bash must never resume: %q", remote)
	}
	if !strings.HasSuffix(remote, "cd /home/agent/workspace && exec bash") {
		t.Fatalf("raw should exec bash: %q", remote)
	}
}
