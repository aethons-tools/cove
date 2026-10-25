package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"github.com/aethons-tools/cove/internal/jam/configtar"
	"github.com/aethons-tools/cove/internal/jam/linediff"
	"github.com/aethons-tools/cove/internal/kittree"
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
		n := len(exp.Files)
		for _, files := range exp.Kits {
			n += len(files)
		}
		fmt.Fprintf(stdout, "wrote %d files to %s (removed %d); review with git diff\n", n, target, len(exp.Remove))
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

// writeConfigTree writes exp.Files, then each exp.Kits dir's files at their
// modes, removes exp.Remove, and mirrors each kit dir (mirrorKitDir). Every
// path is checked before anything is written: Files and Remove stay inside
// dir, a kit dir is kits/<name>, its files have safe paths and mode 0644 or
// 0755, no path is written twice, and kits/ and each kits/<name> on disk,
// when present, is a real directory (never a symlink a write could follow).
func writeConfigTree(dir string, exp jam.ConfigTreeExport) error {
	for p := range exp.Files {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("refusing to write %q outside %s", p, dir)
		}
	}
	for d, files := range exp.Kits {
		if !isKitDir(d) {
			return fmt.Errorf("refusing to write kit dir %q: not kits/<name>", d)
		}
		for p := range exp.Files {
			if c := path.Clean(p); c == d || strings.HasPrefix(c, d+"/") {
				return fmt.Errorf("refusing to write %q twice (a file and a kit dir's)", p)
			}
		}
		for _, p := range exp.Remove {
			if c := path.Clean(p); c == d || strings.HasPrefix(c, d+"/") {
				return fmt.Errorf("refusing to remove %q inside the mirrored kit dir %s", p, d)
			}
		}
		paths := make(map[string]bool, len(files))
		for _, f := range files {
			if kittree.CheckPath(f.Path) != nil {
				return fmt.Errorf("refusing to write an unsafe path under %s", d)
			}
			if f.Mode != 0o644 && f.Mode != 0o755 {
				return fmt.Errorf("refusing to write %s/%s with mode %o", d, f.Path, f.Mode)
			}
			if paths[f.Path] {
				return fmt.Errorf("refusing to write %q twice", d+"/"+f.Path)
			}
			paths[f.Path] = true
		}
		for p := range paths {
			for a := path.Dir(p); a != "."; a = path.Dir(a) {
				if paths[a] {
					return fmt.Errorf("refusing to write %q: it conflicts with %q", d+"/"+p, d+"/"+a)
				}
			}
		}
	}
	for _, p := range exp.Remove {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("refusing to remove %q outside %s", p, dir)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, d := range append([]string{"kits"}, slices.Sorted(maps.Keys(exp.Kits))...) {
		fi, err := root.Lstat(filepath.FromSlash(d))
		if err == nil && !fi.IsDir() {
			return fmt.Errorf("refusing to write %s: it is not a directory", d)
		}
		if err != nil && !errors.Is(err, iofs.ErrNotExist) {
			return err
		}
	}
	for p := range exp.Files {
		if err := checkWritePath(root, path.Clean(p)); err != nil {
			return err
		}
	}
	for d, files := range exp.Kits {
		for _, f := range files {
			if err := checkWritePath(root, d+"/"+f.Path); err != nil {
				return err
			}
		}
	}
	for p, content := range exp.Files {
		if err := writeTreeFile(root, p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	for d, files := range exp.Kits {
		for _, f := range files {
			if err := writeTreeFile(root, d+"/"+f.Path, f.Data, os.FileMode(f.Mode)); err != nil {
				return err
			}
		}
	}
	for _, p := range exp.Remove {
		if err := root.Remove(filepath.FromSlash(p)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, d := range slices.Sorted(maps.Keys(exp.Kits)) {
		files := exp.Kits[d]
		keep := map[string]bool{}
		var ignore *[]byte // nil: nothing exported here, read the dir's own .dockerignore
		if len(files) > 0 {
			empty := []byte{}
			ignore = &empty // exported without a .dockerignore: nothing on disk is excluded
		}
		for _, f := range files {
			keep[d+"/"+f.Path] = true
			if f.Path == ".dockerignore" {
				data := f.Data
				ignore = &data
			}
		}
		if err := mirrorKitDir(root, d, keep, ignore); err != nil {
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
	// os.Root follows a symlink that stays inside the tree: replace one (or
	// any other non-regular file) at p instead of writing through it.
	if fi, err := root.Lstat(name); err == nil && !fi.Mode().IsRegular() {
		if fi.IsDir() {
			return fmt.Errorf("refusing to write %q: it is a directory", p)
		}
		if err := root.Remove(name); err != nil {
			return err
		}
	}
	if err := root.WriteFile(name, data, mode); err != nil {
		return err
	}
	return root.Chmod(name, mode)
}

// checkWritePath refuses to write p (slash, clean, local) when a directory
// on its way is, on disk, a symlink or not a directory (os.Root would follow
// an in-tree symlink onto another file), or p itself is a directory. A
// symlink at p itself is replaced by writeTreeFile.
func checkWritePath(root *os.Root, p string) error {
	segs := strings.Split(p, "/")
	for i := range segs {
		q := strings.Join(segs[:i+1], "/")
		fi, err := root.Lstat(filepath.FromSlash(q))
		if errors.Is(err, iofs.ErrNotExist) {
			return nil // nothing deeper exists either
		}
		if err != nil {
			return fmt.Errorf("refusing to write %q: %q cannot be read", p, q)
		}
		last := i == len(segs)-1
		if !last && !fi.IsDir() {
			return fmt.Errorf("refusing to write %q: %q is not a directory", p, q)
		}
		if last && fi.IsDir() {
			return fmt.Errorf("refusing to write %q: it is a directory", p)
		}
	}
	return nil
}

// isKitDir reports whether p is kits/<name>, name a tag-safe kit name.
func isKitDir(p string) bool {
	segs := strings.Split(p, "/")
	return len(segs) == 2 && segs[0] == "kits" && studio.TagSafeName(segs[1])
}

// mirrorKitDir removes every file under the kit dir p (in root) that keep
// does not hold and the dir's .dockerignore (ignore, as kittree reads it)
// does not exclude, then the dirs left empty, p included (kits/ stays). It
// only descends real dirs: kits/ or a kit dir that is a symlink is refused,
// and a symlink inside p is removed, never followed. kit.yml, Dockerfile and
// .dockerignore are never excluded, so they go unless kept.
//
// ignore nil means no files were exported for p (its kit is now written by
// reference, or gone): p's own .dockerignore then says what Jam never
// stored, so those files — and it — stay; one that is not a regular file
// or is over maxDockerignoreBytes leaves p as it is.
func mirrorKitDir(root *os.Root, p string, keep map[string]bool, ignore *[]byte) error {
	for _, d := range []string{"kits", p} {
		fi, err := root.Lstat(d)
		if errors.Is(err, iofs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("refusing to mirror %q: %s is not a directory", p, d)
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
	ig, err := kittree.NewIgnorer(rules)
	if err != nil {
		return fmt.Errorf("refusing to mirror %q: its .dockerignore cannot be read", p)
	}
	var dirs []string
	err = iofs.WalkDir(root.FS(), p, func(q string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if q != p {
			ignored, merr := ig.Ignored(strings.TrimPrefix(q, p+"/"))
			if merr != nil {
				return merr
			}
			if ignored && d.IsDir() && ig.Prunes() {
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
		lines, tooLarge := linediff.Diff(op.Before, op.After)
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
