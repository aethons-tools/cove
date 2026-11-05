package jam

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrManagerExists reports that a manager name is already registered.
var ErrManagerExists = errors.New("manager already registered")

// LocalManager is the legacy name of the old in-process manager, removed in
// plan 6 and never registered now; records with Manager "" or "local" are
// reaped on cutover. The name stays reserved.
const LocalManager = "local"

var managerNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidManagerName reports whether name may be registered as a Studio
// Manager. "local" is reserved (the legacy in-process manager's name).
func ValidManagerName(name string) error {
	if name == LocalManager {
		return fmt.Errorf("manager name %q is reserved", name)
	}
	if !managerNameRE.MatchString(name) {
		return fmt.Errorf("invalid manager name %q", name)
	}
	return nil
}

// ManagerRecord is one registered Studio Manager; only its token's hash is
// stored.
type ManagerRecord struct {
	Name      string
	TokenHash string
	CreatedAt time.Time
}

// ManagerStore is the Store's manager registry.
type ManagerStore interface {
	// AddManager registers m: ErrManagerExists when the name is taken; an
	// error for an invalid name, an empty token hash, or a token hash another
	// manager already holds.
	AddManager(m ManagerRecord) error
	// ListManagers lists registrations by CreatedAt, then Name.
	ListManagers() []ManagerRecord
	// ManagerByTokenHash finds the registration holding that token hash.
	ManagerByTokenHash(hash string) (ManagerRecord, bool)
	// SetManagerToken replaces name's token hash (wraps ErrNotFound; errors on an
	// empty hash or one another manager holds).
	SetManagerToken(name, tokenHash string) error
	// RemoveManager deletes name's registration (wraps ErrNotFound).
	RemoveManager(name string) error
}

// ManagedStudio is one studio a manager runs: its actor and an opaque
// manager-side location.
type ManagedStudio struct {
	ActorID  string `json:"actor_id"`
	Location string `json:"location"`
}

// ManagerInventory is what a manager reports holding.
type ManagerInventory struct {
	Studios     []ManagedStudio
	StateOwners []string
}

// ManagerReconcile is Jam's comparison of an inventory with its records.
type ManagerReconcile struct {
	Lost         []string
	Strays       []ManagedStudio
	MissingState []string
}

// ManagerStatus is one manager's row in the admin listing.
type ManagerStatus struct {
	Name        string          `json:"name"`
	Connected   bool            `json:"connected"`
	Version     string          `json:"version,omitempty"`
	Capacity    int             `json:"capacity,omitempty"`
	Studios     int             `json:"studios"`
	ConnectedAt time.Time       `json:"connected_at,omitzero"`
	LastSeen    time.Time       `json:"last_seen,omitzero"`
	Strays      []ManagedStudio `json:"strays,omitempty"`
	// Host is the manager's last host report. It is kept across a
	// disconnect (its At says how old it is) and replaced by the next
	// report; nil = never reported, or the last report had none.
	Host *HostStats `json:"host,omitempty"`
	// Activities is the manager's last reported work in progress; dropped
	// when the manager disconnects.
	Activities []ManagerActivity `json:"activities,omitempty"`
}

// HostStats is a manager host's memory and disk, measured where its studios
// run (for jam-colima, inside the Colima VM). All sizes are bytes.
type HostStats struct {
	At          time.Time      `json:"at"`
	MemTotal    uint64         `json:"mem_total"`
	MemUsed     uint64         `json:"mem_used"`
	DiskTotal   uint64         `json:"disk_total"`
	DiskUsed    uint64         `json:"disk_used"`
	Reclaimable Reclaimable    `json:"reclaimable"`
	LastCleanup *CleanupRecord `json:"last_cleanup,omitempty"`
}

// DiskPercent is DiskUsed as a whole percentage of DiskTotal (rounded down),
// and false when the total is unknown.
func (h *HostStats) DiskPercent() (int, bool) {
	if h == nil || h.DiskTotal == 0 {
		return 0, false
	}
	return int(h.DiskUsed * 100 / h.DiskTotal), true
}

// Reclaimable is disk the container runtime could free: unused images, build
// cache and unused volumes.
type Reclaimable struct {
	Images     uint64 `json:"images"`
	BuildCache uint64 `json:"build_cache"`
	Volumes    uint64 `json:"volumes"`
}

// CleanupRecord is the manager's last disk cleanup.
type CleanupRecord struct {
	At     time.Time `json:"at"`
	Freed  uint64    `json:"freed"`
	Reason string    `json:"reason,omitempty"`
	Err    string    `json:"err,omitempty"`
}

