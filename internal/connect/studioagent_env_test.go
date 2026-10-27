package connect

import (
	"regexp"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// The env a studio's agent is launched with names only JAM_* (jam rename):
// no pre-rename AT_* names and no dual writes.
func TestLaunchStudioAgentEnvNamesOnlyJam(t *testing.T) {
	fake := &runner.Fake{}
	err := LaunchStudioAgent(fake, StudioAgentOptions{
		Target:  sshargs.Target{Host: "h", User: "agent", Port: 2222, IdentityFile: "k", KnownHostsFile: "kh"},
		JamHost: "jam.example.com", RuntimeAddr: "jam.example.com:443",
		IdentityToken: "tok-123", LaunchSecret: "sec-456",
		WorkDir: "/home/agent/workspace", Prompt: "p", Resident: true, SessionKind: "standing",
	})
	if err != nil {
		t.Fatal(err)
	}
	var env string
	for _, c := range fake.Calls {
		if c.Name == "ssh" && strings.Contains(strings.Join(c.Args, " "), "cat > "+studioAgentEnvVMPath) {
			env = c.Stdin
		}
	}
	for _, want := range []string{"export JAM_RUNTIME_ADDR=", "export JAM_LAUNCH_SECRET=", "export JAM_IDENTITY_TOKEN=tok-123",
		"export JAM_WORKDIR=", "export JAM_AGENT_PROMPT_FILE=", "export JAM_BASE_URL=", "export JAM_RESIDENT=1",
		"export JAM_SESSION_KIND="} {
		if !strings.Contains(env, want) {
			t.Errorf("env script lacks %q:\n%s", want, env)
		}
	}
	if old := regexp.MustCompile(`\bAT_[A-Z]+_`).FindString(env); old != "" {
		t.Errorf("env script still names %q:\n%s", old, env)
	}
	if strings.Count(env, "tok-123") != 1 || strings.Count(env, "sec-456") != 1 {
		t.Errorf("each secret must be written exactly once:\n%s", env)
	}
}
