package adminui_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// draftFixture pushes kit app (kit.yml, run.sh 0755, docs/a.md, gone.txt) as
// v1, opens its draft from v1 and returns the store and the UI.
func draftFixture(t *testing.T) (jam.Store, http.Handler) {
	t.Helper()
	store := newStore(t)
	if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", "kind: studio\n", "run.sh*", "echo\n", "docs/a.md", "# a\n", "gone.txt", "bye\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := jam.OpenKitDraft(store, "app", 0, "local", time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	return store, draftHandler(store, testLogger())
}

// draftHandler serves the draft editor's writes on store itself, as tests
// of the editor's own handlers need: on a branch, a refused write reloads
// the branch, and a draft is never in the repo.
func draftHandler(store jam.Store, log *slog.Logger) http.Handler {
	return adminui.RegisterAppForTest(store, log, nil, nil, anyCred, nil)
}

// draftETag is kit app's draft ETag now.
func draftETag(t *testing.T, store jam.Store) string {
	t.Helper()
	st, err := jam.KitDraftStatusOf(store, "app")
	if err != nil {
		t.Fatal(err)
	}
	return st.ETag
}

// packCtx is a base64 tar.gz of one file: a packed base.context, as a
// pre-tree Jam stored it.
func packCtx(t *testing.T, name, body string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}
func uiHandler(t *testing.T, store jam.Store) http.Handler {
	t.Helper()
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
}

// markManaged records kit name as applied from the config repo.
func markManaged(t *testing.T, store jam.Store, name string) {
	t.Helper()
	if err := store.ApplyConfig(jam.ConfigWrite{Marks: []jam.ConfigMark{{Kind: jam.ConfigKindKit, Name: name, Commit: "abc1234", Digest: "d", AppliedAt: time.Unix(1, 0).UTC()}}}); err != nil {
		t.Fatal(err)
	}
}

// putDraft writes file p into app's draft (another tab, the CLI).
func putDraft(t *testing.T, store jam.Store, p string, mode fs.FileMode, data string) {
	t.Helper()
	if _, err := jam.PutKitDraftFile(store, nil, "app", p, mode, []byte(data), draftETag(t, store), "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
}

// srcFetcher serves one in-memory repo: ref -> commit -> kit.yml text.
type srcFetcher struct {
	t       *testing.T
	refs    map[string]string
	kits    map[string]string
	fetches int
	err     error
}

func (f *srcFetcher) Resolve(_ context.Context, src gitsource.Source) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	sha, ok := f.refs[src.Ref]
	if !ok {
		return "", errors.Join(gitsource.ErrFetch, errors.New("no such ref"))
	}
	return sha, nil
}

func (f *srcFetcher) Fetch(ctx context.Context, src gitsource.Source, fn func(root, commit string) error) error {
	sha, err := f.Resolve(ctx, src)
	if err != nil {
		return err
	}
	f.fetches++
	root := f.t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kit.yml"), []byte(f.kits[sha]), 0o644); err != nil {
		return err
	}
	return fn(root, sha)
}

const (
	srcKitV1 = "kind: studio\nname: web-dev\nprompt: one\n"
	srcKitV2 = "kind: studio\nname: web-dev\nprompt: two\n"
	srcSHA1  = "0123456789abcdef0123456789abcdef01234567"
	srcSHA2  = "fedcba9876543210fedcba9876543210fedcba98"
)

func sourceUI(t *testing.T, f jam.KitFetcher) (http.Handler, jam.Store) {
	t.Helper()
	store := newStore(t)
	opts := []adminui.Option{}
	if f != nil {
		opts = append(opts, adminui.WithKitFetcher(f))
	}
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, opts...), store
}

