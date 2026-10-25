package studio

import (
	"strings"
	"testing"
)

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
	if _, _, err := (Base{Kit: "./web"}).ParentRef(); err == nil || err.Error() != "path parents resolve only in a git import (kit import/track --git)" {
		t.Errorf("path-parent message: %v", err)
	}
	if _, err := (Base{Kit: "web", Image: "x"}).Kind(); err == nil {
		t.Error("kit+image accepted")
	}
	if k, err := (Base{Kit: "web"}).Kind(); err != nil || k != BaseKit {
		t.Errorf("kit alone: %v %v", k, err)
	}
	if k, err := (Base{Image: "x"}).Kind(); err != nil || k != BaseImage {
		t.Errorf("image: %v %v", k, err)
	}
	if k, err := (Base{}).Kind(); err != nil || k != BaseDefault {
		t.Errorf("default: %v %v", k, err)
	}
	if !(Base{Kit: "web"}).HasParent() || (Base{}).HasParent() {
		t.Error("HasParent wrong")
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

func TestBaseParentPath(t *testing.T) {
	for in, want := range map[string]string{"../web": "../web", "./sub/web": "sub/web", "../../a/b": "../../a/b", "./a/../b": "b"} {
		got, ok := Base{Kit: in}.ParentPath()
		if !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
		if err := (Base{Kit: in}).validate(); err != nil {
			t.Errorf("%q refused: %v", in, err)
		}
	}
	if _, ok := (Base{Kit: "web"}).ParentPath(); ok {
		t.Error("name treated as path")
	}
	long := "./" + strings.Repeat("a", 199) // 201 bytes
	for _, bad := range []string{".", "./", ".\\web", long, "./a/.."} {
		if err := (Base{Kit: bad}).validate(); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := (Base{Kit: "./" + strings.Repeat("a", 198)}).validate(); err != nil {
		t.Errorf("200 bytes refused: %v", err)
	}
}
