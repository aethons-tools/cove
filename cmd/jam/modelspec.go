package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"gopkg.in/yaml.v3"
)

// cmdModelSpec reads model-specs via the admin API: show prints one as YAML,
// list prints a one-line summary per spec. Model-specs change only in git.
func cmdModelSpec(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	const usage = "jam model-spec: expected show <name> | list"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sub, rest := args[0], args[1:]
	if sub != "list" && sub != "show" {
		fmt.Fprintf(stderr, "jam model-spec: unknown subcommand %q (want list|show)\n", sub)
		return 2
	}
	fs := flag.NewFlagSet("model-spec "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token for an OIDC-gated admin API (env: JAM_ADMIN_TOKEN)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam model-spec:", err)
		return 2
	}
	oneArg := func(what string) (string, bool) {
		if len(pos) != 1 {
			fmt.Fprintf(stderr, "jam model-spec %s: expected one %s\n", sub, what)
			return "", false
		}
		return pos[0], true
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "list":
		specs, err := c.ListModelSpecs()
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		for _, m := range specs {
			provider := ""
			if m.Claude != nil {
				provider = m.Claude.Provider
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", m.Name, m.Type, m.Version, m.Principal.Credential, provider)
		}
	case "show":
		name, ok := oneArg("model-spec name")
		if !ok {
			return 2
		}
		m, err := c.GetModelSpec(name)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		out, err := yaml.Marshal(m)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		stdout.Write(out)
	default:
		fmt.Fprintf(stderr, "jam model-spec: unknown subcommand %q (want list|show)\n", sub)
		return 2
	}
	return 0
}
