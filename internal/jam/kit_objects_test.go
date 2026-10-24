package jam

import (
	"bytes"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// bigKitStore holds kit app whose tree has a 4 MiB blob, and a managed mark
// for it (so ConfigMarkStates renders the live state).
func bigKitStore(t *testing.T) (*MemStore, string) {
	t.Helper()
	st := NewMemStore()
	tr, blobs := kittreetest.Tree(t, "kit.yml", "kind: studio\n", "big.bin", string(bytes.Repeat([]byte{'x'}, int(kittree.MaxFile))))
	if _, err := st.PushKitVersion("app", tr.Hash(), KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}, KitVersionMeta{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyConfig(ConfigWrite{Marks: []ConfigMark{{Kind: ConfigKindKit, Name: "app", Commit: "dir", Digest: "d", AppliedAt: time.Unix(1, 0).UTC()}}}); err != nil {
		t.Fatal(err)
	}
	e, _ := tr.Lookup("big.bin")
	return st, e.SHA256
}

func TestConfigSnapshotSharesBlobs(t *testing.T) {
	st, sha := bigKitStore(t)
	live := st.kitBlobs[sha]
	m := stateFromSnapshot(st.configSnapshot(), nil)
	if b := m.kitBlobs[sha]; len(b) == 0 || &b[0] != &live[0] {
		t.Fatal("the planner's scratch state copied a blob")
	}
	if b := st.ExportConfig().KitBlobs[sha]; len(b) == 0 || &b[0] == &live[0] {
		t.Fatal("ExportConfig must not share the store's blobs")
	}
}

func TestConfigMarkStatesDoesNotCopyBlobs(t *testing.T) {
	st, _ := bigKitStore(t)
	if _, err := ConfigMarkStates(st); err != nil { // warm up
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, err := ConfigMarkStates(st); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; n > uint64(kittree.MaxFile)/2 {
		t.Fatalf("ConfigMarkStates allocated %d bytes for a %d-byte blob", n, kittree.MaxFile)
	}
}

func TestPushKitVersionUnstoredTreeNamesNextVersion(t *testing.T) {
	st := NewMemStore()
	if _, err := st.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	tr, _ := kittreetest.Tree(t, "kit.yml", "x")
	_, err := st.PushKitVersion("app", tr.Hash(), KitObjects{}, KitVersionMeta{}, nil)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("kit app v2: tree")) {
		t.Fatalf("err = %v, want it to name v2", err)
	}
}

// gcFailStore fails every GCKitObjects.
type gcFailStore struct{ Store }

func (gcFailStore) GCKitObjects() (KitGC, error) {
	return KitGC{}, errors.New("pgstore: GCKitObjects: boom")
}

func TestRemoveKitCheckedLogsGCFailure(t *testing.T) {
	st := NewMemStore()
	if _, err := st.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	if err := RemoveKitChecked(gcFailStore{st}, log, "app"); err != nil {
		t.Fatalf("a GC failure must not fail the removal: %v", err)
	}
	if _, ok := st.GetKit("app"); ok {
		t.Fatal("kit not removed")
	}
	if out := buf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "kit objects GC failed") || !strings.Contains(out, "boom") {
		t.Fatalf("log = %q", out)
	}
}
