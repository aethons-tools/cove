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

// run parses env-sourced config and drives the switchboard loop until it exits
// or errors. getenv is injected (rather than reading os.Getenv directly) so the
// test can exercise the missing-token usage-error path hermetically.
func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	token := getenv("DISCORD_BOT_TOKEN")
	channels := splitNonEmpty(getenv("SWITCHBOARD_CHANNELS"))
	if token == "" {
		fmt.Fprintln(stderr, "at-switchboard: DISCORD_BOT_TOKEN is required")
		return 2
	}
	if len(channels) == 0 {
		fmt.Fprintln(stderr, "at-switchboard: SWITCHBOARD_CHANNELS is required (comma-separated channel ids)")
		return 2
	}
	interval := 3 * time.Second
	if v := getenv("SWITCHBOARD_POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			fmt.Fprintf(stderr, "at-switchboard: bad SWITCHBOARD_POLL_INTERVAL %q: %v\n", v, err)
			return 2
		}
		interval = d
	}
	workDir := getenv("SWITCHBOARD_WORKDIR")
	if workDir == "" {
		workDir = "/home/agent/workspace"
	}

	d := switchboard.NewRESTClient(token, channels)
	a := switchboard.NewClaudeAgent(runner.OS{}, workDir)
	cfg := switchboard.Config{PollInterval: interval}
	if err := switchboard.Run(context.Background(), cfg, d, a); err != nil {
		fmt.Fprintln(stderr, "at-switchboard:", err)
		return 1
	}
	return 0
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
