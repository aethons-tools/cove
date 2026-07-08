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
