package dispatchrun

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/aethons-tools/cove/internal/dispatch/worker"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// squidLogVMPath is the sealed hardening layer's egress-gate access log. squid
// records a TCP_DENIED entry for every host it blocks — the one reliable,
// hardening-owned signal that a run hit the allow-list wall (as opposed to
// brittle matching of arbitrary agent stdout).
const squidLogVMPath = "/var/log/squid/access.log"

// egressOr turns an otherwise-opaque run failure into a first-class NEEDS INPUT
// when the VM's squid access log shows the run was blocked by the egress
// allow-list. It reads that log over ssh (the hardening layer's own denial
// signal, never agent stdout); if any host was denied it writes a NEEDS INPUT
// task-result naming the host(s) and the fix to outputPath and returns nil, so
// the run exits cleanly with a routable result rather than an opaque error.
//
// When no denial is found it returns origErr unchanged — a non-egress failure is
// surfaced exactly as before. It only ever REPORTS a wall; it never opens one.
func egressOr(r runner.Runner, tgt sshargs.Target, outputPath string, origErr error) error {
	hosts := detectDeniedHosts(r, tgt)
	if len(hosts) == 0 {
		return origErr
	}
	data, err := json.MarshalIndent(egressNeedsInput(hosts), "", "  ")
	if err != nil {
		return origErr
	}
	if err := os.WriteFile(outputPath, data, 0o600); err != nil {
		return origErr
	}
	return nil
}

// detectDeniedHosts reads the VM's squid access log over ssh and returns the
// unique hosts squid blocked. A missing/unreadable log yields no hosts (we never
// mask the real failure on a read error).
func detectDeniedHosts(r runner.Runner, tgt sshargs.Target) []string {
	out, err := r.Output("ssh", append(sshargs.Base(tgt), "cat "+squidLogVMPath)...)
	if err != nil {
		return nil
	}
	return parseDeniedHosts(out)
}

// parseDeniedHosts scans squid access-log content for TCP_DENIED entries and
// returns the unique blocked hostnames, sorted. squid's native logformat is
// "ts elapsed client result/status bytes method target ..."; the target is the
// CONNECT "host:port" (HTTPS) or a request URL (plain HTTP).
func parseDeniedHosts(accessLog string) []string {
	seen := map[string]bool{}
	var hosts []string
	for _, line := range strings.Split(accessLog, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 || !strings.HasPrefix(fields[3], "TCP_DENIED") {
			continue
		}
		host := hostFromTarget(fields[6])
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

// hostFromTarget extracts the bare hostname from a squid log target — either a
// CONNECT "host:port" or a full URL like "http://host/path".
func hostFromTarget(target string) string {
	if i := strings.Index(target, "://"); i >= 0 {
		target = target[i+3:]
	}
	if i := strings.IndexByte(target, '/'); i >= 0 {
		target = target[:i]
	}
	if i := strings.LastIndexByte(target, ':'); i >= 0 {
		target = target[:i]
	}
	return target
}

// egressNeedsInput builds the first-class NEEDS INPUT result for a blocked run:
// it names the blocked host(s) and the exact remedy (add them to the kit's
// image.allowed-domains and recreate). It rides the existing task-result
// NEEDS INPUT contract — the scheduler routes it with no special-casing.
func egressNeedsInput(hosts []string) worker.TaskResult {
	noun, verb := "host", "it"
	if len(hosts) > 1 {
		noun, verb = "hosts", "them"
	}
	msg := fmt.Sprintf(
		"The sandbox's egress allow-list blocked this run from reaching %s %s, "+
			"so the run could not complete. To unblock %s, add the %s to the kit's "+
			"image.allowed-domains in .at-cove/config.yml (additive to the sealed base "+
			"allow-list; a leading dot allows subdomains) and recreate the sandbox with "+
			"`at-cove recreate`.",
		noun, strings.Join(hosts, ", "), verb, noun)
	return worker.TaskResult{Status: worker.TaskStatus{NeedsInput: &worker.TaskNeedsInput{Message: msg}}}
}
