package adminui

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/linediff"
	"github.com/aethons-tools/cove/internal/studio"
	"gopkg.in/yaml.v3"
)

// kitListRow is one kits-table row, summarizing the current version.
type kitListRow struct {
	Name           string
	Current, Count int
	Base           string // base kind label; "" when the current config won't parse
	Egress         int
	UsedBy         int
	Invalid        bool
}

// kitVersion is one entry in a kit's version rail.
type kitVersion struct {
	N       int
	Prev    int // the version before N in the rail; 0 for the first
	Current bool
	Digest  string      // short build digest; "" when the config won't parse
	Source  *sourceView // where it was imported from; nil = pushed, or legacy
	Pushed  string      // push time, "" when unrecorded
}

// sourceView is a git source shaped for display: Repo links only when it is
// an https URL, Commit (12 chars) only when the host's commit URL is known.
type sourceView struct {
	Repo, RepoLink string
	Dir, Ref       string
	Commit         string
	CommitLink     string
}

func newSourceView(s *jam.KitSource) *sourceView {
	if s == nil {
		return nil
	}
	v := &sourceView{Repo: s.Repo, Dir: s.Dir, Ref: s.Ref, Commit: shortCommit(s.Commit)}
	if u := httpsRepo(s.Repo); u != nil {
		v.RepoLink = s.Repo
		v.CommitLink = commitLink(u, s.Commit)
	}
	return v
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// httpsRepo parses repo when it is an https URL with a host, else nil: only
// those are rendered as links.
func httpsRepo(repo string) *url.URL {
	u, err := url.Parse(repo)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil
	}
	return u
}

// commitLink is the host's page for a commit: github.com/.../commit/<sha>,
// gitlab-like hosts .../-/commit/<sha>; "" for any other host.
func commitLink(repo *url.URL, sha string) string {
	if sha == "" {
		return ""
	}
	base := strings.TrimSuffix(strings.TrimRight(repo.Path, "/"), ".git")
	host := strings.ToLower(repo.Hostname())
	switch {
	case host == "github.com":
		return "https://" + repo.Host + base + "/commit/" + sha
	case strings.Contains(host, "gitlab"):
		return "https://" + repo.Host + base + "/-/commit/" + sha
	}
	return ""
}

// ctxEntry is one file in a packed base.context.
type ctxEntry struct {
	Path string
	Size int64
	Mode string
}

// kv is a sorted key/value pair for build-args and secret demands.
type kv struct{ Key, Value string }

// kitView is one parsed version, ready to render.
type kitView struct {
	N           int
	ParseErr    string // the stored config isn't a valid studio kit (legacy row)
	Raw         string // the stored text, shown when ParseErr is set
	YAML        string // display YAML (a packed context abbreviated)
	PushYAML    string // full YAML, for pre-filling a new version
	Base        string
	Image       string
	Files       []string // context-files paths
	Context     []ctxEntry
	ContextSize string // encoded size of a packed context
	ContextNote string // why the entry list is partial or missing
	Egress      []string
	Ceiling     []string
	Excluded    []string
	BuildArgs   []kv
	Secrets     []kv
	MCPServers  []kv // name → type
	Notes       []kv // name → read-when
	Prompt      string
	Digest      string
}

// diffLine is one line of a version or plan diff.
type diffLine = linediff.Line

// kitDiff compares two versions' display YAML.
type kitDiff struct {
	From, To      int
	Lines         []diffLine
	DigestChanged bool
	TooLarge      bool
}

// kitUse is one role bound to the kit. Implicit marks a role with no kit set,
// which raises the built-in default.
type kitUse struct {
	Project, Role string
	Implicit      bool
}

// kitParent is one link of a "Built on" chain: a parent kit, the version it
// resolves to, and whether the child follows its current version.
type kitParent struct {
	Name    string
	Version int
	Follow  bool
}

// kitChild is a kit whose current version builds on the page's kit.
type kitChild struct {
	Name   string
	Pinned bool // pins a version; otherwise follows current
}

// kitDetail is the kit page payload.
type kitDetail struct {
	Title       string
	Name        string
	Current     int
	Versions    []kitVersion
	View        kitView
	Diff        *kitDiff
	Uses        []kitUse
	Chain       []kitParent // parents of the viewed version, root first; empty = unchained
	ChainErr    string      // why the viewed version's chain doesn't resolve
	Effective   *kitView    // the chain merged; nil unless chained
	UsedByKits  []kitChild
	IsDefault   bool
	Source      *sourceView // tracked source; nil = not tracked
	SetWith     string      // chain parents imported from the same repo, comma-joined; "" = not part of a set
	CanSource   bool        // import/track/sync controls are available
	NotFound    bool
	NotFoundFor string
}

