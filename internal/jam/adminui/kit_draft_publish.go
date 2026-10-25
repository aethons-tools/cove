package adminui

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// draftDriftConfirm is the publish form's box a config-managed kit's
// publish must tick (docs/usage/jam/kits-drafts.md#in-the-admin-ui).
const draftDriftConfirm = "I understand this drifts from the config repo"

// publishView is a publish's result banner: the new version, the followers
// pinned, the warning(s), and for a drifting publish the files to commit.
type publishView struct {
	Version, Base int
	Pinned        []string
	Warning       string
	Drift         bool
	ConfigFiles   []configFile
}

// configFile is one config-repo path a publish changed, and how.
type configFile struct{ Path, Change string }

func newPublishView(store jam.Store, kit string, prev int, res jam.KitDraftPublish) *publishView {
	v := &publishView{Version: res.Version, Base: res.Base, Pinned: res.Pinned, Warning: res.Warning, Drift: res.Drift}
	if res.Drift {
		v.ConfigFiles = configFiles(store, kit, prev, res)
	}
	return v
}

// kitManaged reports whether the config repo manages kit name.
func kitManaged(marks []jam.ConfigMark, name string) bool {
	return slices.ContainsFunc(marks, func(m jam.ConfigMark) bool { return m.Kind == jam.ConfigKindKit && m.Name == name })
}

// configFiles lists the config-repo paths a drifting publish changed, paths
// only: when kit is managed, kits/<kit>/<path> for each file that differs
// from version prev (current before the publish); and kits/<f>/kit.yml for
// each managed follower pinned. `at-jam config write <repo>` renders them.
func configFiles(store jam.Store, kit string, prev int, res jam.KitDraftPublish) []configFile {
	marks := store.ListConfigMarks()
	var out []configFile
	if kitManaged(marks, kit) {
		k, _ := store.GetKit(kit)
		before, _ := store.KitTree(k.Trees[prev])
		after, _ := store.KitTree(res.Tree)
		changes, _ := kittree.Changes(before, after)
		for _, c := range changes {
			out = append(out, configFile{Path: pathText("kits/" + kit + "/" + c.Path), Change: c.Kind})
		}
	}
	for _, pin := range res.Pinned {
		if f, _, _ := strings.Cut(pin, " "); kitManaged(marks, f) {
			out = append(out, configFile{Path: pathText("kits/" + f + "/kit.yml"), Change: kittree.ChangeModified})
		}
	}
	return out
}

// registerDraftPublish mounts Publish: jam.PublishKitDraft with the form's
// etag and "pin followers" box. It refuses a draft with problems (400) even
// though the page disables the button, and a config-managed kit's publish
// without the drift box ticked (400).
func registerDraftPublish(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/publish", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		if r.FormValue("drift") == "" && kitManaged(store.ListConfigMarks(), name) {
			renderError(w, http.StatusBadRequest, fmt.Sprintf("kit %s is managed by the config repo: tick “%s” to publish", name, draftDriftConfirm))
			return
		}
		k, _ := store.GetKit(name)
		prev := k.Current
		res, err := jam.PublishKitDraft(store, log, name, r.FormValue("pin") != "", r.FormValue("etag"))
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		log.Info("ui kit draft published", "operator", jam.OperatorID(r), "kit", name, "version", res.Version, "base", res.Base, "tree", shortDigest(res.Tree), "pinned", len(res.Pinned), "drift", res.Drift)
		p, ok := loadDraftPage(w, r, store, log, name, draftQuery{})
		if !ok {
			return
		}
		p.Published = newPublishView(store, name, prev, res)
		writeDraftBody(w, r, p, fmt.Sprintf("published %s v%d", name, res.Version))
	})
}
