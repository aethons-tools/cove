package usersecret

import "testing"

func TestSourceKind(t *testing.T) {
	empty := ""
	val := "x"
	cases := []struct {
		name string
		src  Source
		want string
		err  bool
	}{
		{"value", Source{Value: &val}, "value", false},
		{"empty-value-is-still-a-source", Source{Value: &empty}, "value", false},
		{"command", Source{Command: []string{"gh", "auth"}}, "command", false},
		{"global", Source{Global: "shared"}, "global", false},
		{"mint", Source{Mint: "prof"}, "mint", false},
		{"none", Source{}, "", true},
		{"two", Source{Global: "a", Mint: "b"}, "", true},
		{"value-and-command", Source{Value: &val, Command: []string{"x"}}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.src.Kind()
			if c.err {
				if err == nil {
					t.Fatalf("want error, got kind %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("Kind() = %q, want %q", got, c.want)
			}
		})
	}
}