// baseLabel names a base: its form, and for a chained kit the parent it is
// built on ("kit: web@v2", plus " + context-files" for the kit's own stage).
func baseLabel(b studio.Base) string {
	k, err := b.Kind()
	if err != nil {
		return "invalid"
	}
	switch {
	case k == studio.BaseKit:
		return "kit: " + b.Kit
	case b.HasParent():
		return "kit: " + b.Kit + " + " + ownBaseLabel(k)
	}
	return ownBaseLabel(k)
}

func ownBaseLabel(k studio.BaseKind) string {
	switch k {
	case studio.BaseImage:
		return "image"
	case studio.BaseContextFiles:
		return "context-files"
	case studio.BaseContextZip:
		return "context"
	case studio.BaseContextDir:
		return "context-dir"
	}
	return "blessed default"
}

// kitUses lists the roles bound to name; for the default kit, also the roles
// with no kit set (they raise it implicitly).
func kitUses(store jam.Store, name string) []kitUse {
	var out []kitUse
	for _, p := range store.ListProjects() {
		for _, r := range store.ListRoles(p) {
			switch {
			case r.Kit == name:
				out = append(out, kitUse{Project: p, Role: r.Name})
			case r.Kit == "" && name == studio.DefaultStudioKitID:
				out = append(out, kitUse{Project: p, Role: r.Name, Implicit: true})
			}
		}
	}
	return out
}

func kitListRows(store jam.Store) []kitListRow {
	var out []kitListRow
	for _, k := range store.ListKits() {
		row := kitListRow{Name: k.Name, Current: k.Current, Count: len(k.Versions), UsedBy: len(kitUses(store, k.Name))}
		if sk, err := studio.ParseStudioKit([]byte(k.Versions[k.Current])); err == nil {
			row.Base, row.Egress = baseLabel(sk.Base), len(sk.Egress)
		} else {
			row.Invalid = true
		}
		out = append(out, row)
	}
	return out
}

// displayKit is sk as shown: no legacy name, and a packed context replaced by
// a one-line summary carrying its digest (so a diff still shows it changed).
func displayKit(sk studio.StudioKit) studio.StudioKit {
	sk.LegacyName = ""
	if sk.Base.Context != "" {
		sk.Base.Context = fmt.Sprintf("<packed tar.gz, %s, build digest %s>", sizeLabel(len(sk.Base.Context)), shortDigest(studio.BuildDigest(sk, harnessinstall.Default())))
	}
	return sk
}

func shortDigest(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

func sizeLabel(n int) string {
	if n < 1024 {
		return strconv.Itoa(n) + " B"
	}
	return strconv.Itoa((n+1023)/1024) + " KiB"
}

func toYAML(sk studio.StudioKit) string {
	b, err := yaml.Marshal(sk)
	if err != nil {
		return ""
	}
	return string(b)
}

// maxCtxEntries caps how many packed-context entries the page lists.
const maxCtxEntries = 500

// contextEntries lists a packed base.context's files from the tar headers only
// (no file data is read into memory, nothing is extracted).
func contextEntries(b64 string) ([]ctxEntry, string) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, "not valid base64"
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, "not a readable gzip"
	}
	tr := tar.NewReader(gz)
	var out []ctxEntry
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, ""
		}
		if err != nil {
			return out, "unreadable past this point"
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if len(out) == maxCtxEntries {
			return out, fmt.Sprintf("first %d entries shown", maxCtxEntries)
		}
		out = append(out, ctxEntry{Path: h.Name, Size: h.Size, Mode: fmt.Sprintf("%04o", h.Mode&0o7777)})
	}
}

func treePaths(t studio.ContextTree, prefix string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(t)) {
		n := t[k]
		if n.File != nil {
			out = append(out, prefix+k)
		} else {
			out = append(out, treePaths(n.Dir, prefix+k+"/")...)
		}
	}
	return out
}

// viewKit parses one stored version for display.
func viewKit(n int, stored string) kitView {
	v := kitView{N: n}
	sk, err := studio.ParseStudioKit([]byte(stored))
	if err != nil {
		v.ParseErr, v.Raw = err.Error(), stored
		return v
	}
	full := sk
	full.LegacyName = ""
	v.PushYAML = toYAML(full)
	v.YAML = toYAML(displayKit(sk))
	v.Base, v.Image, v.Digest = baseLabel(sk.Base), sk.Base.Image, studio.BuildDigest(sk, harnessinstall.Default())
	if len(sk.Base.ContextFiles) > 0 {
		v.Files = treePaths(sk.Base.ContextFiles, "")
	}
	if sk.Base.Context != "" {
		v.ContextSize = sizeLabel(len(sk.Base.Context))
		v.Context, v.ContextNote = contextEntries(sk.Base.Context)
	}
	v.Egress = sk.Egress
	v.Ceiling, v.Excluded = studio.Ceiling(sk.Egress)
	for _, k := range slices.Sorted(maps.Keys(sk.BuildArgs)) {
		v.BuildArgs = append(v.BuildArgs, kv{k, sk.BuildArgs[k]})
	}
	for _, k := range slices.Sorted(maps.Keys(sk.Secrets)) {
		v.Secrets = append(v.Secrets, kv{k, sk.Secrets[k].Description})
	}
	for _, k := range slices.Sorted(maps.Keys(sk.MCPServers)) {
		v.MCPServers = append(v.MCPServers, kv{k, sk.MCPServers[k].Type})
	}
	for _, n := range sk.Notes {
		v.Notes = append(v.Notes, kv{n.Name, n.ReadWhen})
	}
	v.Prompt = sk.Prompt
	return v
}

