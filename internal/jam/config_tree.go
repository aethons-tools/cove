package jam

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/studio"
)

// ConfigDirRepo is the KitSource.Repo of the kit versions a --dir apply writes.
const ConfigDirRepo = "local"

// ConfigDirCommit is the ConfigMark.Commit of a --dir apply.
const ConfigDirCommit = "dir"

// ConfigTree is a config repo as loaded from disk: every object that parsed,
// and a file-located refusal for everything that did not.
type ConfigTree struct {
	Serve        map[string]any // jam.yml; nil = absent
	JamContext   *sessionctx.Layer
	Destinations map[string]DestinationFile
	ModelSpecs   map[string]ModelSpecFile
	Users        map[string]UserFile
	Connections  map[string]ConnectionFile
	Projects     map[string]ProjectFile
	Roles        map[string]RoleFile // "<project>/<role>"
	Kits         []kitSetEntry       // every kit under kits/, parents first; path refs as written
	Refusals     []string            // file-located load errors
}

// treeLoader carries one LoadConfigTree run.
type treeLoader struct {
	root string
	r    *os.Root
	fsys fs.FS
	tr   *ConfigTree
}

func (l *treeLoader) refuse(format string, a ...any) {
	l.tr.Refusals = append(l.tr.Refusals, fmt.Sprintf(format, a...))
}

// readDir lists dir (slash, relative to the root) without its dot-entries;
// a missing dir is empty. Entries whose names are unsafe to print
// (studio.HasUnsafeChars) are dropped, with one fixed refusal for dir that
// never echoes the name; at the top level, where unknown entries are
// ignored, they are dropped silently.
func (l *treeLoader) readDir(dir string) []fs.DirEntry {
	ents, err := fs.ReadDir(l.fsys, dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			l.refuse("%s: cannot be read", dir)
		}
		return nil
	}
	unsafe := false
	ents = slices.DeleteFunc(ents, func(e fs.DirEntry) bool {
		if studio.HasUnsafeChars(e.Name()) {
			unsafe = true
			return true
		}
		return strings.HasPrefix(e.Name(), ".")
	})
	if unsafe && dir != "." {
		l.refuse("%s: an entry has an unsafe name", dir)
	}
	return ents
}

// read reads rel through the jail (regular files only); a failure is a refusal.
func (l *treeLoader) read(rel string) ([]byte, bool) {
	data, err := studio.ReadFileInRoot(l.root, rel)
	if err != nil {
		l.refuse("%s: %s", rel, err)
		return nil, false
	}
	return data, true
}

// LoadConfigTree loads the config repo at root. Every load problem is a
// refusal in the tree; the error is only for a root that cannot be opened.
// Top-level files and directories other than the known ones are ignored.
func LoadConfigTree(root string) (*ConfigTree, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("config dir %s cannot be opened: %w", root, err)
	}
	defer r.Close()
	l := &treeLoader{root: root, r: r, fsys: r.FS(), tr: &ConfigTree{
		Destinations: map[string]DestinationFile{}, ModelSpecs: map[string]ModelSpecFile{},
		Users: map[string]UserFile{}, Connections: map[string]ConnectionFile{},
		Projects: map[string]ProjectFile{}, Roles: map[string]RoleFile{},
	}}
	for _, e := range l.readDir(".") {
		switch name := e.Name(); {
		case name == "jam.yml" && !e.IsDir():
			l.loadServe()
		case name == "context.md" && !e.IsDir():
			if data, ok := l.read(name); ok {
				if layer, err := ParseContextMarkdown(name, data); err != nil {
					l.refuse("%s", err)
				} else {
					l.tr.JamContext = &layer
				}
			}
		}
	}
	loadFlat(l, "destinations", l.tr.Destinations, ParseDestinationFile)
	loadFlat(l, "model-specs", l.tr.ModelSpecs, ParseModelSpecFile)
	loadFlat(l, "users", l.tr.Users, ParseUserFile)
	loadFlat(l, "connections", l.tr.Connections, ParseConnectionFile)
	l.loadProjects()
	l.loadKits()
	slices.Sort(l.tr.Refusals)
	l.tr.Refusals = slices.Compact(l.tr.Refusals)
	return l.tr, nil
}

