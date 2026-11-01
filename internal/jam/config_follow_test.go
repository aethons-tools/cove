package jam

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

func followerFor(t *testing.T, st *MemStore, f KitFetcher, env ConfigEnv) (*ConfigFollower, *condition.Tracker) {
	t.Helper()
	tr := condition.New(condition.Options{})
	env.Conditions = tr
	return &ConfigFollower{Store: st, Fetcher: f, Env: env, Now: func() time.Time { return planNow }}, tr
}

func TestFollowerOffWithoutASource(t *testing.T) {
	fl, tr := followerFor(t, NewMemStore(), nil, allCreds)
	fl.Tick(context.Background())
	if s := fl.Status(); s.State != ConfigFollowOff {
		t.Fatalf("status %+v", s)
	}
	if tr.IsOpen(ConfigFollowKey) {
		t.Fatal("condition open")
	}
}

func TestFollowerAwaitsConfirmThenFollows(t *testing.T) {
	st := NewMemStore()
	f, cfg := cfgFetcher(t, cfgFiles)
	fl, tr := followerFor(t, st, f, followEnv)
	ctx := context.Background()
	fl.Tick(ctx)
	if s := fl.Status(); s.State != ConfigFollowAwaiting || s.Head != "c1" || s.Source != "https://github.com/acme/jam//cfg@main" || !s.CheckedAt.Equal(planNow) {
		t.Fatalf("status %+v", s)
	}
	if len(st.ListUsers()) != 0 {
		t.Fatal("an unconfirmed source was applied")
	}
	if c, ok := tr.Get(ConfigFollowKey); !ok || c.Severity != condition.Info {
		t.Fatalf("condition %+v %v", c, ok)
	}
	// The operator confirms: the first apply.
	if _, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, fl.Env, syncOpts(), nil, "alice"); err != nil {
		t.Fatal(err)
	}
	fl.Tick(ctx)
	if s := fl.Status(); s.State != ConfigFollowInSync || s.Applied != "c1" {
		t.Fatalf("status %+v", s)
	}
	if tr.IsOpen(ConfigFollowKey) {
		t.Fatal("condition still open in sync")
	}
	fetches := cfg.fetches
	fl.Tick(ctx)
	if cfg.fetches != fetches {
		t.Fatal("the same head was fetched again")
	}
	moved := withFiles(map[string]string{"cfg/users/eve.yml": "kind: user\nversion: 1\n"})
	cfg.commit("main", "c2", moved)
	fl.Tick(ctx)
	if s := fl.Status(); s.State != ConfigFollowInSync || s.Applied != "c2" || len(st.ListUsers()) != 2 {
		t.Fatalf("after c2: %+v users=%d", s, len(st.ListUsers()))
	}
}

func TestFollowerFetchErrorIsReportedAndRetried(t *testing.T) {
	st := NewMemStore()
	f, cfg := cfgFetcher(t, cfgFiles)
	fl, tr := followerFor(t, st, f, followEnv)
	cfg.failNext = fmt.Errorf("%w: auth failed for https://github.com/acme/", gitsource.ErrFetch)
	fl.Tick(context.Background())
	if s := fl.Status(); s.State != ConfigFollowFailed || s.Error == "" {
		t.Fatalf("status %+v", s)
	}
	if c, ok := tr.Get(ConfigFollowKey); !ok || c.Severity != condition.Warning {
		t.Fatalf("condition %+v %v", c, ok)
	}
	fl.Tick(context.Background())
	if s := fl.Status(); s.State != ConfigFollowAwaiting {
		t.Fatalf("not retried: %+v", s)
	}
}

func TestFollowerFailedApplyIsRetriedAtTheSameHead(t *testing.T) {
	st := NewMemStore()
	f, cfg := cfgFetcher(t, cfgFiles)
	fl, _ := followerFor(t, st, f, followEnv)
	ctx := context.Background()
	if _, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, fl.Env, syncOpts(), nil, "alice"); err != nil {
		t.Fatal(err)
	}
	broken := withFiles(map[string]string{"cfg/users/eve.yml": "kind: user\nversion: 9\n"})
	cfg.commit("main", "c2", broken)
	fl.Tick(ctx)
	if s := fl.Status(); s.State != ConfigFollowFailed || s.Head != "c2" || s.Applied != "c1" || s.Error == "" {
		t.Fatalf("status %+v", s)
	}
	// Fixed in place (say a refusal cleared): same head, retried.
	cfg.commits["c2"] = withFiles(map[string]string{"cfg/users/eve.yml": "kind: user\nversion: 1\n"})
	fl.Tick(ctx)
	if s := fl.Status(); s.State != ConfigFollowInSync || s.Applied != "c2" {
		t.Fatalf("not retried at the same head: %+v", s)
	}
}

func TestFollowerAnotherSourceIsUnconfirmed(t *testing.T) {
	st := NewMemStore()
	f, _ := cfgFetcher(t, cfgFiles)
	if err := st.SetConfigSource(&ConfigSource{Repo: "https://github.com/acme/old", Ref: "main", Commit: "c0"}); err != nil {
		t.Fatal(err)
	}
	fl, _ := followerFor(t, st, f, followEnv)
	fl.Tick(context.Background())
	if s := fl.Status(); s.State != ConfigFollowAwaiting || len(st.ListUsers()) != 0 {
		t.Fatalf("status %+v", s)
	}
}

