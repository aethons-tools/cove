package jam

import (
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
)

// HomeSession is channel ch's live session: a session channel's own, or the
// live session working a ticket channel's unit in its project (ok=false: none).
func HomeSession(st Store, ch ident.ID) (Instance, bool) {
	c, ok := st.GetChannel(ch)
	if !ok {
		return Instance{}, false
	}
	live := func(i Instance) bool { return i.Phase != PhaseGone }
	switch c.Kind {
	case SourceSession:
		inst, ok := st.GetInstance(c.Key)
		return inst, ok && live(inst)
	case SourceTicket:
		_, unit, found := strings.Cut(c.Key, "/")
		if !found {
			return Instance{}, false
		}
		for _, inst := range st.ListInstances() {
			if inst.Unit == unit && live(inst) {
				if pr, ok := st.GetProject(inst.Project); ok && pr.ID == c.ProjectID {
					return inst, true
				}
			}
		}
	}
	return Instance{}, false
}
