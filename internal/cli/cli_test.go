package cli

import (
	"bytes"
	"flag"
	"io"
	"strings"
	"testing"
)

func testApp(rec *string) App {
	return App{
		Name: "tool", Version: "1.2.3",
		Commands: []Command{
			{Name: "greet", Brief: "say hi", Run: func(args []string, g Globals, stdout, stderr io.Writer) int {
				*rec = strings.Join(args, ",") + "|dry=" + map[bool]string{true: "1", false: "0"}[g.DryRun]
				return 0
			}},
		},
	}
}

func TestAppDispatchesWithGlobals(t *testing.T) {
	var rec string
	var out, errOut bytes.Buffer
	code := testApp(&rec).Run([]string{"--dry-run", "greet", "a", "--x"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if rec != "a,--x|dry=1" {
		t.Fatalf("command got %q", rec)
	}
}

func TestAppVersionAndHelp(t *testing.T) {
	var rec string
	app := testApp(&rec)
	var out, errOut bytes.Buffer
	if app.Run([]string{"--version"}, &out, &errOut); !strings.Contains(out.String(), "tool 1.2.3") {
		t.Fatalf("--version = %q", out.String())
	}
	out.Reset()
	if app.Run([]string{"version"}, &out, &errOut); !strings.Contains(out.String(), "tool 1.2.3") {
		t.Fatalf("version cmd = %q", out.String())
	}
	out.Reset()
	if code := app.Run([]string{"help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "greet") {
		t.Fatalf("help code=%d out=%q", code, out.String())
	}
}

func TestAppUnknownCommandAndNoArgs(t *testing.T) {
	var rec string
	app := testApp(&rec)
	var out, errOut bytes.Buffer
	if code := app.Run([]string{"nope"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("unknown: code=%d err=%q", code, errOut.String())
	}
	errOut.Reset()
	if code := app.Run(nil, &out, &errOut); code != 2 {
		t.Fatalf("no args code=%d", code)
	}
}

func TestAppDashHelpToStdout(t *testing.T) {
	var rec string
	app := testApp(&rec)
	for _, f := range []string{"-h", "--help"} {
		var out, errOut bytes.Buffer
		code := app.Run([]string{f}, &out, &errOut)
		if code != 0 {
			t.Fatalf("%s: code=%d", f, code)
		}
		if !strings.Contains(out.String(), "greet") {
			t.Fatalf("%s: usage not on stdout: out=%q err=%q", f, out.String(), errOut.String())
		}
	}
}

func TestAppUnknownGlobalFlag(t *testing.T) {
	var rec string
	app := testApp(&rec)
	var out, errOut bytes.Buffer
	code := app.Run([]string{"--nope"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("--nope: code=%d want 2", code)
	}
	if !strings.Contains(errOut.String(), "nope") {
		t.Fatalf("--nope: error message not on stderr: %q", errOut.String())
	}
}

// TestGlobalsParseLogFlags mirrors TestAppDispatchesWithGlobals but for the
// new --log-mode/--log-level/--no-log-file globals: since there is no
// standalone ParseGlobals entry point (globals are parsed inline in
// App.Run), a probe command captures the Globals it receives.
func TestGlobalsParseLogFlags(t *testing.T) {
	var got Globals
	app := App{
		Name: "tool", Version: "1.2.3",
		Commands: []Command{
			{Name: "probe", Brief: "capture globals", Run: func(args []string, g Globals, stdout, stderr io.Writer) int {
				got = g
				return 0
			}},
		},
	}
	var out, errOut bytes.Buffer
	code := app.Run([]string{"--log-mode", "unattended", "--log-level", "debug", "--no-log-file", "probe"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if got.LogMode != "unattended" || got.LogLevel != "debug" || !got.NoLogFile {
		t.Fatalf("globals not parsed: %+v", got)
	}
}

// TestGlobalsLogLevelDefaultsEmpty guards the AT_LOG_LEVEL env fallback on the
// dispatch path (cmd/at-cove's logging.EnvOr(g.LogLevel, "AT_LOG_LEVEL")): envOr only
// consults the environment when the flag is at its zero value, so
// --log-level's flag.String default must be "" (not "info") or the env var
// is silently ignored. logging.LevelFrom("") still maps to slog.LevelInfo, so the
// effective default is unchanged; only the zero value matters here.
func TestGlobalsLogLevelDefaultsEmpty(t *testing.T) {
	var got Globals
	app := App{
		Name: "tool", Version: "1.2.3",
		Commands: []Command{
			{Name: "probe", Brief: "capture globals", Run: func(args []string, g Globals, stdout, stderr io.Writer) int {
				got = g
				return 0
			}},
		},
	}
	var out, errOut bytes.Buffer
	code := app.Run([]string{"probe"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if got.LogLevel != "" {
		t.Fatalf("g.LogLevel = %q, want %q so logging.EnvOr(g.LogLevel, \"AT_LOG_LEVEL\") falls through to the environment", got.LogLevel, "")
	}
}

func TestParseInterspersed(t *testing.T) {
	for _, args := range [][]string{
		{"./kit", "--raw"},
		{"--raw", "./kit"},
	} {
		fs := flag.NewFlagSet("connect", flag.ContinueOnError)
		raw := fs.Bool("raw", false, "")
		pos, err := ParseInterspersed(fs, args)
		if err != nil {
			t.Fatalf("args %v: %v", args, err)
		}
		if !*raw || len(pos) != 1 || pos[0] != "./kit" {
			t.Fatalf("args %v -> raw=%v pos=%v", args, *raw, pos)
		}
	}
	// unknown flag errors
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	if _, err := ParseInterspersed(fs, []string{"--nope"}); err == nil {
		t.Fatal("expected error for unknown flag")
	}
}

// TestParseFlags covers the shared subcommand parse convention (COV-94): -h/--help
// prints usage to stdout and signals exit 0; a bad flag prints to stderr and
// signals exit 2; a clean parse returns the positionals with ok=true.
func TestParseFlags(t *testing.T) {
	newFS := func() *flag.FlagSet {
		fs := flag.NewFlagSet("work", flag.ContinueOnError)
		fs.Bool("reap", false, "scavenge orphans")
		return fs
	}

	for _, f := range []string{"-h", "--help"} {
		var out, errOut bytes.Buffer
		pos, code, ok := ParseFlags(newFS(), []string{f}, &out, &errOut)
		if ok || code != 0 {
			t.Fatalf("%s: ok=%v code=%d, want ok=false code=0", f, ok, code)
		}
		if pos != nil || !strings.Contains(out.String(), "-reap") || errOut.Len() != 0 {
			t.Fatalf("%s: usage must go to stdout, not stderr; out=%q err=%q", f, out.String(), errOut.String())
		}
	}

	// A bad flag: usage error to stderr, exit 2.
	var out, errOut bytes.Buffer
	if _, code, ok := ParseFlags(newFS(), []string{"--nope"}, &out, &errOut); ok || code != 2 {
		t.Fatalf("bad flag: ok=%v code=%d, want ok=false code=2", ok, code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "work:") {
		t.Fatalf("bad flag: error must go to stderr; out=%q err=%q", out.String(), errOut.String())
	}

	// Clean parse: positionals returned, ok=true.
	if pos, code, ok := ParseFlags(newFS(), []string{"--reap", "extra"}, &bytes.Buffer{}, &bytes.Buffer{}); !ok || code != 0 || len(pos) != 1 || pos[0] != "extra" {
		t.Fatalf("clean parse: pos=%v code=%d ok=%v", pos, code, ok)
	}
}
