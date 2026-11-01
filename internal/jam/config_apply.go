package jam

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// configMu serializes config applies. It is taken before kitMu: an apply
// holds configMu, then kitMu, then the store's own lock.
var configMu sync.Mutex

var (
	// ErrConfigRefused is an apply whose plan has refusals; nothing is written.
	ErrConfigRefused = errors.New("config plan has refusals")
	// ErrConfigPlanChanged is an apply whose plan no longer has the digest
	// the operator reviewed; nothing is written.
	ErrConfigPlanChanged = errors.New("the config plan changed since it was reviewed; run config diff again")
)

// ConfigApplyOptions is one ApplyConfigTree run's provenance and mode.
type ConfigApplyOptions struct {
	Source     gitsource.Source // {Repo: ConfigDirRepo} for --dir; the git source for --git (set by ApplyConfigSource)
	Commit     string           // ConfigDirCommit for --dir
	Force      bool             // write over drift
	DryRun     bool             // plan only (config diff)
	WantDigest string           // "" = any plan; else the reviewed plan's digest
	Now        time.Time
}

// ConfigApplyResult is an apply's (or a dry run's) plan and outcome. Applied
// is false for a dry run and for a plan that changes nothing.
type ConfigApplyResult struct {
	Plan    ConfigPlan     `json:"plan"`
	Applied bool           `json:"applied"`
	Counts  map[string]int `json:"counts,omitempty"`
	Source  string         `json:"source,omitempty"` // "dir", or the git source planned
	Commit  string         `json:"commit,omitempty"` // the config commit planned; "" for --dir
}

// ApplyConfigTree plans tree against store — fetching its .source.yml kits
// with f first (f may be nil when it has none) — and, unless opt.DryRun,
// writes the whole plan in one Store.ApplyConfig call. A plan with refusals
// is ErrConfigRefused, a plan whose digest is not opt.WantDigest is
// ErrConfigPlanChanged; either writes nothing. configMu is held from the
// first fetch to the write, kitMu only for the plan and the write, and no
// kit lock at all: no kit push lands between plan and write, and two config
// runs never interleave. actor is the operator the audit log names; log may
// be nil.
func ApplyConfigTree(ctx context.Context, store Store, f KitFetcher, tree *ConfigTree, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	configMu.Lock() // the fetch deadline starts here: queue wait does not count
	defer configMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, configFetchTimeout)
	defer cancel()
	return applyConfigTreeLocked(ctx, store, f, tree, env, opt, log, actor)
}

// applyConfigTreeLocked is ApplyConfigTree for a caller holding configMu.
func applyConfigTreeLocked(ctx context.Context, store Store, f KitFetcher, tree *ConfigTree, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	if tree == nil {
		tree = &ConfigTree{}
	}
	FetchConfigKits(ctx, f, tree) // holding configMu only: kitMu is never held across a fetch
	if err := configFetchErr(ctx); err != nil {
		return ConfigApplyResult{}, err
	}
	res, wrote, err := planAndWriteConfig(store, tree, env, opt, log, actor)
	if wrote {
		// configMu only: a refreshed mark or the write itself can clear
		// config.drift, an adopt or forget can raise it.
		checkConfigDriftLocked(store, env.Conditions, log)
	}
	return res, err
}

