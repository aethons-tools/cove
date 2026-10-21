package jam

import (
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
	Source     gitsource.Source // {Repo: ConfigDirRepo} for --dir
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
}

// ApplyConfigTree plans tree against store and, unless opt.DryRun, writes the
// whole plan in one Store.ApplyConfig call. A plan with refusals is
// ErrConfigRefused, a plan whose digest is not opt.WantDigest is
// ErrConfigPlanChanged; either writes nothing. The plan and the write run
// under configMu and kitMu, so no kit push lands between them. actor is the
// operator the audit log names; log may be nil.
func ApplyConfigTree(store Store, tree *ConfigTree, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	configMu.Lock()
	defer configMu.Unlock()
	kitMu.Lock() // configMu → kitMu → store mu, as every kit writer takes kitMu before the store
	defer kitMu.Unlock()
	plan := PlanConfig(ConfigPlanInput{Tree: tree, Snapshot: store.ExportConfig(), Instances: store.ListInstances(),
		Marks: store.ListConfigMarks(), Env: env, Source: opt.Source, Commit: opt.Commit, Force: opt.Force, Now: opt.Now})
	res := ConfigApplyResult{Plan: plan}
	if opt.DryRun {
		return res, nil
	}
	if len(plan.Refusals) > 0 {
		return res, fmt.Errorf("%w:\n%s", ErrConfigRefused, strings.Join(plan.Refusals, "\n"))
	}
	if opt.WantDigest != "" && opt.WantDigest != plan.Digest {
		return res, ErrConfigPlanChanged
	}
	res.Counts = plan.Counts()
	if !planChanges(plan) {
		return res, nil // every op unchanged: keep the marks as they are
	}
	if err := store.ApplyConfig(plan.write); err != nil {
		return res, fmt.Errorf("config apply: write: %w", err)
	}
	res.Applied = true
	logConfigApply(log, actor, opt, plan)
	return res, nil
}

// planChanges reports whether plan writes anything beyond re-marking
// unchanged objects. (Every op writes a mark, so plan.write is never empty
// when there are ops; a dropped mark has no op.)
func planChanges(plan ConfigPlan) bool {
	n := plan.Counts()
	return n[ConfigCreate]+n[ConfigUpdate]+n[ConfigDelete]+n[ConfigAdopt] > 0 || len(plan.write.RemoveMarks) > 0
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
func logConfigApply(log *slog.Logger, actor string, opt ConfigApplyOptions, plan ConfigPlan) {
	if log == nil {
		return
	}
	source := opt.Source.Repo
	if source == ConfigDirRepo {
		source = "dir"
	}
	n := plan.Counts()
	log.Info("config applied", "operator", actor, "source", source, "commit", opt.Commit,
		"created", n[ConfigCreate], "updated", n[ConfigUpdate], "deleted", n[ConfigDelete],
		"adopted", n[ConfigAdopt], "unchanged", n[ConfigUnchanged], "forced", opt.Force)
	for _, op := range plan.Ops {
		if msg, ok := configAuditMsgs[op.Kind][op.Action]; ok {
			log.Info(msg, "operator", actor, "via", "config", "kind", op.Kind, "name", op.Name)
		}
	}
}
