package jam

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

// Following the config repo (docs/usage/jam/config-git-sources.md): the Jam
// checks its followed branch (ConfigEnv.Followed, config.github in the serve
// config) every ConfigFollowEvery and applies a new commit, the repo winning
// over any drift. Until the first apply is confirmed (ConfigFollowConfirmed)
// it only reports the head.

// ConfigFollowKey is the config.follow condition's key: one for the Jam.
const ConfigFollowKey = "config.follow:jam"

// ConfigFollowEvery is how often the follower checks the branch.
const ConfigFollowEvery = time.Minute

// The follower's states.
const (
	ConfigFollowOff      = "off"              // no followed repo
	ConfigFollowAwaiting = "awaiting-confirm" // the first apply is not confirmed yet
	ConfigFollowInSync   = "in-sync"          // the head is applied
	ConfigFollowFailed   = "failed"           // the last check or apply failed; retried next tick
)

// ConfigFollowStatus is what the follower last saw and did.
type ConfigFollowStatus struct {
	State     string    `json:"state"`
	Source    string    `json:"source,omitempty"`  // the followed repo, as configSourceLabel names it
	Head      string    `json:"head,omitempty"`    // the branch head last seen
	Applied   string    `json:"applied,omitempty"` // the last commit applied from it
	Error     string    `json:"error,omitempty"`   // why the last tick failed (redacted)
	CheckedAt time.Time `json:"checkedAt,omitzero"`
}

// ConfigFollower follows Env.Followed. Tick is safe to call concurrently
// with itself (a sync's refresh) and with Status.
type ConfigFollower struct {
	Store   Store
	Fetcher KitFetcher
	Env     ConfigEnv
	Log     *slog.Logger
	Now     func() time.Time // nil = time.Now

	tickMu sync.Mutex // one tick at a time
	mu     sync.Mutex // guards status and applyFailed
	status ConfigFollowStatus
	// applyFailed is the head whose apply failed last ("" = none): it is
	// retried even though the record may match (a failed check is not).
	applyFailed string
}

const configFollowActor = "config-follow"

// Status is the last tick's outcome ({State: off} before the first).
func (f *ConfigFollower) Status() ConfigFollowStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status.State == "" {
		return ConfigFollowStatus{State: ConfigFollowOff, Source: configSourceLabel(f.Env, f.Env.Followed)}
	}
	return f.status
}

func (f *ConfigFollower) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Tick checks the followed branch once: off without a repo; awaiting confirm
// until the first apply; else applies the head unless it is already applied
// and the last tick did not fail (a failure is retried at the same head).
func (f *ConfigFollower) Tick(ctx context.Context) {
	f.tickMu.Lock()
	defer f.tickMu.Unlock()
	src := f.Env.Followed
	st := ConfigFollowStatus{Source: configSourceLabel(f.Env, src), CheckedAt: f.now()}
	if src.Repo == "" {
		st.State = ConfigFollowOff
		f.set(st)
		return
	}
	if rec, ok := f.Store.GetConfigSource(); ok && rec.Source() == src {
		st.Applied = rec.Commit
	}
	if f.Fetcher == nil {
		f.fail(st, "config: git sources are not available on this serve")
		return
	}
	head, err := f.Fetcher.Resolve(ctx, src)
	if err != nil {
		_, msg := ConfigStatus(err)
		f.fail(st, msg)
		return
	}
	st.Head = head
	if !ConfigFollowConfirmed(f.Store, f.Env) {
		st.State = ConfigFollowAwaiting
		f.set(st)
		f.Env.Conditions.Raise(condition.Condition{Key: ConfigFollowKey, Severity: condition.Info,
			Summary: "the config repo's first apply is waiting to be confirmed",
			Fix:     "On Config › Tracking press Diff, review the plan and press Apply this plan, or run `jam config sync`."})
		return
	}
	f.mu.Lock()
	retry := f.applyFailed == head
	f.mu.Unlock()
	if st.Applied == head && !retry {
		st.State = ConfigFollowInSync
		f.set(st)
		f.Env.Conditions.Clear(ConfigFollowKey)
		return
	}
	res, err := ApplyConfigSource(ctx, f.Store, f.Fetcher, src, f.Env, ConfigApplyOptions{Now: st.CheckedAt}, f.Log, configFollowActor)
	if err != nil {
		status, msg := ConfigStatus(err)
		if status == http.StatusInternalServerError && f.Log != nil {
			f.Log.Error("config follow: store error", "source", st.Source, "head", head, "err", err.Error())
		}
		f.mu.Lock()
		f.applyFailed = head
		f.mu.Unlock()
		f.fail(st, msg)
		return
	}
	f.mu.Lock()
	f.applyFailed = ""
	f.mu.Unlock()
	st.State, st.Applied = ConfigFollowInSync, res.Commit
	if rec, ok := f.Store.GetConfigSource(); ok && rec.Source() == src {
		st.Applied = rec.Commit
	}
	f.set(st)
	f.Env.Conditions.Clear(ConfigFollowKey)
}

func (f *ConfigFollower) set(st ConfigFollowStatus) {
	f.mu.Lock()
	f.status = st
	f.mu.Unlock()
}

// fail records a failed tick and raises config.follow (warning).
func (f *ConfigFollower) fail(st ConfigFollowStatus, msg string) {
	st.State, st.Error = ConfigFollowFailed, msg
	f.set(st)
	if f.Log != nil {
		f.Log.Warn("config follow: failed; retried next check", "source", st.Source, "head", st.Head, "err", msg)
	}
	f.Env.Conditions.Raise(condition.Condition{Key: ConfigFollowKey, Severity: condition.Warning,
		Summary: "the Jam could not apply its config repo",
		Detail:  msg,
		Fix:     "Fix the repo (or the credential) and push; the Jam retries every minute. Config › Tracking shows the error."})
}

// Run ticks now and every `every` until ctx ends.
func (f *ConfigFollower) Run(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		f.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Refresh updates the status from the stored record alone, with no check:
// after a sync (the first apply's confirm) it is in sync with the commit
// the sync applied, at once. The next tick checks the branch again.
func (f *ConfigFollower) Refresh() {
	rec, ok := f.Store.GetConfigSource()
	if !ok || !ConfigFollowConfirmed(f.Store, f.Env) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.status
	st.Source, st.Applied, st.Error = configSourceLabel(f.Env, f.Env.Followed), rec.Commit, ""
	if st.Head == "" || st.Head == rec.Commit || st.State != ConfigFollowFailed {
		st.State = ConfigFollowInSync
		if st.Head == "" {
			st.Head = rec.Commit
		}
	}
	f.status = st
	f.applyFailed = ""
	if st.State == ConfigFollowInSync {
		f.Env.Conditions.Clear(ConfigFollowKey)
	}
}
