package dispatchrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	"github.com/aethons-tools/cove/internal/dispatch/worker"
	"github.com/aethons-tools/cove/internal/logging"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// mergeVMRecords grafts the VM's captured `at-task` stderr into the host stream
// (spec §7). Each line is parsed as one structured slog JSONL record and
// re-emitted through the host logger lg — which already carries the run/issue/
// class context — with the layer `step` grafted on, so a VM record lands in the
// one unified trace with full attribution. A line that does NOT parse as a slog
// record is not dropped: it is logged at debug as raw, scrubbed of any known
// secret value (the §6.4 redaction backstop), still tagged with `step`.
//
// Structured records are secret-free by construction (§6.2) — only self-owned
// fields — so they pass through verbatim; `secrets` masks any raw text that
// falls to the debug path. `at-task` still emits unstructured text until #5, so
// today everything falls to that path; #5 makes the records parse.
func mergeVMRecords(ctx context.Context, lg *logging.Logger, step string, captured []byte, secrets []string) {
	sc := bufio.NewScanner(bytes.NewReader(captured))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // tolerate long JSONL lines
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if rec, ok := parseRecord(line); ok {
			attrs := append(rec.attrs, slog.String("step", step))
			lg.LogAttrs(ctx, rec.level, rec.msg, attrs...)
			continue
		}
		lg.LogAttrs(ctx, slog.LevelDebug, "vm output (unparsed)",
			slog.String("step", step),
			slog.String("raw", logging.Scrub(line, secrets...)))
	}
}

// vmRecord is a decoded slog JSONL record from the VM: its level, message, and
// the remaining attributes (everything but the reserved time/level/msg keys).
type vmRecord struct {
	level slog.Level
	msg   string
	attrs []slog.Attr
}

// parseRecord decodes one line as a slog JSONHandler record. A line qualifies
// only if it is a JSON object carrying a string `msg` — the reliable marker of
// a slog record, as opposed to arbitrary agent JSON. The VM's own `time` is
// preserved as a `vm_time` attr (the host re-stamps `time` itself); level/msg
// are lifted out and every other key is grafted on as an attribute.
func parseRecord(line string) (vmRecord, bool) {
	if !strings.HasPrefix(line, "{") {
		return vmRecord{}, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return vmRecord{}, false
	}
	msgRaw, ok := m["msg"]
	if !ok {
		return vmRecord{}, false
	}
	var msg string
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		return vmRecord{}, false
	}
	rec := vmRecord{level: slog.LevelInfo, msg: msg}
	if lvlRaw, ok := m["level"]; ok {
		var lvl string
		if json.Unmarshal(lvlRaw, &lvl) == nil {
			rec.level = parseLevel(lvl)
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic attr order
	for _, k := range keys {
		switch k {
		case "level", "msg":
			continue
		case "time":
			var t string
			if json.Unmarshal(m[k], &t) == nil && t != "" {
				rec.attrs = append(rec.attrs, slog.String("vm_time", t))
			}
			continue
		}
		var v any
		if json.Unmarshal(m[k], &v) != nil {
			continue
		}
		rec.attrs = append(rec.attrs, slog.Any(k, v))
	}
	return rec, true
}

// parseLevel maps an slog JSON level string (uppercase, per slog's TextHandler/
// JSONHandler) to a slog.Level, defaulting to Info.
func parseLevel(s string) slog.Level {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// authFailedMarkers are substrings in the agent's raw output that mark an
// authentication failure — the motivating 401 (§3, §8). Matched
// case-insensitively against the captured (never shipped) agent output.
var authFailedMarkers = []string{
	" 401 ", "http 401", "status 401", "error 401",
	"invalid authentication", "authentication_error",
	"invalid x-api-key", "invalid bearer token",
}

// emitAgentOutcome emits the structured agent-outcome record (§6.3): it
// classifies the run's result — ok / needs-input / error — from the extracted
// task-result, and on a non-ok outcome runs the detectable classification
// hooks: `auth_failed` (a 401, scanned from the captured agent output, never
// shipped raw) and the egress-wall denial (the hardening layer's squid log, the
// same signal egress.go uses; where COV-21 plugs in). The record carries only
// self-owned, secret-free fields; the agent output is scanned in memory and
// never emitted.
func emitAgentOutcome(ctx context.Context, lg *logging.Logger, r runner.Runner, tgt sshargs.Target, resultJSON, agentOutput string) {
	outcome := "unknown"
	var tr worker.TaskResult
	if json.Unmarshal([]byte(resultJSON), &tr) == nil {
		if v, err := tr.Status.ActiveTask(); err == nil {
			outcome = v
		}
	}
	level := slog.LevelInfo
	switch outcome {
	case "error", "unknown":
		level = slog.LevelError
	case "needs-input":
		level = slog.LevelWarn
	}
	attrs := []slog.Attr{slog.String("step", "agent"), slog.String("outcome", outcome)}
	if outcome != "ok" {
		if scanAuthFailed(agentOutput) {
			attrs = append(attrs, slog.Bool("auth_failed", true))
			level = slog.LevelError
		}
		if hosts := detectDeniedHosts(r, tgt); len(hosts) > 0 {
			attrs = append(attrs, slog.String("egress_wall", strings.Join(hosts, ",")))
			if level < slog.LevelWarn {
				level = slog.LevelWarn
			}
		}
	}
	lg.LogAttrs(ctx, level, "agent outcome", attrs...)
}

// scanAuthFailed reports whether the captured agent output shows an
// authentication failure (a 401). Case-insensitive substring match.
func scanAuthFailed(agentOutput string) bool {
	low := strings.ToLower(agentOutput)
	for _, m := range authFailedMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// secretValues returns the distinct non-empty values across the given env maps —
// the resolved secret values to mask in any raw text that reaches the debug
// path (the §6.4 backstop). Names are irrelevant; only values are masked.
func secretValues(envs ...map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range envs {
		for _, v := range e {
			if v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}
