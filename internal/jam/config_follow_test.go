package jam

import (
	"context"
	"fmt"
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
