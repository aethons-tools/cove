package main

import (
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
)

func TestContextScopeFlags(t *testing.T) {
	for args, want := range map[string]adminclient.ContextScope{
		"--role acme/dev": {Project: "acme", Role: "dev"},
		"--role dev":      {Project: jam.DefaultProject, Role: "dev"},
		"--project acme":  {Project: "acme"},
		"--jam":           {Jam: true},
	} {
		got, err := scopeFromArgs(strings.Fields(args))
		if err != nil || got != want {
			t.Errorf("%s → %+v, %v; want %+v", args, got, err, want)
		}
	}
	for _, bad := range []string{"", "--jam --project acme", "--role a/b --project c"} {
		if _, err := scopeFromArgs(strings.Fields(bad)); err == nil {
			t.Errorf("%q: want exactly one scope", bad)
		}
	}
}

// scopeFromArgs parses only the scope flags, then applies scopeOf (the rule
// cmdContext uses).
func scopeFromArgs(args []string) (adminclient.ContextScope, error) {
	fs := flag.NewFlagSet("scope", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	role, project := fs.String("role", "", ""), fs.String("project", "", "")
	jamWide := fs.Bool("jam", false, "")
	if err := fs.Parse(args); err != nil {
		return adminclient.ContextScope{}, err
	}
	return scopeOf(*role, *project, *jamWide)
}
