package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
)

// cmdStudioShare runs `at-jam studio share|unshare|access <studio>` on the
// plane the ssh commands pick (--participant / --operator; default the
// participant login when cached, else the operator).
func cmdStudioShare(sub string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("studio "+sub, flag.ContinueOnError)
	f := addSSHFlags(fs)
	var with, rights, dur *string
	if sub != "access" {
		with = fs.String("with", "", "who: user:<name>")
	}
	if sub == "share" {
		rights = fs.String("rights", "", "comma-separated rights to grant: preview, ssh")
		dur = fs.String("for", "", "how long: a duration (4h, 90m) or days (7d); default 8h, at most 7d")
	}
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	usage := map[string]string{
		"share":   "usage: at-jam studio share <studio> --with user:<name> --rights preview,ssh [--for 8h] [--participant|--operator]",
		"unshare": "usage: at-jam studio unshare <studio> --with user:<name> [--participant|--operator]",
		"access":  "usage: at-jam studio access <studio> [--participant|--operator]",
	}[sub]
	if len(pos) != 1 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	var user string
	if with != nil {
		name, isUser := strings.CutPrefix(*with, "user:")
		if !isUser || name == "" {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		user = name
	}
	var rightList []string
	if rights != nil {
		for r := range strings.SplitSeq(*rights, ",") {
			if r = strings.TrimSpace(r); r != "" {
				rightList = append(rightList, r)
			}
		}
		if len(rightList) == 0 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
	}
	t, isUsage, err := f.resolve(pos[0], stderr)
	if err != nil {
		fmt.Fprintf(stderr, "at-jam studio %s: %v\n", sub, err)
		if isUsage {
			return 2
		}
		return 1
	}
	c := adminclient.New(t.adminURL, t.token)
	switch sub {
	case "share":
		g, err := c.ShareStudio(t.prefix(), t.studio, adminclient.ShareParams{User: user, Rights: rightList, For: *dur})
		if err != nil {
			fmt.Fprintf(stderr, "at-jam studio share: %s\n", shareErrMessage(err, t))
			return 1
		}
		fmt.Fprintf(stdout, "shared %s with %s: %s until %s\n", t.studio, g.User, strings.Join(g.Rights, ","), g.ExpiresAt.UTC().Format(time.RFC3339))
	case "unshare":
		if err := c.UnshareStudio(t.prefix(), t.studio, user); err != nil {
			fmt.Fprintf(stderr, "at-jam studio unshare: %s\n", shareErrMessage(err, t))
			return 1
		}
		fmt.Fprintf(stdout, "unshared %s from %s\n", t.studio, user)
	case "access":
		v, err := c.StudioAccess(t.prefix(), t.studio)
		if err != nil {
			fmt.Fprintf(stderr, "at-jam studio access: %s\n", shareErrMessage(err, t))
			return 1
		}
		fmt.Fprintf(stdout, "operators\talways\n")
		if v.Requester != nil {
			fmt.Fprintf(stdout, "requester\t%s\tpreview,ssh\n", v.Requester.Name)
		}
		fmt.Fprintf(stdout, "role %s/%s\tpreview=%s\tssh=%s\t(project members)\n", v.Role.Project, v.Role.Role, v.Role.Preview, v.Role.SSH)
		for _, g := range v.Grants {
			state := "until " + g.ExpiresAt.UTC().Format(time.RFC3339)
			if g.Expired {
				state = "expired " + g.ExpiresAt.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(stdout, "grant\t%s\t%s\t%s\tby=%s\n", g.User, strings.Join(g.Rights, ","), state, g.GrantedByName)
		}
		if len(v.Live) == 0 {
			fmt.Fprintln(stdout, "live\tnone")
		}
		for _, l := range v.Live {
			fmt.Fprintf(stdout, "live\t%s\t%s\tsince %s\n", l.Name, l.User, l.Since.UTC().Format(time.RFC3339))
		}
	}
	return 0
}

// shareErrMessage turns a sharing failure into one line: a sign-in hint on
// 401, else Jam's own message.
func shareErrMessage(err error, t sshTarget) string {
	se, ok := errors.AsType[*adminclient.StatusError](err)
	if !ok {
		return err.Error()
	}
	if se.Code == http.StatusUnauthorized {
		return sshErrMessage(err, t)
	}
	return fmt.Sprintf("Jam refused (%d): %s", se.Code, se.Body)
}
