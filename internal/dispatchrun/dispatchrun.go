// Package dispatchrun orchestrates `at-cove dispatch`: a synchronous, one-shot run
// of a unit of work in a fresh ephemeral hardened VM. It reuses at-cove's secret,
// ssh, and backend machinery; it never parses the in/out files.
package dispatchrun

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// Label tags every ephemeral dispatch container so scavenging can find orphans.
const Label = "at-cove.dispatch"

const (
	inputVMPath  = "/in/input.json"
	outputVMPath = "/out/output.json"
	credsVMPath  = "/agent-data/.credentials.json"
	envVMPath    = "/dev/shm/at-cove-dispatch-env"
)

type Options struct {
	Ops             backend.DispatchOps
	R               runner.Runner
	Cfg             kit.Config
	BuildDir        string
	Name            string // unique container name
	Secrets         []secret.Spec
	CredentialsFile string // host-saved agent login to seed; "" = none
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

// Dispatch runs one unit of work: scavenge → build → ephemeral run → inject →
// exec the kit's dispatch command → extract output → destroy. Blocking.
func Dispatch(o Options) error {
	if len(o.Cfg.Dispatch.Command) == 0 {
		return fmt.Errorf("kit %q declares no dispatch.command", o.Cfg.Name)
	}
	// Scavenge crash orphans (best-effort; never blocks a live dispatch).
	_, _ = o.Ops.ScavengeLabeled(Label, o.GraceWindow, o.Now)

	// Resolve secrets before creating anything (fail closed).
	env, err := secret.Resolve(o.R, o.Secrets)
	if err != nil {
		return err
	}

	img := "at-cove-for-" + o.Cfg.Name
	if err := o.Ops.BuildImage(o.BuildDir, img); err != nil {
		return err
	}
	if _, err := o.Ops.RunEphemeral(img, o.Name, Label); err != nil {
		return err
	}
	defer o.Ops.RemoveContainer(o.Name) // teardown on every path

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

	if err := seedFile(o.R, tgt, o.CredentialsFile, credsVMPath); err != nil {
		return fmt.Errorf("seed agent credentials: %w", err)
	}
	input, err := os.ReadFile(o.InputPath)
	if err != nil {
		return err
	}
	if err := writeVM(o.R, tgt, input, inputVMPath); err != nil {
		return fmt.Errorf("inject input: %w", err)
	}
	if err := runWork(o.R, tgt, env, o.Cfg.Dispatch.Command, o.Timeout); err != nil {
		return fmt.Errorf("dispatch command: %w", err)
	}
	out, err := o.R.Output("ssh", append(sshargs.Base(tgt), "cat "+outputVMPath)...)
	if err != nil {
		return fmt.Errorf("extract output at %s: %w", outputVMPath, err)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("dispatch produced no output at %s", outputVMPath)
	}
	return os.WriteFile(o.OutputPath, []byte(out), 0o600)
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

// runWork runs the kit's dispatch command with secrets sourced from a tmpfs env
// script (never on argv), bounded by timeout, /out ready for the output.
func runWork(r runner.Runner, tgt sshargs.Target, env map[string]string, cmd []string, timeout time.Duration) error {
	if err := writeVM(r, tgt, []byte(envScript(env)), envVMPath); err != nil {
		return err
	}
	secs := int(timeout.Seconds())
	if secs <= 0 {
		secs = 1800
	}
	remote := fmt.Sprintf("set -a; . %s; rm -f %s; mkdir -p /out; timeout %d %s",
		envVMPath, envVMPath, secs, shellJoin(cmd))
	return r.RunStdin(nil, "ssh", append(sshargs.Base(tgt), remote)...)
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

func shellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
