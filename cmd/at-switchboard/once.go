package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/switchboard"
)

// runOnce is the `at-switchboard once` tracer: it feeds ONE canned message to a
// real ClaudeAgent turn and prints what the agent produced — no Discord, no bot
// token. It exists to validate the claude side of the pipeline in isolation
// (does `claude -p --continue` run headless, does the agent write a valid
// .switchboard/turn-result.json) before wiring up a real bot. Run it inside a
// created, logged-in sandbox: `at-switchboard once --message "fix the flaky test"`.
func runOnce(args []string, getenv func(string) string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("once", flag.ContinueOnError)
	fs.SetOutput(errw)
	msg := fs.String("message", "", "message content to feed the agent (reads stdin if empty)")
	channel := fs.String("channel", "demo", "channel id/name to tag the message with")
	author := fs.String("author", "tracer", "author name to tag the message with")
	workdir := fs.String("workdir", getenv("SWITCHBOARD_WORKDIR"), "workspace dir (default $SWITCHBOARD_WORKDIR or /home/agent/workspace)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	wd := *workdir
	if wd == "" {
		wd = "/home/agent/workspace"
	}

	content := strings.TrimSpace(*msg)
	if content == "" {
		b, _ := io.ReadAll(os.Stdin)
		content = strings.TrimSpace(string(b))
	}
	if content == "" {
		fmt.Fprintln(errw, "at-switchboard once: no message (use --message or pipe it on stdin)")
		return 2
	}

	input := switchboard.RenderInbox([]switchboard.Message{{Channel: *channel, Author: *author, Content: content}})
	a := switchboard.NewClaudeAgent(runner.OS{}, wd)
	return doOnce(a, input, wd, out)
}

// doOnce runs a single agent turn and prints a diagnostic: the turn input as the
// agent sees it, the raw result file the agent wrote (or its absence), and a
// verdict. It returns 0 when the agent produced a valid TurnResult, 1 otherwise.
// The Agent is injected so this is unit-testable without a real claude.
func doOnce(a switchboard.Agent, input, workDir string, out io.Writer) int {
	fmt.Fprintln(out, "── turn input (as the agent sees it) ──")
	fmt.Fprintln(out, input)

	res, turnErr := a.RunTurn(context.Background(), input)

	// Best-effort: show what the agent actually wrote, regardless of parse outcome.
	resultPath := filepath.Join(workDir, ".switchboard", "turn-result.json")
	raw, readErr := os.ReadFile(resultPath)
	fmt.Fprintf(out, "── raw %s ──\n", resultPath)
	if readErr != nil {
		fmt.Fprintf(out, "(absent: %v)\n", readErr)
	} else {
		fmt.Fprintln(out, strings.TrimRight(string(raw), "\n"))
	}

	fmt.Fprintln(out, "── verdict ──")
	if turnErr != nil {
		if readErr != nil {
			fmt.Fprintf(out, "FAIL: the agent did not write a result file (%v)\n", turnErr)
			fmt.Fprintln(out, "→ the agent likely answered in prose instead of writing .switchboard/turn-result.json.")
			fmt.Fprintln(out, "  Check: does headless `claude -p` have write permission here, and is the turn prompt clear enough?")
		} else {
			fmt.Fprintf(out, "FAIL: the agent wrote a result file but it did not parse (%v)\n", turnErr)
			fmt.Fprintln(out, "→ tune the turn prompt so the agent emits EXACTLY {\"messages\":[...],\"action\":\"exit|wait|get\"}.")
		}
		return 1
	}

	fmt.Fprintf(out, "OK: action=%q, %d message(s) the conductor would post:\n", res.Action, len(res.Messages))
	for _, m := range res.Messages {
		fmt.Fprintf(out, "  → #%s: %s\n", m.Channel, m.Content)
	}
	return 0
}
