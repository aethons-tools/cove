package adminui

import "github.com/aethons-tools/cove/internal/jam"

// publishView is a publish's result banner: the new version, the followers
// pinned and the warning(s).
type publishView struct {
	Version, Base int
	Pinned        []string
	Warning       string
}

func newPublishView(res jam.KitDraftPublish) *publishView {
	return &publishView{Version: res.Version, Base: res.Base, Pinned: res.Pinned, Warning: res.Warning}
}
