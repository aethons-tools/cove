// Command jam-colima runs a Studio Manager on a Colima host: the per-host
// daemon that raises and tends studios on behalf of a Jam. `run` serves until
// signalled; `colima` prepares the colima VM's Docker for docker:true kits.
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

const prog = "jam-colima"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	app := cli.App{
		Name:        prog,
		Version:     version,
		VersionNote: basedigest.VersionNote(),
		Commands: []cli.Command{
			{Name: "run", Brief: "run the Studio Manager until SIGINT or SIGTERM (run [--config <path>])",
				Run: func(args []string, _ cli.Globals, _, stderr io.Writer) int {
					return cmdRun(args, getenv, stderr)
				}},
			{Name: "colima", Brief: "prepare the Docker backend (setup-docker|check-docker)",
				Run: colimacmd.Command(prog, getenv, runner.OS{}, colimaConfig(getenv))},
		},
	}
	return app.Run(argv, stdout, stderr)
}

// defaultConfig is the config file run reads without --config.
func defaultConfig(getenv func(string) string) string {
	return filepath.Join(studiomanager.DefaultDir(getenv), "config.yml")
}

// colimaConfig lets `colima setup-docker|check-docker` read colima.profile
// from the manager config (strictly, as run does).
func colimaConfig(getenv func(string) string) colimacmd.Config {
	return colimacmd.Config{
		DefaultPath: defaultConfig(getenv),
		Profile: func(path string) (string, error) {
			c, err := studiomanager.Load(path, getenv)
			return c.Colima.Profile, err
		},
	}
}

func cmdRun(args []string, getenv func(string) string, stderr io.Writer) int {
	fs := flag.NewFlagSet(prog+" run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file (default: <config dir>/config.yml)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Before the config path resolves: the default config lives in the moved dir.
	if from, err := studiomanager.MigrateDir(getenv); err != nil {
		fmt.Fprintf(stderr, "%s run: %v\n", prog, err)
		return 1
	} else if from != "" {
		fmt.Fprintf(stderr, "%s run: moved config dir %s to %s (renamed binary)\n", prog, from, studiomanager.DefaultDir(getenv))
	}
	if *cfgPath == "" {
		*cfgPath = defaultConfig(getenv)
	}
	cfg, err := studiomanager.Load(*cfgPath, getenv)
	if err != nil {
		fmt.Fprintf(stderr, "%s run: %v\n", prog, err)
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
		fmt.Fprintf(stderr, "%s run: %v\n", prog, err)
		return 1
	}
	return 0
}
