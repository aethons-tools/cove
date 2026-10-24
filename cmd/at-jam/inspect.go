package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
)

// inspectFlags are the flags role inspect and studio inspect share.
type inspectFlags struct {
	app, adminURL, token *string
	json                 *bool
	sections             multiFlag
}

func addInspectFlags(fs *flag.FlagSet, stderr io.Writer) *inspectFlags {
	f := &inspectFlags{
		app:      fs.String("app", defaultApp, "settings/token profile"),
		adminURL: fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)"),
		token:    fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)"),
		json:     fs.Bool("json", false, "print the setup as JSON"),
	}
	fs.Var(&f.sections, "section", "only these sections (repeatable or comma-separated): "+strings.Join(jam.SetupSectionNames(), ", "))
	return f
}

// client validates the shared flags and returns the admin client and the
// section list (nil = all); code != 0 means stop with it.
func (f *inspectFlags) client(verb string, stderr io.Writer) (*adminclient.Client, []string, int) {
	if err := validateApp(*f.app); err != nil {
		fmt.Fprintf(stderr, "at-jam %s: %v\n", verb, err)
		return nil, nil, 2
	}
	var sections []string
	if len(f.sections) > 0 {
		s, err := jam.ParseSetupSections(strings.Join(f.sections, ","))
		if err != nil {
			fmt.Fprintf(stderr, "at-jam %s: %v\n", verb, err)
			return nil, nil, 2
		}
		sections = s
	}
	adminURL := firstNonEmpty(*f.adminURL, loadSettings(*f.app).AdminURL, defaultAdminURL)
	return adminclient.New(adminURL, resolveToken(*f.app, *f.token, stderr)), sections, 0
}

func (f *inspectFlags) print(s jam.StudioSetup, stdout io.Writer) {
	if *f.json {
		b, _ := json.MarshalIndent(s, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return
	}
	jam.RenderSetupText(stdout, s)
}

// cmdRoleInspect is `at-jam role inspect <project>/<role>`: what a studio of
// the role raised now would get (a bare <role> is in the default project).
func cmdRoleInspect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("role inspect", flag.ContinueOnError)
	f := addInspectFlags(fs, stderr)
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) != 1 || pos[0] == "" {
		fmt.Fprintln(stderr, "usage: at-jam role inspect <project>/<role> [--json] [--section name]…")
		return 2
	}
	project, role, found := strings.Cut(pos[0], "/")
	if !found {
		project, role = jam.DefaultProject, pos[0]
	}
	c, sections, code := f.client("role inspect", stderr)
	if code != 0 {
		return code
	}
	s, err := c.RoleSetup(project, role, sections)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	f.print(s, stdout)
	return 0
}

// cmdStudioInspect is `at-jam studio inspect <studio> [--diff]`: what the
// studio got, optionally diffed against a raise of its role now.
func cmdStudioInspect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("studio inspect", flag.ContinueOnError)
	f := addInspectFlags(fs, stderr)
	diff := fs.Bool("diff", false, "add each section's line diff against what a raise of its role would get now")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) != 1 || pos[0] == "" {
		fmt.Fprintln(stderr, "usage: at-jam studio inspect <studio> [--diff] [--json] [--section name]…")
		return 2
	}
	c, sections, code := f.client("studio inspect", stderr)
	if code != 0 {
		return code
	}
	s, err := c.StudioSetup(pos[0], sections, *diff)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	f.print(s, stdout)
	return 0
}