// A record from before following (config track wrote it with no commit) of
// the same repo does not confirm: the first apply still waits.
func TestFollowerPreFollowRecordIsUnconfirmed(t *testing.T) {
	st := NewMemStore()
	f, _ := cfgFetcher(t, cfgFiles)
	if err := st.SetConfigSource(&ConfigSource{Repo: cfgSrc.Repo, Dir: cfgSrc.Dir, Ref: cfgSrc.Ref}); err != nil {
		t.Fatal(err)
	}
	fl, _ := followerFor(t, st, f, followEnv)
	fl.Tick(context.Background())
	if s := fl.Status(); s.State != ConfigFollowAwaiting || len(st.ListUsers()) != 0 {
		t.Fatalf("status %+v users=%d", s, len(st.ListUsers()))
	}
}

// A failed check (not a failed apply) does not re-apply the unchanged head.
func TestFollowerCheckFailureDoesNotReapply(t *testing.T) {
	st := NewMemStore()
	f, cfg := cfgFetcher(t, cfgFiles)
	fl, _ := followerFor(t, st, f, followEnv)
	ctx := context.Background()
	if _, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, fl.Env, syncOpts(), nil, "alice"); err != nil {
		t.Fatal(err)
	}
	cfg.failNext = fmt.Errorf("%w: connection reset", gitsource.ErrFetch)
	fl.Tick(ctx)
	if fl.Status().State != ConfigFollowFailed {
		t.Fatalf("status %+v", fl.Status())
	}
	fetches := cfg.fetches
	fl.Tick(ctx)
	if s := fl.Status(); s.State != ConfigFollowInSync || cfg.fetches != fetches {
		t.Fatalf("re-applied after a check failure: %+v fetches %d→%d", s, fetches, cfg.fetches)
	}
}

// Refresh reads the record only: after a confirm the status is in sync
// without a check.
func TestFollowerRefreshAfterConfirm(t *testing.T) {
	st := NewMemStore()
	f, _ := cfgFetcher(t, cfgFiles)
	fl, _ := followerFor(t, st, f, followEnv)
	ctx := context.Background()
	fl.Tick(ctx)
	if _, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, fl.Env, syncOpts(), nil, "alice"); err != nil {
		t.Fatal(err)
	}
	fl.Refresh()
	if s := fl.Status(); s.State != ConfigFollowInSync || s.Applied != "c1" {
		t.Fatalf("status %+v", s)
	}
}

// A store error in a follower's apply is logged with its cause.
func TestFollowerLogsStoreErrors(t *testing.T) {
	st := NewMemStore()
	f, _ := cfgFetcher(t, cfgFiles)
	var buf bytes.Buffer
	fl, _ := followerFor(t, st, f, followEnv)
	fl.Log = slog.New(slog.NewTextHandler(&buf, nil))
	if _, err := ApplyConfigSource(context.Background(), st, f, gitsource.Source{}, fl.Env, syncOpts(), nil, "alice"); err != nil {
		t.Fatal(err)
	}
	fl.Store = failApplyStore{st}
	f2, cfg := cfgFetcher(t, withFiles(map[string]string{"cfg/users/eve.yml": "kind: user\nversion: 1\n"}))
	cfg.commit("main", "c2", withFiles(map[string]string{"cfg/users/eve.yml": "kind: user\nversion: 1\n"}))
	fl.Fetcher = f2
	fl.Tick(context.Background())
	if s := fl.Status(); s.State != ConfigFollowFailed || !strings.Contains(buf.String(), "disk on fire") {
		t.Fatalf("status %+v log:\n%s", s, buf.String())
	}
	if strings.Contains(s(fl), "disk on fire") {
		t.Fatal("the raw store error reached the status")
	}
}

func s(fl *ConfigFollower) string { return fl.Status().Error }

type failApplyStore struct{ Store }

func (failApplyStore) ApplyConfig(ConfigWrite) error { return errors.New("disk on fire") }

func TestFollowerAppliesLocalRepo(t *testing.T) {
	st := NewMemStore()
	f, commit := localFollowed(t, map[string]string{"config/users/bob.yml": localBob}, nil)
	env := allCreds
	env.Followed = f.Followed
	fl, _ := followerFor(t, st, f, env)
	ctx := context.Background()
	if _, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, fl.Env, syncOpts(), nil, "alice"); err != nil {
		t.Fatal(err) // the confirmed first apply
	}
	head := commit(map[string]string{"config/users/eve.yml": localBob})
	fl.Tick(ctx)
	if s := fl.Status(); s.State != ConfigFollowInSync || s.Applied != head || s.Head != head {
		t.Fatalf("status %+v, want applied %s", s, head)
	}
	if !hasUser(st, "eve") {
		t.Fatal("eve was not applied")
	}
}

func TestFollowedLabel(t *testing.T) {
	env := allCreds
	env.Followed = gitsource.Source{Repo: "git@h:x.git", Ref: "main"}
	env.FollowedLabel = "git@h:x.git@main"
	fl, _ := followerFor(t, NewMemStore(), nil, env)
	if s := fl.Status(); s.Source != env.FollowedLabel {
		t.Fatalf("status before a tick %+v", s)
	}
	fl.Tick(context.Background())
	if s := fl.Status(); s.Source != env.FollowedLabel {
		t.Fatalf("status %+v", s)
	}
	if got := configSourceLabel(env, gitsource.Source{Repo: "git@h:y.git", Ref: "main"}); got != "invalid" {
		t.Fatalf("another unparsable source is %q", got)
	}
}
