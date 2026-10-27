package jam

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/legacynames"
)

// StudioRenamer is the launcher surface the studio-name migration drives
// (the local launcher; *launcher.Launcher).
type StudioRenamer interface {
	RetireLegacyContainers(ctx context.Context, ids []string) ([]string, error)
	RetireLegacyStudio(ctx context.Context, actorID string) error
	MigrateStateVolumes(ctx context.Context, also []string) ([]string, error)
	StudioLocation(actorID string) string
}

// StudioRenameMigration reports a MigrateStudioNames run.
type StudioRenameMigration struct {
	Retired []string // instances whose pre-rename container was retired
	Volumes []string // "old → new" per state volume moved
}

// LocalManagerName is fleet.LocalName, the in-process manager's name, which
// this package cannot import (cmd/jam's test pins the two equal).
const LocalManagerName = "local"

// localManager reports whether an instance or standing session raised by
// manager is the local launcher's ("" = raised before managers).
func localManager(manager string) bool { return manager == "" || manager == LocalManagerName }

// MigrateStudioNames is the jam rename's studio migration
// (docs/usage/jam/renamed-from-at.md), run at serve start before the
// supervisor, the standing reconciler or any egress or SSH CA push can reach
// a studio. This Jam's pre-rename containers on the local docker host are
// retired first (an old image's agent can never attach to this Jam): those
// named for its local instances (not gone) and standing sessions, or
// mounting its pre-rename state volumes; another Jam's are left. Then each local
// instance not gone whose Location is a pre-rename container is retired
// (its cache and known hosts pin removed) and rewritten — the new container
// name as Location, ImageTag and KitDraftImage cleared (stale), Phase lost
// so the supervisor's next reconcile finishes its teardown. One row per
// instance, each after its own docker side effect, so a retry is
// idempotent. Then every standing session's state volumes (and any this Jam
// labelled) move to their new names. Any failure stops it: serve start fails
// closed. Logs carry names and counts only.
func MigrateStudioNames(ctx context.Context, store Store, r StudioRenamer, log *slog.Logger) (StudioRenameMigration, error) {
	var rep StudioRenameMigration
	var standing, owned []string
	for _, ref := range store.ListStandingSessions() {
		if localManager(ref.Manager) {
			standing = append(standing, ref.SessionID)
		}
	}
	instances := store.ListInstances()
	for _, inst := range instances {
		if localManager(inst.Manager) && inst.Phase != PhaseGone {
			owned = append(owned, inst.ActorID)
		}
	}
	owned = append(owned, standing...)
	slices.Sort(owned)
	containers, err := r.RetireLegacyContainers(ctx, slices.Compact(owned))
	if err != nil {
		return rep, err
	}
	for _, inst := range instances {
		if !localManager(inst.Manager) {
			continue
		}
		if inst.SessionKind == SessionKindStanding {
			standing = append(standing, inst.ActorID)
		}
		if inst.Phase == PhaseGone || !strings.HasPrefix(inst.Location, legacynames.ContainerPrefix) {
			continue
		}
		if err := r.RetireLegacyStudio(ctx, inst.ActorID); err != nil {
			return rep, err
		}
		inst.Location = r.StudioLocation(inst.ActorID)
		inst.ImageTag, inst.KitDraftImage = "", ""
		if inst.Phase != PhaseTerminating {
			inst.Phase = PhaseLost
		}
		if err := store.PutInstance(inst); err != nil {
			return rep, fmt.Errorf("migration: studio %s: %w", inst.ActorID, err)
		}
		rep.Retired = append(rep.Retired, inst.ActorID)
	}
	slices.Sort(standing)
	vols, err := r.MigrateStateVolumes(ctx, slices.Compact(standing))
	rep.Volumes = vols
	if err != nil {
		return rep, err
	}
	if log != nil && (len(containers) > 0 || len(rep.Retired) > 0 || len(rep.Volumes) > 0) {
		log.Info("studios migrated to the jam names", "containers", len(containers), "instances", rep.Retired,
			"volumes", len(rep.Volumes), "see", "docs/usage/jam/renamed-from-at.md")
	}
	return rep, nil
}