// buildKitDetail gathers the kit page for version view (0 = current), diffed
// against version diff when diff > 0. false when the kit doesn't exist.
func buildKitDetail(store jam.Store, name string, view, diff int) (kitDetail, bool) {
	k, ok := store.GetKit(name)
	if !ok {
		return kitDetail{}, false
	}
	if _, ok := k.Versions[view]; !ok {
		view = k.Current
	}
	d := kitDetail{Title: "Kits", Name: name, Current: k.Current, IsDefault: name == studio.DefaultStudioKitID, Uses: kitUses(store, name), Source: newSourceView(k.Source)}
	prev := 0
	for _, n := range slices.Sorted(maps.Keys(k.Versions)) {
		ver := kitVersion{N: n, Prev: prev, Current: n == k.Current}
		if m, ok := k.Meta[n]; ok {
			ver.Source = newSourceView(m.Source)
			if !m.PushedAt.IsZero() {
				ver.Pushed = m.PushedAt.UTC().Format("2006-01-02 15:04")
			}
		}
		prev = n
		if sk, err := studio.ParseStudioKit([]byte(k.Versions[n])); err == nil {
			ver.Digest = shortDigest(studio.BuildDigest(sk, harnessinstall.Default()))
		}
		d.Versions = append(d.Versions, ver)
	}
	d.View = viewKit(view, k.Versions[view])
	if d.View.ParseErr == "" {
		d.Chain, d.Effective, d.ChainErr = chainOf(store, name, k.Versions[view])
	}
	if src := k.Meta[view].Source; src != nil {
		d.SetWith = setParents(store, d.Chain, src)
	}
	follow, pinned := jam.KitsReferencing(store, name)
	for _, c := range follow {
		d.UsedByKits = append(d.UsedByKits, kitChild{Name: c})
	}
	for _, c := range pinned {
		d.UsedByKits = append(d.UsedByKits, kitChild{Name: c, Pinned: true})
	}
	slices.SortFunc(d.UsedByKits, func(a, b kitChild) int { return strings.Compare(a.Name, b.Name) })
	if from, ok := k.Versions[diff]; ok && diff != view {
		fv := viewKit(diff, from)
		a, b := fv.YAML, d.View.YAML
		if fv.ParseErr != "" {
			a = fv.Raw
		}
		if d.View.ParseErr != "" {
			b = d.View.Raw
		}
		lines, tooLarge := lineDiff(a, b)
		d.Diff = &kitDiff{From: diff, To: view, Lines: lines, TooLarge: tooLarge, DigestChanged: fv.Digest != d.View.Digest}
	}
	return d, true
}

// setParents names the chain parents whose resolved version was imported from
// own's repo at another dir, comma-joined: the kits a repo import brought in
// together with this one.
func setParents(store jam.Store, chain []kitParent, own *jam.KitSource) string {
	var names []string
	for _, p := range chain {
		if k, ok := store.GetKit(p.Name); ok {
			if src := k.Meta[p.Version].Source; src != nil && src.Repo == own.Repo && path.Clean("./"+src.Dir) != path.Clean("./"+own.Dir) {
				names = append(names, p.Name)
			}
		}
	}
	return strings.Join(names, ", ")
}

// chainOf resolves the chain of one stored kit version for display: its
// parents root first and, when there are any, the merged effective kit. A chain
// that doesn't resolve comes back as an error message.
func chainOf(store jam.Store, name, config string) (chain []kitParent, eff *kitView, errMsg string) {
	links, y, err := jam.KitChainView(store, name, config)
	if err != nil {
		return nil, nil, err.Error()
	}
	if len(links) == 0 {
		return nil, nil, ""
	}
	for _, l := range links {
		chain = append(chain, kitParent{Name: l.Name, Version: l.Version, Follow: l.Follow})
	}
	v := viewKit(0, y)
	if v.ParseErr != "" {
		return chain, nil, v.ParseErr
	}
	return chain, &v, ""
}

// lineDiff is the plan and version diff (linediff.Diff).
var lineDiff = linediff.Diff
