package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
)

// cmdManager manages Studio Managers via the admin API. add and rotate print
// the manager's token, which Jam shows only once.
func cmdManager(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	const usage = "jam manager: expected add <name> | list | rotate <name> | revoke <name> [--force]"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("manager "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token for an OIDC-gated admin API (env: JAM_ADMIN_TOKEN)")
	force := fs.Bool("force", false, "revoke: forget the manager's studios too")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "jam manager:", err)
		return 2
	}
	oneName := func() (string, bool) {
		if len(pos) != 1 {
			fmt.Fprintf(stderr, "jam manager %s: expected one manager name\n", sub)
			return "", false
		}
		return pos[0], true
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	fail := func(err error) int {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	}
	switch sub {
	case "add", "rotate":
		name, ok := oneName()
		if !ok {
			return 2
		}
		var err error
		var tok string
		if sub == "add" {
			t, e := c.AddManager(name)
			tok, err = t.Token, e
		} else {
			t, e := c.RotateManager(name)
			tok, err = t.Token, e
		}
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "manager %s token (shown once): %s\n", name, tok)
		fmt.Fprintln(stdout, "put it in the manager's config; Jam stores only its hash")
	case "list":
		ms, err := c.Managers()
		if err != nil {
			return fail(err)
		}
		for _, m := range ms {
			seen := "-"
			if !m.LastSeen.IsZero() {
				seen = m.LastSeen.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(stdout, "%s\tconnected=%t\tversion=%s\tstudios=%d\tcapacity=%d\tlast-seen=%s\tstrays=%d\n",
				m.Name, m.Connected, m.Version, m.Studios, m.Capacity, seen, len(m.Strays))
		}
	case "revoke":
		name, ok := oneName()
		if !ok {
			return 2
		}
		if err := c.RevokeManager(name, *force); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "revoked", name)
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	return 0
}
