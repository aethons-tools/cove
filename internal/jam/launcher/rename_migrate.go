package launcher

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/legacynames"
	"github.com/aethons-tools/cove/internal/naming"
)

// The jam-rename migration (docs/usage/jam/renamed-from-at.md) moves this
// Jam's studios off their pre-rename docker names at serve start.

// MigratedFromLabel marks a state volume the jam-rename migration created;
// its value is the pre-rename volume it was copied from.
const MigratedFromLabel = "jam.studio.migrated-from"

// MigrationLabel, valued MigrationIncomplete, marks a copy in progress. Docker
// volume labels cannot change, so the marker is its own small volume
// (migrationMarker of the new one, also carrying MigratedFromLabel): created
// before the new volume, removed only after its copy verified. A new-named
// volume is a partial copy (safe to remove and redo) only while its marker
// exists; one without is complete, and is never deleted.
const (
	MigrationLabel      = "jam.studio.migration"
	MigrationIncomplete = "incomplete"
)

// migrationMarker is the marker volume of a copy into new volume nw.
func migrationMarker(nw string) string { return nw + "-migration" }

// ErrNoCopyImage is MigrateStateVolumes' error (wrapped by noCopyImage)
// when no image that can run the copy is on the host. Nothing is pulled at
// serve start: the operator pulls the jam base and starts again.
var ErrNoCopyImage = errors.New("migration: no image on this host can copy the state volumes (nothing was moved)")

// noCopyImage is ErrNoCopyImage with the pull command for the jam base this
// migration would use, on this launcher's docker context.
func (l *Launcher) noCopyImage() error {
	docker := "docker"
	if l.cfg.DockerContext != "" {
		docker += " --context " + l.cfg.DockerContext
	}
	return fmt.Errorf("%w; pull the jam base — %s pull %s — and start jam serve again", ErrNoCopyImage, docker, basedigest.DefaultRef())
}

// DockerContext is the docker context this launcher's docker calls are
// pinned to (Config.DockerContext).
func (l *Launcher) DockerContext() string { return l.cfg.DockerContext }

// StudioLocation is actorID's container name: the Location Raise records.
func (l *Launcher) StudioLocation(actorID string) string { return naming.StudioContainer(actorID) }

// RetireLegacyStudio removes actorID's pre-rename container (it runs an image
// whose agent cannot attach to this Jam), its -docker cache volume and its
// pinned known_hosts file. Absent ones are fine; its state volumes are
// MigrateStateVolumes'.
func (l *Launcher) RetireLegacyStudio(ctx context.Context, actorID string) error {
	old := legacynames.Container(actorID)
	st, err := l.cfg.Ops.GetStatus(old)
	if err != nil {
		return fmt.Errorf("migration: inspect %s: %w", old, err)
	}
	return l.retireContainer(old, st != backend.StateAbsent)
}

// RetireLegacyContainers retires this Jam's pre-rename containers left on
// the docker host, as RetireLegacyStudio does. Candidates are found by the
// pre-rename label and by the pre-rename name prefix (so one the store no
// longer tracks is found too); one is this Jam's when its name is the
// pre-rename name of one of ids (the store's instance and standing session
// ids), or it mounts a pre-rename state volume labelled with this Jam's id.
// Any other — another Jam's, on a shared docker host — is left running and
// named in one warning. Each retired one's image is kept as the first
// candidate to copy the state volumes with (it is on the host). It returns
// the names retired, sorted.
func (l *Launcher) RetireLegacyContainers(ctx context.Context, ids []string) ([]string, error) {
	found := map[string]backend.ContainerInfo{}
	for _, filter := range []string{"label=" + legacynames.Label, "name=" + legacynames.ContainerPrefix} {
		got, err := l.cfg.Ops.FindContainers(filter)
		if err != nil {
			return nil, fmt.Errorf("migration: list pre-rename containers: %w", err)
		}
		for name, info := range got {
			// docker's name filter matches a substring: keep the prefixed ones.
			if strings.HasPrefix(filter, "label=") || strings.HasPrefix(name, legacynames.ContainerPrefix) {
				found[name] = info
			}
		}
	}
	if len(found) == 0 {
		return nil, nil
	}
	mine := map[string]bool{}
	for _, id := range ids {
		mine[legacynames.Container(id)] = true
	}
	ourVols, err := l.cfg.Ops.ListVolumes(legacynames.StateLabel, legacynames.JamLabel+"="+l.cfg.JamID)
	if err != nil {
		return nil, fmt.Errorf("migration: list pre-rename volumes: %w", err)
	}
	var retired, foreign []string
	for _, name := range slices.Sorted(maps.Keys(found)) {
		info := found[name]
		if !mine[name] && !slices.ContainsFunc(info.Mounts, func(v string) bool { _, ok := ourVols[v]; return ok }) {
			foreign = append(foreign, name)
			continue
		}
		if info.Image != "" {
			l.mu.Lock()
			if !slices.Contains(l.copyImages, info.Image) {
				l.copyImages = append(l.copyImages, info.Image)
			}
			l.mu.Unlock()
		}
		if err := l.retireContainer(name, true); err != nil {
			return nil, err
		}
		retired = append(retired, name)
	}
	if len(foreign) > 0 {
		l.cfg.Log.Warn("migration: pre-rename containers not owned by this Jam left running: "+strings.Join(foreign, ", "), "containers", foreign)
	}
	return retired, nil
}

