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
