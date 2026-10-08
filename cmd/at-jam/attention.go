package main

import (
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
)

// cmdAttention lists operator-attention conditions via the admin API.
func cmdAttention(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(stderr, "at-jam attention: expected list")
		return 2
	}
	fs := flag.NewFlagSet("attention list", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token for an OIDC-gated admin API (env: AT_JAM_ADMIN_TOKEN)")
	all := fs.Bool("all", false, "include conditions resolved in the last 7 days")
	if _, code, ok := cli.ParseFlags(fs, args[1:], stdout, stderr); !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam attention:", err)
		return 2
	}
	c := adminclient.New(firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL), resolveToken(*app, *token, stderr))
	state := "open"
	if *all {
		state = "all"
	}
	cs, err := c.ListConditions(state)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	if len(cs) == 0 {
		fmt.Fprintln(stdout, "nothing needs attention")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SEV\tKEY\tSINCE\tSUMMARY")
	for _, k := range cs {
		sev := string(k.Severity)
		if !k.IsOpen() {
			sev = "resolved"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", sev, k.Key, k.Since.Local().Format("2006-01-02 15:04"), k.Summary)
		if k.Fix != "" && k.IsOpen() {
			fmt.Fprintf(tw, "\t\t\tfix: %s\n", k.Fix)
		}
	}
	_ = tw.Flush()
	return 0
}
