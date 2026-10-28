package adminui

import (
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// draftDriftConfirm is the publish form's box a config-managed kit's
// publish must tick (docs/usage/jam/kits-drafts-ui.md).
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
// each managed follower pinned. `jam config write <repo>` renders them.
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
