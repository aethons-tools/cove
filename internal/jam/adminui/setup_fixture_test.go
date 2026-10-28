package adminui_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// The role page links its Setup tab; the tab is a shell that loads its
// sections lazily and shows nothing of the setup itself.

type countingImages struct{ n atomic.Int32 }

func (c *countingImages) ImageFactsOn(context.Context, string, string) (jam.ImageFacts, error) {
	c.n.Add(1)
	return jam.ImageFacts{}, jam.ErrImageNotPresent
}

// contextSnap is a context snapshot body in contextBody's form, with hostile
// text in CORE.md.
const contextSnap = "FILE                          BYTES\n" +
	"/agent-data/context/CORE.md   44\n" +
	"/agent-data/context/INDEX.md  6\n" +
	"\n--- /agent-data/context/CORE.md\n# Rules <script>alert(1)</script>\nevil\u202etxt\n" +
	"\n--- /agent-data/context/INDEX.md\nindex\n"

// setupStudioFixture: acme/dev (egress new.example.com) and its running studio
// sess-1 (image cove-ic:1), whose egress snapshot predates the role's change
// (stale) and whose context snapshot holds contextSnap; ghost-1 is a running
// studio of a deleted role (gone-1 a gone studio), whose context snapshot is not the file table.
func setupStudioFixture(t *testing.T) (http.Handler, *countingImages) {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "dev", Scope: jam.Scope{Egress: &jam.EgressPolicy{Domains: []string{"new.example.com"}}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, inst := range []jam.Instance{
		{ActorID: "sess-1", Project: "acme", Role: "dev", Phase: jam.PhaseLive, RaisedAt: now, LastSeen: now, ImageTag: "cove-ic:1"},
		{ActorID: "ghost-1", Project: "acme", Role: "ghost", Phase: jam.PhaseLive, RaisedAt: now, LastSeen: now},
		{ActorID: "gone-1", Project: "acme", Role: "dev", Phase: jam.PhaseGone, RaisedAt: now, LastSeen: now},
	} {
		if err := store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 24, 9, 30, 0, 0, time.UTC)
	for _, s := range []jam.SetupSnapshot{
		{ActorID: "sess-1", Part: jam.SetupPartEgress, At: at, Fingerprint: "egress-before",
			Body: "policy: role (1 domain(s))\nkit ceiling: unknown\nHOST             FROM\nold.example.com  role\n"},
		{ActorID: "sess-1", Part: jam.SetupPartContext, At: at, Fingerprint: "ctx", Body: contextSnap},
		{ActorID: "ghost-1", Part: jam.SetupPartEgress, At: at, Fingerprint: "x", Body: "policy: kit default\n"},
		{ActorID: "ghost-1", Part: jam.SetupPartContext, At: at, Fingerprint: "y", Body: "no context files\n--- /agent-data/context/CORE.md\nnot a file\n"},
	} {
		if err := store.PutSetupSnapshot(s); err != nil {
			t.Fatal(err)
		}
	}
	imgs := &countingImages{}
	// No supervisor: a zero jam.Supervisor{} panics resolving the image status
	// of a studio with an image tag (the attention items read it).
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil,
		adminui.WithSetupDeps(jam.SetupDeps{Store: store, Images: imgs}))
	return h, imgs
}

// The agent page links Setup while its studio runs; the shell offers the
