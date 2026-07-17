// Package dispatchrun orchestrates `at-cove work`: a synchronous, one-shot run
// of a unit of work in a fresh ephemeral hardened VM. It reads the injected task's
// worker.class to resolve the kit's prompt for that class, then drives the
// prepare/agent/complete bracket step-by-step over ssh, with the code-host token
// withheld from the agent step. It reuses at-cove's secret, ssh, and backend
// machinery; it never parses the task-result. When the bracket fails, it first
// checks the hardening layer's squid access log for egress-wall denials and, if
// found, surfaces them as a first-class NEEDS INPUT result (see egress.go) rather
// than an opaque error.
package dispatchrun

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// Label tags every ephemeral dispatch container so scavenging can find orphans.
const Label = "at-cove.work"

const (
	sshReadyAttempts = 10
	sshReadyDelay    = 2 * time.Second
)

const (
	credsVMPath = "/agent-data/.credentials.json"
	envVMPath   = "/dev/shm/at-cove-work-env"
)

const (
	workDir        = "/home/agent/work"
	taskVMPath     = workDir + "/.at-task/task.json"
	resultVMPath   = workDir + "/.at-task/task-result.json"
	agentLogVMPath = workDir + "/.at-task/agent.log" // agent step's combined output; at-task complete reads it when the agent leaves no result
	promptVMPath   = "/dev/shm/at-cove-prompt"
)

type Options struct {
	Ops             backend.DispatchOps
	R               runner.Runner
	Cfg             kit.Config
	BuildDir        string
	Base            backend.BaseSpec // base-image resolution + provenance gate inputs
	Name            string           // unique container name
	Secrets         []secret.Spec    // root (shared) secrets — resolved up front, all steps
	WorkerSecrets   []secret.Spec    // worker-class bucket — resolved lazily, agent step only
	GitToken        secret.Spec      // code-host token; withheld from the agent step
	CredentialsFile string           // host-saved agent login to seed; "" = none
	IdentityFile    string
	KnownHostsDir   string
	InputPath       string
	OutputPath      string
	Timeout         time.Duration
	GraceWindow     time.Duration
	Now             time.Time
}

// Reap removes labeled dispatch orphans older than grace (the `--reap` path).
func Reap(ops backend.DispatchOps, grace time.Duration, now time.Time) error {
	_, err := ops.ScavengeLabeled(Label, grace, now)
	return err
}