func (l *treeLoader) loadServe() {
	data, ok := l.read("jam.yml")
	if !ok {
		return
	}
	m := map[string]any{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		l.refuse("jam.yml: not valid YAML")
		return
	}
	l.tr.Serve = m
}

// loadFlat loads dir/<n>.yml files into into with parse.
func loadFlat[F any](l *treeLoader, dir string, into map[string]F, parse func(path, name string, data []byte) (F, error)) {
	for _, e := range l.readDir(dir) {
		rel := dir + "/" + e.Name()
		n, isYML := strings.CutSuffix(e.Name(), ".yml")
		switch {
		case e.IsDir():
			l.refuse("%s: unexpected directory", rel)
		case strings.HasSuffix(e.Name(), ".yaml"):
			l.refuse("%s: use .yml", rel)
		case !isYML || n == "":
			l.refuse("%s: unexpected file", rel)
		default:
			data, ok := l.read(rel)
			if !ok {
				continue
			}
			f, err := parse(rel, n, data)
			if err != nil {
				l.refuse("%s", err)
				continue
			}
			into[n] = f
		}
	}
}

// loadProjects loads projects/<p>/{project.yml,context.md,roles/<r>.yml}. A
// project dir holding only roles/ is fine: its project need not be managed.
func (l *treeLoader) loadProjects() {
	for _, pe := range l.readDir("projects") {
		pdir := "projects/" + pe.Name()
		if !pe.IsDir() {
			l.refuse("%s: unexpected file", pdir)
			continue
		}
		p := pe.Name()
		if checkProjectName(p) != nil {
			l.refuse("%s: not a valid project name", pdir)
			continue
		}
		var proj *ProjectFile
		var ctx *sessionctx.Layer
		ctxSeen := false
		for _, e := range l.readDir(pdir) {
			rel := pdir + "/" + e.Name()
			switch {
			case e.Name() == "project.yml" && !e.IsDir():
				if data, ok := l.read(rel); ok {
					if f, err := ParseProjectFile(rel, p, data); err != nil {
						l.refuse("%s", err)
					} else {
						proj = &f
					}
				}
			case e.Name() == "context.md" && !e.IsDir():
				ctxSeen = true
				if data, ok := l.read(rel); ok {
					if layer, err := ParseContextMarkdown(rel, data); err != nil {
						l.refuse("%s", err)
					} else {
						ctx = &layer
					}
				}
			case e.Name() == "roles" && e.IsDir():
				roles := map[string]RoleFile{}
				loadFlat(l, rel, roles, func(path, name string, data []byte) (RoleFile, error) {
					f, err := ParseRoleFile(path, name, data)
					if err != nil {
						return f, err
					}
					if _, v, err := (studio.Base{Kit: f.Kit}).ParentRef(); err == nil && v > 0 {
						return f, fmt.Errorf("%s: kit %s pins a version; versions are per-Jam — name the kit", path, f.Kit)
					}
					return f, nil
				})
				for r, f := range roles {
					l.tr.Roles[p+"/"+r] = f
				}
			case e.IsDir():
				l.refuse("%s: unexpected directory", rel)
			default:
				l.refuse("%s: unexpected file", rel)
			}
		}
		switch {
		case proj != nil:
			if ctx != nil {
				proj.Context = *ctx
			}
			l.tr.Projects[p] = *proj
		case ctxSeen && !l.exists(pdir+"/project.yml"):
			l.refuse("%s/context.md: no %s/project.yml", pdir, pdir)
		}
	}
}

// isRealDir reports whether rel is a directory and not a symlink.
func (l *treeLoader) isRealDir(rel string) bool {
	fi, err := l.r.Lstat(rel)
	return err == nil && fi.IsDir()
}

func (l *treeLoader) exists(rel string) bool {
	_, err := fs.Stat(l.fsys, rel)
	return err == nil
}

