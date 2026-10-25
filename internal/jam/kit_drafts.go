package jam

import (
	"fmt"
	"time"

	"github.com/aethons-tools/cove/internal/kittree"
)

// KitDraft is a kit's one open draft (docs/usage/jam/kits-drafts.md): a
// mutable tree opened from version Base. Its tree and blobs live with the
// versions' (kit_trees, kit_blobs) and count as referenced for GC. A draft
// is working state: never in a config snapshot, an export or a config plan.
type KitDraft struct {
	Kit       string    `json:"kit"`
	Base      int       `json:"base"` // the version it was opened from
	Tree      string    `json:"tree"` // the draft tree's sha256: its ETag
	OpenedAt  time.Time `json:"opened_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Actor     string    `json:"actor"` // who last wrote it (opened or saved)
}

// GetKitDraft returns kit's open draft.
func (m *memState) GetKitDraft(kit string) (KitDraft, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.kitDrafts[kit]
	return d, ok
}

// checkKitDraft refuses a draft write that does not hold together: an
// unknown kit (ErrNotFound) or base version, objects failing the file rules
// (kittree.Tree.ValidateFiles: a draft may lack kit.yml), or a tree neither
// written nor stored. Caller holds mu.
func checkKitDraft(m *memState, d KitDraft, objs KitObjects) error {
	k, ok := m.kits[d.Kit]
	if !ok {
		return fmt.Errorf("kit %q: %w", d.Kit, ErrNotFound)
	}
	if !k.HasVersion(d.Base) {
		return fmt.Errorf("kit %s has no version %d", d.Kit, d.Base)
	}
	written, err := checkObjects(m, objs, kittree.Tree.ValidateFiles)
	if err != nil {
		return err
	}
	if _, stored := m.kitTrees[d.Tree]; !written[d.Tree] && !stored {
		return fmt.Errorf("kit %s draft: tree %.12s is not stored", d.Kit, d.Tree)
	}
	return nil
}

func (m *memState) applyPutKitDraft(d KitDraft) { m.kitDrafts[d.Kit] = d }

func (m *memState) applyRemoveKitDraft(kit string) bool {
	if _, ok := m.kitDrafts[kit]; !ok {
		return false
	}
	delete(m.kitDrafts, kit)
	return true
}
