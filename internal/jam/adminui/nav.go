package adminui

import (
	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/condition"
)

// frameSource is what frameFor reads, carried on each request's context by
// Handler so the renderer can build the frame.
type frameSource struct {
	store jam.Store
	img   jam.ImageResolver
	conds *condition.Tracker
	// appTokenRevoke ends a revoked app token's requests (nil: none).
	appTokenRevoke func(ident.ID)
}

// items is every attention item: Jam's open operator conditions first, then
// attention()'s.
func (s frameSource) items() []attnItem {
	return append(conditionItems(s.conds.Open()), attention(s.store, s.img)...)
}
