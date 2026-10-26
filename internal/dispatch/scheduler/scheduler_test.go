package scheduler

import "testing"

func TestClassifyLabels(t *testing.T) {
	cases := []struct {
		name           string
		labels         []string
		classPrefix    string
		dispatchPrefix string
		wantClass      string
		wantDispatch   bool
	}{
		{"dispatch label present", []string{"dispatch:go", "class:implement"}, "class:", "dispatch:", "implement", true},
		{"no dispatch label", []string{"class:implement"}, "class:", "dispatch:", "implement", false},
		{"no labels at all", nil, "class:", "dispatch:", "", false},
		{"dispatch only, no class", []string{"dispatch:anything"}, "class:", "dispatch:", "", true},
		{"unrelated labels", []string{"bug", "p1"}, "class:", "dispatch:", "", false},
		{"empty dispatch prefix = no gate", []string{"whatever"}, "class:", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotClass, gotDispatch := ClassifyLabels(c.labels, c.classPrefix, c.dispatchPrefix)
			if gotClass != c.wantClass || gotDispatch != c.wantDispatch {
				t.Fatalf("ClassifyLabels(%v) = (%q,%v); want (%q,%v)",
					c.labels, gotClass, gotDispatch, c.wantClass, c.wantDispatch)
			}
		})
	}
}
