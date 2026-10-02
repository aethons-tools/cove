package connect

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

func TestLaunchCoveMasterInjectsAndLaunches(t *testing.T) {
	fake := &runner.Fake{}
	tgt := sshargs.Target{Host: "h", User: "agent", Port: 2222, IdentityFile: "k", KnownHostsFile: "kh"}
	err := LaunchCoveMaster(fake, CoveMasterOptions{
		Target:        tgt,
		JamHost:       "jam.example.com",
		RuntimeAddr:   "jam.example.com:443",
		IdentityToken: "tok-123",
		LaunchSecret:  "sec-456",
		WorkDir:       "/home/agent/workspace",
		Prompt:        "do the task",
	})
	if err != nil {
		t.Fatal(err)
	}

	// runner.Fake records each invocation in f.Calls ([]runner.Call{Name, Args, Stdin}).
	// stdinTo returns the Stdin piped to the ssh call whose argv contains "cat > <path>".
	stdinTo := func(path string) string {
		for _, c := range fake.Calls {
			if c.Name == "ssh" && strings.Contains(strings.Join(c.Args, " "), "cat > "+path) {
				return c.Stdin
			}
		}
		t.Fatalf("no ssh stdin write to %s; calls=%+v", path, fake.Calls)
		return ""
	}

	// The prompt is written to a tmpfs file via ssh stdin (never argv).
	if got := stdinTo(coveMasterPromptVMPath); got != "do the task" {
		t.Fatalf("prompt stdin = %q, want %q", got, "do the task")
	}

	// The env script is written to a tmpfs file and contains the connector + cove-master vars.
	envWrite := stdinTo(coveMasterEnvVMPath)
	for _, want := range []string{
		"AT_JAM_RUNTIME_ADDR=", "jam.example.com:443",
		"AT_JAM_LAUNCH_SECRET=", "sec-456",
		"AT_COVE_WORKDIR=", "/home/agent/workspace",
		"AT_COVE_AGENT_PROMPT_FILE=", coveMasterPromptVMPath,
		"AT_JAM_IDENTITY_TOKEN=tok-123",
		// The deprecated names are still set for older images, each exported
		// from its new variable so no secret is written twice.
		`export AT_HARBOR_IDENTITY_TOKEN="$AT_JAM_IDENTITY_TOKEN"`,
		`export AT_HARBOR_LAUNCH_SECRET="$AT_JAM_LAUNCH_SECRET"`,
		`export AT_HARBOR_RUNTIME_ADDR="$AT_JAM_RUNTIME_ADDR"`,
		"ANTHROPIC_BASE_URL=\"https://jam.example.com/anthropic\"",
		"git config --global",
	} {
		if !strings.Contains(envWrite, want) {
			t.Fatalf("env script missing %q; got:\n%s", want, envWrite)
		}
	}

	// The detached launch command starts cove-master, and no secret rides argv.
	var launched bool
	for _, c := range fake.Calls {
		argv := strings.Join(c.Args, " ")
		if c.Name == "ssh" && strings.Contains(argv, "setsid nohup cove-master") {
			launched = true
		}
		for _, sec := range []string{"tok-123", "sec-456", "do the task"} {
			if strings.Contains(argv, sec) {
				t.Fatalf("secret %q leaked onto argv: %s", sec, argv)
			}
		}
	}
	if !launched {
		t.Fatalf("no detached cove-master launch; calls=%+v", fake.Calls)
	}
	for _, sec := range []string{"tok-123", "sec-456"} {
		if n := strings.Count(envWrite, sec); n != 1 {
			t.Fatalf("secret %q written %d times in the env script, want exactly 1:\n%s", sec, n, envWrite)
		}
	}
}

