package colima

import (
	"errors"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

// TestHasSysboxRuntime pins the shared probe at-cove's preflight and
// `at-jam colima check-docker` both use: present, absent, unreachable, garbage.
func TestHasSysboxRuntime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		res     runner.FakeResult
		want    bool
		wantErr string
	}{
		{"present", runner.FakeResult{Stdout: sysboxRuntimesOutput}, true, ""},
		{"absent", runner.FakeResult{Stdout: `{"runc":{"path":"runc"}}`}, false, ""},
		{"unreachable", runner.FakeResult{Err: errors.New("exit 1")}, false, "cannot query docker runtimes"},
		{"garbage", runner.FakeResult{Stdout: "not json"}, false, "cannot parse docker runtimes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &runner.Fake{Outputs: []runner.FakeResult{tc.res}}
			got, err := HasSysboxRuntime(f)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("HasSysboxRuntime = %v, %v; want %v", got, err, tc.want)
			}
			if c := f.Calls[0]; c.Name != "docker" || strings.Join(c.Args, " ") != "--context colima info -f {{json .Runtimes}}" {
				t.Fatalf("probe call = %+v", c)
			}
		})
	}
}

// TestRequireSysboxRuntimeNamesSetupCommand: the preflight error points the
// operator at the one-command fix as well as the doc.
func TestRequireSysboxRuntimeNamesSetupCommand(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"runc":{"path":"runc"}}`}}}
	err := (&Colima{r: f}).requireSysboxRuntime()
	if err == nil || !strings.Contains(err.Error(), "at-jam colima setup-docker") || !strings.Contains(err.Error(), "docs/usage/docker-in-sandbox.md") {
		t.Fatalf("err = %v", err)
	}
}
