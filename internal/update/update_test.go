package update

import (
	"os"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestTargetPrefersFlagThenEnv(t *testing.T) {
	if got := Target("1-0102", "9-9999"); got != "1-0102" {
		t.Fatalf("flag must win over env; got %q", got)
	}
	if got := Target("", "9-9999"); got != "9-9999" {
		t.Fatalf("env (COVE_VERSION) is the fallback pin; got %q", got)
	}
	if got := Target("", ""); got != "" {
		t.Fatalf("no flag/env means unresolved latest (empty); got %q", got)
	}
}

func TestUpToDate(t *testing.T) {
	if !UpToDate("1-0102", "1-0102") {
		t.Fatal("equal current/target must be up to date")
	}
	if !UpToDate("1-0102", " 1-0102 ") {
		t.Fatal("surrounding whitespace must not defeat the compare")
	}
	if UpToDate("1-0102", "2-0203") {
		t.Fatal("a newer target is not up to date")
	}
	if UpToDate("1-0102", "") {
		t.Fatal("an unresolved (empty) target is never up to date")
	}
}

func TestEnvPinsTarget(t *testing.T) {
	if env := Env(""); env != nil {
		t.Fatalf("no target means no explicit env (knobs inherited); got %v", env)
	}
	env := Env("3-0405")
	if len(env) != 1 || env[0] != "COVE_VERSION=3-0405" {
		t.Fatalf("a target must pin COVE_VERSION; got %v", env)
	}
}

func TestWriteScriptRoundTrips(t *testing.T) {
	path, cleanup, err := WriteScript([]byte("#!/usr/bin/env bash\necho hi\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("script not written: %v", err)
	}
	if string(b) != "#!/usr/bin/env bash\necho hi\n" {
		t.Fatalf("script contents = %q", b)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanup must remove the temp script; stat err=%v", err)
	}
}

// Run drives the embedded installer through the Runner as `bash <script>` with
// the pinning env — the testable invocation the update command asserts on.
func TestRunInvokesBashWithEnv(t *testing.T) {
	f := &runner.Fake{}
	if err := Run(f, "/tmp/install.sh", Env("4-0506")); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 {
		t.Fatalf("expected one call; got %+v", f.Calls)
	}
	c := f.Calls[0]
	if c.Name != "bash" || len(c.Args) != 1 || c.Args[0] != "/tmp/install.sh" {
		t.Fatalf("must run `bash <script>`; got %s %v", c.Name, c.Args)
	}
	if len(c.Env) != 1 || c.Env[0] != "COVE_VERSION=4-0506" {
		t.Fatalf("must pass the pinning env; got %v", c.Env)
	}
}

// ResolveLatest reuses install.sh's own resolve_version (sourced in lib mode)
// rather than reimplementing release resolution in Go.
func TestResolveLatestSourcesLibMode(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "7-0708\n"}}}
	got, err := ResolveLatest(f, "/tmp/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if got != "7-0708" {
		t.Fatalf("resolved tag = %q; want trimmed 7-0708", got)
	}
	if len(f.Calls) != 1 {
		t.Fatalf("expected one call; got %+v", f.Calls)
	}
	c := f.Calls[0]
	if c.Name != "bash" {
		t.Fatalf("resolve must shell out to bash; got %s", c.Name)
	}
	if !strings.Contains(strings.Join(c.Args, " "), "resolve_version") {
		t.Fatalf("resolve must call resolve_version; args=%v", c.Args)
	}
	if !containsSlice(c.Env, "COVE_INSTALL_LIB=1") {
		t.Fatalf("resolve must source install.sh in lib mode; env=%v", c.Env)
	}
}

func containsSlice(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