// TestLaunchCoveMasterSubscription: with Subscription set, a dummy claudeAiOauth
// credentials file (accessToken = the identity token) is staged over ssh stdin,
// and the env script uses the subscription render (no ANTHROPIC_API_KEY).
func TestLaunchCoveMasterSubscription(t *testing.T) {
	fake := &runner.Fake{}
	err := LaunchCoveMaster(fake, CoveMasterOptions{
		Target: sshargs.Target{Host: "h", User: "agent", Port: 2222}, JamHost: "jam.example.com",
		RuntimeAddr: "jam.example.com:443", IdentityToken: "cove-identity", LaunchSecret: "s",
		WorkDir: "/w", Prompt: "p", Subscription: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stdinTo := func(path string) string {
		for _, c := range fake.Calls {
			if c.Name == "ssh" && strings.Contains(strings.Join(c.Args, " "), "cat > "+path) {
				return c.Stdin
			}
		}
		t.Fatalf("no ssh stdin write to %s; calls=%+v", path, fake.Calls)
		return ""
	}
	// Subscription mode writes NO credentials file — the identity rides on
	// ANTHROPIC_AUTH_TOKEN as a static bearer instead.
	for _, c := range fake.Calls {
		if c.Name == "ssh" && strings.Contains(strings.Join(c.Args, " "), "cat > "+credsVMPath) {
			t.Fatalf("subscription launch must NOT write a credentials file (%s)", credsVMPath)
		}
	}
	env := stdinTo(coveMasterEnvVMPath)
	if strings.Contains(env, "ANTHROPIC_API_KEY") {
		t.Fatalf("subscription env must NOT set ANTHROPIC_API_KEY:\n%s", env)
	}
	if !strings.Contains(env, "export ANTHROPIC_AUTH_TOKEN=\"${AT_JAM_IDENTITY_TOKEN}\"") {
		t.Fatalf("subscription env must set ANTHROPIC_AUTH_TOKEN from the identity var:\n%s", env)
	}
	if !strings.Contains(env, "ANTHROPIC_BASE_URL=\"https://jam.example.com/anthropic\"") {
		t.Fatalf("env missing base URL:\n%s", env)
	}
	if !strings.Contains(env, "export AT_JAM_IDENTITY_TOKEN=cove-identity") {
		t.Fatalf("env missing identity export:\n%s", env)
	}
	// No secret rides argv.
	for _, c := range fake.Calls {
		if strings.Contains(strings.Join(c.Args, " "), "cove-identity") {
			t.Fatalf("identity leaked onto argv: %v", c.Args)
		}
	}
}

// TestLaunchCoveMasterNonSubscriptionKeepsAPIKey: the default path still uses the
// API-key render (ANTHROPIC_API_KEY set) and writes no credentials file.
func TestLaunchCoveMasterNonSubscriptionKeepsAPIKey(t *testing.T) {
	fake := &runner.Fake{}
	if err := LaunchCoveMaster(fake, CoveMasterOptions{
		Target: sshargs.Target{Host: "h", User: "agent", Port: 2222}, JamHost: "j", RuntimeAddr: "j:443",
		IdentityToken: "t", LaunchSecret: "s", WorkDir: "/w", Prompt: "p",
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.Calls {
		if c.Name == "ssh" && strings.Contains(strings.Join(c.Args, " "), "cat > "+credsVMPath) {
			t.Fatal("non-subscription launch must not write a credentials file")
		}
	}
	var env string
	for _, c := range fake.Calls {
		if c.Name == "ssh" && strings.Contains(strings.Join(c.Args, " "), "cat > "+coveMasterEnvVMPath) {
			env = c.Stdin
		}
	}
	if !strings.Contains(env, "ANTHROPIC_API_KEY=") {
		t.Fatalf("non-subscription env should set ANTHROPIC_API_KEY:\n%s", env)
	}
}

// TestLaunchCoveMasterResident: AT_COVE_RESIDENT=1 is exported only when the
// options ask for a resident (personal-session) cove.
func TestLaunchCoveMasterResident(t *testing.T) {
	for _, resident := range []bool{false, true} {
		fake := &runner.Fake{}
		err := LaunchCoveMaster(fake, CoveMasterOptions{
			Target: sshargs.Target{Host: "h", User: "agent", Port: 2222}, JamHost: "hh", RuntimeAddr: "hh:443",
			IdentityToken: "t", LaunchSecret: "s", WorkDir: "/w", Prompt: "p", Resident: resident,
		})
		if err != nil {
			t.Fatal(err)
		}
		var env string
		for _, c := range fake.Calls {
			if c.Name == "ssh" && strings.Contains(strings.Join(c.Args, " "), "cat > "+coveMasterEnvVMPath) {
				env = c.Stdin
			}
		}
		if got := strings.Contains(env, "export AT_COVE_RESIDENT=1\n"); got != resident {
			t.Fatalf("resident=%v: env exports AT_COVE_RESIDENT=%v; env:\n%s", resident, got, env)
		}
	}
}

func TestLaunchCoveMasterUsesConnector(t *testing.T) {
	fake := &runner.Fake{}
	c := snippet.Connector{Env: map[string]string{"GH_HOST": "{host}", "ANTHROPIC_API_KEY": "{token}"}}
	if err := LaunchCoveMaster(fake, CoveMasterOptions{
		Target: sshargs.Target{Host: "h"}, JamHost: "jam.example.com", IdentityToken: "tok-123",
		Connector: &c, Subscription: true, // the connector wins over the legacy subscription render
	}); err != nil {
		t.Fatal(err)
	}
	var env string
	for _, call := range fake.Calls {
		if strings.Contains(strings.Join(call.Args, " "), "cat > "+coveMasterEnvVMPath) {
			env = call.Stdin
		}
	}
	if !strings.Contains(env, `export GH_HOST="jam.example.com"`) || !strings.Contains(env, `export ANTHROPIC_API_KEY="${AT_JAM_IDENTITY_TOKEN}"`) {
		t.Fatalf("env script lacks connector vars:\n%s", env)
	}
	if strings.Contains(env, "ANTHROPIC_AUTH_TOKEN") || strings.Contains(env, "git config") {
		t.Fatalf("legacy contract leaked in alongside the connector:\n%s", env)
	}
	if n := strings.Count(env, "tok-123"); n != 1 {
		t.Fatalf("raw token appears %d times", n)
	}
}

func TestLaunchCoveMasterHandsOffConnector(t *testing.T) {
	fake := &runner.Fake{}
	c := snippet.Connector{Env: map[string]string{"GH_HOST": "{host}", "ANTHROPIC_API_KEY": "{token}"}, GitRoute: "/git/"}
	if err := LaunchCoveMaster(fake, CoveMasterOptions{
		Target: sshargs.Target{Host: "h"}, JamHost: "jam.example.com", IdentityToken: "tok-123", Connector: &c,
	}); err != nil {
		t.Fatal(err)
	}
	var env string
	for _, call := range fake.Calls {
		if strings.Contains(strings.Join(call.Args, " "), "cat > "+coveMasterEnvVMPath) {
			env = call.Stdin
		}
	}
	if !strings.Contains(env, "export AT_JAM_BASE_URL='https://jam.example.com'") {
		t.Fatalf("no base url:\n%s", env)
	}
	if !strings.Contains(env, "export AT_JAM_CONNECTOR=") || !strings.Contains(env, `"git_route":"/git/"`) {
		t.Fatalf("no connector handoff:\n%s", env)
	}
	if n := strings.Count(env, "tok-123"); n != 1 {
		t.Fatalf("raw token appears %d times (must only be the single export)", n)
	}
}

func launchWith(t *testing.T, b *sessionctx.Bundle) *runner.Fake {
	t.Helper()
	fake := &runner.Fake{}
	err := LaunchCoveMaster(fake, CoveMasterOptions{
		Target: sshargs.Target{Host: "h", User: "agent", Port: 2222}, JamHost: "jam.example.com",
		RuntimeAddr: "jam.example.com:443", IdentityToken: "tok", LaunchSecret: "s",
		WorkDir: "/w", Prompt: "p", Context: b,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fake
}

// stdinWrites maps each "cat > <path>" ssh target to the stdin piped to it.
func stdinWrites(f *runner.Fake) map[string]string {
	out := map[string]string{}
	for _, c := range f.Calls {
		argv := strings.Join(c.Args, " ")
		if i := strings.Index(argv, "cat > "); c.Name == "ssh" && i >= 0 {
			out[strings.Fields(argv[i+len("cat > "):])[0]] = c.Stdin
		}
	}
	return out
}

func TestLaunchCoveMasterStagesContext(t *testing.T) {
	b := &sessionctx.Bundle{Core: "# Session context\n", Files: map[string]string{"INDEX.md": "x"}, Fingerprint: "fp", Layers: map[string]string{}}
	w := stdinWrites(launchWith(t, b))
	raw, ok := w[coveMasterContextVMPath]
	if !ok {
		t.Fatalf("no write to %s; writes=%v", coveMasterContextVMPath, w)
	}
	var got sessionctx.Bundle
	if err := json.Unmarshal([]byte(raw), &got); err != nil || !reflect.DeepEqual(&got, b) {
		t.Fatalf("staged bundle = %+v (%v), want %+v", got, err, b)
	}
	if env := w[coveMasterEnvVMPath]; !strings.Contains(env, "export AT_COVE_AGENT_CONTEXT_FILE='"+coveMasterContextVMPath+"'") {
		t.Fatalf("env missing AT_COVE_AGENT_CONTEXT_FILE:\n%s", env)
	}
}

func TestLaunchCoveMasterNoContextNoEnv(t *testing.T) {
	w := stdinWrites(launchWith(t, nil))
	if _, ok := w[coveMasterContextVMPath]; ok {
		t.Fatal("nil context must stage nothing")
	}
	if strings.Contains(w[coveMasterEnvVMPath], "AT_COVE_AGENT_CONTEXT_FILE") {
		t.Fatal("nil context must not export AT_COVE_AGENT_CONTEXT_FILE")
	}
}
