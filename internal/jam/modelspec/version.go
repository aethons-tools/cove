package modelspec

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a harness CLI release, numeric core only (a pre-release or build
// suffix is ignored).
type Version struct{ Major, Minor, Patch int }

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

func (v Version) less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// ParseVersion reads a CLI's version from its `--version` output: the first
// whitespace-separated field, an optional leading "v", then MAJOR.MINOR.PATCH;
// a "-pre" or "+build" suffix is dropped. "2.1.287 (Claude Code)" → 2.1.287.
func ParseVersion(s string) (Version, error) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return Version{}, fmt.Errorf("empty version")
	}
	core := strings.TrimPrefix(f[0], "v")
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	n, err := numbers(core, 3, 3)
	if err != nil {
		return Version{}, fmt.Errorf("version %q: %w", f[0], err)
	}
	return Version{n[0], n[1], n[2]}, nil
}

// numbers parses a dotted list of between lo and hi non-negative integers.
func numbers(s string, lo, hi int) ([]int, error) {
	parts := strings.Split(s, ".")
	if len(parts) < lo || len(parts) > hi {
		if lo == hi {
			return nil, fmt.Errorf("want %d dot-separated numbers", lo)
		}
		return nil, fmt.Errorf("want %d to %d dot-separated numbers", lo, hi)
	}
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || (p[0] == '+') {
			return nil, fmt.Errorf("%q is not a number", p)
		}
		out[i] = n
	}
	return out, nil
}

type constraintKind int

const (
	anyVersion constraintKind = iota
	exactVersion
	prefixVersion // X.x / X.Y.x: the leading components match
	minVersion    // >=X.Y.Z
)

// Constraint is a parsed harness CLI version constraint. The grammar:
//
//	""  "*"  "x"         any version (no check)
//	X.Y.Z                exactly that release
//	X.x  X.*             any X.*.* release
//	X.Y.x  X.Y.*         any X.Y.* release
//	>=X  >=X.Y  >=X.Y.Z  that release or later (missing components are 0)
type Constraint struct {
	kind constraintKind
	v    Version
	n    int // prefixVersion: how many leading components must match (1 or 2)
	raw  string
}

// ParseConstraint parses a version constraint (see Constraint).
func ParseConstraint(s string) (Constraint, error) {
	raw := strings.TrimSpace(s)
	c := Constraint{raw: raw}
	bad := func(why string) (Constraint, error) {
		return Constraint{}, fmt.Errorf("version constraint %q: %s (want X.Y.Z, X.x, X.Y.x or >=X.Y.Z)", raw, why)
	}
	switch {
	case raw == "" || raw == "*" || raw == "x":
		return c, nil
	case strings.HasPrefix(raw, ">="):
		n, err := numbers(strings.TrimSpace(raw[2:]), 1, 3)
		if err != nil {
			return bad(err.Error())
		}
		n = append(n, 0, 0)
		c.kind, c.v = minVersion, Version{n[0], n[1], n[2]}
		return c, nil
	case strings.HasSuffix(raw, ".x") || strings.HasSuffix(raw, ".*"):
		n, err := numbers(raw[:len(raw)-2], 1, 2)
		if err != nil {
			return bad(err.Error())
		}
		c.kind, c.n = prefixVersion, len(n)
		n = append(n, 0)
		c.v = Version{Major: n[0], Minor: n[1]}
		return c, nil
	}
	n, err := numbers(raw, 3, 3)
	if err != nil {
		return bad(err.Error())
	}
	c.kind, c.v = exactVersion, Version{n[0], n[1], n[2]}
	return c, nil
}

// Any reports whether the constraint accepts every version (no check needed).
func (c Constraint) Any() bool { return c.kind == anyVersion }

// Allows reports whether v satisfies the constraint.
func (c Constraint) Allows(v Version) bool {
	switch c.kind {
	case exactVersion:
		return v == c.v
	case prefixVersion:
		return v.Major == c.v.Major && (c.n == 1 || v.Minor == c.v.Minor)
	case minVersion:
		return !v.less(c.v)
	}
	return true
}

// String returns the constraint as written (trimmed).
func (c Constraint) String() string { return c.raw }
