package adminui

import (
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// managedView is a managed object's badge: the config commit its mark was
// applied from (7 chars; "dir" for a --dir apply) and when.
type managedView struct {
	Commit, Applied string
}

// configTime is a config apply's time as the UI shows it.
func configTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// managedOf is kind/name's badge; nil when the config repo does not manage it.
func managedOf(marks map[jam.ConfigKey]jam.ConfigMark, kind, name string) *managedView {
	mk, ok := marks[jam.ConfigKey{Kind: kind, Name: name}]
	if !ok {
		return nil
	}
	return &managedView{Commit: jam.ShortConfigCommit(mk.Commit), Applied: configTime(mk.AppliedAt)}
}

// driftNoteOf is the warning a write to managed kind/name confirms before it
// is sent; "" for an unmanaged object.
func driftNoteOf(marks map[jam.ConfigKey]jam.ConfigMark, kind, name string) string {
	m := managedOf(marks, kind, name)
	if m == nil {
		return ""
	}
	return "This will drift from the config repo (managed · config @" + m.Commit + ")."
}
