// Command jam-studio-manager runs a Studio Manager: the per-host daemon that
// raises and tends studios on behalf of a Jam. `run` serves until signalled;
// `colima` prepares the Docker backend on macOS.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/colimacmd"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/studiomanager"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	app := cli.App{
		Name:    "jam-studio-manager",
		Version: version + basedigest.VersionNote(),
		Commands: []cli.Command{
			{Name: "run", Brief: "run the Studio Manager until SIGINT or SIGTERM (run [--config <path>])",
				Run: func(args []string, _ cli.Globals, _, stderr io.Writer) int {
					return cmdRun(args, getenv, stderr)
				}},
			{Name: "colima", Brief: "prepare the Docker backend (setup-docker|check-docker)",
				Run: colimacmd.Command("jam-studio-manager", getenv, runner.OS{})},
		},
	}
	return app.Run(argv, stdout, stderr)
}

func cmdRun(args []string, getenv func(string) string, stderr io.Writer) int {
	fs := flag.NewFlagSet("jam-studio-manager run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file (default: <config dir>/config.yml)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *cfgPath == "" {
		*cfgPath = filepath.Join(studiomanager.DefaultDir(getenv), "config.yml")
	}
	cfg, err := studiomanager.Load(*cfgPath, getenv)
	if err != nil {
		fmt.Fprintf(stderr, "jam-studio-manager run: %v\n", err)
		return 2
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		lvl = slog.LevelInfo
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: lvl}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := studiomanager.Serve(ctx, cfg, version, log, studiomanager.Deps{}); err != nil {
		fmt.Fprintf(stderr, "jam-studio-manager run: %v\n", err)
		return 1
	}
	return 0
}