// retireContainer removes pre-rename container old (when present), then
// best-effort its -docker cache and known_hosts pin.
func (l *Launcher) retireContainer(old string, present bool) error {
	if present {
		if err := l.cfg.Ops.RemoveContainer(old); err != nil {
			return fmt.Errorf("migration: remove %s: %w", old, err)
		}
	}
	if err := l.cfg.Ops.RemoveVolumes(naming.DockerVolume(old)); err != nil {
		l.cfg.Log.Warn("migration: removing a pre-rename docker cache failed", "volume", naming.DockerVolume(old), "error", err.Error())
	}
	if err := os.Remove(filepath.Join(l.cfg.KnownHostsDir, old)); err != nil && !errors.Is(err, os.ErrNotExist) {
		l.cfg.Log.Warn("migration: removing a pre-rename known_hosts pin failed", "file", old, "error", err.Error())
	}
	if present {
		l.cfg.Log.Info("pre-rename studio retired", "container", old)
	}
	return nil
}

// MigrateStateVolumes copies this Jam's pre-rename state volumes to their new
// names: every volume carrying the old state label and this Jam's old Jam
// label, plus the agent-data/workspace volumes of each actor in also that
// exist unlabelled. Each copy goes, under an incomplete marker
// (MigrationLabel), into a fresh volume labelled as state and
// MigratedFromLabel; the copy is verified, the marker cleared, and only then
// is the old volume removed. Any failure stops the migration (serve start
// fails closed: nothing raises onto an empty volume while its data sits in
// the old one), with the old volume left in place. A retry redoes a partial
// copy (its marker still there) and never deletes or overwrites any other
// new-named volume — one the migration completed (a rollback's old volume
// beside it is for the operator to settle) or did not create. It returns
// "old → new" per volume moved.
func (l *Launcher) MigrateStateVolumes(ctx context.Context, also []string) ([]string, error) {
	owners, err := l.cfg.Ops.ListVolumes(legacynames.StateLabel, legacynames.JamLabel+"="+l.cfg.JamID)
	if err != nil {
		return nil, fmt.Errorf("migration: list pre-rename volumes: %w", err)
	}
	for _, id := range also {
		for _, v := range stateVolumes(legacynames.Container(id)) {
			if _, seen := owners[v]; seen {
				continue
			}
			ok, err := l.cfg.Ops.VolumeExists(v)
			if err != nil {
				return nil, fmt.Errorf("migration: inspect volume %s: %w", v, err)
			}
			if ok {
				owners[v] = id
			}
		}
	}
	var done []string
	image := ""
	for _, old := range slices.Sorted(maps.Keys(owners)) {
		id := owners[old]
		nw, ok := renamedStateVolume(old, id)
		if !ok {
			if old == naming.DockerVolume(legacynames.Container(id)) {
				_ = l.cfg.Ops.RemoveVolumes(old) // a cache once labelled as state: never copied
			} else {
				l.cfg.Log.Warn("migration: pre-rename volume with an unknown name left in place", "volume", old, "id", id)
			}
			continue
		}
		if image == "" {
			if image, err = l.copyImage(); err != nil {
				return done, err
			}
		}
		l.cfg.Log.Info("migration: copying volume "+old+" → "+nw, "image", image)
		if err := l.moveVolume(image, id, old, nw); err != nil {
			return done, err
		}
		done = append(done, old+" → "+nw)
	}
	if len(done) > 0 {
		l.cfg.Log.Info("state volumes migrated to the jam names", "volumes", done)
	}
	return done, nil
}