// loadKits loads each kits/<n>/ dir as a kit set (the kit and its path
// parents), refusing a parent outside kits/ and a registry pin, then merges
// the clean sets into one parents-first list.
func (l *treeLoader) loadKits() {
	byDir := map[string]kitSetEntry{}
	for _, e := range l.readDir("kits") {
		n := e.Name()
		rel := "kits/" + n
		if !e.IsDir() {
			if strings.HasSuffix(n, ".source.yml") {
				l.refuse("%s: kit source references are not supported yet", rel)
			} else {
				l.refuse("%s: unexpected file (kits are directories: kits/<name>/kit.yml)", rel)
			}
			continue
		}
		if !l.exists(rel + "/" + gitsource.KitFile) {
			l.refuse("%s: no kit.yml", rel)
			continue
		}
		set, err := loadKitSet(l.root, gitsource.Source{Repo: ConfigDirRepo, Dir: rel}, n)
		if err != nil {
			l.refuse("%s/kit.yml: %s", rel, err)
			continue
		}
		if msg := l.checkTreeKitSet(set); msg != "" {
			l.refuse("%s", msg)
			continue
		}
		for _, se := range set {
			byDir[se.Dir] = se
		}
	}
	l.tr.Kits = l.orderKits(byDir)
}

// checkTreeKitSet is a loaded set's tree refusal, "" = none: an ancestor not
// at kits/<its name> or not a real directory there (a symlink, say — the
// same entry kits/ itself refuses), or a root pinning a registry parent's
// version. set is root first; it is walked from the top kit down so the
// refusal names the file holding the bad reference.
func (l *treeLoader) checkTreeKitSet(set []kitSetEntry) string {
	for i := len(set) - 2; i >= 0; i-- {
		if set[i].Dir != "kits/"+set[i].Name || !l.isRealDir(set[i].Dir) {
			child := set[i+1]
			return fmt.Sprintf("%s/kit.yml: parent %s is not a kit under kits/ (kits/<name>/kit.yml)", child.Dir, child.Kit.Base.Kit)
		}
	}
	if _, isPath := set[0].Kit.Base.ParentPath(); !isPath && set[0].Kit.Base.HasParent() {
		if _, v, err := set[0].Kit.Base.ParentRef(); err == nil && v > 0 {
			return fmt.Sprintf("%s/kit.yml: base.kit %s pins a version; versions are per-Jam — name the kit or use a path parent", set[0].Dir, set[0].Kit.Base.Kit)
		}
	}
	return ""
}

// orderKits orders the tree's kits parents first (Kahn's algorithm, ties by
// name) over path parents and registry-name parents that are tree kits. Kits
// left in a cycle are refused.
func (l *treeLoader) orderKits(byDir map[string]kitSetEntry) []kitSetEntry {
	byName := map[string]kitSetEntry{}
	for _, e := range byDir {
		byName[e.Name] = e
	}
	parentOf := map[string]string{} // child → tree parent
	for name, e := range byName {
		if p, isPath := e.Kit.Base.ParentPath(); isPath {
			if pe, ok := byDir[path.Join(checkoutDir(e.Dir), p)]; ok {
				parentOf[name] = pe.Name
			}
		} else if e.Kit.Base.HasParent() {
			if pn, _, err := e.Kit.Base.ParentRef(); err == nil {
				if _, ok := byName[pn]; ok {
					parentOf[name] = pn
				}
			}
		}
	}
	var out []kitSetEntry
	done := map[string]bool{}
	for len(done) < len(byName) {
		var ready []string
		for name := range byName {
			if p, has := parentOf[name]; !done[name] && (!has || done[p]) {
				ready = append(ready, name)
			}
		}
		if len(ready) == 0 {
			break
		}
		next := slices.Min(ready)
		done[next] = true
		out = append(out, byName[next])
	}
	if len(done) < len(byName) {
		var left []string
		for name := range byName {
			if !done[name] {
				left = append(left, name)
			}
		}
		slices.Sort(left)
		l.refuse("kits: cycle among %s", strings.Join(left, ", "))
	}
	return out
}