// ManagerActivity is one piece of long-running work a manager reports in progress
// (e.g. a kit prepare).
type ManagerActivity struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Kit     KitRef    `json:"kit,omitzero"`
	Step    string    `json:"step,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	Started time.Time `json:"started"`
}

// ManagerActivity kinds.
const (
	// ActivityPrepareKit is a kit image build (PrepareKit); Kit names it.
	ActivityPrepareKit = "prepare-kit"
	// ActivityBuildKitDraft is a kit draft's test build; Kit names the draft.
	ActivityBuildKitDraft = "build-kit-draft"
	// ActivityCleanup is a disk cleanup; Kit is unset.
	ActivityCleanup = "cleanup"
)

// Progress is the activity's step and detail as one phrase ("building step
// 7/12"); "" when it reported neither.
func (a ManagerActivity) Progress() string {
	return strings.TrimSpace(a.Step + " " + a.Detail)
}

// Age is how long the activity has run at now, in whole seconds under a
// minute, else whole minutes: "45s", "2m", "1h5m".
func (a ManagerActivity) Age(now time.Time) string {
	d := max(now.Sub(a.Started), 0)
	if d < time.Minute {
		return FormatDuration(d.Truncate(time.Second))
	}
	return FormatDuration(d.Truncate(time.Minute))
}

// KitActivityFunc finds a connected manager's prepare-kit activity for a kit
// (FindKitActivity), preferring manager's.
type KitActivityFunc func(kit KitRef, manager string) (ManagerActivity, bool)

// FindKitActivity is the prepare-kit activity in ms for kit's id: on manager
// first (when set), then on any manager in ms's order; within each, one for
// kit's exact version before one for another version of the kit (a recovery
// may prepare the role's newer version). False for a kit with no id.
func FindKitActivity(ms []ManagerStatus, kit KitRef, manager string) (ManagerActivity, bool) {
	if kit.ID == "" {
		return ManagerActivity{}, false
	}
	find := func(only string, exact bool) (ManagerActivity, bool) {
		for _, m := range ms {
			if only != "" && m.Name != only {
				continue
			}
			for _, a := range m.Activities {
				if a.Kind == ActivityPrepareKit && a.Kit.ID == kit.ID && (!exact || a.Kit.Version == kit.Version) {
					return a, true
				}
			}
		}
		return ManagerActivity{}, false
	}
	var scopes []string
	if manager != "" {
		scopes = append(scopes, manager)
	}
	for _, only := range append(scopes, "") {
		for _, exact := range []bool{true, false} {
			if a, ok := find(only, exact); ok {
				return a, true
			}
		}
	}
	return ManagerActivity{}, false
}

// HostReporter is the optional manager-side source of host stats; a manager
// without one reports no host.
type HostReporter interface {
	HostStats(ctx context.Context) (HostStats, error)
}

// HostCleaner is the optional manager-side means of freeing host disk; the
// manager's disk policy (studiomanager) decides when and in what order to
// call it. Every step removes only what nothing uses: never a named volume,
// a container, or an image a container (running or stopped) uses.
type HostCleaner interface {
	// PruneDanglingImages removes untagged images no container uses.
	PruneDanglingImages(ctx context.Context) error
	// KitImages lists the images tagged only with Jam kit tags (jam-kit,
	// jam-kit-base, jam-kit-draft), each marked InUse when a container uses it.
	KitImages(ctx context.Context) ([]KitImage, error)
	// RemoveImage removes an image by id, without force: the runtime refuses
	// one a container uses or another repository still names.
	RemoveImage(ctx context.Context, id string) error
	// PruneVolumes removes unused anonymous volumes, never a named one.
	PruneVolumes(ctx context.Context) error
	// PruneBuildCache removes the image builder's unused cache.
	PruneBuildCache(ctx context.Context) error
}

// KitImageRepos are the image repositories Jam's kit images are tagged in:
// built kits (launcher tagFor), kit bases (launcher and colima base
// resolution) and draft builds (DraftImageTag).
var KitImageRepos = []string{"jam-kit", "jam-kit-base", "jam-kit-draft"}

// KitImage is one Jam kit image on a manager host. Size is bytes.
type KitImage struct {
	ID      string
	Tags    []string
	Created time.Time
	Size    uint64
	InUse   bool
}

// ManagerToken is the one-time response to adding or rotating a manager.
type ManagerToken struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}
