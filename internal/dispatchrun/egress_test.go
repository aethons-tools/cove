package dispatchrun

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
)

func TestParseDeniedHosts(t *testing.T) {
	log := strings.Join([]string{
		"1626000000.100 0 172.17.0.1 TCP_TUNNEL/200 5 CONNECT github.com:443 - HIER_DIRECT/1.2.3.4 -",
		"1626000000.123 0 172.17.0.1 TCP_DENIED/403 3921 CONNECT api.example.com:443 - HIER_NONE/- text/html",
		"1626000000.200 0 172.17.0.1 TCP_DENIED/403 3921 CONNECT api.example.com:443 - HIER_NONE/- text/html", // dup
		"1626000000.300 0 172.17.0.1 TCP_DENIED/403 100 GET http://pkgs.internal.test/x - HIER_NONE/- text/html",
		"garbage line that should be ignored",
		"", // trailing blank
	}, "\n")

	got := parseDeniedHosts(log)
	want := []string{"api.example.com", "pkgs.internal.test"}
	if len(got) != len(want) {
		t.Fatalf("parseDeniedHosts = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseDeniedHosts = %v; want %v (sorted, deduped)", got, want)
		}
	}
}

func TestParseDeniedHostsNone(t *testing.T) {
	log := "1626000000.100 0 172.17.0.1 TCP_TUNNEL/200 5 CONNECT github.com:443 - HIER_DIRECT/1.2.3.4 -\n"
	if got := parseDeniedHosts(log); len(got) != 0 {
		t.Fatalf("parseDeniedHosts on a clean log = %v; want none", got)
	}
}

// TestDispatchEgressWallSurfacesNeedsInput is the DoD test: a run blocked by the
// squid allow-list (here a git step fails and the VM's squid log shows the denial)
// ends as a first-class NEEDS INPUT whose message names the blocked host and the
// image.allowed-domains remedy — not as an opaque error.
func TestDispatchEgressWallSurfacesNeedsInput(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"

	// prepare fails (its git fetch was blocked); the only Output call on this path
	// is detectDeniedHosts' `cat .../access.log`, which returns a TCP_DENIED record.
	pf := &prepareFailer{Fake: &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "1626000000.123 0 172.17.0.1 TCP_DENIED/403 3921 CONNECT api.example.com:443 - HIER_NONE/- text/html\n"},
	}}}

	err := Dispatch(context.Background(), Options{
		Ops: &fakeOps{}, R: pf,
		Cfg: kit.Config{
			Name:          "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
			Workers:       map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		Image: "at-cove-for-w", Name: "disp-egress",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("an egress wall must be surfaced as a NEEDS INPUT result, not an error: %v", err)
	}

	res := readFile(t, out)
	if !strings.Contains(res, `"needs-input"`) {
		t.Fatalf("result must be a needs-input status:\n%s", res)
	}
	if !strings.Contains(res, "api.example.com") {
		t.Fatalf("needs-input message must name the blocked host:\n%s", res)
	}
	if !strings.Contains(res, "image.allowed-domains") {
		t.Fatalf("needs-input message must state the image.allowed-domains remedy:\n%s", res)
	}
	// The agent and complete steps must not have run after the blocked prepare.
	calls := allCalls(pf.Fake)
	if strings.Contains(calls, "claude -p") || strings.Contains(calls, "at-task complete") {
		t.Fatalf("bracket must abort after a blocked prepare:\n%s", calls)
	}
}
