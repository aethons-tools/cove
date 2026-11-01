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
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: JAM_ADMIN_TOKEN)")
	return func() (*adminclient.Client, error) {
		if err := validateApp(*app); err != nil {
			return nil, err
		}
		adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
		return adminclient.New(adminURL, resolveToken(*app, *token, stderr)), nil
	}
}

// cmdUser manages users: `user list|show|oidc`.
func cmdUser(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "jam user: expected list|show|oidc")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("user "+sub, flag.ContinueOnError)
	client := clientFlags(fs, stderr)
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	c, err := client()
	if err != nil {
		fmt.Fprintln(stderr, "jam user:", err)
		return 2
	}
	usage := func(want string) int {
		fmt.Fprintf(stderr, "jam user %s: expected %s\n", sub, want)
		return 2
	}
	parseOIDC := func(specs []string) ([]jam.OIDCIdentity, bool) {
		var ids []jam.OIDCIdentity
		for _, o := range specs {
			id, err := jam.ParseOIDCSpec(o)
			if err != nil {
				fmt.Fprintf(stderr, "jam user %s: invalid oidc %q: %v\n", sub, o, err)
				return nil, false
			}
			ids = append(ids, id)
		}
		return ids, true
	}
	switch sub {
	case "list":
		if len(pos) != 0 {
			return usage("no arguments")
		}
		users, err := c.ListUsers()
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
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
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, userLine(u))
		for _, a := range u.Accounts {
			fmt.Fprintln(stdout, accountLine(a))
		}
	case "oidc":
		if len(pos) < 1 {
			return usage("<user> [issuer:subject...] (replaces the set; none clears it)")
		}
		ids, ok := parseOIDC(pos[1:])
		if !ok {
			return 2
		}
		if err := c.SetUserOIDC(pos[0], ids); err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "set oidc bindings of", pos[0])
	default:
		fmt.Fprintln(stderr, "jam user: unknown subcommand", sub)
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

// cmdProjectMember manages a project's members: `project member list`.
func cmdProjectMember(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "jam project member: expected list")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("project member "+sub, flag.ContinueOnError)
	client := clientFlags(fs, stderr)
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	c, err := client()
	if err != nil {
		fmt.Fprintln(stderr, "jam project member:", err)
		return 2
	}
	switch sub {
	case "list":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam project member list: expected <project>")
			return 2
		}
		ms, err := c.ListMembers(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		for _, m := range ms {
			fmt.Fprintln(stdout, memberLine(m))
		}
	default:
		fmt.Fprintln(stderr, "jam project member: unknown subcommand", sub)
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
		fmt.Fprintln(stderr, "jam account: expected list|add|link|unlink")
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
		fmt.Fprintln(stderr, "jam account:", err)
		return 2
	}
	switch sub {
	case "list":
		accs, err := c.ListAccounts(*conn)
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		for _, a := range accs {
			fmt.Fprintln(stdout, accountLine(a))
		}
	case "add":
		if len(pos) != 0 || *conn == "" || (*uid == "" && *handle == "") {
			fmt.Fprintln(stderr, "jam account add: expected --connection and --uid and/or --handle [--label l] [--user u]")
			return 2
		}
		a, err := c.AddAccount(jam.AccountBody{Connection: *conn, ServiceUID: *uid, Handle: *handle, Label: *label, User: *user})
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, accountLine(a))
	case "link":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "jam account link: expected <account> <user>")
			return 2
		}
		if err := c.LinkAccount(pos[0], pos[1]); err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "linked account", pos[0], "to", pos[1])
	case "unlink":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam account unlink: expected <account>")
			return 2
		}
		if err := c.UnlinkAccount(pos[0]); err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "unlinked account", pos[0])
	default:
		fmt.Fprintln(stderr, "jam account: unknown subcommand", sub)
		return 2
	}
	return 0
}

// cmdConnection manages connections — configured instances of an external
// service: `connection list`.
func cmdConnection(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "jam connection: expected list")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("connection "+sub, flag.ContinueOnError)
	client := clientFlags(fs, stderr)
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	c, err := client()
	if err != nil {
		fmt.Fprintln(stderr, "jam connection:", err)
		return 2
	}
	usage := func(want string) int {
		fmt.Fprintf(stderr, "jam connection %s: expected %s\n", sub, want)
		return 2
	}
	switch sub {
	case "list":
		if len(pos) != 0 {
			return usage("no arguments")
		}
		conns, err := c.ListConnections()
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		for _, cn := range conns {
			fmt.Fprintln(stdout, connectionLine(cn))
		}
	default:
		fmt.Fprintln(stderr, "jam connection: unknown subcommand", sub)
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

// cmdRoom manages a project's rooms: `room add|list|rename|rm`.
func cmdRoom(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "jam room: expected add|list|rename|rm")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("room "+sub, flag.ContinueOnError)
	client := clientFlags(fs, stderr)
	conn := fs.String("connection", "", "the connection the ref is on: a name, id or kind (add; default: the tracker connection)")
	ref := fs.String("ref", "", "the surface the room posts to and hears from: a Linear issue key or a Discord channel id (add)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	usage := func(want string) int {
		fmt.Fprintf(stderr, "jam room %s: expected %s\n", sub, want)
		return 2
	}
	switch sub {
	case "add":
		if len(pos) != 2 || *ref == "" {
			return usage("<project> <name> --ref r [--connection c]")
		}
	case "list":
		if len(pos) != 1 {
			return usage("<project>")
		}
	case "rename":
		if len(pos) != 3 {
			return usage("<project> <room> <new-name>")
		}
	case "rm":
		if len(pos) != 2 {
			return usage("<project> <room>")
		}
	default:
		fmt.Fprintln(stderr, "jam room: unknown subcommand", sub)
		return 2
	}
	c, err := client()
	if err != nil {
		fmt.Fprintln(stderr, "jam room:", err)
		return 2
	}
	switch sub {
	case "add":
		v, err := c.PutRoom(pos[0], jam.RoomBody{Name: pos[1], Connection: *conn, Ref: *ref})
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, roomLine(v))
	case "list":
		rooms, err := c.ListRooms(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		for _, v := range rooms {
			fmt.Fprintln(stdout, roomLine(v))
		}
	case "rename":
		if err := c.RenameRoom(pos[0], pos[1], pos[2]); err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "renamed room", pos[1], "to", pos[2])
	case "rm":
		if err := c.RemoveRoom(pos[0], pos[1]); err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed room", pos[1])
	}
	return 0
}

// roomLine renders a room: name, id, connection, ref, and whether it only
// posts there (another channel receives that ref's replies).
func roomLine(v jam.RoomView) string {
	line := fmt.Sprintf("room\t%s\t%s\t%s\tref=%s", v.Name, v.ID, v.Connection, v.Ref)
	if v.PostOnly {
		line += "\tpost-only"
	}
	return line
}
