package jam

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// countingSetupStore counts snapshot writes (and can fail them).
type countingSetupStore struct {
	*MemStore
	puts int
	fail error
}

func (c *countingSetupStore) PutSetupSnapshot(s SetupSnapshot) error {
	c.puts++
	if c.fail != nil {
		return c.fail
	}
	return c.MemStore.PutSetupSnapshot(s)
}

func countingSup(t *testing.T, fl *fakeLauncher) (*Supervisor, *countingSetupStore, *time.Time) {
	t.Helper()
	st := &countingSetupStore{MemStore: NewMemStore()}
	if err := st.PutRole("default", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	clk := time.Unix(1000, 0).UTC()
	sup := NewSupervisor(st, fl, "holder-A", time.Minute, 30*time.Second, func() time.Time { return clk }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return sup, st, &clk
}

func parts(st Store, id string) map[string]SetupSnapshot {
	out := map[string]SetupSnapshot{}
	for _, s := range st.SetupSnapshots(id) {
		out[s.Part] = s
	}
	return out
}

func TestRaiseRecordsSetupSnapshots(t *testing.T) {
	sup, store, fl := chainRaiseKit(t)
	inst, err := raiseDev(t, sup, "w1")
	if err != nil {
		t.Fatal(err)
	}
	p := parts(store, "w1")
	if len(p) != 4 {
		t.Fatalf("parts = %v, want context, egress, connector, kit", p)
	}
	if p[SetupPartContext].Fingerprint != fl.gotSpec.Context.Fingerprint || !strings.Contains(p[SetupPartContext].Body, "/agent-data/context/CORE.md") {
		t.Errorf("context part = %+v", p[SetupPartContext])
	}
	if p[SetupPartEgress].Fingerprint != "kit" || !strings.Contains(p[SetupPartEgress].Body, "npmjs.org") {
		t.Errorf("egress part = %+v (want the kit default with the merged ceiling)", p[SetupPartEgress])
	}
	if p[SetupPartKit].Fingerprint != kitFingerprint(inst.Kit, inst.ImageTag) || !strings.Contains(p[SetupPartKit].Body, "default@v1 → web-dev@v1 → my-app@v1") {
		t.Errorf("kit part = %+v", p[SetupPartKit])
	}
	if !p[SetupPartContext].At.Equal(time.Unix(1000, 0).UTC()) {
		t.Errorf("at = %v, want the supervisor clock", p[SetupPartContext].At)
	}
}

func TestContextRefreshWritesOnlyOnChange(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, st, clk := countingSup(t, fl)
	_, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewContextHandler(st, sup, func() time.Time { return *clk }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	get := func() {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/context", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /context = %d", rec.Code)
		}
	}
	get()
	n := st.puts
	get()
	get()
	if st.puts != n {
		t.Fatalf("unchanged refreshes wrote %d snapshot(s)", st.puts-n)
	}
	if err := SetRoleContext(st, "default", "guest", sessionctx.Layer{Core: "A new rule."}); err != nil {
		t.Fatal(err)
	}
	get()
	if st.puts != n+1 || !strings.Contains(parts(st, "w1")[SetupPartContext].Body, "A new rule.") {
		t.Fatalf("an edit must write exactly once (puts %d → %d)", n, st.puts)
	}
}

func TestSetupSnapshotWriteFailureNeverFailsDelivery(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, st, clk := countingSup(t, fl)
	st.fail = errors.New("disk full")
	_, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest"})
	if err != nil {
		t.Fatalf("a failed snapshot write must not fail the raise: %v", err)
	}
	for _, h := range []http.Handler{
		NewContextHandler(st, sup, func() time.Time { return *clk }, slog.New(slog.NewTextHandler(io.Discard, nil))),
		NewConnectorHandler(st, func() time.Time { return *clk }, slog.New(slog.NewTextHandler(io.Discard, nil))),
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery = %d, want 200 despite the failed write", rec.Code)
		}
	}
}

