package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"github.com/aethons-tools/cove/internal/jam/configtar"
	"github.com/aethons-tools/cove/internal/textesc"
)

// configStdin is the apply prompt's input (tests swap it).
var configStdin io.Reader = os.Stdin

// cmdConfig keeps a Jam's configuration in git: `config write <dir> [--all]`,
// `config diff|apply [--dir <path> | --git <src>]` (neither: the followed
// repo), `config sync`, `config status`, `config release <kind>/<name>`.
func cmdConfig(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	const usage = "jam config: expected write <dir> [--all] [--force] | diff [--dir <path> | --git <src>] | apply [--dir <path> | --git <src>] [--yes] | sync [--yes] | status | release <kind>/<name>"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "write", "diff", "apply", "sync", "status", "release":
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("config "+sub, flag.ContinueOnError)
	client := clientFlags(fs, stderr)
	none := ""
	no := false
	all, yes, force := &no, &no, &no
	dir, git := &none, &none
	switch sub {
	case "write":
		all = fs.Bool("all", false, "write every live object, not only the managed ones")
		force = fs.Bool("force", false, "allow a directory outside a git working tree")
	case "diff":
		dir = fs.String("dir", "", "the config checkout to plan against the Jam")
		git = fs.String("git", "", "a config source <repo>[//<dir>]@<ref> to plan (default: the followed repo)")
	case "apply":
		dir = fs.String("dir", "", "the config checkout to apply")
		git = fs.String("git", "", "a config source <repo>[//<dir>]@<ref> to apply (default: the followed repo)")
		yes = fs.Bool("yes", false, "apply without the confirmation prompt")
	case "sync":
		yes = fs.Bool("yes", false, "apply without the confirmation prompt")
	}
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "jam config %s: %v\n", sub, err)
		return 1
	}
	if sub == "write" {
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam config write: expected one <dir>")
			return 2
		}
		c, err := client()
		if err != nil {
			return fail(err)
		}
		target := pos[0]
		abs, err := filepath.Abs(target)
		if err != nil {
			return fail(err)
		}
		if !*force && !insideGitWorkTree(abs) {
			return fail(fmt.Errorf("%s is not inside a git working tree (use --force to write anyway)", target))
		}
		exp, err := c.ConfigExportTree(*all)
		if err != nil {
			return fail(err)
		}
		if err := jam.WriteConfigTree(target, exp); err != nil {
			return fail(err)
		}
		n := len(exp.Files)
		for _, files := range exp.Kits {
			n += len(files)
		}
		fmt.Fprintf(stdout, "wrote %d files to %s (removed %d); review with git diff\n", n, target, len(exp.Remove))
		return 0
	}
	if sub == "release" {
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "jam config release: expected one <kind>/<name> (a role: role/<project>/<role>)")
			return 2
		}
		kind, name, ok := strings.Cut(pos[0], "/")
		if !ok || kind == "" || name == "" {
			fmt.Fprintln(stderr, "jam config release: expected one <kind>/<name> (a role: role/<project>/<role>)")
			return 2
		}
		c, err := client()
		if err != nil {
			return fail(err)
		}
		if err := c.ConfigRelease(kind, name); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "released %s %s: it stays in Jam, unmanaged\n", kind, name)
		return 0
	}
	if len(pos) != 0 {
		fmt.Fprintf(stderr, "jam config %s: unexpected argument %q\n", sub, pos[0])
		return 2
	}
	if *dir != "" && *git != "" {
		fmt.Fprintf(stderr, "jam config %s: --dir and --git are mutually exclusive\n", sub)
		return 2
	}
	c, err := client()
	if err != nil {
		return fail(err)
	}
	if sub == "status" {
		s, err := c.ConfigFollow()
		if err != nil {
			return fail(err)
		}
		printConfigFollow(stdout, s)
		return 0
	}
	t := configTarget{source: *git, sync: sub == "sync"}
	if *dir != "" {
		if t.tree, err = configtar.Pack(*dir); err != nil {
			return fail(err)
		}
	}
	res, err := t.diff(c)
	if err != nil {
		return fail(err)
	}
	printConfigSource(stdout, res)
	printConfigPlan(stdout, res.Plan)
	if len(res.Plan.Refusals) > 0 {
		return 1
	}
	if sub == "diff" {
		return 0
	}
	n := jam.ConfigChanges(res.Plan)
	// A plan with nothing to change still applies, without a prompt: the
	// server's metadata-only write refreshes stale marks (after a config
	// write, say) and, from git, the tracked commits.
	if n > 0 && !*yes {
		word := "changes"
		if n == 1 {
			word = "change"
		}
		fmt.Fprintf(stdout, "Apply %d %s? [y/N] ", n, word)
		line, _ := bufio.NewReader(configStdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(stderr, "aborted")
			return 1
		}
	}
	applied, err := t.apply(c, res.Plan.Digest)
	if err != nil {
		return fail(err)
	}
	if !applied.Applied || n == 0 {
		fmt.Fprintln(stdout, "nothing to apply")
		return 0
	}
	k := applied.Counts
	fmt.Fprintf(stdout, "applied: %d created, %d updated, %d deleted, %d adopted, %d forgotten\n",
		k[jam.ConfigCreate], k[jam.ConfigUpdate], k[jam.ConfigDelete], k[jam.ConfigAdopt], k[jam.ConfigForget])
	return 0
}

