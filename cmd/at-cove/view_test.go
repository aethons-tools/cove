package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/state"
)

// viewTestEnv is the minimal fixture `view` needs: a kit declaring a
// collaborator class (so the "agent" positional resolves), a current install
// manifest (loadInstanceState requires currency), and a saved instance state
// for that class (so state.LoadFor finds it). Built from the same helpers
// (writeStateFor/writeInstall/seedConfigDir) the chat/status tests already use
// — see TestChatCollaboratorSessionName for the pattern this mirrors.
type viewTestEnv struct {
	projectDir string
	fakeRunner *runner.Fake
	lookup     func(string) (string, bool)
	lookPath   func(string) (string, error)
	container  string
}

func newViewTestEnv(t *testing.T) viewTestEnv {
	t.Helper()
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\ncollaborators:\n  agent:\n    prompt: \"you are the agent\"\n"
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	seedConfigDir(t) // keys.Ensure hits pre-seeded key files, no ssh-keygen shell-out
	container := "box-agent"
	writeStateFor(t, kitDir, state.Instance("agent"), "box", container)
	writeInstall(t, kitDir)
	return viewTestEnv{
		projectDir: dir,
		fakeRunner: &runner.Fake{},
		lookup:     os.LookupEnv,
		lookPath:   dummyLookPath,
		container:  container,
	}
}

// chdir switches the process cwd to dir for the duration of the test,
// restoring the original on cleanup. (Not testing.T.Chdir: this module
// targets go1.22, and T.Chdir requires go1.24 — `go vet` under the module's
// go directive rejects it.)
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatal(err)
		}
	})
}

// TestView_Write exercises the --write path, which has zero prior coverage.
// It reproduces `at-cove view --write --project-dir . agent`: doView is
// called with a RELATIVE kitDir (cwd chdir'd into the project dir first), the
// same shape a relative --project-dir produces once resolveCollaborator joins
// it onto the kit dir. Before the absolutize+quote fix, the embedded
// ProxyCommand's --project-dir is the bare relative "." (broken: ssh runs
// ProxyCommand from an arbitrary cwd) and unquoted (broken: word-splits on
// shell metacharacters). Both bugs make the assertions below fail pre-fix.
func TestView_Write(t *testing.T) {
	env := newViewTestEnv(t)
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir) // --write's target; must never touch the real $HOME

	chdir(t, env.projectDir)
	relKitDir := ".at-cove"

	var out strings.Builder
	if err := doView("agent", relKitDir, env.fakeRunner, true, &out); err != nil {
		t.Fatalf("doView --write: %v", err)
	}

	alias := "cove-" + env.container
	cfgPath := filepath.Join(homeDir, ".ssh", "config")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read %s: %v", cfgPath, err)
	}
	body := string(data)

	if !strings.Contains(body, "# >>> at-cove managed block: "+alias+" >>>") ||
		!strings.Contains(body, "# <<< at-cove managed block: "+alias+" <<<") {
		t.Fatalf("managed block missing:\n%s", body)
	}
	if !strings.Contains(body, "Host "+alias) {
		t.Fatalf("Host line missing:\n%s", body)
	}
	// ProxyCommand fields must be single-quoted...
	if !strings.Contains(body, "ssh-proxy --project-dir '") {
		t.Fatalf("ProxyCommand --project-dir not single-quoted:\n%s", body)
	}
	// ...and the quoted project dir must be absolute, not the relative "."
	// that a relative --project-dir would otherwise embed.
	if strings.Contains(body, "--project-dir '.'") {
		t.Fatalf("ProxyCommand embeds a relative project dir:\n%s", body)
	}
	if !strings.Contains(body, "--project-dir '"+env.projectDir+"'") {
		t.Fatalf("ProxyCommand missing absolutized, quoted project dir %q:\n%s", env.projectDir, body)
	}

	// The confirmation (not the file) carries the git-remote line.
	o := out.String()
	if !strings.Contains(o, "wrote managed block for "+alias+" to "+cfgPath) {
		t.Fatalf("confirmation missing path:\n%s", o)
	}
	if !strings.Contains(o, "git remote add sandbox "+alias+":/home/agent/workspace") {
		t.Fatalf("confirmation missing git remote line:\n%s", o)
	}

	// Idempotency: writing again yields a byte-identical file.
	var out2 strings.Builder
	if err := doView("agent", relKitDir, env.fakeRunner, true, &out2); err != nil {
		t.Fatalf("doView --write (second): %v", err)
	}
	data2, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read %s (second): %v", cfgPath, err)
	}
	if string(data2) != body {
		t.Fatalf("--write is not idempotent:\n--- first ---\n%s\n--- second ---\n%s", body, string(data2))
	}
}

func TestView_PrintsHostBlockAndGitRemote(t *testing.T) {
	env := newViewTestEnv(t)
	var out, errbuf strings.Builder
	code := run([]string{"view", "--project-dir", env.projectDir, "agent"},
		env.fakeRunner, env.lookup, env.lookPath, &out, &errbuf)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errbuf.String())
	}
	s := out.String()
	for _, want := range []string{
		"Host cove-" + env.container,
		"ProxyCommand",
		"ssh-proxy",
		"StrictHostKeyChecking accept-new",
		"git remote add sandbox cove-" + env.container + ":/home/agent/workspace",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("output missing %q:\n%s", want, s)
		}
	}
}