func TestConnectorHandlerRecordsSnapshot(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, st, clk := countingSup(t, fl)
	_, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.AddDestination(Destination{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com",
		Env: map[string]string{"GH_TOKEN": "ghp_LITERAL", "ANTHROPIC_API_KEY": "{token}"}})
	h := NewConnectorHandler(st, func() time.Time { return *clk }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	serve := func(token string) {
		req := httptest.NewRequest(http.MethodGet, "/connector", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	serve(tok)
	body := parts(st, "w1")[SetupPartConnector].Body
	if !strings.Contains(body, "ANTHROPIC_API_KEY=«identity token»") || strings.Contains(body, "ghp_LITERAL") || strings.Contains(body, tok) {
		t.Fatalf("connector part = %q", body)
	}
	orphan, err := Enroll(st, "hand", "default", "guest", nil, *clk)
	if err != nil {
		t.Fatal(err)
	}
	n := st.puts
	serve(orphan)
	if st.puts != n {
		t.Fatal("an identity without an instance must not write a snapshot")
	}
}

func TestEgressReapplyRecordsSnapshot(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, st, _ := countingSup(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SetRoleEgress(st, "default", "guest", []string{"example.com"}); err != nil {
		t.Fatal(err)
	}
	fl.egressErr = errors.New("exec failed")
	_ = sup.Reconcile(context.Background())
	if got := parts(st, "w1")[SetupPartEgress].Fingerprint; got != "kit" {
		t.Fatalf("a failed apply recorded %q", got)
	}
	fl.egressErr = nil
	_ = sup.Reconcile(context.Background())
	if got := parts(st, "w1")[SetupPartEgress]; got.Fingerprint != "d:example.com" || !strings.Contains(got.Body, "example.com") {
		t.Fatalf("egress part after a landed apply = %+v", got)
	}
}

func TestCapSetupBody(t *testing.T) {
	s := strings.Repeat("é", MaxSetupBody) // 2 bytes a rune: over the cap
	got := capSetupBody(s)
	if len(got) > MaxSetupBody || !utf8.ValidString(got) || !strings.Contains(got, "truncated") {
		t.Fatalf("capped to %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	if capSetupBody("short") != "short" {
		t.Fatal("a short body must be kept")
	}
}

func TestContextSnapshotBodyRedacted(t *testing.T) {
	kit := sessionctx.KitLayer("Kit prompt.", nil, map[string]string{"NPM_TOKEN": "npm_SECRETVALUE1", "GO_VERSION": "1.23"})
	b := sessionctx.Compile(sessionctx.Inputs{Kit: kit, Role: sessionctx.Layer{Core: "Mirror at https://bob:hunter2pw@mirror.example.com/x."}})
	if !strings.Contains(b.Files["kit/"+sessionctx.ToolsLeaf], "npm_SECRETVALUE1") {
		t.Fatalf("fixture: tools.md lacks the build arg: %q", b.Files)
	}
	got := contextSnapshotBody(b)
	for _, bad := range []string{"npm_SECRETVALUE1", "hunter2pw", "bob:"} {
		if strings.Contains(got, bad) {
			t.Errorf("context snapshot body leaks %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{"| NPM_TOKEN | " + MarkMasked + " |", "| go | 1.23 |", "mirror.example.com/x", "Kit prompt."} {
		if !strings.Contains(got, want) {
			t.Errorf("context snapshot body lacks %q:\n%s", want, got)
		}
	}
}

func TestRecordSetupPartBuildsBodyOnlyOnChange(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, st, clk := countingSup(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	builds := 0
	body := func() string { builds++; return "b" }
	RecordSetupPart(st, nil, "w1", SetupPartKit, "fp-new", body, *clk)
	RecordSetupPart(st, nil, "w1", SetupPartKit, "fp-new", body, *clk)
	if builds != 1 {
		t.Fatalf("body built %d times, want once (only on a changed fingerprint)", builds)
	}
}
