package fleet

import "github.com/aethons-tools/cove/internal/jam"

// LocalName is the reserved name of the in-process manager. Instances raised
// before managers existed carry no manager name and route here.
const LocalName = "local"

type local struct {
	jam.Launcher
}

// Local wraps l as the manager named LocalName, always available. The router
// reaches l's optional interfaces (StateKeeper, ImageTagger, KitDraftBuilder)
// through Unwrap.
func Local(l jam.Launcher) Manager { return local{Launcher: l} }

func (local) Name() string    { return LocalName }
func (local) Available() bool { return true }

// Unwrap returns the wrapped launcher.
func (l local) Unwrap() jam.Launcher { return l.Launcher }
