package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
)

// parseContextFile parses a context YAML whose leaf files are read relative to
// dir and may not escape it.
func parseContextFile(data []byte, dir string) (jam.ContextBody, error) {
	return jam.ParseContextYAML(data, func(name string) ([]byte, error) {
		if !filepath.IsLocal(name) {
			return nil, fmt.Errorf("file %q must be a relative path inside %s", name, dir)
		}
		return os.ReadFile(filepath.Join(dir, name))
	})
}

func scopeOf(role, project string, jamWide bool) (adminclient.ContextScope, error) {
	n := 0
	for _, set := range []bool{role != "", project != "", jamWide} {
		if set {
			n++
		}
	}
	if n != 1 {
		return adminclient.ContextScope{}, errors.New("pass exactly one of --role [project/]role, --project p, --jam")
	}
	switch {
	case jamWide:
		return adminclient.ContextScope{Jam: true}, nil
	case project != "":
		return adminclient.ContextScope{Project: project}, nil
	}
	p, r, ok := strings.Cut(role, "/")
	if !ok {
		p, r = jam.DefaultProject, role
	}
	return adminclient.ContextScope{Project: p, Role: r}, nil
}

func cmdContext(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-jam context: expected show|set|clear")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("context "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	role := fs.String("role", "", "[project/]role whose context to manage")
	project := fs.String("project", "", "project whose context (and resources) to manage")
	jamWide := fs.Bool("jam", false, "manage the Jam-wide context")
	file := fs.String("file", "", "context YAML (set only; - = stdin)")
	if _, code, ok := cli.ParseFlags(fs, rest, stdout, stderr); !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam context:", err)
		return 2
	}
	scope, err := scopeOf(*role, *project, *jamWide)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam context:", err)
		return 2
	}
	c := adminclient.New(firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL), resolveToken(*app, *token, stderr))
	switch sub {
	case "show":
		b, err := c.GetContext(scope)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		out, err := jam.MarshalContextYAML(b)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		stdout.Write(out)
	case "set":
		if *file == "" {
			fmt.Fprintln(stderr, "at-jam context set: --file is required")
			return 2
		}
		data, err := readConfig(*file)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		dir := "."
		if *file != "-" {
			dir = filepath.Dir(*file)
		}
		b, err := parseContextFile(data, dir)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam context set:", err)
			return 1
		}
		if err := c.SetContext(scope, b); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "context set")
	case "clear":
		if err := c.ClearContext(scope); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "context cleared")
	default:
		fmt.Fprintf(stderr, "at-jam context: unknown subcommand %q (want show|set|clear)\n", sub)
		return 2
	}
	return 0
}