// moveVolume copies old into nw (id's) under the incomplete marker, then
// removes old: MigrateStateVolumes' one volume.
func (l *Launcher) moveVolume(image, id, old, nw string) error {
	failed := fmt.Errorf("migration: volume %s could not be copied to %s; left in place", old, nw)
	why := func(reason string, err error) error {
		args := []any{"from", old, "to", nw}
		if err != nil {
			args = append(args, "error", err.Error())
		}
		l.cfg.Log.Warn("migration: "+reason, args...)
		return failed
	}
	marker := migrationMarker(nw)
	exists, err := l.cfg.Ops.VolumeExists(nw)
	if err != nil {
		return why("inspecting the new volume failed", err)
	}
	if exists {
		partial, err := l.partialCopy(old, nw, marker)
		if err != nil {
			return why("reading the migration labels failed", err)
		}
		if !partial {
			return why("the new volume already exists and is not a partial copy of the old one; neither was changed", nil)
		}
		if err := l.cfg.Ops.RemoveVolumes(nw); err != nil {
			return why("removing a partial copy failed", err)
		}
	}
	if err := l.cfg.Ops.CreateVolume(marker, MigrationLabel+"="+MigrationIncomplete, MigratedFromLabel+"="+old); err != nil {
		return why("creating the migration marker failed", err)
	}
	if err := l.cfg.Ops.CreateVolume(nw, StateLabel+"="+id, JamLabel+"="+l.cfg.JamID, MigratedFromLabel+"="+old); err != nil {
		return why("creating the new volume failed", err)
	}
	if err := l.cfg.Ops.CopyVolume(image, old, nw); err != nil {
		// The partial copy goes; its marker only once it has, so a failed
		// removal leaves it recognisable as partial.
		if rmErr := l.cfg.Ops.RemoveVolumes(nw); rmErr == nil {
			_ = l.cfg.Ops.RemoveVolumes(marker)
		}
		return why("the copy failed", err)
	}
	if err := l.cfg.Ops.RemoveVolumes(marker); err != nil {
		return why("clearing the migration marker failed (the copy is redone next start)", err)
	}
	// The marker is cleared first: a marker outliving its old volume would
	// let a later start (after a rollback recreated the old name) take the
	// completed copy for a partial one.
	if err := l.cfg.Ops.RemoveVolumes(old); err != nil {
		return why("the copy verified, but the old volume could not be removed; remove it by hand once the new one is checked", err)
	}
	return nil
}

// partialCopy reports whether existing new volume nw is the migration's
// unfinished copy of old: it carries MigratedFromLabel=old and its marker
// volume, MigrationIncomplete and copied from old, still exists.
func (l *Launcher) partialCopy(old, nw, marker string) (bool, error) {
	from, err := l.cfg.Ops.ListVolumes(MigratedFromLabel)
	if err != nil {
		return false, err
	}
	if from[nw] != old || from[marker] != old {
		return false, nil
	}
	state, err := l.cfg.Ops.ListVolumes(MigrationLabel)
	if err != nil {
		return false, err
	}
	return state[marker] == MigrationIncomplete, nil
}

// copyImage is the first image already on the host that can run the volume
// copy: the image of a pre-rename studio retired this run (on the host by
// construction), the jam base (blessed, newest first), then the pre-rename
// base at the same digests. An image check that errors is logged and the
// next tried. Nothing is pulled: none on the host is ErrNoCopyImage.
func (l *Launcher) copyImage() (string, error) {
	l.mu.Lock()
	candidates := slices.Clone(l.copyImages)
	l.mu.Unlock()
	candidates = append(candidates, basedigest.BlessedRefs()...)
	for _, d := range basedigest.Blessed() {
		candidates = append(candidates, legacynames.BaseImageRepo+"@"+d)
	}
	for _, img := range candidates {
		ok, err := l.cfg.Ops.HasKitImage(img)
		if err != nil {
			l.cfg.Log.Warn("migration: checking a copy image failed; trying the next", "image", img, "error", err.Error())
			continue
		}
		if ok {
			return img, nil
		}
	}
	return "", l.noCopyImage()
}

// renamedStateVolume is old's new name when old is one of id's pre-rename
// state volumes.
func renamedStateVolume(old, id string) (string, bool) {
	oc, nc := legacynames.Container(id), naming.StudioContainer(id)
	switch old {
	case naming.AgentDataVolume(oc):
		return naming.AgentDataVolume(nc), true
	case naming.WorkspaceVolume(oc):
		return naming.WorkspaceVolume(nc), true
	}
	return "", false
}
