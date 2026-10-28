package adminui_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
)

func anyCred(string) bool { return true }

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newStore(t *testing.T) jam.Store {
	t.Helper()
	return jam.NewMemStore()
}

// get serves GET path to h as a Local admin, as the /app gate does.
func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, appui.WithPrincipal(httptest.NewRequest(http.MethodGet, path, nil), appui.Principal{Admin: true, Local: true}))
	return rec
}

// mustCreateProject records project name on st: every project-scoped write
// needs its project to exist first.
func mustCreateProject(t *testing.T, st jam.Store, name string) {
	t.Helper()
	if err := st.CreateProject(name); err != nil {
		t.Fatalf("CreateProject(%q): %v", name, err)
	}
}

// newIntercomLog is a reader over a legacy log holding squawks.
func newIntercomLog(t *testing.T, squawks ...intercom.LegacySquawk) adminui.SquawkReader {
	t.Helper()
	l := intercom.NewLegacyMemLog()
	for _, m := range squawks {
		if _, err := l.Append(m); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	return adminui.NewSquawkReader(nil, nil, nil, l)
}

func actor(ref string) intercom.Target   { return intercom.Target{Kind: "actor", Ref: ref} }
func human(ref string) intercom.Target   { return intercom.Target{Kind: "human", Ref: ref} }
func channel(ref string) intercom.Target { return intercom.Target{Kind: "channel", Ref: ref} }

// fixtureLog is a legacy log of three squawks over two projects.
func fixtureLog(t *testing.T) adminui.SquawkReader {
	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	return newIntercomLog(t,
		intercom.LegacySquawk{From: actor("cove-1"), To: []intercom.Target{channel("eng")}, Body: "deploy started", At: t0, Project: "acme"},
		intercom.LegacySquawk{From: human("alice"), To: []intercom.Target{actor("cove-1")}, Body: "please HOLD", At: t0.Add(24 * time.Hour), Project: "acme"},
		intercom.LegacySquawk{From: actor("cove-9"), To: []intercom.Target{human("bob")}, Body: "beta status", At: t0.Add(48 * time.Hour), Project: "beta"},
	)
}

// fakeLauncher implements the exported jam.Launcher for hermetic studio tests.
type fakeLauncher struct{}

func (fakeLauncher) Raise(_ context.Context, spec jam.RaiseSpec, _ jam.LaunchCreds) (string, error) {
	return "loc-" + spec.ActorID, nil
}
func (fakeLauncher) Teardown(_ context.Context, _ jam.Instance) error { return nil }
func (fakeLauncher) Probe(_ context.Context, _ jam.Instance) (jam.Liveness, error) {
	return jam.LivenessAlive, nil
}
func (fakeLauncher) Pause(_ context.Context, _ jam.Instance) error   { return nil }
func (fakeLauncher) Unpause(_ context.Context, _ jam.Instance) error { return nil }
func (fakeLauncher) ApplyEgress(context.Context, jam.Instance, *jam.EgressPolicy) error {
	return nil
}
func (fakeLauncher) PrepareKit(context.Context, jam.KitDefinition) (jam.KitStatus, error) {
	return jam.KitStatus{State: jam.KitReady}, nil
}

func newSup(t *testing.T, store jam.Store) *jam.Supervisor {
	t.Helper()
	return jam.NewSupervisor(store, fakeLauncher{}, "test-holder",
		60*time.Second, 30*time.Second, time.Now, testLogger())
}
