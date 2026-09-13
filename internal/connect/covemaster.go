package connect

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/harbor/snippet"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// coveMasterPromptVMPath / coveMasterEnvVMPath are tmpfs files the prompt and env
// are staged into over ssh stdin (never argv, never persistent disk), sourced and
// (env) removed by the detached launch — mirrors the teammate env path.
const (
	coveMasterPromptVMPath = "/dev/shm/cove-agent-prompt"
	coveMasterEnvVMPath    = "/dev/shm/cove-master-env"
	coveMasterLogVMPath    = "/agent-data/cove-master.log"
)

// CoveMasterOptions carries what LaunchCoveMaster injects into a raised cove.
type CoveMasterOptions struct {
	Target        sshargs.Target
	HarborHost    string // harbor.host — connector base is https://<HarborHost>
	RuntimeAddr   string // AT_HARBOR_RUNTIME_ADDR (harbor.host:443)
	IdentityToken string // shared: AT_HARBOR_IDENTITY_TOKEN + the agent connector token
	LaunchSecret  string // AT_HARBOR_LAUNCH_SECRET
	WorkDir       string // AT_COVE_WORKDIR
	Prompt        string // written to tmpfs; AT_COVE_AGENT_PROMPT_FILE points at it
}

// LaunchCoveMaster stages the agent connector (Anthropic + git through harbor)
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
	script.WriteString(snippet.Render("https://"+o.HarborHost, o.IdentityToken))
	// cove-master's own env (AT_HARBOR_IDENTITY_TOKEN already exported by Render).
	fmt.Fprintf(&script, "export AT_HARBOR_RUNTIME_ADDR=%s\n", shellQuote(o.RuntimeAddr))
	fmt.Fprintf(&script, "export AT_HARBOR_LAUNCH_SECRET=%s\n", shellQuote(o.LaunchSecret))
	fmt.Fprintf(&script, "export AT_COVE_WORKDIR=%s\n", shellQuote(o.WorkDir))
	fmt.Fprintf(&script, "export AT_COVE_AGENT_PROMPT_FILE=%s\n", shellQuote(coveMasterPromptVMPath))
	if err := writeVM(r, o.Target, script.String(), coveMasterEnvVMPath); err != nil {
		return fmt.Errorf("cove-master env: %w", err)
	}
	cmd := "set -a; . " + coveMasterEnvVMPath + "; set +a; rm -f " + coveMasterEnvVMPath + "; " +
		"setsid nohup cove-master </dev/null >>" + coveMasterLogVMPath + " 2>&1 &"
	if err := r.Run("ssh", append(sshargs.Base(o.Target), cmd)...); err != nil {
		return fmt.Errorf("cove-master launch: %w", err)
	}
	return nil
}
