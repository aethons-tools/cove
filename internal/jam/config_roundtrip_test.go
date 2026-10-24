package jam

import (
	"context"
	"reflect"
	"testing"
)

// TestConfigRoundTripReplicates is the replication check (spec §8): a Jam's
// exported tree applied to a fresh serve reproduces the same tree, carries no
// actors, and applies again as a no-op.
func TestConfigRoundTripReplicates(t *testing.T) {
	src, _ := populatedJam(t)
	exp, err := ExportConfigTree(src.ExportConfig(), nil, map[string]any{"display-name": "A"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := exp.Files["model-specs/"+DefaultModelSpec+".yml"]; !ok {
		t.Fatalf("the fixture's %s model spec is not exported", DefaultModelSpec)
	}
	dir := t.TempDir()
	writeExport(t, dir, exp)
	tr, err := LoadConfigTree(dir)
	if err != nil || len(tr.Refusals) > 0 {
		t.Fatalf("%v %v", err, tr.Refusals)
	}
	dst := NewMemStore()
	// A fresh serve seeds the default model spec (here as the pool: a fresh
	// Jam has no anthropic destination to take its principal from).
	if created, err := EnsureDefaultModelSpec(dst, true); err != nil || !created {
		t.Fatalf("seed: created=%v err=%v", created, err)
	}
	res, err := ApplyConfigTree(context.Background(), dst, nil, tr, allCreds, dirOpts(), nil, "local")
	if err != nil {
		t.Fatal(err)
	}
	if op := opOf(res.Plan, ConfigKindModelSpec, DefaultModelSpec); op.Action != ConfigUpdate {
		t.Fatalf("the seeded default is not adopted and updated: %+v", op)
	}
	got, _ := ExportConfigTree(dst.ExportConfig(), nil, map[string]any{"display-name": "A"}, true)
	if !reflect.DeepEqual(got.Files, exp.Files) {
		for p := range exp.Files {
			if got.Files[p] != exp.Files[p] {
				t.Errorf("%s differs:\n%s\n---\n%s", p, exp.Files[p], got.Files[p])
			}
		}
		for p := range got.Files {
			if _, ok := exp.Files[p]; !ok {
				t.Errorf("%s is extra on the replica", p)
			}
		}
		t.FailNow()
	}
	if !reflect.DeepEqual(got.Kits, exp.Kits) {
		t.Fatalf("kit dirs differ on the replica:\n%+v\n---\n%+v", exp.Kits, got.Kits)
	}
	if len(dst.ListActors()) != 0 {
		t.Fatal("actors replicated")
	}
	again, err := ApplyConfigTree(context.Background(), dst, nil, tr, allCreds, dirOpts(), nil, "local")
	if err != nil || again.Plan.Counts()[ConfigUnchanged] != len(again.Plan.Ops) {
		t.Fatalf("second apply is not a no-op: %v %+v", err, again.Plan.Ops)
	}
}
