package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
)

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
		fmt.Fprintln(stderr, "jam context: expected show")
		return 2
	}
	sub, rest := args[0], args[1:]
	if sub != "show" {
		fmt.Fprintf(stderr, "jam context: unknown subcommand %q (want show)\n", sub)
		return 2
	}
	fs := flag.NewFlagSet("context "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	role := fs.String("role", "", "[project/]role whose context to manage")
	project := fs.String("project", "", "project whose context (and resources) to manage")
	jamWide := fs.Bool("jam", false, "manage the Jam-wide context")
	if _, code, ok := cli.ParseFlags(fs, rest, stdout, stderr); !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam context:", err)
		return 2
	}
	scope, err := scopeOf(*role, *project, *jamWide)
	if err != nil {
		fmt.Fprintln(stderr, "jam context:", err)
		return 2
	}
	c := adminclient.New(firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL), resolveToken(*app, *token, stderr))
	b, err := c.GetContext(scope)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	out, err := jam.MarshalContextYAML(b)
	if err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	stdout.Write(out)
	return 0
}
