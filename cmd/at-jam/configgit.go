package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"github.com/aethons-tools/cove/internal/jam/configtar"
)

// configStdin is the apply prompt's input (tests swap it).
var configStdin io.Reader = os.Stdin

// cmdConfig keeps a Jam's configuration in git: `config write <dir> [--all]`,
// `config diff|apply [--dir <path> | --git <src>]` (neither: the tracked
// source), `config track --git <src>`, `config sync`, `config untrack`, `config release <kind>/<name>`.
func cmdConfig(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	const usage = "at-jam config: expected write <dir> [--all] [--force] | diff [--dir <path> | --git <src>] | apply [--dir <path> | --git <src>] [--yes] [--force] | track --git <src> | sync [--yes] [--force] | untrack | release <kind>/<name>"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "write", "diff", "apply", "track", "sync", "untrack", "release":
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
		git = fs.String("git", "", "a config source <repo>[//<dir>]@<ref> to plan (default: the tracked source)")
	case "apply":
		dir = fs.String("dir", "", "the config checkout to apply")
		git = fs.String("git", "", "a config source <repo>[//<dir>]@<ref> to apply (default: the tracked source)")
		yes = fs.Bool("yes", false, "apply without the confirmation prompt")
		force = fs.Bool("force", false, "write over drift")
	case "track":
		git = fs.String("git", "", "the config source <repo>[//<dir>]@<ref> to track")
	case "sync":
		yes = fs.Bool("yes", false, "apply without the confirmation prompt")
		force = fs.Bool("force", false, "write over drift")
	}
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "at-jam config %s: %v\n", sub, err)
		return 1
	}
	if sub == "write" {
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam config write: expected one <dir>")
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
		if err := writeConfigTree(target, exp); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "wrote %d files to %s (removed %d); review with git diff\n", len(exp.Files), target, len(exp.Remove))
		return 0
	}
	if sub == "release" {
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam config release: expected one <kind>/<name> (a role: role/<project>/<role>)")
			return 2
		}
		kind, name, ok := strings.Cut(pos[0], "/")
		if !ok || kind == "" || name == "" {
			fmt.Fprintln(stderr, "at-jam config release: expected one <kind>/<name> (a role: role/<project>/<role>)")
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
		fmt.Fprintf(stderr, "at-jam config %s: unexpected argument %q\n", sub, pos[0])
		return 2
	}
	if *dir != "" && *git != "" {
		fmt.Fprintf(stderr, "at-jam config %s: --dir and --git are mutually exclusive\n", sub)
		return 2
	}
	if sub == "track" && *git == "" {
		fmt.Fprintln(stderr, "at-jam config track: --git <repo>[//<dir>]@<ref> is required")
		return 2
	}
	c, err := client()
	if err != nil {
		return fail(err)
	}
	switch sub {
	case "untrack":
		if err := c.ConfigUntrack(); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "config source untracked; managed objects and their marks are kept")
		return 0
	case "track":
		res, err := c.ConfigTrack(*git)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "tracking %s\n", res.Source)
		printConfigSource(stdout, res)
		printConfigPlan(stdout, res.Plan)
		if len(res.Plan.Refusals) > 0 {
			return 1
		}
		return 0
	}
	t := configTarget{source: *git, sync: sub == "sync"}
	if *dir != "" {
		if t.tree, err = configtar.Pack(*dir); err != nil {
			return fail(err)
		}
	}
	// apply and sync preview with their own --force, so the reviewed plan
	// (and its digest) is the one the forced apply writes.
	res, err := t.diff(c, sub != "diff" && *force)
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
	applied, err := t.apply(c, *force, res.Plan.Digest)
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
// --git source, or (neither) the tracked source — through /sync for sync.
type configTarget struct {
	tree   []byte
	source string
	sync   bool
}

func (t configTarget) diff(c *adminclient.Client, force bool) (jam.ConfigApplyResult, error) {
	if t.tree != nil {
		return c.ConfigDiff(t.tree, force)
	}
	return c.ConfigDiffSource(t.source, force)
}

