package adminui

import (
	"testing"

	"github.com/aethons-tools/cove/internal/studio"
)

func TestBaseLabel(t *testing.T) {
	for _, tc := range []struct {
		sk   studio.StudioKit
		want string
	}{
		{studio.StudioKit{}, "blessed default"},
		{studio.StudioKit{BuildContext: "abc"}, "Dockerfile"},
		{studio.StudioKit{Base: studio.Base{Image: "x:1"}}, "image"},
		{studio.StudioKit{Base: studio.Base{Kit: "web"}}, "kit: web"},
		{studio.StudioKit{Base: studio.Base{Kit: "web"}, BuildContext: "abc"}, "kit: web + Dockerfile"},
		{studio.StudioKit{Base: studio.Base{Kit: "web@v2"}, BuildContext: "abc"}, "kit: web@v2 + Dockerfile"},
	} {
		if got := baseLabel(tc.sk); got != tc.want {
			t.Errorf("baseLabel(%+v) = %q, want %q", tc.sk.Base, got, tc.want)
		}
	}
}
