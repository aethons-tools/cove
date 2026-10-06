package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
)

// The registry verbs (intercom slice 1a-3b): users, project members, accounts
// and connections. A <user> is a name or a usr_ id.

// clientFlags adds the admin-client flags every registry verb takes and
// returns a constructor for the client they select.
func clientFlags(fs *flag.FlagSet, stderr io.Writer) func() (*adminclient.Client, error) {
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	return func() (*adminclient.Client, error) {
		if err := validateApp(*app); err != nil {
			return nil, err
		}
		adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
		return adminclient.New(adminURL, resolveToken(*app, *token, stderr)), nil
	}
}

// cmdUser manages users: `user add|list|show|rename|rm|login|oidc`.
func cmdUser(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "at-jam user: expected add|list|show|rename|rm|login|oidc")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("user "+sub, flag.ContinueOnError)
	client := clientFlags(fs, stderr)
	var logins, oidc multiFlag
	fs.Var(&logins, "login", "an admin login (OIDC sub, or \"local\" on loopback) the user signs in as (repeatable, add)")
	fs.Var(&oidc, "oidc", "OIDC identity binding `issuer:subject` (repeatable, add); the subject is the text after the final colon")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if sub != "add" && (len(logins) > 0 || len(oidc) > 0) {
		fmt.Fprintf(stderr, "at-jam user %s: --login/--oidc are for add; give the set as arguments\n", sub)
		return 2
	}
	c, err := client()
	if err != nil {
		fmt.Fprintln(stderr, "at-jam user:", err)
		return 2
	}
	usage := func(want string) int {
		fmt.Fprintf(stderr, "at-jam user %s: expected %s\n", sub, want)
		return 2
	}
	parseOIDC := func(specs []string) ([]jam.OIDCIdentity, bool) {
		var ids []jam.OIDCIdentity
		for _, o := range specs {
			id, err := jam.ParseOIDCSpec(o)
			if err != nil {
				fmt.Fprintf(stderr, "at-jam user %s: invalid oidc %q: %v\n", sub, o, err)
				return nil, false
			}
			ids = append(ids, id)
		}
		return ids, true
	}
	switch sub {
	case "add":
		if len(pos) != 1 {
			return usage("<name> [--login L]... [--oidc issuer:subject]...")
		}
		ids, ok := parseOIDC(oidc)
		if !ok {
			return 2
		}
		u, err := c.CreateUser(jam.UserBody{Name: pos[0], Logins: logins, OIDC: ids})
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added user", u.Name, u.ID)
	case "list":
		if len(pos) != 0 {
			return usage("no arguments")
		}
		users, err := c.ListUsers()
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, u := range users {
			fmt.Fprintln(stdout, userLine(u))
		}
	case "show":
		if len(pos) != 1 {
			return usage("<user>")
		}
		u, err := c.GetUser(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, userLine(u))
		for _, a := range u.Accounts {
			fmt.Fprintln(stdout, accountLine(a))
		}
	case "rename":
		if len(pos) != 2 {
			return usage("<user> <new-name>")
		}
		if err := c.RenameUser(pos[0], pos[1]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "renamed user", pos[0], "to", pos[1])
	case "rm":
		if len(pos) != 1 {
			return usage("<user>")
		}
		if err := c.RemoveUser(pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed user", pos[0])
	case "login":
		if len(pos) < 1 {
			return usage("<user> [login...] (replaces the set; none clears it)")
		}
		if err := c.SetUserLogins(pos[0], pos[1:]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "set logins of", pos[0])
	case "oidc":
		if len(pos) < 1 {
			return usage("<user> [issuer:subject...] (replaces the set; none clears it)")
		}
		ids, ok := parseOIDC(pos[1:])
		if !ok {
			return 2
		}
		if err := c.SetUserOIDC(pos[0], ids); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "set oidc bindings of", pos[0])
	default:
		fmt.Fprintln(stderr, "at-jam user: unknown subcommand", sub)
		return 2
	}
	return 0
}

func userLine(u jam.UserView) string {
	line := fmt.Sprintf("user\t%s\t%s", u.Name, u.ID)
	if u.Status == jam.StatusRemoved {
		line += "\tremoved"
	}
	for _, l := range u.Logins {
		line += "\tlogin=" + l
	}
	for _, o := range u.OIDC {
		line += "\toidc=" + o.Issuer + ":" + o.Subject
	}
	if len(u.Projects) > 0 {
		line += "\tprojects=" + strings.Join(u.Projects, ",")
	}
	return line
}

func accountLine(a jam.AccountView) string {
	line := fmt.Sprintf("account\t%s\t%s", a.ID, a.Connection)
	if a.ServiceUID != "" {
		line += "\tuid=" + a.ServiceUID
	}
	if a.Handle != "" {
		line += "\thandle=" + a.Handle
	}
	if a.Label != "" {
		line += "\tlabel=" + a.Label
	}
	if a.User != "" {
		line += "\tuser=" + a.User
	}
	return line
}

