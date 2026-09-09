// Command at-switchboard is the in-sandbox Discord conductor (Component A). It
// polls Discord, drives a headless claude turn loop, and posts replies. It is
// launched by `at-cove teammate` over SSH with the bot token injected in-session;
// it is not run directly by users. See docs/superpowers/specs/2026-08-26-remote-teammate-design.md §A.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/switchboard"
)

// usageError is returned by buildConfig for problems that should be reported
// as a CLI usage error (exit 2) — required env vars missing or malformed.
// It carries no secret values, only the offending var name / description.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// buildConfig reads env-sourced config and constructs the switchboard.Config,
// the target channel list, and the Discord bot token. It is factored out of
// run so config-building — including the ErrorChannel default and Log wiring
// — is unit-testable without launching the loop. getenv is injected (rather
// than reading os.Getenv directly) so tests can exercise it hermetically.
func buildConfig(getenv func(string) string) (switchboard.Config, []string, string, error) {
	token := getenv("DISCORD_BOT_TOKEN")
	channels := splitNonEmpty(getenv("SWITCHBOARD_CHANNELS"))
	if token == "" {
		return switchboard.Config{}, nil, "", &usageError{"DISCORD_BOT_TOKEN is required"}
	}
	if len(channels) == 0 {
		return switchboard.Config{}, nil, "", &usageError{"SWITCHBOARD_CHANNELS is required (comma-separated channel ids)"}
	}
	interval := 3 * time.Second
	if v := getenv("SWITCHBOARD_POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return switchboard.Config{}, nil, "", &usageError{fmt.Sprintf("bad SWITCHBOARD_POLL_INTERVAL %q: %v", v, err)}
		}
		interval = d
	}

	// ErrorChannel: explicit env var, else default to the first channel so
	// fail-soft recovered errors always have somewhere to post.
	errCh := getenv("SWITCHBOARD_ERROR_CHANNEL")
	if errCh == "" && len(channels) > 0 {
		errCh = channels[0]
	}

	cfg := switchboard.Config{
		PollInterval: interval,
		ErrorChannel: errCh,
		Log:          func(s string) { fmt.Fprintln(os.Stderr, "at-switchboard:", s) },
	}
	return cfg, channels, token, nil
}

// run parses env-sourced config and drives the switchboard loop until it exits
// or errors.
func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	// `at-switchboard once ...` is a Discord-free tracer that runs a single
	// claude turn against a canned message (see once.go). The default (no
	// subcommand) is the standing poll loop that `at-cove teammate` launches.
	if len(argv) > 0 && argv[0] == "once" {
		return runOnce(argv[1:], getenv, stdout, stderr)
	}

	cfg, channels, token, err := buildConfig(getenv)
	if err != nil {
		fmt.Fprintln(stderr, "at-switchboard:", err)
		return 2
	}

	workDir := resolveWorkDir(getenv)

	d := switchboard.NewRESTClient(token, channels)
	a := switchboard.NewClaudeAgent(runner.OS{}, workDir)
	if err := switchboard.Run(context.Background(), cfg, d, a); err != nil {
		fmt.Fprintln(stderr, "at-switchboard:", err)
		return 1
	}
	return 0
}

// resolveWorkDir returns the sandbox workspace dir: $SWITCHBOARD_WORKDIR, else
// the default. Shared by the loop (run) and the `once` tracer so they can't
// silently diverge.
func resolveWorkDir(getenv func(string) string) string {
	if wd := getenv("SWITCHBOARD_WORKDIR"); wd != "" {
		return wd
	}
	return "/home/agent/workspace"
}

// splitNonEmpty splits a comma-separated list, trimming whitespace and
// dropping empty entries.
func splitNonEmpty(csv string) []string {
	var out []string
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }
