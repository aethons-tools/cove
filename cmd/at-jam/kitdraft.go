package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/textesc"
)

// kitDraftVerbs is each `kit draft` verb's arguments (for its usage line)
// and how many are positional.
var kitDraftVerbs = map[string]struct {
	args string
	n    int
}{
	"open":    {"<kit> [--from vN]", 1},
	"status":  {"<kit> [--json]", 1},
	"get":     {"<kit> <path> [-o file]", 2},
	"put":     {"<kit> <path> <file|-> [--mode 755]", 3},
	"rm":      {"<kit> <path>", 2},
	"discard": {"<kit>", 1},
	"publish": {"<kit> [--pin]", 1},
	"sync":    {"<kit> <dir>", 2},
}

const kitDraftUsage = "at-jam kit draft: expected open|status|get|put|rm|discard|publish|sync"

// cmdKitDraft is `at-jam kit draft <verb>` (docs/usage/jam/kits-drafts.md).
// A write sends the draft's etag as If-Match: --etag, else the one a status
// fetched just before it.
func cmdKitDraft(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, kitDraftUsage)
		return 2
	}
	verb, rest := args[0], args[1:]
	if verb == "-h" || verb == "-help" || verb == "--help" {
		fmt.Fprintln(stdout, "Usage of kit draft:")
		for _, v := range strings.Split(strings.TrimPrefix(kitDraftUsage, "at-jam kit draft: expected "), "|") {
			fmt.Fprintf(stdout, "  at-jam kit draft %s %s\n", v, kitDraftVerbs[v].args)
		}
		return 0
	}
	spec, known := kitDraftVerbs[verb]
	if !known {
		fmt.Fprintln(stderr, kitDraftUsage)
		return 2
	}
	fs := flag.NewFlagSet("kit draft "+verb, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	// Each verb defines only its own flags, so another verb's is a usage error.
	from, asJSON, outFile, mode, pin, etag := new(string), new(bool), new(string), new(string), new(bool), new(string)
	switch verb {
	case "open":
		from = fs.String("from", "", "the version to open the draft from (vN; default: current)")
	case "status":
		asJSON = fs.Bool("json", false, "print the status as JSON")
	case "get":
		outFile = fs.String("o", "", "write the file here (with its mode) instead of stdout")
	case "put":
		mode = fs.String("mode", "", "the file's mode, 755 or 644 (default: the file's, else 644)")
	case "publish":
		pin = fs.Bool("pin", false, "pin the kits that follow this one to the version they follow now")
	}
	if verb == "put" || verb == "rm" || verb == "discard" || verb == "publish" || verb == "sync" {
		etag = fs.String("etag", "", "write against this draft etag (default: fetch the draft's status first)")
	}
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "at-jam kit draft %s: %s\n", verb, msg)
		return 2
	}
	if err := validateApp(*app); err != nil {
		return usage(err.Error())
	}
	if len(pos) != spec.n {
		return usage("expected " + spec.args)
	}
	if spec.n >= 2 {
		if err := kittree.CheckPath(pos[1]); err != nil && verb != "sync" {
			return usage(err.Error())
		}
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "at-jam:", textesc.Escape(err.Error(), false))
		return 1
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	kit, name := pos[0], textesc.Escape(pos[0], false)
	// etagFor is the etag a write is sent against.
	etagFor := func() (string, error) {
		if *etag != "" {
			return `"` + strings.Trim(*etag, `"`) + `"`, nil
		}
		st, err := c.KitDraftStatus(kit)
		return st.ETag, err
	}
	switch verb {
	case "open":
		v, err := parseDraftVersion(*from)
		if err != nil {
			return usage(err.Error())
		}
		st, err := c.OpenKitDraft(kit, v)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "opened draft %s from v%d (etag %s)\n", name, st.Base, textesc.Escape(st.ETag, false))
	case "status":
		st, err := c.KitDraftStatus(kit)
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(st); err != nil {
				return fail(err)
			}
			break
		}
		printDraftStatus(stdout, st)
	case "get":
		b, m, err := c.KitDraftFile(kit, pos[1])
		if err != nil {
			return fail(err)
		}
		if *outFile == "" {
			_, err = stdout.Write(b)
		} else {
			// The entry's mode; a temp file renamed over the destination
			// replaces a symlink there instead of writing through it.
			perm := os.FileMode(0o644)
			if m == "0755" {
				perm = 0o755
			}
			err = writeFileReplace(*outFile, b, perm)
		}
		if err != nil {
			return fail(err)
		}
	case "put":
		m, err := draftModeFlag(*mode)
		if err != nil {
			return usage(err.Error())
		}
		data, err := readConfig(pos[2])
		if err != nil {
			return fail(err)
		}
		tag, err := etagFor()
		if err != nil {
			return fail(err)
		}
		s, err := c.PutKitDraftFile(kit, pos[1], data, m, tag)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "saved %s to draft %s (etag %s)\n", textesc.Escape(pos[1], false), name, textesc.Escape(s.ETag, false))
		printProblems(stdout, s.Problems)
	case "rm":
		tag, err := etagFor()
		if err != nil {
			return fail(err)
		}
		s, err := c.RemoveKitDraftFile(kit, pos[1], tag)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "removed %s from draft %s (etag %s)\n", textesc.Escape(pos[1], false), name, textesc.Escape(s.ETag, false))
		printProblems(stdout, s.Problems)
	case "discard":
		tag, err := etagFor()
		if err != nil {
			return fail(err)
		}
		if err := c.DiscardKitDraft(kit, tag); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "discarded draft %s\n", name)
	case "sync":
		// The directory as `kit push` reads it, sent whole; the draft must
		// already be open.
		dir := pos[1]
		files, err := readKitDir(dir)
		if err != nil {
			return fail(err)
		}
		var body bytes.Buffer
		if err := kittree.WriteTar(&body, files); err != nil {
			return fail(err)
		}
		tag, err := etagFor()
		if err != nil {
			return fail(err)
		}
		s, err := c.ReplaceKitDraft(kit, body.Bytes(), tag)
		if err != nil {
			return fail(err)
		}
		if len(s.Changed) == 0 {
			fmt.Fprintf(stdout, "draft %s unchanged (etag %s)\n", name, textesc.Escape(s.ETag, false))
		} else {
			fmt.Fprintf(stdout, "synced %s to draft %s (etag %s)\n", textesc.Escape(dir, false), name, textesc.Escape(s.ETag, false))
			printDraftChanges(stdout, s.Changed)
		}
		printProblems(stdout, s.Problems)
	case "publish":
		tag, err := etagFor()
		if err != nil {
			return fail(err)
		}
		res, err := c.PublishKitDraft(kit, *pin, tag)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "published draft %s as v%d\n", name, res.Version)
		for _, p := range res.Pinned {
			fmt.Fprintf(stdout, "pinned %s\n", textesc.Escape(p, false))
		}
		if res.Drift {
			fmt.Fprintln(stderr, "warning:", textesc.Escape(res.Warning, false))
		}
	}
	return 0
}

