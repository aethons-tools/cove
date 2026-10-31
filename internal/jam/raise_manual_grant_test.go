package jam

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A manual raise takes an ephemeral grant like every other raise: denied, it
// is refused (409) with nothing raised; granted, it raises; a failed raise
// releases the grant.
func TestRaiseManualTakesGrant(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	denied := &fakeSessionAlloc{}
	if _, _, _, _, err := RaiseManual(context.Background(), store, sup, denied, "m1", RaiseSpec{Role: "guest"}); err == nil || WriteStatus(err, 500) != http.StatusConflict {
		t.Fatalf("denied raise = %v, want 409", err)
	}
	if len(store.ListInstances()) != 0 || len(denied.grants) != 1 {
		t.Fatalf("instances = %v, grants = %v", store.ListInstances(), denied.grants)
	}
	ok := &fakeSessionAlloc{granted: true}
	inst, id, _, _, err := RaiseManual(context.Background(), store, sup, ok, "m2", RaiseSpec{Role: "guest"})
	if err != nil || inst.ActorID != id || len(ok.grants) != 1 || !strings.HasSuffix(ok.grants[0], "/"+id+"/") || len(ok.releases) != 0 {
		t.Fatalf("granted raise = %+v, %v; grants %v releases %v", inst, err, ok.grants, ok.releases)
	}
	if _, _, _, _, err := RaiseManual(context.Background(), store, sup, ok, "m3", RaiseSpec{Role: "nope"}); err == nil || len(ok.releases) != 1 {
		t.Fatalf("failed raise = %v; releases %v", err, ok.releases)
	}
}