// seedRichRole stores acme/review with every field set, a destination whose
// default credential applies, a holder with an override, a running standing
// session and an ordinary studio of the role.
func seedRichRole(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	for _, d := range []jam.Destination{
		{Name: "git", Route: "/git/", Upstream: "https://git.example", CredName: "git-default"},
		{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", CredName: "anth-key"},
	} {
		if err := store.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	role := jam.Role{
		Name: "review",
		Scope: jam.Scope{
			Destinations: []string{"git", "anthropic"},
			Credentials:  map[string]string{"git": "git-pat"},
			Addressing:   []string{"human:*", "channel:eng"},
			TTL:          90 * time.Minute,
			Egress:       &jam.EgressPolicy{Domains: []string{"pypi.org"}},
		},
		Allocation: jam.RoleAllocation{
			MaxPersonal: 2,
			NagEvery:    2 * time.Hour,
			Standing:    []jam.StandingSession{{Name: "nightly", Prompt: "run the nightly sweep"}},
		},
	}
	if err := store.PutRole("acme", role); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", jam.Role{Name: "other"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []jam.Actor{
		{ID: "holder-plain", TokenHash: "h1", Grants: []jam.Grant{{Project: "acme", Role: "review"}}},
		{ID: "holder-ovr", TokenHash: "h2", Grants: []jam.Grant{{Project: "acme", Role: "review", Overrides: &jam.Override{Destinations: []string{"git"}}}}},
		{ID: "not-a-holder", TokenHash: "h3", Grants: []jam.Grant{{Project: "acme", Role: "other"}}},
	} {
		if err := store.AddActor(a); err != nil {
			t.Fatal(err)
		}
	}
	for _, i := range []jam.Instance{
		{ActorID: jam.SeedStandingSession(store, "acme", "review", "nightly"), Project: "acme", Role: "review", Name: "nightly", Phase: jam.PhaseLive},
		{ActorID: "studio-of-review", Project: "acme", Role: "review", Phase: jam.PhaseLive},
		{ActorID: "studio-of-other", Project: "acme", Role: "other", Phase: jam.PhaseLive},
	} {
		if err := store.PutInstance(i); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// teardownResetter resets by tearing the session down via the supervisor, or
// fails with err (a pending reset).
type teardownResetter struct {
	sup *jam.Supervisor
	err error
}

func (r *teardownResetter) ResetStanding(ctx context.Context, project, role, name string) error {
	if r.err != nil {
		return r.err
	}
	return r.sup.Teardown(ctx, jam.StandingActorID(project, role, name))
}

// credKnown accepts the credentials the rich-role fixture maps.
func credKnown(n string) bool { return n == "git-pat" || n == "other-pat" }
func sessionUI(t *testing.T) (http.Handler, *sessionevents.MemStore, *sessionevents.Hub, *sessionevents.Ingest) {
	t.Helper()
	st := sessionevents.NewMemStore()
	hub := sessionevents.NewHub()
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil, adminui.WithSessions(st, hub))
	return h, st, hub, sessionevents.NewIngest(st, hub, nil)
}
func sessIn(seq uint64, raw string) sessionevents.Incoming {
	return sessionevents.Incoming{StreamID: sessSID, Seq: seq, Turn: 1, Raw: []byte(raw)}
}

const sessSID = "0123456789abcdef0123456789abcdef"

// sse runs the SSE handler until stop() is called and returns the body.
func sse(t *testing.T, h http.Handler, path, lastID string) (stop func() string) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()
	return func() string { cancel(); <-done; return rec.Body.String() }
}

// cfgTreeFetcher serves one config repo: every ref resolves to sha, holding files.
type cfgTreeFetcher struct {
	t       *testing.T
	sha     string
	files   map[string]string
	fetches int
}

func (f *cfgTreeFetcher) Resolve(context.Context, gitsource.Source) (string, error) {
	return f.sha, nil
}

func (f *cfgTreeFetcher) Fetch(_ context.Context, _ gitsource.Source, fn func(root, commit string) error) error {
	f.fetches++
	root := f.t.TempDir()
	for p, b := range f.files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(b), 0o644); err != nil {
			return err
		}
	}
	return fn(root, f.sha)
}

// managedSHA is the config commit the fixtures apply from.
const managedSHA = "abc1234def5678abc1234def5678abc1234def56"

// managedSource is the config repo the fixtures follow.
var managedSource = gitsource.Source{Repo: "https://github.com/acme/jam", Ref: "main"}

// configUI is a Jam following managedSource through f, with a follower.
func configUI(t *testing.T, f jam.KitFetcher, conds *condition.Tracker) (http.Handler, jam.Store) {
	t.Helper()
	store := newStore(t)
	env := jam.ConfigEnv{Conditions: conds, Followed: managedSource}
	env.Follower = &jam.ConfigFollower{Store: store, Fetcher: f, Env: env}
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, adminui.WithKitFetcher(f),
		adminui.WithConfigEnv(env), adminui.WithConditions(conds, "")), store
}

