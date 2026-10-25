package adminui

import (
	"fmt"
	"log/slog"
	"net/http"
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

// managedFollowersToPin lists the config-managed kits a publish of kit with
// Pin followers would rewrite (jam.KitFollowersToPin): pinning drifts them.
func managedFollowersToPin(store jam.Store, kit string) []string {
	var out []string
	for _, f := range jam.KitFollowersToPin(store, kit) {
		if jam.KitManaged(store, f) {
			out = append(out, f)
		}
	}
	return out
}

// configFiles lists the config-repo paths a drifting publish changed, paths
// only: when kit is managed, kits/<kit>/<path> for each file that differs
// from version prev (current before the publish); and kits/<f>/kit.yml for
// each managed follower pinned. `at-jam config write <repo>` renders them.
func configFiles(store jam.Store, kit string, prev int, res jam.KitDraftPublish) []configFile {
	var out []configFile
	if jam.KitManaged(store, kit) {
		k, _ := store.GetKit(kit)
		before, _ := store.KitTree(k.Trees[prev])
		after, _ := store.KitTree(res.Tree)
		changes, _ := kittree.Changes(before, after)
		for _, c := range changes {
			out = append(out, configFile{Path: pathText("kits/" + kit + "/" + c.Path), Change: c.Kind})
		}
	}
	for _, pin := range res.Pinned {
		if f, _, _ := strings.Cut(pin, " "); jam.KitManaged(store, f) {
			out = append(out, configFile{Path: pathText("kits/" + f + "/kit.yml"), Change: kittree.ChangeModified})
		}
	}
	return out
}

// registerDraftPublish mounts Publish: jam.PublishKitDraft with the form's
// etag and "pin followers" box. It refuses a draft with problems (400) even
// though the page disables the button, and without the drift box ticked
// (400) a config-managed kit's publish or one whose Pin followers would
// rewrite a config-managed follower. The box is checked before the draft's
// existence and ETag (docs/usage/jam/kits-drafts.md#in-the-admin-ui).
func registerDraftPublish(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/publish", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		pin := r.FormValue("pin") != ""
		if r.FormValue("drift") == "" {
			if jam.KitManaged(store, name) {
				renderError(w, http.StatusBadRequest, fmt.Sprintf("kit %s is managed by the config repo: tick “%s” to publish", name, draftDriftConfirm))
				return
			}
			if managed := managedFollowersToPin(store, name); pin && len(managed) > 0 {
				renderError(w, http.StatusBadRequest, fmt.Sprintf("pinning followers drifts config-managed %s from the config repo: tick “%s” to publish", strings.Join(managed, ", "), draftDriftConfirm))
				return
			}
		}
		k, _ := store.GetKit(name)
		prev := k.Current
		res, err := jam.PublishKitDraft(store, log, name, pin, r.FormValue("etag"))
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