// configTarget is what a diff, apply or sync plans: a packed --dir tree, a
// --git source, or (neither) the followed repo — through /sync for sync.
type configTarget struct {
	tree   []byte
	source string
	sync   bool
}

func (t configTarget) diff(c *adminclient.Client) (jam.ConfigApplyResult, error) {
	if t.tree != nil {
		return c.ConfigDiff(t.tree)
	}
	return c.ConfigDiffSource(t.source)
}

func (t configTarget) apply(c *adminclient.Client, digest string) (jam.ConfigApplyResult, error) {
	switch {
	case t.tree != nil:
		return c.ConfigApply(t.tree, digest)
	case t.sync:
		return c.ConfigSync(digest)
	}
	return c.ConfigApplySource(t.source, digest)
}

// printConfigFollow prints the follower's status, one field a line.
func printConfigFollow(w io.Writer, s jam.ConfigFollowStatus) {
	fmt.Fprintf(w, "state: %s\n", s.State)
	if s.Source != "" {
		fmt.Fprintf(w, "source: %s\n", s.Source)
	}
	if s.Head != "" {
		fmt.Fprintf(w, "head: %s\n", jam.ShortConfigCommit(s.Head))
	}
	if s.Applied != "" {
		fmt.Fprintf(w, "applied: %s\n", jam.ShortConfigCommit(s.Applied))
	}
	if !s.CheckedAt.IsZero() {
		fmt.Fprintf(w, "checked: %s\n", s.CheckedAt.Local().Format(time.DateTime))
	}
	if s.Error != "" {
		fmt.Fprintf(w, "error: %s\n", s.Error)
	}
}

// printConfigSource names the git source and commit a run planned.
func printConfigSource(w io.Writer, res jam.ConfigApplyResult) {
	if res.Commit != "" {
		fmt.Fprintf(w, "config %s @ %s\n", res.Source, jam.ShortConfigCommit(res.Commit))
	}
}

// insideGitWorkTree reports whether dir or an ancestor holds a .git.
func insideGitWorkTree(dir string) bool {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// printConfigPlan renders a plan: one line per changed object (a kit update
// with its version diff), the unchanged count, unmanaged objects, the
// serve-config state, refusals.
func printConfigPlan(w io.Writer, p jam.ConfigPlan) {
	for _, ks := range p.KitSources {
		fmt.Fprintf(w, "kit-source  %s  %s @ %s\n", ks.Path, ks.Source, jam.ShortConfigCommit(ks.Commit))
	}
	unchanged := 0
	indent := func(s string) string {
		s = strings.TrimRight(s, "\n")
		return "    " + strings.ReplaceAll(s, "\n", "\n    ")
	}
	for _, op := range p.Ops {
		if op.Action == jam.ConfigUnchanged {
			unchanged++
			continue
		}
		fmt.Fprintf(w, "%s  %s %s\n", op.Action, op.Kind, op.Name)
		if op.Kind == jam.ConfigKindKit && op.Diff != "" {
			fmt.Fprintln(w, indent(textesc.Escape(op.Diff, true))) // kit.yml's line diff, then the files
		}
	}
	for _, k := range p.Forget {
		fmt.Fprintf(w, "forget  %s %s  (mark only)\n", k.Kind, k.Name)
	}
	fmt.Fprintf(w, "%d unchanged\n", unchanged)
	if len(p.Unmanaged) > 0 {
		names := make([]string, 0, len(p.Unmanaged))
		for _, k := range p.Unmanaged {
			names = append(names, k.Kind+" "+k.Name)
		}
		sort.Strings(names)
		fmt.Fprintf(w, "unmanaged: %s\n", strings.Join(names, ", "))
	}
	sc := p.ServeConfig
	switch sc.State {
	case "differs":
		fmt.Fprintf(w, "serve-config: differs (restart to apply): %s\n", strings.Join(sc.Keys, ", "))
	case "":
	default:
		fmt.Fprintf(w, "serve-config: %s\n", sc.State)
	}
	if len(p.Refusals) > 0 {
		fmt.Fprintln(w, "refused:")
		for _, r := range p.Refusals {
			fmt.Fprintf(w, "  %s\n", r)
		}
	}
}
