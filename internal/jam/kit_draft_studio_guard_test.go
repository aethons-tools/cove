package jam

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestDraftRaiseRequestGone: a raise whose context goes away while it
// waits for the draft's build answers a fixed text, rolls back its
// identity, and leaves the build running for a later raise to reuse.
func TestDraftRaiseRequestGone(t *testing.T) {
	fb := &fakeDraftBuilder{started: make(chan string, 1), release: make(chan struct{})}
	sup, store, _, s := draftStudioSetup(t, fb)
	release := sync.OnceFunc(func() { close(fb.release) })
	t.Cleanup(release)
	actorsBefore := len(store.ListActors())
	ctx, cancel := context.WithCancel(context.Background())
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _, _, err = RaiseManual(ctx, store, sup, nil, "try", RaiseSpec{Role: "dev", KitDraft: "my-app"})
	}()
	select {
	case <-fb.started:
	case <-done:
		t.Fatalf("raise ended before its build started: %v", err)
	}
	cancel()
	<-done
	if err == nil || WriteStatus(err, 500) != http.StatusServiceUnavailable || err.Error() != "raise: draft studio: "+draftRaiseGone {
		t.Fatalf("request gone = %v", err)
	}
	for _, inst := range store.ListInstances() {
		t.Fatalf("an instance was recorded: %+v", inst)
	}
	// The identity the raise enrolled is revoked: no actor is left for it.
	if got := store.ListActors(); len(got) != actorsBefore {
		t.Fatalf("actors = %+v, want the raise's identity rolled back (%d before)", got, actorsBefore)
	}
	if st, err := sup.KitDraftBuilds().Status("my-app", "", 0); err != nil || st.State != DraftBuildRunning || st.Tree != s.Tree {
		t.Fatalf("build = %+v %v, want it still running on %s", st, err, s.Tree)
	}
	release()
	if _, _, _, _, err := RaiseManual(context.Background(), store, sup, nil, "try", RaiseSpec{Role: "dev", KitDraft: "my-app"}); err != nil {
		t.Fatalf("raise after = %v", err)
	}
}

// TestNoRaiseSetsKitDraft: no raise path sets RaiseSpec.KitDraft (a draft
// studio) now that live drafts are gone from /admin and the CLI. Every raise
// path — the admin and console raises, personal and standing sessions, the
// requisitioner — builds its RaiseSpec without it; the one assignment is a
// context refresh of an already-raised draft studio (ContextForTurns), which
// never raises. It walks every non-test Go file in the module.
func TestNoRaiseSetsKitDraft(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root %s: %v", root, err)
	}
	type site struct{ file, fn, what string }
	var got []site
	fset := token.NewFileSet()
	walked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); path != root && (strings.HasPrefix(n, ".") || n == "vendor" || n == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		walked++
		rel, _ := filepath.Rel(root, path)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CompositeLit:
					if !isRaiseSpecType(n.Type) {
						return true
					}
					for _, el := range n.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "KitDraft" {
								got = append(got, site{rel, fd.Name.Name, "RaiseSpec{KitDraft: …}"})
							}
						}
					}
				case *ast.AssignStmt:
					for _, lhs := range n.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "KitDraft" {
							x, _ := sel.X.(*ast.Ident)
							name := "?"
							if x != nil {
								name = x.Name
							}
							got = append(got, site{rel, fd.Name.Name, name + ".KitDraft ="})
						}
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if walked < 100 {
		t.Fatalf("walked only %d Go files under %s", walked, root)
	}
	want := map[site]bool{
		{filepath.Join("internal", "jam", "supervisor.go"), "Raise", "inst.KitDraft ="}:           true, // the instance record, not the spec
		{filepath.Join("internal", "jam", "supervisor.go"), "ContextForTurns", "spec.KitDraft ="}: true, // a refresh of a raised draft studio
	}
	seen := map[site]bool{}
	for _, s := range got {
		if !want[s] {
			t.Errorf("%s (%s): %s — no raise path may raise a draft studio", s.file, s.fn, s.what)
		}
		seen[s] = true
	}
	for s := range want {
		if !seen[s] {
			t.Errorf("expected site gone: %s (%s): %s (update this test)", s.file, s.fn, s.what)
		}
	}
}

// isRaiseSpecType: RaiseSpec or jam.RaiseSpec.
func isRaiseSpecType(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name == "RaiseSpec"
	case *ast.SelectorExpr:
		return e.Sel.Name == "RaiseSpec"
	}
	return false
}