func (t configTarget) apply(c *adminclient.Client, force bool, digest string) (jam.ConfigApplyResult, error) {
	switch {
	case t.tree != nil:
		return c.ConfigApply(t.tree, force, digest)
	case t.sync:
		return c.ConfigSync(force, digest)
	}
	return c.ConfigApplySource(t.source, force, digest)
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

// writeConfigTree writes exp.Files and exp.Binary (at their modes) under
// dir, removes exp.Remove, and prunes each exp.Prune dir to the files the
// export holds there. Every path is checked before anything is written.
func writeConfigTree(dir string, exp jam.ConfigTreeExport) error {
	for p := range exp.Files {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("refusing to write %q outside %s", p, dir)
		}
	}
	for p, b := range exp.Binary {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("refusing to write %q outside %s", p, dir)
		}
		if b.Mode&^0o777 != 0 {
			return fmt.Errorf("refusing to write %q with mode %o", p, b.Mode)
		}
	}
	for _, p := range exp.Remove {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("refusing to remove %q outside %s", p, dir)
		}
	}
	for _, p := range exp.Prune {
		if !isKitContextDir(p) {
			return fmt.Errorf("refusing to prune %q: not a kits/<name>/context dir", p)
		}
	}
	for p, content := range exp.Files {
		if err := writeTreeFile(dir, p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	for p, b := range exp.Binary {
		if err := writeTreeFile(dir, p, b.Data, os.FileMode(b.Mode)); err != nil {
			return err
		}
	}
	for _, p := range exp.Remove {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(p))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if len(exp.Prune) == 0 {
		return nil
	}
	keep := map[string]bool{}
	for p := range exp.Files {
		keep[path.Clean(p)] = true
	}
	for p := range exp.Binary {
		keep[path.Clean(p)] = true
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, p := range exp.Prune {
		if err := pruneTreeDir(root, p, keep); err != nil {
			return err
		}
	}
	return nil
}

// writeTreeFile writes data at dir/p with mode, past the umask and over an
// existing file's mode.
func writeTreeFile(dir, p string, data []byte, mode os.FileMode) error {
	full := filepath.Join(dir, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(full, data, mode); err != nil {
		return err
	}
	return os.Chmod(full, mode)
}

// isKitContextDir reports whether p is kits/<name>/context, name one plain
// path segment.
func isKitContextDir(p string) bool {
	segs := strings.Split(p, "/")
	return len(segs) == 3 && segs[0] == "kits" && segs[2] == "context" &&
		segs[1] != "" && segs[1] != "." && segs[1] != ".." && !strings.ContainsRune(segs[1], '\\')
}

// pruneTreeDir removes every entry under the kit context dir p (in root) that
// keep does not hold, then the dirs left empty — p and its kit dir included.
// It only descends real dirs: a kit or context dir that is a symlink is
// refused, and a symlink inside p is removed, never followed.
func pruneTreeDir(root *os.Root, p string, keep map[string]bool) error {
	for _, d := range []string{path.Dir(p), p} {
		fi, err := root.Lstat(d)
		if errors.Is(err, iofs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("refusing to prune %q: %s is not a directory", p, d)
		}
	}
	var dirs []string
	err := iofs.WalkDir(root.FS(), p, func(q string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, q)
			return nil
		}
		if keep[q] {
			return nil
		}
		return root.Remove(q)
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = root.Remove(dirs[i]) // only an empty dir goes
	}
	_ = root.Remove(path.Dir(p))
	return nil
}

// printConfigPlan renders a plan: one line per changed object, drift blocks,
// the unchanged count, unmanaged objects, the serve-config state, refusals.
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
		mark := ""
		if op.Drift {
			mark = "  (drift)"
		}
		fmt.Fprintf(w, "%s  %s %s%s\n", op.Action, op.Kind, op.Name, mark)
		if op.Drift {
			repo := indent(op.After)
			if op.After == "" { // a delete: the repo no longer has it
				repo = "    (not in the repo)"
			}
			fmt.Fprintf(w, "  jam:\n%s\n  repo:\n%s\n", indent(op.Before), repo)
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
