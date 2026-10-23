package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"github.com/aethons-tools/cove/internal/jam/configtar"
	"github.com/aethons-tools/cove/internal/jam/linediff"
	"github.com/aethons-tools/cove/internal/studio"
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
		for _, n := range exp.Inline {
			if studio.TagSafeName(n) { // a kit name, never other server text
				fmt.Fprintf(stderr, "at-jam config write: kit %s: context kept inline in kits/%s/kit.yml (no context-dir reproduces it, or its files would pass the config upload limits)\n", n, n)
			}
		}
		fmt.Fprintf(stdout, "wrote %d files to %s (removed %d); review with git diff\n", len(exp.Files)+len(exp.Binary), target, len(exp.Remove))
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
		if _, dup := exp.Files[p]; dup {
			return fmt.Errorf("refusing to write %q twice (a text and a binary file)", p)
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
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for p, content := range exp.Files {
		if err := writeTreeFile(root, p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	for p, b := range exp.Binary {
		if err := writeTreeFile(root, p, b.Data, os.FileMode(b.Mode)); err != nil {
			return err
		}
	}
	for _, p := range exp.Remove {
		if err := root.Remove(filepath.FromSlash(p)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	keep := map[string]bool{}
	for p := range exp.Files {
		keep[path.Clean(p)] = true
	}
	for p := range exp.Binary {
		keep[path.Clean(p)] = true
	}
	for _, p := range exp.Prune {
		var ignore *[]byte // nil: no context exported here, read the dir's own
		if di, ok := exp.Binary[p+"/.dockerignore"]; ok {
			ignore = &di.Data
		}
		if err := pruneTreeDir(root, p, keep, ignore); err != nil {
			return err
		}
	}
	return nil
}

// maxDockerignoreBytes caps an on-disk .dockerignore Prune reads.
const maxDockerignoreBytes = 64 << 10

// readTreeDockerignore reads the .dockerignore at p in root: nil, true when
// there is none; ok=false when it is not a regular file (never followed) or
// is over maxDockerignoreBytes.
func readTreeDockerignore(root *os.Root, p string) (data []byte, ok bool) {
	name := filepath.FromSlash(p)
	fi, err := root.Lstat(name)
	switch {
	case errors.Is(err, iofs.ErrNotExist):
		return nil, true
	case err != nil || !fi.Mode().IsRegular() || fi.Size() > maxDockerignoreBytes:
		return nil, false
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, maxDockerignoreBytes+1))
	if err != nil || len(data) > maxDockerignoreBytes {
		return nil, false
	}
	return data, true
}

// writeTreeFile writes data at p in root with mode, past the umask and over
// an existing file's mode; through root, nothing lands outside the tree.
func writeTreeFile(root *os.Root, p string, data []byte, mode os.FileMode) error {
	name := filepath.FromSlash(p)
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	if err := root.WriteFile(name, data, mode); err != nil {
		return err
	}
	return root.Chmod(name, mode)
}

// isKitContextDir reports whether p is kits/<name>/context, name one plain
// path segment.
func isKitContextDir(p string) bool {
	segs := strings.Split(p, "/")
	return len(segs) == 3 && segs[0] == "kits" && segs[2] == "context" &&
		segs[1] != "" && segs[1] != "." && segs[1] != ".." && !strings.ContainsRune(segs[1], '\\')
}

// pruneTreeDir removes every entry under the kit context dir p (in root) that
// keep does not hold and the context's .dockerignore (ignore, as the packer
// reads it) does not exclude, then the dirs left empty — p and its kit dir
// included. It only descends real dirs: a kit or context dir that is a
// symlink is refused, and a symlink inside p is removed, never followed.
//
// ignore nil means no context was exported for p (its kit is now written by
// reference, or gone): p's own .dockerignore then says what was never
// packed, so those files — and it — stay; one that is not a regular file
// or is over maxDockerignoreBytes leaves p as it is.
func pruneTreeDir(root *os.Root, p string, keep map[string]bool, ignore *[]byte) error {
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
	var rules []byte
	if ignore != nil {
		rules = *ignore
	} else {
		var ok bool
		if rules, ok = readTreeDockerignore(root, p+"/.dockerignore"); !ok {
			return nil
		}
		if rules != nil {
			keep = maps.Clone(keep)
			keep[p+"/.dockerignore"] = true
		}
	}
	pm, err := studio.DockerignoreMatcher(bytes.NewReader(rules))
	if err != nil {
		return fmt.Errorf("refusing to prune %q: its .dockerignore cannot be read", p)
	}
	var dirs []string
	err = iofs.WalkDir(root.FS(), p, func(q string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if q != p {
			ignored, merr := pm.MatchesOrParentMatches(strings.TrimPrefix(q, p+"/"))
			if merr != nil {
				return merr
			}
			if ignored && d.IsDir() && !pm.Exclusions() {
				return iofs.SkipDir
			}
			if ignored && !d.IsDir() {
				return nil
			}
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
		switch {
		case op.Drift:
			mark = "  (drift)"
		case op.Unmanaged:
			mark = "  (not managed)"
		}
		fmt.Fprintf(w, "%s  %s %s%s\n", op.Action, op.Kind, op.Name, mark)
		if !op.Drift && !op.Unmanaged {
			continue
		}
		if op.After == "" { // a delete: the repo no longer has it
			fmt.Fprintf(w, "  jam:\n%s\n  repo:\n    (not in the repo)\n", indent(op.Before))
			continue
		}
		lines, tooLarge := linediff.Diff(jam.ConfigCanonDisplay(op.Kind, op.Before), jam.ConfigCanonDisplay(op.Kind, op.After))
		if tooLarge {
			fmt.Fprintf(w, "  jam:\n%s\n  repo:\n%s\n", indent(op.Before), indent(op.After))
			continue
		}
		fmt.Fprintf(w, "  --- jam\n  +++ repo\n")
		for _, l := range lines {
			if l.Op == "…" {
				fmt.Fprintf(w, "  … %s\n", l.Text)
			} else {
				fmt.Fprintf(w, "  %s%s\n", l.Op, l.Text)
			}
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
