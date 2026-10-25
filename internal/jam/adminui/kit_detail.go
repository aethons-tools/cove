package adminui

import (
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

// kv is a sorted key/value pair for build-args and secret demands.
type kv struct{ Key, Value string }

// kitView is one parsed version, ready to render.
type kitView struct {
	N          int
	ParseErr   string // the version is broken, or its kit.yml isn't a valid studio kit
	RawShown   string // an unparsable kit.yml as shown (maskKitYML; escape with SetupDisplayText)
	Masked     bool   // RawShown has base's packed context masked: differs from the stored text
	Hidden     bool   // RawShown is the placeholder alone: the stored text is not shown
	PushRaw    string // the push form's text when ParseErr is set: RawShown, or "" when Hidden
	YAML       string // display YAML
	PushYAML   string // full YAML, for pre-filling a new version
	Base       string
	Image      string
	Egress     []string
	Ceiling    []string
	Excluded   []string
	BuildArgs  []kv
	Secrets    []kv
	MCPServers []kv // name → type
	Notes      []kv // name → read-when
	Prompt     string
	Digest     string

	ctx      *ctxRead // the version's tree, read once; nil = none
	diffText string   // what a version diff compares
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
	Source      *sourceView  // tracked source; nil = not tracked
	SetWith     string       // chain parents imported from the same repo, comma-joined; "" = not part of a set
	CanSource   bool         // import/track/sync controls are available
	Ctx         *ctxListView // the viewed version's files; nil = none
	CtxDiff     *ctxDiffView // the two versions' files compared; nil = no diff or no tree
	File        *ctxFileView // one file (?file=); nil = none asked
	NotFound    bool
	NotFoundFor string
}

// baseLabel names a kit's base: its form, and for a chained kit the parent
// it is built on ("kit: web@v2", plus " + Dockerfile" for its own stage).
func baseLabel(sk studio.StudioKit) string {
	b := sk.Base
	own := "blessed default"
	if sk.BuildContext != "" {
		own = "Dockerfile"
	}
	k, err := b.Kind()
	switch {
	case err != nil:
		return "invalid"
	case k == studio.BaseKit && sk.BuildContext == "":
		return "kit: " + b.Kit
	case b.HasParent():
		if sk.BuildContext == "" {
			own = ownBaseLabel(k)
		}
		return "kit: " + b.Kit + " + " + own
	case sk.BuildContext != "":
		return own
	}
	return ownBaseLabel(k)
}

func ownBaseLabel(k studio.BaseKind) string {
	switch k {
	case studio.BaseImage:
		return "image"
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
		row := kitListRow{Name: k.Name, Current: k.Current, Count: len(k.VersionNumbers()), UsedBy: len(kitUses(store, k.Name))}
		if cur, ok := jam.LoadKitVersion(store, k.Name, 0); ok && cur.Err == nil {
			row.Base, row.Egress = baseLabel(cur.Kit), len(cur.Kit.Egress)
		} else {
			row.Invalid = true
		}
		out = append(out, row)
	}
	return out
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

// viewKit renders stored version v (number n): its kit.yml as stored and
// its files. A version with no tree (broken, or pre-tree on a store that
// failed to migrate) shows only its own fixed error; a kit.yml that does not
// parse shows its error and the text with any packed context hidden
// (maskKitYML).
func viewKit(n int, v jam.KitVersion, unpack bool, want string) kitView {
	if v.TreeHash == "" {
		msg := "version has no tree"
		if v.Err != nil {
			msg = v.Err.Error()
		}
		return kitView{N: n, ParseErr: msg}
	}
	kv := kitView{N: n, ctx: treeRead(v, want, unpack)}
	text := string(v.KitYML)
	if v.Err != nil {
		shown, how := maskKitYML(text)
		kv.ParseErr, kv.RawShown, kv.diffText = v.Err.Error(), shown, shown
		kv.Masked, kv.Hidden = how == maskPartial, how == maskWhole
		if !kv.Hidden {
			kv.PushRaw = shown
		}
		return kv
	}
	kv.YAML, kv.PushYAML, kv.diffText = jam.SetupDisplayText(text), text, text
	fillKitView(&kv, v.Kit)
	return kv
}

// viewYAML renders a chain's merged YAML y for display (the Effective card).
func viewYAML(n int, y string) kitView {
	v := kitView{N: n}
	sk, err := studio.ParseStudioKit([]byte(y))
	if err != nil {
		v.ParseErr = err.Error()
		return v
	}
	fillKitView(&v, sk)
	return v
}

// fillKitView sets v's fields read from parsed kit sk: base, digest, egress,
// build args, secrets, MCP servers, notes and prompt.
func fillKitView(v *kitView, sk studio.StudioKit) {
	v.Base, v.Image, v.Digest = baseLabel(sk), sk.Base.Image, studio.BuildDigest(sk, harnessinstall.Default())
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
}

// buildKitDetail gathers the kit page for version view (0 = current), diffed
// against version diff when diff > 0, showing the file named file when it is
// non-empty (File.Missing when neither version has it). false
// when the kit doesn't exist.
func buildKitDetail(store jam.Store, name string, view, diff int, file string) (kitDetail, bool) {
	k, ok := store.GetKit(name)
	if !ok {
		return kitDetail{}, false
	}
	if !k.HasVersion(view) {
		view = k.Current
	}
	d := kitDetail{Title: "Kits", Name: name, Current: k.Current, IsDefault: name == studio.DefaultStudioKitID, Uses: kitUses(store, name), Source: newSourceView(k.Source)}
	prev := 0
	for _, n := range k.VersionNumbers() {
		ver := kitVersion{N: n, Prev: prev, Current: n == k.Current}
		if m, ok := k.Meta[n]; ok {
			ver.Source = newSourceView(m.Source)
			if !m.PushedAt.IsZero() {
				ver.Pushed = m.PushedAt.UTC().Format("2006-01-02 15:04")
			}
		}
		prev = n
		if kv, _ := jam.LoadKitVersion(store, name, n); kv.Err == nil {
			ver.Digest = shortDigest(studio.BuildDigest(kv.Kit, harnessinstall.Default()))
		}
		d.Versions = append(d.Versions, ver)
	}
	viewVer, _ := jam.LoadKitVersion(store, name, view)
	diffing := k.HasVersion(diff) && diff != view
	d.View = viewKit(view, viewVer, true, file)
	if d.View.ParseErr == "" {
		d.Chain, d.Effective, d.ChainErr = chainOf(store, name, viewVer.Kit)
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
	d.Ctx = ctxList(name, d.View)
	if !diffing {
		if file != "" {
			d.File = fileView(file, nil, d.View.ctx, false)
		}
		return d, true
	}
	fromVer, _ := jam.LoadKitVersion(store, name, diff)
	fv := viewKit(diff, fromVer, true, file)
	lines, tooLarge := lineDiff(fv.diffText, d.View.diffText)
	for i := range lines { // a tree's kit.yml is shown as stored: make its control and format characters visible
		lines[i].Text = jam.SetupDisplayText(lines[i].Text)
	}
	d.Diff = &kitDiff{From: diff, To: view, Lines: lines, TooLarge: tooLarge, DigestChanged: fv.Digest != d.View.Digest}
	d.CtxDiff = ctxDiff(name, fv, d.View)
	if file != "" {
		d.File = fileView(file, fv.ctx, d.View.ctx, true)
		d.File.From, d.File.To = diff, view
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
func chainOf(store jam.Store, name string, sk studio.StudioKit) (chain []kitParent, eff *kitView, errMsg string) {
	links, y, err := jam.KitChainView(store, name, sk)
	if err != nil {
		return nil, nil, err.Error()
	}
	if len(links) == 0 {
		return nil, nil, ""
	}
	for _, l := range links {
		chain = append(chain, kitParent{Name: l.Name, Version: l.Version, Follow: l.Follow})
	}
	v := viewYAML(0, y)
	if v.ParseErr != "" {
		return chain, nil, v.ParseErr
	}
	return chain, &v, ""
}

// lineDiff is the plan and version diff (linediff.Diff).
var lineDiff = linediff.Diff
