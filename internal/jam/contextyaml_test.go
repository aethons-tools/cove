package jam

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func TestParseContextYAML(t *testing.T) {
	src := "core: C\nleaves:\n  - name: a.md\n    read-when: w\n    body: B\nresources:\n  - {name: cove, kind: repo, ref: aethons-tools/cove}\n"
	b, err := ParseContextYAML([]byte(src), nil)
	if err != nil || b.Core != "C" || len(b.Leaves) != 1 || b.Leaves[0].Body != "B" || len(b.Resources) != 1 {
		t.Fatalf("parse = %+v, %v", b, err)
	}
	out, err := MarshalContextYAML(b)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseContextYAML(out, nil)
	if err != nil || again.Core != b.Core || again.Leaves[0] != b.Leaves[0] || again.Resources[0] != b.Resources[0] {
		t.Fatalf("round trip = %+v, %v\n%s", again, err, out)
	}
	if _, err := ParseContextYAML([]byte("core: C\nleaves:\n  - name: a.md\n    read-when: w\n    file: a.md\n"), nil); err == nil || !strings.Contains(err.Error(), "not allowed here") {
		t.Errorf("file without a reader: %v", err)
	}
	read := func(name string) ([]byte, error) {
		if name == "a.md" {
			return []byte("FROM FILE"), nil
		}
		return nil, errors.New("nope")
	}
	if b, err := ParseContextYAML([]byte("core: C\nleaves:\n  - name: a.md\n    read-when: w\n    file: a.md\n"), read); err != nil || b.Leaves[0].Body != "FROM FILE" {
		t.Errorf("file with a reader = %+v, %v", b, err)
	}
	for _, bad := range []string{"", "core: \"\"\n", "nope: 1\n", "core: C\nleaves:\n  - {name: a.md, read-when: w, body: B, file: a.md}\n"} {
		if _, err := ParseContextYAML([]byte(bad), read); err == nil {
			t.Errorf("want an error for %q", bad)
		}
	}
}

func TestCheckedWriters(t *testing.T) {
	store := NewMemStore()
	if err := store.PutRole("default", Role{Name: "dev", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if err := SetRoleContextChecked(store, "default", "dev", ContextBody{Resources: []sessionctx.Resource{{Name: "r", Kind: "url", Ref: "x"}}}); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Errorf("resources on a role = %v, want 400", err)
	}
	if err := SetProjectContextChecked(store, "ghost", ContextBody{Core: "C"}); WriteStatus(err, 0) != http.StatusNotFound {
		t.Errorf("unknown project = %v, want 404", err)
	}
	if err := SetProjectContextChecked(store, "default", ContextBody{Leaves: []sessionctx.Leaf{{Name: sessionctx.ResourcesLeaf, ReadWhen: "w"}}}); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Errorf("reserved resources.md = %v, want 400", err)
	}
	if err := SetJamContextChecked(store, ContextBody{Core: strings.Repeat("x", sessionctx.BudgetJam+1)}); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Errorf("over budget = %v, want 400", err)
	}
	if err := SetJamContextChecked(store, ContextBody{Core: "J"}); err != nil || store.GetJamContext().Core != "J" {
		t.Fatalf("set jam: %v", err)
	}
	if err := SetJamContextChecked(store, ContextBody{}); err != nil || !store.GetJamContext().Empty() {
		t.Fatalf("clear jam: %v", err)
	}
}