// cmdProjectMember manages a project's members: `project member add|list|rm`.
func cmdProjectMember(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "at-jam project member: expected add|list|rm")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("project member "+sub, flag.ContinueOnError)
	client := clientFlags(fs, stderr)
	var delivery multiFlag
	fs.Var(&delivery, "delivery", "per-project delivery target `service:address` (repeatable, add; replaces the member's set, none keeps it), e.g. discord:<inbox-channel-id>")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	c, err := client()
	if err != nil {
		fmt.Fprintln(stderr, "at-jam project member:", err)
		return 2
	}
	switch sub {
	case "add":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "at-jam project member add: expected <project> <user> [--delivery service:address]...")
			return 2
		}
		var profiles []jam.DeliveryProfile
		if len(delivery) == 0 { // keep an existing member's delivery
			ms, err := c.ListMembers(pos[0])
			if err != nil {
				fmt.Fprintln(stderr, "at-jam:", err)
				return 1
			}
			for _, m := range ms {
				if m.User == pos[1] || string(m.UserID) == pos[1] {
					profiles = m.Delivery
				}
			}
		}
		for _, d := range delivery {
			p, err := jam.ParseDeliverySpec(d)
			if err != nil || p.UserID != "" {
				fmt.Fprintf(stderr, "at-jam project member add: invalid --delivery %q (want service:address; bind a Discord user id with `at-jam account add`)\n", d)
				return 2
			}
			profiles = append(profiles, p)
		}
		if err := c.PutMember(pos[0], pos[1], profiles); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "set member", pos[1], "of", pos[0])
	case "list":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam project member list: expected <project>")
			return 2
		}
		ms, err := c.ListMembers(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, m := range ms {
			fmt.Fprintln(stdout, memberLine(m))
		}
	case "rm":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "at-jam project member rm: expected <project> <user>")
			return 2
		}
		if err := c.RemoveMember(pos[0], pos[1]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed member", pos[1], "from", pos[0])
	default:
		fmt.Fprintln(stderr, "at-jam project member: unknown subcommand", sub)
		return 2
	}
	return 0
}

func memberLine(m jam.MemberView) string {
	line := fmt.Sprintf("member\t%s\t%s", m.User, m.UserID)
	for _, d := range m.Delivery {
		line += "\tdelivery=" + d.Service + ":" + d.Address
	}
	return line
}

// cmdAccount manages accounts: `account list|add|link|unlink`.
func cmdAccount(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "at-jam account: expected list|add|link|unlink")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("account "+sub, flag.ContinueOnError)
	client := clientFlags(fs, stderr)
	conn := fs.String("connection", "", "connection name or id (list: filter; add: required)")
	uid := fs.String("uid", "", "the service's user id, e.g. a Discord user id (add)")
	handle := fs.String("handle", "", "the service's mention handle, e.g. a Linear @-handle (add)")
	label := fs.String("label", "", "display label (add)")
	user := fs.String("user", "", "link the account to this user (add)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	c, err := client()
	if err != nil {
		fmt.Fprintln(stderr, "at-jam account:", err)
		return 2
	}
	switch sub {
	case "list":
		accs, err := c.ListAccounts(*conn)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, a := range accs {
			fmt.Fprintln(stdout, accountLine(a))
		}
	case "add":
		if len(pos) != 0 || *conn == "" || (*uid == "" && *handle == "") {
			fmt.Fprintln(stderr, "at-jam account add: expected --connection and --uid and/or --handle [--label l] [--user u]")
			return 2
		}
		a, err := c.AddAccount(jam.AccountBody{Connection: *conn, ServiceUID: *uid, Handle: *handle, Label: *label, User: *user})
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, accountLine(a))
	case "link":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "at-jam account link: expected <account> <user>")
			return 2
		}
		if err := c.LinkAccount(pos[0], pos[1]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "linked account", pos[0], "to", pos[1])
	case "unlink":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam account unlink: expected <account>")
			return 2
		}
		if err := c.UnlinkAccount(pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "unlinked account", pos[0])
	default:
		fmt.Fprintln(stderr, "at-jam account: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdConnection manages connections — configured instances of an external
// service: `connection add|list|rename|cred|rm`.
func cmdConnection(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "at-jam connection: expected add|list|rename|cred|rm")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("connection "+sub, flag.ContinueOnError)
	client := clientFlags(fs, stderr)
	kind := fs.String("kind", "", "the service: "+strings.Join(jam.ConnectionKinds, "|")+" (add)")
	name := fs.String("name", "", "the connection's name (add)")
	cred := fs.String("cred", "", "the demanded credential (serve config) Jam uses for it (add)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	c, err := client()
	if err != nil {
		fmt.Fprintln(stderr, "at-jam connection:", err)
		return 2
	}
	usage := func(want string) int {
		fmt.Fprintf(stderr, "at-jam connection %s: expected %s\n", sub, want)
		return 2
	}
	switch sub {
	case "add":
		if len(pos) != 0 || *kind == "" || *name == "" {
			return usage("--kind and --name [--cred c]")
		}
		cn, err := c.CreateConnection(jam.ConnectionBody{Kind: *kind, Name: *name, Cred: *cred})
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, connectionLine(cn))
	case "list":
		if len(pos) != 0 {
			return usage("no arguments")
		}
		conns, err := c.ListConnections()
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, cn := range conns {
			fmt.Fprintln(stdout, connectionLine(cn))
		}
	case "rename":
		if len(pos) != 2 {
			return usage("<connection> <new-name>")
		}
		if err := c.RenameConnection(pos[0], pos[1]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "renamed connection", pos[0], "to", pos[1])
	case "cred":
		if len(pos) != 2 {
			return usage("<connection> <credential>")
		}
		if err := c.SetConnectionCred(pos[0], pos[1]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "set credential of", pos[0])
	case "rm":
		if len(pos) != 1 {
			return usage("<connection>")
		}
		if err := c.RemoveConnection(pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed connection", pos[0])
	default:
		fmt.Fprintln(stderr, "at-jam connection: unknown subcommand", sub)
		return 2
	}
	return 0
}

func connectionLine(c jam.Connection) string {
	line := fmt.Sprintf("connection\t%s\t%s\tkind=%s", c.Name, c.ID, c.Kind)
	if c.CredName != "" {
		line += "\tcred=" + c.CredName
	}
	return line
}