// Dispatch runs one unit of work: resolve the class → build → ephemeral run → inject the
// task → prepare/agent/complete bracket (token withheld from the agent, minted fresh
// before each git step) → extract → destroy.
func Dispatch(o Options) error {
	input, err := os.ReadFile(o.InputPath)
	if err != nil {
		return err
	}
	var task worker.Task
	if err := json.Unmarshal(input, &task); err != nil {
		return fmt.Errorf("parse task: %w", err)
	}
	if task.Worker.Class == "" {
		return fmt.Errorf("task declares no worker.class")
	}
	w, err := o.Cfg.ResolvedWorker(task.Worker.Class)
	if err != nil {
		return err
	}
	// Fill the repo from the kit's source-control — the single source of truth.
	if o.Cfg.SourceControl == nil || o.Cfg.SourceControl.GitHub == nil {
		return fmt.Errorf("kit %q declares no source-control (required for dispatch)", o.Cfg.Name)
	}
	task.Repo.Name = o.Cfg.SourceControl.GitHub.Project
	task.Repo.Host = "https://github.com"
	if task.Repo.SourceBranch == "" {
		task.Repo.SourceBranch = o.Cfg.SourceControl.GitHub.MainBranch // defaulted to "main" at parse
	}
	filled, err := json.MarshalIndent(&task, "", "  ")
	if err != nil {
		return err
	}

	_, _ = o.Ops.ScavengeLabeled(Label, o.GraceWindow, o.Now)

	// Run parameters exposed to the secret resolvers (e.g. a per-task minter).
	runEnv := map[string]string{
		"COVE_RUN_REPO":    o.Cfg.SourceControl.GitHub.Project,
		"COVE_RUN_ISSUE":   task.Issue.Key,
		"COVE_RUN_CLASS":   task.Worker.Class,
		"COVE_RUN_TIMEOUT": o.Timeout.String(),
	}
	// The code-host token arrives as a distinct spec (o.GitToken), structurally
	// separate from the root/agent secrets (o.Secrets) — never mixed in, so it
	// cannot leak into the agent step by name-matching accident.
	base, err := secret.Resolve(o.R, runEnv, o.Secrets) // root secrets → agent bucket; fail closed
	if err != nil {
		return err
	}
	hasToken := o.GitToken.Name != ""
	// mint returns base + a freshly-minted code-host token (or just base if none is declared).
	mint := func() (map[string]string, error) {
		e := make(map[string]string, len(base)+1)
		for k, v := range base {
			e[k] = v
		}
		if hasToken {
			tok, err := secret.Resolve(o.R, runEnv, []secret.Spec{o.GitToken})
			if err != nil {
				return nil, err
			}
			for k, v := range tok {
				e[k] = v
			}
		}
		return e, nil
	}

	img := "at-cove-for-" + o.Cfg.Name
	if err := o.Ops.BuildImage(o.BuildDir, img, o.Base); err != nil {
		return err
	}
	if _, err := o.Ops.RunEphemeral(img, o.Name, Label); err != nil {
		return err
	}
	defer o.Ops.RemoveContainer(o.Name)

	ep, cleanup, err := o.Ops.Dial(o.Name)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := os.MkdirAll(o.KnownHostsDir, 0o700); err != nil {
		return err
	}
	tgt := sshargs.Target{
		Host: ep.Host, User: ep.User, Port: ep.Port,
		IdentityFile: o.IdentityFile, KnownHostsFile: filepath.Join(o.KnownHostsDir, o.Name),
	}
	if err := waitForSSH(o.R, tgt, sshReadyAttempts, sshReadyDelay, time.Sleep); err != nil {
		return err
	}
	if err := seedFile(o.R, tgt, o.CredentialsFile, credsVMPath); err != nil {
		return fmt.Errorf("seed agent credentials: %w", err)
	}
	if err := writeVM(o.R, tgt, filled, taskVMPath); err != nil {
		return fmt.Errorf("inject task: %w", err)
	}

	// The bracket. prepare MUST succeed or the run aborts — surfacing the failure
	// (e.g. a git auth 403) instead of masking it as a downstream "no worker result".
	// The agent then runs, and complete always runs after a good prepare (at-task
	// complete always writes a task-result). Each git step (prepare, complete) gets a
	// freshly minted code-host token; the agent runs on base, never carrying the token.
	prepEnv, err := mint()
	if err != nil {
		return fmt.Errorf("mint token for prepare: %w", err)
	}
	if err := runStep(o.R, tgt, prepEnv, "at-task prepare", o.Timeout); err != nil {
		return egressOr(o.R, tgt, o.OutputPath, fmt.Errorf("at-task prepare: %w", err))
	}
	if err := writeVM(o.R, tgt, []byte(agentPrompt(w.Prompt)), promptVMPath); err != nil {
		return err
	}
	// Resolve the worker-class bucket now — immediately before the agent runs —
	// so a freshly minted bearer's TTL only has to cover the agent's own run
	// (the build/prepare overhead is already spent). It is merged only into the
	// agent env; the git steps never carry it.
	agentEnv := base
	if len(o.WorkerSecrets) > 0 {
		ws, err := secret.Resolve(o.R, runEnv, o.WorkerSecrets)
		if err != nil {
			return egressOr(o.R, tgt, o.OutputPath, fmt.Errorf("resolve worker secrets: %w", err))
		}
		agentEnv = make(map[string]string, len(base)+len(ws))
		for k, v := range base {
			agentEnv[k] = v
		}
		for k, v := range ws {
			agentEnv[k] = v
		}
	}
	// Tee the agent's combined output to a VM-local file: it still streams live to
	// the host, and at-task complete reads it back as the "Agent did not respond"
	// detail when the agent leaves no worker-result (e.g. an auth 401).
	agentCmd := fmt.Sprintf("claude -p --dangerously-skip-permissions \"$(cat %s)\" 2>&1 | tee %s",
		shellQuote(promptVMPath), shellQuote(agentLogVMPath))
	_ = runStep(o.R, tgt, agentEnv, agentCmd, o.Timeout) // agent: root + worker bucket; no git token
	compEnv, err := mint()
	if err != nil {
		return fmt.Errorf("mint token for complete: %w", err)
	}
	if err := runStep(o.R, tgt, compEnv, "at-task complete", o.Timeout); err != nil {
		return egressOr(o.R, tgt, o.OutputPath, fmt.Errorf("at-task complete: %w", err))
	}

	out, err := o.R.Output("ssh", append(sshargs.Base(tgt), "cat "+resultVMPath)...)
	if err != nil {
		return egressOr(o.R, tgt, o.OutputPath, fmt.Errorf("extract result at %s: %w", resultVMPath, err))
	}
	if strings.TrimSpace(out) == "" {
		return egressOr(o.R, tgt, o.OutputPath, fmt.Errorf("dispatch produced no result at %s", resultVMPath))
	}
	return os.WriteFile(o.OutputPath, []byte(out), 0o600)
}

