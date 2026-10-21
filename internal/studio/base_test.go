package studio

import "testing"

func validCtx(t *testing.T) string {
	t.Helper()
	return tarB64(t, map[string]string{"Dockerfile": "FROM x\n"})
}

func TestBaseKitParse(t *testing.T) {
	for in, want := range map[string]struct {
		name string
		ver  int
	}{"web-dev": {"web-dev", 0}, "web-dev@v3": {"web-dev", 3}} {
		n, v, err := Base{Kit: in}.ParentRef()
		if err != nil || n != want.name || v != want.ver {
			t.Errorf("%q: %q %d %v", in, n, v, err)
		}
	}
	for _, bad := range []string{"web@v01", "web@v+1", "web@", "web@v", "../web", "./web", ".web", "web@3", "web@v0", "web@vx", "we b", "web@v1@v2", "@v1", "web@v-1"} {
		if _, _, err := (Base{Kit: bad}).ParentRef(); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, _, err := (Base{Kit: "./web"}).ParentRef(); err == nil || err.Error() != "path parents are not supported yet; use the parent kit's registry name" {
		t.Errorf("path-parent message: %v", err)
	}
	if _, err := (Base{Kit: "web", Image: "x"}).Kind(); err == nil {
		t.Error("kit+image accepted")
	}
	if k, err := (Base{Kit: "web"}).Kind(); err != nil || k != BaseKit {
		t.Errorf("kit alone: %v %v", k, err)
	}
	if k, err := (Base{Kit: "web", Context: validCtx(t)}).Kind(); err != nil || k != BaseContextZip {
		t.Errorf("kit+context: %v %v", k, err)
	}
	if !(Base{Kit: "web", Context: validCtx(t)}).HasParent() || (Base{}).HasParent() {
		t.Error("HasParent wrong")
	}
	if _, err := (Base{Kit: "web", Context: "a", ContextDir: "b"}).Kind(); err == nil {
		t.Error("kit+two contexts accepted")
	}
}

func TestValidateRejectsBadParentRef(t *testing.T) {
	sk := StudioKit{Kind: Kind, Base: Base{Kit: "web@v0"}}
	if err := sk.Validate(); err == nil {
		t.Fatal("bad parent ref passed Validate")
	}
	sk.Base.Kit = "web@v2"
	if err := sk.Validate(); err != nil {
		t.Fatalf("good parent ref: %v", err)
	}
}

func TestParseKitYAML(t *testing.T) {
	sk, err := ParseStudioKit([]byte("kind: studio\nbase:\n  kit: web-dev@v3\n"))
	if err != nil || sk.Base.Kit != "web-dev@v3" {
		t.Fatalf("%+v %v", sk, err)
	}
}
