package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"github.com/aethons-tools/cove/internal/jam/vscodeext"
)

// Seams for tests.
var (
	vscodeLookPath = exec.LookPath
	vscodeRun      = func(name string, args []string, stdout, stderr io.Writer) error {
		c := exec.Command(name, args...)
		c.Stdout, c.Stderr = stdout, stderr
		return c.Run()
	}
	vscodeGOOS = runtime.GOOS
)

// macCodeCLI is where VS Code keeps its `code` CLI on macOS when it was never
// put on PATH ("Shell Command: Install 'code' command in PATH").
const macCodeCLI = "/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code"

// vsixDir is where `jam vscode install` keeps the downloaded .vsix.
func vsixDir() string { return filepath.Join(configDir(), "vscode") }

func cmdVSCode(args []string, g cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "install" {
		fmt.Fprintln(stderr, "usage: jam vscode install [--code PATH] [--participant|--operator]")
		return 2
	}
	return cmdVSCodeInstall(args[1:], stdout, stderr)
}

// cmdVSCodeInstall downloads the VS Code extension the Jam serves (the one it
// was released with) and installs it with `code --install-extension`.
// Installed once, the extension offers its own updates.
func cmdVSCodeInstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vscode install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	f := addSSHFlags(fs)
	code := fs.String("code", "", "the VS Code CLI to install with (default: code on PATH; e.g. code-insiders)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, "Usage of vscode install: jam vscode install [flags]")
			fs.SetOutput(stdout)
			fs.PrintDefaults()
			return 0
		}
		fmt.Fprintln(stderr, "vscode install:", err)
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: jam vscode install [--code PATH] [--participant|--operator]")
		return 2
	}
	t, usage, err := f.resolveJam(stderr)
	if err != nil {
		fmt.Fprintln(stderr, "jam vscode install:", err)
		if usage {
			return 2
		}
		return 1
	}
	c := adminclient.New(t.adminURL, t.token)
	available, version, err := c.VSCodeExtension(t.prefix())
	if err != nil {
		fmt.Fprintf(stderr, "jam vscode install: %s\n", vscodeErrMessage(err, t))
		return 1
	}
	if !available {
		fmt.Fprintln(stderr, "jam vscode install: this Jam was built without the VS Code extension; install jam-vscode_<version>.vsix from its release instead (docs/usage/jam/vscode.md)")
		return 1
	}
	data, err := c.DownloadVSCodeExtension(t.prefix())
	if err != nil {
		fmt.Fprintf(stderr, "jam vscode install: downloading: %s\n", vscodeErrMessage(err, t))
		return 1
	}
	b, err := vscodeext.Parse(data)
	if err != nil {
		fmt.Fprintf(stderr, "jam vscode install: Jam sent an unreadable .vsix: %v\n", err)
		return 1
	}
	if b.Version() != version {
		fmt.Fprintf(stderr, "jam vscode install: Jam sent extension %s, not the %s it advertised; try again\n", b.Version(), version)
		return 1
	}
	if err := os.MkdirAll(vsixDir(), 0o700); err != nil {
		fmt.Fprintln(stderr, "jam vscode install:", err)
		return 1
	}
	file := filepath.Join(vsixDir(), "jam-vscode_"+version+".vsix")
	if err := writeFileReplace(file, data, 0o644); err != nil {
		fmt.Fprintln(stderr, "jam vscode install:", err)
		return 1
	}
	cli, ok := findCodeCLI(*code)
	if !ok {
		fmt.Fprintf(stderr, "jam vscode install: downloaded %s, but found no VS Code CLI (%s).\n", file, codeCLIHint(*code))
		fmt.Fprintln(stderr, "Install it from VS Code instead: Extensions: Install from VSIX…, and pick that file.")
		return 1
	}
	fmt.Fprintf(stdout, "Installing the Jam extension %s with %s…\n", version, cli)
	if err := vscodeRun(cli, []string{"--install-extension", file, "--force"}, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "jam vscode install: %s --install-extension failed: %v (the .vsix is at %s)\n", cli, err, file)
		return 1
	}
	fmt.Fprintf(stdout, "Installed the Jam extension %s. Reload any open VS Code windows, then run Jam: Sign In.\n", version)
	return 0
}

// vscodeErrMessage turns a failed /vscode request into one line for a
// person: sign-in problems as for SSH, a 404 as a Jam that predates serving
// the extension.
func vscodeErrMessage(err error, t sshTarget) string {
	se, ok := errors.AsType[*adminclient.StatusError](err)
	switch {
	case !ok:
		return err.Error()
	case se.Code == http.StatusUnauthorized:
		return sshErrMessage(err, t)
	case se.Code == http.StatusNotFound:
		return "this Jam doesn't serve the VS Code extension (it predates it); install jam-vscode_<version>.vsix from a release instead (docs/usage/jam/vscode.md)"
	}
	return fmt.Sprintf("Jam refused (%d): %s", se.Code, se.Body)
}

// findCodeCLI is the VS Code CLI to use: --code as given (a path or a name on
// PATH), else code on PATH, else, on macOS, the one inside the app bundle.
func findCodeCLI(flagVal string) (string, bool) {
	if flagVal != "" {
		p, err := vscodeLookPath(flagVal)
		return p, err == nil
	}
	if p, err := vscodeLookPath("code"); err == nil {
		return p, true
	}
	if vscodeGOOS == "darwin" {
		if p, err := vscodeLookPath(macCodeCLI); err == nil {
			return p, true
		}
	}
	return "", false
}

func codeCLIHint(flagVal string) string {
	if flagVal != "" {
		return fmt.Sprintf("--code %q is not runnable", flagVal)
	}
	return "no code on PATH; pass --code, or in VS Code run Shell Command: Install 'code' command in PATH"
}
