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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/logging"
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
	Image           string        // the pre-built installed image tag (from install.json); never built here
	ImageDigest     string        // the built image's own sha256 to pin the run to (COV-78); empty for a legacy manifest → falls back to Image
	Name            string        // unique container name
	Secrets         []secret.Spec // root (shared) secrets — resolved up front, all steps
	WorkerSecrets   []secret.Spec // worker-class bucket — resolved lazily, agent step only
	GitToken        secret.Spec   // code-host token; withheld from the agent step
	CredentialsFile string        // host-saved agent login to seed; "" = none
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

// Dispatch runs one unit of work: resolve the class → ephemeral run of the
// pre-built installed image → inject the task → prepare/agent/complete bracket
// (token withheld from the agent, minted fresh before each git step) → extract →
// destroy. It never builds — the image is compiled once by `at-cove install`.
//
// ctx carries the host logger (via logging.From) already seeded with the run's
// run/issue/class context; Dispatch captures the VM's SSH channel and grafts the
// per-step `step` attribution as it merges the VM's structured records back into
// that host stream (spec §7), rather than letting VM output spill undemuxed.
func Dispatch(ctx context.Context, o Options) error {
	lg := logging.From(ctx)
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
	repo, ok := o.Cfg.SourceControl.Repo()
	if !ok {
		return fmt.Errorf("kit %q declares no source-control (required for dispatch)", o.Cfg.Name)
	}
	task.Repo.Provider = repo.Provider
	task.Repo.Name = repo.Project
	task.Repo.Host = "https://" + repo.Host
	if task.Repo.SourceBranch == "" {
		task.Repo.SourceBranch = repo.MainBranch // defaulted to "main" at parse
	}
	filled, err := json.MarshalIndent(&task, "", "  ")
	if err != nil {
		return err
	}

	_, _ = o.Ops.ScavengeLabeled(Label, o.GraceWindow, o.Now)

	// Run parameters exposed to the secret resolvers (e.g. a per-task minter).
	runEnv := map[string]string{
		"COVE_RUN_REPO":    repo.Project,
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

	// The image was built + gated once by `at-cove install`; work never builds
	// (COV-38). doWork verified the install is current before calling here, so we
	// simply run the pre-built image — no per-unit build, no per-unit gate, so
	// concurrent dispatch units never race on a shared .build dir.
	if _, err := o.Ops.RunEphemeral(o.Image, o.ImageDigest, o.Name, Label); err != nil {
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
	// Scope egress to the worker class before any step runs (COV-39 §5). The
	// container booted with the baked sealed + root lists; this adds the class's
	// <common> ∪ class delta for this unit only, sourced from the currency-pinned
	// install.json RunConfig (o.Cfg) — "install froze it", never a live config.yml.
	// It is applied before the agent step, so the workload never sees a
	// wider-than-intended window (a class with no extra domains applies an empty
	// delta; root still applies via the baked kit file). The delivery op is
	// privileged (host docker exec as root) and lives in its own interface, so we
	// type-assert it independently of the DispatchOps lifecycle.
	eg, ok := o.Ops.(backend.SessionEgress)
	if !ok {
		return fmt.Errorf("backend does not support session egress (required to scope a dispatched worker's allow-list)")
	}
	domains, err := o.Cfg.ResolvedWorkerDomains(task.Worker.Class)
	if err != nil {
		return err
	}
	if err := eg.ApplySessionEgress(o.Name, domains); err != nil {
		return fmt.Errorf("apply session egress: %w", err)
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
	// Capture each at-task step's stderr (its structured JSONL, spec §7) rather
	// than letting it spill; merge it into the host stream even on failure, so a
	// step's own error record surfaces before we return. stdout is left to spill
	// (it carries no structured records); the merge reads the stderr channel.
	var prepErr bytes.Buffer
	prepStepErr := runStep(o.R, tgt, prepEnv, "at-task prepare", o.Timeout, nil, &prepErr)
	mergeVMRecords(ctx, lg, "prepare", prepErr.Bytes(), secretValues(prepEnv))
	if prepStepErr != nil {
		return egressOr(o.R, tgt, o.OutputPath, fmt.Errorf("at-task prepare: %w", prepStepErr))
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
	// The agent's raw output is demuxed away from the structured sink (§6.3): tee
	// it to a VM-local file (which at-task complete reads back as the "Agent did
	// not respond" detail when the agent leaves no worker-result, e.g. an auth
	// 401) and route the pipeline's combined stream to the SSH channel's stderr,
	// which the host captures instead of letting it spill. That raw output NEVER
	// reaches the structured/cloud sink — it stays VM-local and is only scanned
	// in memory (for a 401) to classify the agent-outcome record below.
	agentCmd := fmt.Sprintf("claude -p --dangerously-skip-permissions \"$(cat %s)\" 2>&1 | tee %s 1>&2",
		shellQuote(promptVMPath), shellQuote(agentLogVMPath))
	var agentOut bytes.Buffer
	_ = runStep(o.R, tgt, agentEnv, agentCmd, o.Timeout, nil, &agentOut) // agent: root + worker bucket; no git token
	compEnv, err := mint()
	if err != nil {
		return fmt.Errorf("mint token for complete: %w", err)
	}
	var compErr bytes.Buffer
	compStepErr := runStep(o.R, tgt, compEnv, "at-task complete", o.Timeout, nil, &compErr)
	mergeVMRecords(ctx, lg, "complete", compErr.Bytes(), secretValues(compEnv))
	if compStepErr != nil {
		return egressOr(o.R, tgt, o.OutputPath, fmt.Errorf("at-task complete: %w", compStepErr))
	}

	out, err := o.R.Output("ssh", append(sshargs.Base(tgt), "cat "+resultVMPath)...)
	if err != nil {
		return egressOr(o.R, tgt, o.OutputPath, fmt.Errorf("extract result at %s: %w", resultVMPath, err))
	}
	if strings.TrimSpace(out) == "" {
		return egressOr(o.R, tgt, o.OutputPath, fmt.Errorf("dispatch produced no result at %s", resultVMPath))
	}
	// Classify the run's outcome into one self-attributing structured record
	// (ok / needs-input / error), with the auth_failed + egress-wall hooks (§6.3).
	emitAgentOutcome(ctx, lg, o.R, tgt, out, agentOut.String())
	return os.WriteFile(o.OutputPath, []byte(out), 0o600)
}

// runStep sources the given env from a tmpfs file (never on argv), removes it, cds to the
// workdir, and runs command under a timeout. It uses the runner's writer-injection
// seam (COV-66) so the caller can capture the SSH channel — a nil stdout/stderr
// falls back to the process default (spill), a supplied writer captures it.
func runStep(r runner.Runner, tgt sshargs.Target, env map[string]string, command string, timeout time.Duration, stdout, stderr io.Writer) error {
	if err := writeVM(r, tgt, []byte(envScript(env)), envVMPath); err != nil {
		return err
	}
	secs := int(timeout.Seconds())
	if secs <= 0 {
		secs = 1800
	}
	remote := fmt.Sprintf("set -a; . %s; rm -f %s; cd %s; timeout %d %s",
		envVMPath, envVMPath, shellQuote(workDir), secs, command)
	return r.RunIO(nil, stdout, stderr, "ssh", append(sshargs.Base(tgt), remote)...)
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
