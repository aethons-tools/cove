// Package cli is a tiny zero-dependency command registry shared by the cove
// binaries. Global flags (--dry-run, --version) come before the command; each
// command owns its own flags, after the command name.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
)

// Globals are the cross-cutting flags parsed before the command name.
type Globals struct {
	DryRun bool
	// LogMode selects the logging mode: "" (auto-detect), "attended", or
	// "unattended". Mapped to logging.Mode by logging.ModeFrom.
	LogMode string
	// LogLevel is the minimum level shown on stderr: "debug", "info", "warn",
	// or "error" (default "info"). Mapped to slog.Level by logging.LevelFrom.
	LogLevel string
	// NoLogFile disables the JSON debug-level log file sink.
	NoLogFile bool
}

// Command is one subcommand. Run receives the tokens after the command name
// (its flags + positionals, in any order), the parsed globals, and the writers;
// it returns the process exit code.
type Command struct {
	Name  string
	Brief string
	Run   func(args []string, g Globals, stdout, stderr io.Writer) int
}

// App is a registry + dispatcher for one binary.
type App struct {
	Name     string
	Version  string
	Commands []Command
}

// Run parses leading globals, handles version/help, then dispatches to a command.
func (a App) Run(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(a.Name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	dry := fs.Bool("dry-run", false, "print planned actions without executing")
	logMode := fs.String("log-mode", "", `logging mode: "attended" or "unattended" (default: auto-detect)`)
	logLevel := fs.String("log-level", "", "debug|info|warn|error (default info)")
	noLogFile := fs.Bool("no-log-file", false, "disable the JSON debug-level log file")
	ver := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(argv); err != nil {
		if err == flag.ErrHelp { // -h / --help
			a.usage(stdout)
			return 0
		}
		fmt.Fprintf(stderr, "%s: %v\n\n", a.Name, err)
		a.usage(stderr)
		return 2
	}
	if *ver {
		fmt.Fprintln(stdout, a.Name+" "+a.Version)
		return 0
	}
	rest := fs.Args()
	if len(rest) == 0 {
		a.usage(stderr)
		return 2
	}
	name, cmdArgs := rest[0], rest[1:]
	switch name {
	case "version":
		fmt.Fprintln(stdout, a.Name+" "+a.Version)
		return 0
	case "help":
		a.usage(stdout)
		return 0
	}
	for _, c := range a.Commands {
		if c.Name == name {
			return c.Run(cmdArgs, Globals{DryRun: *dry, LogMode: *logMode, LogLevel: *logLevel, NoLogFile: *noLogFile}, stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "%s: unknown command %q\n\n", a.Name, name)
	a.usage(stderr)
	return 2
}

func (a App) usage(w io.Writer) {
	fmt.Fprintf(w, "usage: %s [--dry-run] [--log-mode attended|unattended] [--log-level debug|info|warn|error] [--no-log-file] <command> [flags] [args]\n\ncommands:\n", a.Name)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range a.Commands {
		fmt.Fprintf(tw, "  %s\t%s\n", c.Name, c.Brief)
	}
	tw.Flush()
}

// ParseInterspersed parses fs against args allowing flags and positionals in any
// order (the "Parse in a loop" idiom), returning the collected positionals. A flag
// error (unknown flag, bad value) returns a non-nil error; callers should exit 2.
func ParseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positionals, nil
		}
		positionals = append(positionals, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// ParseFlags parses a subcommand's own flags (after the command name), applying
// the shared CLI convention so every subcommand matches the top-level help/usage
// behavior:
//
//   - -h/--help   → prints the flagset's usage to stdout; ok=false, code 0
//   - parse error → prints "<cmd>: <err>" + usage to stderr; ok=false, code 2
//   - success     → returns the positionals; ok=true, code 0
//
// It owns the flagset's output routing, so callers need not call fs.SetOutput.
// The pattern at each site is: `pos, code, ok := cli.ParseFlags(...); if !ok {
// return code }`.
func ParseFlags(fs *flag.FlagSet, args []string, stdout, stderr io.Writer) (pos []string, code int, ok bool) {
	fs.SetOutput(io.Discard) // we route usage/errors ourselves, to the right stream
	fs.Usage = func() {}
	pos, err := ParseInterspersed(fs, args)
	if err == nil {
		return pos, 0, true
	}
	if errors.Is(err, flag.ErrHelp) { // -h / --help: usage to stdout, exit 0
		fmt.Fprintf(stdout, "Usage of %s:\n", fs.Name())
		fs.SetOutput(stdout)
		fs.PrintDefaults()
		return nil, 0, false
	}
	fmt.Fprintf(stderr, "%s: %v\n", fs.Name(), err) // bad flag/value: usage to stderr, exit 2
	fs.SetOutput(stderr)
	fs.PrintDefaults()
	return nil, 2, false
}