// trackManaged records the source managedUI's objects came from.
func trackManaged(t *testing.T, store jam.Store) {
	t.Helper()
	if err := store.SetConfigSource(&jam.ConfigSource{Repo: "https://github.com/acme/jam", Ref: "main", Commit: managedSHA, AppliedAt: time.Unix(1000, 0)}); err != nil {
		t.Fatal(err)
	}
}

// managedUI is a Jam with managedTree applied, and its UI.
func managedUI(t *testing.T, opts ...adminui.Option) (http.Handler, jam.Store) {
	t.Helper()
	store := newStore(t)
	applyManaged(t, store, managedTree)
	opts = append([]adminui.Option{adminui.WithConfigEnv(jam.ConfigEnv{Followed: managedSource})}, opts...)
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, opts...), store
}

// applyManaged applies files to store from https://github.com/acme/jam@main
// at managedSHA, as a sync would.
func applyManaged(t *testing.T, store jam.Store, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for p, b := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := jam.LoadConfigTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := jam.ApplyConfigTree(context.Background(), store, nil, tree, jam.ConfigEnv{CredExists: anyCred},
		jam.ConfigApplyOptions{Source: gitsource.Source{Repo: "https://github.com/acme/jam", Ref: "main"}, Commit: managedSHA, Now: time.Unix(1000, 0)},
		nil, "test")
	if err != nil || !res.Applied {
		t.Fatalf("apply: %+v %v", res, err)
	}
}

// managedTree holds every kind with a UI page once.
var managedTree = map[string]string{
	"users/bob.yml":             "kind: user\nversion: 1\nlogins: [bob@example.com]\n",
	"destinations/gh.yml":       "kind: destination\nversion: 1\nroute: /gh/\nupstream: https://api.github.com\n",
	"model-specs/fast.yml":      "kind: model-spec\nversion: 1\ntype: claude\nharness-version: 2.1.3\nprincipal: {credential: anthropic}\nclaude: {provider: anthropic}\n",
	"kits/web-dev/kit.yml":      "kind: studio\nname: web-dev\nprompt: one\n",
	"projects/app/project.yml":  "kind: project\nversion: 1\nmembers: [{user: bob}]\n",
	"projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: web-dev\nmodel-spec: fast\nscope: {destinations: [gh], ttl: 1h}\n",
	"context.md":                "Be kind.\n",
}

// seedProjects: acme (two roles, two holders, a studio, a full roster,
// escalation and chat service), beta (empty, removable), and default (one role).
func seedProjects(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	for _, p := range []string{"acme", "beta"} {
		mustCreateProject(t, store, p)
	}
	for _, r := range []struct{ project, name string }{{"acme", "dev"}, {"acme", "ops"}, {"default", "solo"}} {
		if err := store.PutRole(r.project, jam.Role{Name: r.name}); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []jam.Actor{
		{ID: "a1", TokenHash: "h1", Grants: []jam.Grant{{Project: "acme", Role: "dev"}}},
		{ID: "a2", TokenHash: "h2", Grants: []jam.Grant{{Project: "acme", Role: "ops"}, {Project: "default", Role: "solo"}}},
		{ID: "a3", TokenHash: "h3", Grants: []jam.Grant{{Project: "default", Role: "solo"}}},
	} {
		if err := store.AddActor(a); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutInstance(jam.Instance{ActorID: "studio-acme", Project: "acme", Role: "dev", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutInstance(jam.Instance{ActorID: "studio-solo", Project: "default", Role: "solo", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if err := jam.AddPerson(store, "acme", jam.Human{
		Name: "alice", Handle: "alice-h", Login: "sub-alice",
		Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "dm-alice", UserID: "123456789"}},
		Identity: []jam.OIDCIdentity{{Issuer: "https://idp.example", Subject: "oidc-alice"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jam.PutRoom(store, "acme", jam.RoomBody{Name: "eng", Connection: "discord", Ref: "chan-eng"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEscalationPolicy("acme", "", []jam.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 30 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEscalationPolicy("acme", "deploy", []jam.EscalationTier{{Targets: []string{"human:alice", "channel:eng"}, Timeout: 10 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	return store
}
