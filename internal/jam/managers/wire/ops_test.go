package wire

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
)

func TestRaiseRequestRoundTrip(t *testing.T) {
	in := RaiseRequest{
		Spec: jam.RaiseSpec{
			ActorID: "a1", Project: "p", Role: "r", Unit: "u", Mission: "m", Prompt: "pr",
			Owner: "bob", Name: "n", SessionKind: "standing",
			Egress: &jam.EgressPolicy{Domains: []string{"example.com"}},
			Kit:    jam.KitRef{ID: "k", Version: 3, Digest: "d"},
			Connector: &snippet.Connector{
				Env:       map[string]string{"A": "b"},
				GitRoute:  "/git/",
				ModelSpec: &modelspec.Spec{Name: "ms", Version: "1.2.3"},
			},
			Context:    &sessionctx.Bundle{Core: "core", Files: map[string]string{"f.md": "x"}, Fingerprint: "fp", Layers: map[string]string{"l": "1"}},
			DraftImage: &jam.KitDraftImage{Tag: "t", ID: "i"},
			KitDraft:   "kd",
		},
		Creds: jam.LaunchCreds{IdentityToken: "tok", LaunchSecret: "sec"},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out RaiseRequest
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip differs:\n%+v\n%+v", in, out)
	}
}

func TestStatusReportRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	in := StatusReport{
		Host: &jam.HostStats{At: at, MemTotal: 8 << 30, MemUsed: 3 << 30, DiskTotal: 100 << 30, DiskUsed: 91 << 30,
			Reclaimable: jam.Reclaimable{Images: 1, BuildCache: 2, Volumes: 3},
			LastCleanup: &jam.CleanupRecord{At: at, Freed: 5, Reason: "high mark", Err: ""}},
		Activities: []jam.ManagerActivity{{ID: "a", Kind: "prepare-kit", Kit: jam.KitRef{ID: "k", Version: 2}, Step: "build", Started: at}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out StatusReport
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", out, in)
	}
}
