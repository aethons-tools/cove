package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"gopkg.in/yaml.v3"
)

// cmdExport fetches the config snapshot and writes it to a file (or stdout).
func cmdExport(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	format := fs.String("format", "json", "output format: json|yaml")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if *format != "json" && *format != "yaml" {
		fmt.Fprintln(stderr, "at-jam export: --format must be json or yaml")
		return 2
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam export:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))

	snap, err := c.ExportConfig()
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	out, err := marshalSnapshot(snap, *format)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam export:", err)
		return 1
	}
	if len(pos) == 0 || pos[0] == "-" {
		stdout.Write(out)
		return 0
	}
	// 0o600: the backup carries token hashes — treat it as sensitive.
	if err := os.WriteFile(pos[0], out, 0o600); err != nil {
		fmt.Fprintln(stderr, "at-jam export:", err)
		return 1
	}
	// WriteFile keeps an existing file's mode; enforce 0o600 regardless.
	if err := os.Chmod(pos[0], 0o600); err != nil {
		fmt.Fprintln(stderr, "at-jam export:", err)
		return 1
	}
	fmt.Fprintln(stdout, "exported config to", pos[0])
	return 0
}

// cmdImport reads a snapshot file (or stdin) and restores it into an empty Jam.
func cmdImport(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	format := fs.String("format", "", "input format: json|yaml (default: sniff)")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if *format != "" && *format != "json" && *format != "yaml" {
		fmt.Fprintln(stderr, "at-jam import: --format must be json or yaml")
		return 2
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam import:", err)
		return 2
	}

	var data []byte
	var err error
	if len(pos) == 0 || pos[0] == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(pos[0])
	}
	if err != nil {
		fmt.Fprintln(stderr, "at-jam import:", err)
		return 1
	}
	snap, err := unmarshalSnapshot(data, *format)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam import:", err)
		return 1
	}

	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	if err := c.ImportConfig(snap); err != nil {
		if errors.Is(err, adminclient.ErrConflict) {
			fmt.Fprintln(stderr, "at-jam import: target Jam is not empty: it already has config (import requires an empty config); refusing")
			return 1
		}
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	fmt.Fprintf(stdout, "imported config: %d actors, %d kits, %d destinations, %d projects\n",
		len(snap.Actors), len(snap.Kits), len(snap.Destinations), len(snap.Projects))
	return 0
}

func marshalSnapshot(s jam.ConfigSnapshot, format string) ([]byte, error) {
	if format == "yaml" {
		return yaml.Marshal(s)
	}
	return json.MarshalIndent(s, "", "  ")
}

// unmarshalSnapshot decodes data as the given format, or sniffs when format is
// "": a leading '{' (after whitespace) is JSON, otherwise YAML.
func unmarshalSnapshot(data []byte, format string) (jam.ConfigSnapshot, error) {
	var s jam.ConfigSnapshot
	if format == "" {
		if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
			format = "json"
		} else {
			format = "yaml"
		}
	}
	if format == "yaml" {
		return s, yaml.Unmarshal(data, &s)
	}
	return s, json.Unmarshal(data, &s)
}