// parseDraftVersion reads --from: "" (current), "3" or "v3".
func parseDraftVersion(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, "v"))
	if err != nil || n < 1 {
		return 0, errors.New("--from must be a version (vN)")
	}
	return n, nil
}

// draftModeFlag reads --mode: "" (keep), 644/0644 or 755/0755.
func draftModeFlag(s string) (string, error) {
	switch s {
	case "":
		return "", nil
	case "644", "0644":
		return "0644", nil
	case "755", "0755":
		return "0755", nil
	}
	return "", errors.New("--mode must be 755 or 644")
}

func draftTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// printDraftStatus renders a draft's status; every name is escaped.
func printDraftStatus(w io.Writer, st jam.KitDraftStatus) {
	fmt.Fprintf(w, "draft %s: base v%d, current v%d\n", textesc.Escape(st.Kit, false), st.Base, st.Current)
	if st.Current != st.Base {
		fmt.Fprintf(w, "  v%d landed after the draft was opened; publishing replaces it with the draft (nothing is merged)\n", st.Current)
	}
	fmt.Fprintf(w, "etag %s\n", textesc.Escape(st.ETag, false))
	fmt.Fprintf(w, "saved %s by %s (opened %s)\n", draftTime(st.UpdatedAt), textesc.Escape(st.Actor, false), draftTime(st.OpenedAt))
	if st.Managed {
		fmt.Fprintln(w, "managed by the config repo: publishing will drift from it")
	}
	fmt.Fprintf(w, "files (%d):\n", len(st.Entries))
	for _, e := range st.Entries {
		fmt.Fprintf(w, "  %s %9d  %s\n", textesc.Escape(e.Mode, false), e.Size, textesc.Escape(e.Path, false))
	}
	if len(st.Changes) == 0 {
		fmt.Fprintf(w, "no changes vs v%d\n", st.Base)
	} else {
		fmt.Fprintf(w, "changes vs v%d:\n", st.Base)
		printDraftChanges(w, st.Changes)
	}
	if len(st.Problems) == 0 {
		fmt.Fprintln(w, "problems: none")
	} else {
		printProblems(w, st.Problems)
	}
}

// printDraftChanges prints one "  <kind> <path>" line per change.
func printDraftChanges(w io.Writer, cs []jam.DraftChange) {
	for _, c := range cs {
		fmt.Fprintf(w, "  %-8s %s\n", textesc.Escape(c.Kind, false), textesc.Escape(c.Path, false))
	}
}

// printProblems prints a draft's problems; nothing when there are none.
func printProblems(w io.Writer, ps []string) {
	if len(ps) == 0 {
		return
	}
	fmt.Fprintln(w, "problems:")
	for _, p := range ps {
		fmt.Fprintf(w, "  - %s\n", textesc.Escape(p, false))
	}
}
