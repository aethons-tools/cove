package connect

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// coveMasterPromptVMPath / coveMasterEnvVMPath are tmpfs files the prompt and env
// are staged into over ssh stdin (never argv, never persistent disk), sourced and
// (env) removed by the detached launch — mirrors the teammate env path.
const (
	coveMasterPromptVMPath = "/dev/shm/cove-agent-prompt"
	coveMasterEnvVMPath    = "/dev/shm/cove-master-env"
	// CoveMasterLogVMPath is where cove-master's detached launch appends its
	// combined stdout+stderr inside the cove. Exported so the launcher can grab
	// its tail on teardown (a post-mortem for a cove that died). Single source
	// of truth for the path.
	CoveMasterLogVMPath = "/agent-data/cove-master.log"
)

// CoveMasterOptions carries what LaunchCoveMaster injects into a raised cove.
type CoveMasterOptions struct {
	Target        sshargs.Target
	JamHost       string // jam.host — connector base is https://<JamHost>
	RuntimeAddr   string // AT_JAM_RUNTIME_ADDR (jam.host:443)
	IdentityToken string // shared: AT_JAM_IDENTITY_TOKEN + the agent connector token
	LaunchSecret  string // AT_JAM_LAUNCH_SECRET
	WorkDir       string // AT_COVE_WORKDIR
	Prompt        string // written to tmpfs; AT_COVE_AGENT_PROMPT_FILE points at it
	Resident      bool   // AT_COVE_RESIDENT=1: a personal session's agent waits for a Wake after every turn
}

// LaunchCoveMaster stages the agent connector (Anthropic + git through Jam)
// plus the cove-master env and the prompt into tmpfs over ssh stdin, then starts
// cove-master detached over a non-tty ssh (fire-and-forget), so it survives the
// ssh channel closing. Secrets never touch argv or persistent disk.
func LaunchCoveMaster(r runner.Runner, o CoveMasterOptions) error {
	if err := writeVM(r, o.Target, o.Prompt, coveMasterPromptVMPath); err != nil {
		return fmt.Errorf("cove-master prompt: %w", err)
	}
	var script strings.Builder
	// Agent connector (Anthropic base URL + x-api-key token + git routing). The
	// identity token is exported here and shared with cove-master below.
	script.WriteString(snippet.Render("https://"+o.JamHost, o.IdentityToken))
	// cove-master's own env (AT_JAM_IDENTITY_TOKEN already exported by Render).
	fmt.Fprintf(&script, "export AT_JAM_RUNTIME_ADDR=%s\n", shellQuote(o.RuntimeAddr))
	fmt.Fprintf(&script, "export AT_JAM_LAUNCH_SECRET=%s\n", shellQuote(o.LaunchSecret))
	// The deprecated AT_HARBOR_* names, still read by cove-master in older
	// images, for one release (docs/usage/jam/renamed-from-harbor.md). Each is
	// exported from its new variable, so no value is written twice.
	script.WriteString("export AT_HARBOR_RUNTIME_ADDR=\"$AT_JAM_RUNTIME_ADDR\"\n")
	script.WriteString("export AT_HARBOR_LAUNCH_SECRET=\"$AT_JAM_LAUNCH_SECRET\"\n")
	fmt.Fprintf(&script, "export AT_COVE_WORKDIR=%s\n", shellQuote(o.WorkDir))
	fmt.Fprintf(&script, "export AT_COVE_AGENT_PROMPT_FILE=%s\n", shellQuote(coveMasterPromptVMPath))
	if o.Resident {
		script.WriteString("export AT_COVE_RESIDENT=1\n")
	}
	if err := writeVM(r, o.Target, script.String(), coveMasterEnvVMPath); err != nil {
		return fmt.Errorf("cove-master env: %w", err)
	}
	cmd := "set -a; . " + coveMasterEnvVMPath + "; set +a; rm -f " + coveMasterEnvVMPath + "; " +
		"setsid nohup cove-master </dev/null >>" + CoveMasterLogVMPath + " 2>&1 &"
	if err := r.Run("ssh", append(sshargs.Base(o.Target), cmd)...); err != nil {
		return fmt.Errorf("cove-master launch: %w", err)
	}
	return nil
}
