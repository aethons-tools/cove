package jam

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/studio"
)

func dirOpts() ConfigApplyOptions {
	return ConfigApplyOptions{Source: gitsource.Source{Repo: ConfigDirRepo}, Commit: ConfigDirCommit, Now: time.Unix(1000, 0).UTC()}
}

// mustWrite writes body at path (parents created), with mode past the umask.
func mustWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// parentVersion is the @vN a kit config's base.kit pins.
func parentVersion(t *testing.T, cfg string) int {
	t.Helper()
	sk, err := studio.ParseStudioKit([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, v, err := sk.Base.ParentRef()
	if err != nil || v == 0 {
		t.Fatalf("base.kit of %q: v=%d err=%v", cfg, v, err)
	}
	return v
}

func TestApplyConfigTreeRefusesAndWritesNothing(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n",
		"projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: missing\n"})
	_, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, dirOpts(), nil, "local")
	if !errors.Is(err, ErrConfigRefused) || len(st.ListUsers()) != 0 {
		t.Fatalf("err=%v users=%v", err, st.ListUsers())
	}

	// A kit whose path parent lies outside kits/: the tree's other kits and
	// users are refused with it.
	tr = treeOf(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n",
		"kits/web/kit.yml": "kind: studio\n",
		"kits/app/kit.yml": "kind: studio\nbase: {kit: ../../x}\n"})
	_, err = ApplyConfigTree(context.Background(), st, nil, tr, allCreds, dirOpts(), nil, "local")
	if !errors.Is(err, ErrConfigRefused) || len(st.ListUsers()) != 0 || len(st.ListKits()) != 0 || len(st.ListConfigMarks()) != 0 {
		t.Fatalf("err=%v users=%v kits=%v marks=%v", err, st.ListUsers(), st.ListKits(), st.ListConfigMarks())
	}
}

func TestApplyConfigTreeDryRunWritesNothing(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	opt := dirOpts()
	opt.DryRun = true
	res, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, opt, nil, "local")
	if err != nil || res.Applied || len(st.ListUsers()) != 0 || len(st.ListConfigMarks()) != 0 || res.Plan.Counts()[ConfigCreate] != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestApplyConfigTreePlanDigestGuard(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	opt := dirOpts()
	opt.WantDigest = "stale"
	if _, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, opt, nil, "local"); !errors.Is(err, ErrConfigPlanChanged) {
		t.Fatal(err)
	}
	if len(st.ListUsers()) != 0 {
		t.Fatal("a stale plan wrote")
	}
	opt.DryRun = true
	diff, _ := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, opt, nil, "local")
	opt.DryRun, opt.WantDigest = false, diff.Plan.Digest
	if res, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, opt, nil, "local"); err != nil || !res.Applied {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestApplyConfigTreeAudit(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n", "destinations/gh.yml": ghDest})
	res, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, dirOpts(), log, "alice")
	if err != nil || !res.Applied || res.Counts[ConfigCreate] != 2 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	out := buf.String()
	for _, want := range []string{`"msg":"config applied"`, `"operator":"alice"`, `"source":"dir"`, `"commit":"dir"`, `"created":2`, `"forced":false`,
		`"msg":"admin user created"`, `"msg":"admin destination added"`, `"via":"config"`} {
		if !strings.Contains(out, want) {
			t.Errorf("audit lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "api.github.com") {
		t.Errorf("audit logs a value:\n%s", out)
	}

	buf.Reset()
	if res, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, dirOpts(), log, "alice"); err != nil || res.Applied {
		t.Fatalf("an unchanged tree wrote: res=%+v err=%v", res, err)
	}
	if buf.Len() != 0 {
		t.Errorf("an unchanged apply logged:\n%s", buf.String())
	}
}

func TestApplyVsConcurrentKitPush(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"kits/web/kit.yml": "kind: studio\n", "kits/app/kit.yml": "kind: studio\nbase: {kit: ../web}\n"})
	force := ConfigApplyOptions{Source: gitsource.Source{Repo: ConfigDirRepo}, Commit: ConfigDirCommit, Force: true, Now: time.Now()}
	if res, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, force, nil, "local"); err != nil || !res.Applied {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 50 {
			_, _, _ = PushStudioKit(st, "web", fmt.Sprintf("kind: studio\nprompt: p%d\n", i))
		}
	}()
	for range 20 {
		_, _ = ApplyConfigTree(context.Background(), st, nil, tr, allCreds, force, nil, "local")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock between config apply and kit push")
	}
	k, _ := st.GetKit("app")
	cfg, _ := st.KitConfig("app", k.Current)
	web, _ := st.GetKit("web")
	if !web.HasVersion(parentVersion(t, cfg)) {
		t.Fatalf("app pins a web version that does not exist: %s", cfg)
	}
}
