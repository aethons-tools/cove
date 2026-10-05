package modelspec

import "testing"

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]Version{
		"2.1.287 (Claude Code)": {2, 1, 287},
		"v2.0.0":                {2, 0, 0},
		"  3.4.5\n":             {3, 4, 5},
		"2.1.0-beta.1":          {2, 1, 0},
		"2.1.0+build7 extra":    {2, 1, 0},
	} {
		got, err := ParseVersion(in)
		if err != nil || got != want {
			t.Errorf("ParseVersion(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "Claude Code", "2.1", "2.1.x", "2.1.2.3", "a.b.c", "2..1", "2.+1.0"} {
		if _, err := ParseVersion(in); err == nil {
			t.Errorf("ParseVersion(%q) accepted", in)
		}
	}
}

func TestConstraintAllows(t *testing.T) {
	cases := []struct {
		constraint, version string
		want                bool
	}{
		{"", "0.0.1", true},
		{"*", "9.9.9", true},
		{"x", "1.0.0", true},
		{"2.1.287", "2.1.287", true},
		{"2.1.287", "2.1.286", false},
		{" 2.1.287 ", "2.1.287", true},
		{"2.x", "2.0.0", true},
		{"2.x", "2.99.1", true},
		{"2.x", "3.0.0", false},
		{"2.x", "1.9.9", false},
		{"2.*", "2.5.0", true},
		{"2.1.x", "2.1.0", true},
		{"2.1.x", "2.1.999", true},
		{"2.1.x", "2.2.0", false},
		{"2.1.*", "2.1.3", true},
		{">=2.0.0", "2.0.0", true},
		{">=2.0.0", "3.1.0", true},
		{">=2.0.0", "1.99.99", false},
		{">=2.1.5", "2.1.4", false},
		{">=2.1.5", "2.2.0", true},
		{">=2", "2.0.0", true},
		{">= 2.1", "2.0.9", false},
	}
	for _, c := range cases {
		con, err := ParseConstraint(c.constraint)
		if err != nil {
			t.Fatalf("ParseConstraint(%q): %v", c.constraint, err)
		}
		v, err := ParseVersion(c.version)
		if err != nil {
			t.Fatal(err)
		}
		if got := con.Allows(v); got != c.want {
			t.Errorf("%q allows %s = %v, want %v", c.constraint, c.version, got, c.want)
		}
	}
}

func TestConstraintAny(t *testing.T) {
	for in, want := range map[string]bool{"": true, " * ": true, "x": true, "2.x": false, ">=1.0.0": false} {
		c, err := ParseConstraint(in)
		if err != nil || c.Any() != want {
			t.Errorf("ParseConstraint(%q).Any() = %v, %v; want %v", in, c.Any(), err, want)
		}
	}
}

func TestParseConstraintRejects(t *testing.T) {
	for _, in := range []string{"latest", "2", "2.1", "~2.1", "^2", "2.x.1", "x.1", ">=", ">=2.x", ">2.0.0", "2.1.2.x", "<=2.0.0", "2.1.2.3", "-1.0.0"} {
		if _, err := ParseConstraint(in); err == nil {
			t.Errorf("ParseConstraint(%q) accepted", in)
		}
	}
}

// A model-spec's version is an exact install pin: digits only, three parts.
func TestParseExactVersion(t *testing.T) {
	if v, err := ParseExactVersion("2.1.287"); err != nil || v != (Version{2, 1, 287}) {
		t.Fatalf("2.1.287 = %v, %v", v, err)
	}
	for _, bad := range []string{"", "2.x", ">=2.0.0", "2.1", "v2.1.0", "2.1.0-beta", " 2.1.0", "2.1.0 ", "2.01.0", "1.2.3;id", "latest", "*"} {
		if _, err := ParseExactVersion(bad); err == nil {
			t.Errorf("%q accepted as an exact version", bad)
		}
	}
}