// planAndWriteConfig plans the fetched tree and, unless a dry run, refused
// or stale, writes it, holding kitMu for just that. wrote reports a
// non-dry-run apply that reached the store (with or without changes).
func planAndWriteConfig(store Store, tree *ConfigTree, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (_ ConfigApplyResult, wrote bool, _ error) {
	kitMu.Lock() // configMu → kitMu → store mu, as every kit writer takes kitMu before the store
	defer kitMu.Unlock()
	plan := PlanConfig(ConfigPlanInput{Tree: tree, Snapshot: store.configSnapshot(), Instances: store.ListInstances(),
		Marks: store.ListConfigMarks(), Env: env, Source: opt.Source, Commit: opt.Commit, Force: opt.Force, Now: opt.Now})
	res := ConfigApplyResult{Plan: plan, Source: configSourceLabel(env, opt.Source), Commit: provenanceCommit(opt.Commit)}
	if opt.DryRun {
		return res, false, nil
	}
	if len(plan.Refusals) > 0 {
		return res, false, fmt.Errorf("%w:\n%s", ErrConfigRefused, strings.Join(plan.Refusals, "\n"))
	}
	if opt.WantDigest != "" && opt.WantDigest != plan.Digest {
		return res, false, ErrConfigPlanChanged
	}
	res.Counts = plan.Counts()
	changes := planChanges(plan)
	w := plan.write
	w.ConfigSource = trackedRecord(store, env, opt, changes)
	if !changes {
		// Nothing to apply; still record what moved without changing an
		// object — the .source.yml kits' tracked commits, the config
		// source's commit, the stale marks of unchanged objects: no
		// version, no other mark, no audit entry.
		if meta := (ConfigWrite{Kits: w.Kits, KitObjects: w.KitObjects, Marks: plan.refreshMarks, ConfigSource: w.ConfigSource}); !meta.Empty() {
			if err := store.ApplyConfig(meta); err != nil {
				return res, false, fmt.Errorf("config apply: write: %w", err)
			}
		}
		return res, true, nil
	}
	if err := store.ApplyConfig(w); err != nil {
		return res, false, fmt.Errorf("config apply: write: %w", err)
	}
	if len(w.RemoveKits) > 0 {
		gcKitObjects(store, log)
	}
	res.Applied = true
	logConfigApply(log, actor, env, opt, plan)
	return res, true, nil
}

// planChanges reports whether plan writes anything beyond re-marking
// unchanged objects. (Every op writes a mark, so plan.write is never empty
// when there are ops; a forgotten mark has no op.)
func planChanges(plan ConfigPlan) bool {
	return ConfigChanges(plan) > 0
}

// ConfigChanges is the number of changes plan makes: its creates, updates,
// deletes and adopts, and its forgotten marks.
func ConfigChanges(plan ConfigPlan) int {
	n := plan.Counts()
	return n[ConfigCreate] + n[ConfigUpdate] + n[ConfigDelete] + n[ConfigAdopt] + n[ConfigForget]
}

// configAuditMsgs is each kind's usual admin audit message by action.
var configAuditMsgs = map[string]map[string]string{
	ConfigKindDestination: {ConfigCreate: "admin destination added", ConfigUpdate: "admin destination added", ConfigDelete: "admin destination removed"},
	ConfigKindModelSpec:   {ConfigCreate: "admin model-spec created", ConfigUpdate: "admin model-spec updated", ConfigDelete: "admin model-spec deleted"},
	ConfigKindUser:        {ConfigCreate: "admin user created", ConfigUpdate: "admin user updated", ConfigDelete: "admin user removed"},
	ConfigKindConnection:  {ConfigCreate: "admin connection created", ConfigUpdate: "admin connection cred set", ConfigDelete: "admin connection removed"},
	ConfigKindKit:         {ConfigCreate: "admin kit pushed", ConfigUpdate: "admin kit pushed", ConfigDelete: "admin kit removed"},
	ConfigKindProject:     {ConfigCreate: "admin project created", ConfigUpdate: "admin project updated", ConfigDelete: "admin project removed"},
	ConfigKindRole:        {ConfigCreate: "admin role put", ConfigUpdate: "admin role put", ConfigDelete: "admin role removed"},
	ConfigKindJamContext:  {ConfigCreate: "admin jam context set", ConfigUpdate: "admin jam context set", ConfigDelete: "admin jam context set"},
}

// logConfigApply writes the apply's audit: one "config applied" entry with
// the source, commit, counts, operator and forced, then each written object's
// usual admin entry with via=config. Names only, never values.
func logConfigApply(log *slog.Logger, actor string, env ConfigEnv, opt ConfigApplyOptions, plan ConfigPlan) {
	if log == nil {
		return
	}
	source := configSourceLabel(env, opt.Source)
	n := plan.Counts()
	log.Info("config applied", "operator", actor, "source", source, "commit", opt.Commit,
		"created", n[ConfigCreate], "updated", n[ConfigUpdate], "deleted", n[ConfigDelete],
		"adopted", n[ConfigAdopt], "forgotten", n[ConfigForget], "unchanged", n[ConfigUnchanged], "forced", opt.Force)
	for _, op := range plan.Ops {
		if msg, ok := configAuditMsgs[op.Kind][op.Action]; ok {
			log.Info(msg, "operator", actor, "via", "config", "kind", op.Kind, "name", op.Name)
		}
	}
}
