package agentrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

type seqContext struct {
	bundles []sessionctx.Bundle
	errs    []error
	i       int
}

func (s *seqContext) Fetch(context.Context) (sessionctx.Bundle, error) {
	i := min(s.i, len(s.bundles)-1)
	s.i++
	if i < len(s.errs) && s.errs[i] != nil {
		return sessionctx.Bundle{}, s.errs[i]
	}
	return s.bundles[i], nil
}

func compileRole(core string) sessionctx.Bundle {
	return sessionctx.Compile(sessionctx.Inputs{Session: sessionctx.SessionFacts{Kind: "standing", Name: "n", Project: "p", Role: "r"}, Role: sessionctx.Layer{Core: core}})
}

func TestContextRefresher(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "context")
	first := compileRole("ONE")
	if err := writeContext(dir, first); err != nil {
		t.Fatal(err)
	}
	src := &seqContext{bundles: []sessionctx.Bundle{first, compileRole("TWO"), compileRole("TWO"), compileRole("THREE")}, errs: []error{nil, nil, nil, errors.New("jam down")}}
	r := newContextRefresher(src, first, dir, nil)
	if got := r.refresh(context.Background()); got != nil {
		t.Fatalf("unchanged: %v", got)
	}
	if got := r.refresh(context.Background()); strings.Join(got, ",") != "role" {
		t.Fatalf("changed: %v", got)
	}
	if core, _ := os.ReadFile(filepath.Join(dir, "CORE.md")); !strings.Contains(string(core), "TWO") {
		t.Fatalf("files not rewritten: %s", core)
	}
	if got := r.refresh(context.Background()); got != nil {
		t.Fatalf("same again: %v", got)
	}
	if got := r.refresh(context.Background()); got != nil {
		t.Fatalf("fetch error must keep the last bundle silently: %v", got)
	}
	if core, _ := os.ReadFile(filepath.Join(dir, "CORE.md")); !strings.Contains(string(core), "TWO") {
		t.Fatal("a failed fetch must not touch the files")
	}
}
