// Command at-dispatch is the Linear-driven dispatcher that schedules work onto
// at-cove sandboxes. Today it loads and validates its config; the scheduler is
// not implemented yet — see docs/orchestration/.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/aethons-tools/cove/internal/dispatch/config"
	dexec "github.com/aethons-tools/cove/internal/dispatch/exec"
	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/runner"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `at-dispatch — Linear-driven dispatcher for at-cove sandboxes

Usage:
  at-dispatch version                 print the build version
  at-dispatch serve --config <path>   load + validate the config (scheduler not implemented yet)

See docs/orchestration/ for the design.
`

// run is the testable entry point: it returns a process exit code and writes
// only to the provided streams (no direct os.Stdout/os.Stderr use).
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "serve":
		return doServe(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "at-dispatch: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
}

func doServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to the at-dispatch config file (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *cfgPath == "" {
		fmt.Fprintln(stderr, "at-dispatch serve: --config <path> is required")
		return 2
	}
	cfg, err := config.LoadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-dispatch serve: %v\n", err)
		return 1
	}

	classes := make([]string, 0, len(cfg.Classes))
	for name := range cfg.Classes {
		classes = append(classes, name)
	}
	sort.Strings(classes)
	fmt.Fprintf(stdout, "at-dispatch: config OK for %s — %d class(es): %s\n",
		cfg.Repo.Slug, len(classes), strings.Join(classes, ", "))

	// resolver: run a secret's argv on the host, return trimmed stdout (in memory).
	resolve := func(argv []string) (string, error) {
		out, err := runner.OS{}.Output(argv[0], argv[1:]...)
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(out, "\n"), nil
	}

	token, err := resolve(cfg.Tracker.Token.Command)
	if err != nil {
		fmt.Fprintf(stderr, "at-dispatch serve: resolve tracker token: %v\n", err)
		return 1
	}

	tracker, err := linear.New(cfg, token, nil)
	if err != nil {
		fmt.Fprintf(stderr, "at-dispatch serve: connect to Linear: %v\n", err)
		return 1
	}

	logger := log.New(stderr, "at-dispatch ", log.LstdFlags)
	engine := scheduler.New(cfg, tracker, dexec.New(), resolve, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Printf("scheduler started (poll %s); Ctrl-C to stop", cfg.Tracker.PollInterval)
	_ = engine.Run(ctx) // returns ctx.Err() on signal — a clean shutdown
	logger.Printf("scheduler stopped")
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