// runStep sources the given env from a tmpfs file (never on argv), removes it, cds to the
// workdir, and runs command under a timeout.
func runStep(r runner.Runner, tgt sshargs.Target, env map[string]string, command string, timeout time.Duration) error {
	if err := writeVM(r, tgt, []byte(envScript(env)), envVMPath); err != nil {
		return err
	}
	secs := int(timeout.Seconds())
	if secs <= 0 {
		secs = 1800
	}
	remote := fmt.Sprintf("set -a; . %s; rm -f %s; cd %s; timeout %d %s",
		envVMPath, envVMPath, shellQuote(workDir), secs, command)
	return r.RunStdin(nil, "ssh", append(sshargs.Base(tgt), remote)...)
}

// agentPrompt joins the class's role prompt with the standard worker-result protocol.
func agentPrompt(classPrompt string) string { return classPrompt + "\n\n" + resultProtocol }

const resultProtocol = `---
Your task is specified in .at-task/task.json in the current directory (the "task" -> "brief"
field is your instructions; "repo" describes the checked-out repository, already cloned into
the cwd on the work branch). Do the work: make the changes and run the project's tests.

When finished, write your result to .at-task/worker-result.json as EXACTLY ONE of:
  {"status":{"ok":{"pull-request":{"title":"<PR title>","message":"<PR description>"}}}}
  {"status":{"needs-input":{"doing":"…","blocker":"…","need":"…","tried":"…"}}}
  {"status":{"error":{"message":"<what went wrong>"}}}
Use ok only if the change is complete and tests pass (omit "pull-request" to push without a PR).
Do NOT push or open a PR yourself — that is handled after you exit.`

// waitForSSH probes the VM's sshd with a trivial command until it succeeds or
// attempts are exhausted, sleeping delay between tries. The container's sshd may
// not be accepting connections the instant RunEphemeral returns. sleep is injected
// so tests run without real delay.
func waitForSSH(r runner.Runner, tgt sshargs.Target, attempts int, delay time.Duration, sleep func(time.Duration)) error {
	probe := append([]string{"-o", "ConnectTimeout=5"}, sshargs.Base(tgt)...)
	probe = append(probe, "true")
	var err error
	for i := 0; i < attempts; i++ {
		if err = r.Run("ssh", probe...); err == nil {
			return nil
		}
		if i < attempts-1 {
			sleep(delay)
		}
	}
	return fmt.Errorf("sshd not ready after %d attempts: %w", attempts, err)
}

// seedFile copies a host file into the VM (mode 077, via stdin, never on argv).
// A "" local path or a missing file is a no-op.
func seedFile(r runner.Runner, tgt sshargs.Target, localPath, vmPath string) error {
	if localPath == "" {
		return nil
	}
	data, err := os.ReadFile(localPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return writeVM(r, tgt, data, vmPath)
}

// writeVM writes data to vmPath in the VM over ssh stdin (values never on argv).
func writeVM(r runner.Runner, tgt sshargs.Target, data []byte, vmPath string) error {
	remote := "umask 077; mkdir -p " + filepath.Dir(vmPath) + "; cat > " + vmPath
	return r.RunStdin(bytes.NewReader(data), "ssh", append(sshargs.Base(tgt), remote)...)
}

func envScript(env map[string]string) string {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(env[k]))
	}
	return b.String()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
