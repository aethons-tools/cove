package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"gopkg.in/yaml.v3"
)

// cmdModelSpec manages model-specs via the admin API: add/update take a YAML
// spec file, show prints one as YAML (a valid input to update), list prints a
// one-line summary per spec, delete removes one. Validation is the server's
// (jam.ValidateModelSpec) — the CLI only parses.
func cmdModelSpec(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	const usage = "at-jam model-spec: expected add <file.yaml> | update <file.yaml> | show <name> | list | delete <name>"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("model-spec "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token for an OIDC-gated admin API (env: AT_JAM_ADMIN_TOKEN)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam model-spec:", err)
		return 2
	}
	oneArg := func(what string) (string, bool) {
		if len(pos) != 1 {
			fmt.Fprintf(stderr, "at-jam model-spec %s: expected one %s\n", sub, what)
			return "", false
		}
		return pos[0], true
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "add", "update":
		path, ok := oneArg("YAML spec file")
		if !ok {
			return 2
		}
		m, err := readModelSpec(path)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		verb := "added"
		if sub == "add" {
			err = c.CreateModelSpec(m)
		} else {
			verb, err = "updated", c.UpdateModelSpec(m)
		}
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, verb, "model-spec", m.Name)
	case "list":
		specs, err := c.ListModelSpecs()
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		for _, m := range specs {
			provider := ""
			if m.Claude != nil {
				provider = m.Claude.Provider
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", m.Name, m.Type, m.Version, m.Principal.Credential, provider)
		}
	case "show":
		name, ok := oneArg("model-spec name")
		if !ok {
			return 2
		}
		m, err := c.GetModelSpec(name)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		out, err := yaml.Marshal(m)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		stdout.Write(out)
	case "delete":
		name, ok := oneArg("model-spec name")
		if !ok {
			return 2
		}
		if err := c.DeleteModelSpec(name); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "deleted model-spec", name)
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	return 0
}

// readModelSpec strictly decodes one model-spec from a YAML file: an unknown
// key (a typo) is an error rather than silently dropped.
func readModelSpec(path string) (jam.ModelSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return jam.ModelSpec{}, err
	}
	var m jam.ModelSpec
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return jam.ModelSpec{}, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}
