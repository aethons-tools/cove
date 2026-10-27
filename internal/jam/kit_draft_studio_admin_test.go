package jam

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestAdminRaisesADraftStudio(t *testing.T) {
	sup, store, _, s := draftStudioSetup(t, &fakeDraftBuilder{})
	var logs bytes.Buffer
	h := NewAdminHandler(store, sup, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	rec := doJSON(t, h, "POST", "/admin/coves", CoveRaiseBody{ID: "try", Role: "dev", KitDraft: "my-app"})
	var res CoveRaiseResult
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &res) != nil || res.KitDraftTree != s.Tree || res.Label != "try" {
		t.Fatalf("raise = %d %s", rec.Code, rec.Body)
	}
	if inst, ok := store.GetInstance(res.ID); !ok || inst.KitDraft == nil || inst.SessionKind != "" || inst.Name != "try" {
		t.Fatalf("instance = %+v %v (a manual, ephemeral session)", inst, ok)
	}
	if !regexp.MustCompile(`(?m)msg="admin cove raised" .*(^| )kit_draft=my-app( |$)`).MatchString(logs.String()) {
		t.Fatalf("audit:\n%s", logs.String())
	}
	rec = doJSON(t, h, "POST", "/admin/coves", CoveRaiseBody{ID: "try2", Role: "dev", KitDraft: "web-dev"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "kit web-dev has no draft") {
		t.Fatalf("no draft = %d %s", rec.Code, rec.Body)
	}
}

// TestAdminDraftRaiseRequestGone: a raise whose request goes away while
// it waits for the draft's build answers a fixed text, rolls back its
// identity, and leaves the build running for a later raise to reuse.
func TestAdminDraftRaiseRequestGone(t *testing.T) {
	fb := &fakeDraftBuilder{started: make(chan string, 1), release: make(chan struct{})}
	sup, store, _, s := draftStudioSetup(t, fb)
	release := sync.OnceFunc(func() { close(fb.release) })
	t.Cleanup(release)
	h := NewAdminHandler(store, sup, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.DiscardHandler), nil)
	actorsBefore := len(store.ListActors())
	ctx, cancel := context.WithCancel(context.Background())
	body, _ := json.Marshal(CoveRaiseBody{ID: "try", Role: "dev", KitDraft: "my-app"})
	req := httptest.NewRequestWithContext(ctx, "POST", "/admin/coves", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:5000"
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(rec, req) }()
	select {
	case <-fb.started:
	case <-done:
		t.Fatalf("raise ended before its build started: %d %s", rec.Code, rec.Body)
	}
	cancel()
	<-done
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "raise: draft studio: "+draftRaiseGone+"\n" {
		t.Fatalf("request gone = %d %q", rec.Code, rec.Body)
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
	rec = doJSON(t, h, "POST", "/admin/coves", CoveRaiseBody{ID: "try", Role: "dev", KitDraft: "my-app"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("raise after = %d %s", rec.Code, rec.Body)
	}
}

// TestOnlyTheAdminRaiseSetsKitDraft: RaiseSpec.KitDraft (a draft studio)
// is set by POST /admin/coves only. Every other raise path — the admin UI's
// raise, personal and standing sessions, the requisitioner — builds its
// RaiseSpec without it, and the one other assignment is a context refresh
// of an already-raised draft studio (ContextForActor), which never raises.
// It walks every non-test Go file in the module.
func TestOnlyTheAdminRaiseSetsKitDraft(t *testing.T) {
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
	// The POST /admin/coves handler is a closure in the function that
	// registers it; its RaiseSpec literal is the one place a raise names
	// KitDraft.
	want := map[site]bool{
		{filepath.Join("internal", "jam", "admin.go"), adminRaiseFunc(t, root), "RaiseSpec{KitDraft: …}"}: true,
		{filepath.Join("internal", "jam", "supervisor.go"), "Raise", "inst.KitDraft ="}:                   true, // the instance record, not the spec
		{filepath.Join("internal", "jam", "supervisor.go"), "ContextForActor", "spec.KitDraft ="}:         true, // a refresh of a raised draft studio
	}
	seen := map[site]bool{}
	for _, s := range got {
		if !want[s] {
			t.Errorf("%s (%s): %s — only POST /admin/coves may raise a draft studio", s.file, s.fn, s.what)
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

// adminRaiseFunc is the name of the function in admin.go whose body
// registers POST /admin/coves.
func adminRaiseFunc(t *testing.T, root string) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, "internal", "jam", "admin.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		found := false
		ast.Inspect(fd, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Value == `"POST /admin/coves"` {
				found = true
			}
			return !found
		})
		if found {
			return fd.Name.Name
		}
	}
	t.Fatal("admin.go registers no POST /admin/coves")
	return ""
}
