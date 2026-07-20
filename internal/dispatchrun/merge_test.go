package dispatchrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/logging"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
)

// bufLogger builds an unattended (JSON→buffer) host logger at debug level, seeded
// with the given run/issue/class correlation — the context dispatchrun grafts
// onto every merged VM record.
func bufLogger(t *testing.T, buf *bytes.Buffer, run, issue, class string) (*logging.Logger, context.Context) {
	t.Helper()
	lg, err := logging.New(logging.Options{Mode: logging.Unattended, Stderr: buf, Level: slog.LevelDebug})
	if err != nil {
		t.Fatalf("logging.New: %v", err)
	}
	lg = lg.With(slog.String("run", run), slog.String("issue", issue), slog.String("class", class))
	return lg, logging.Into(context.Background(), lg)
}

// records parses the buffer's JSONL into decoded records.
func records(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("emitted line is not JSON: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

// findRec returns the first record whose msg equals want, or fails.
func findRec(t *testing.T, recs []map[string]any, want string) map[string]any {
	t.Helper()
	for _, r := range recs {
		if r["msg"] == want {
			return r
		}
	}
	t.Fatalf("no record with msg %q among %v", want, recs)
	return nil
}

// TestMergeVMRecordsGraftsContext is the §10 merge fixture: a JSONL fixture fed
// through the merge lands in the host stream with the run/issue/class context
// grafted on and a per-record `step`, preserving each VM record's level/msg/attrs.
// An unparseable line is not dropped — it surfaces at debug as raw, tagged.
func TestMergeVMRecordsGraftsContext(t *testing.T) {
	var buf bytes.Buffer
	lg, ctx := bufLogger(t, &buf, "run_COV-1_abcd", "COV-1", "implement")

	fixture := strings.Join([]string{
		`{"time":"2026-07-15T00:00:00Z","level":"INFO","msg":"cloning repo","attempt":1}`,
		`{"time":"2026-07-15T00:00:01Z","level":"ERROR","msg":"git fetch failed","code":128}`,
		`this line is not JSON at all`,
		"", // blank line — skipped
	}, "\n")

	mergeVMRecords(ctx, lg, "prepare", []byte(fixture), nil)

	recs := records(t, &buf)
	if len(recs) != 3 {
		t.Fatalf("want 3 merged records (2 structured + 1 raw); got %d: %v", len(recs), recs)
	}

	info := findRec(t, recs, "cloning repo")
	for k, want := range map[string]any{
		"run": "run_COV-1_abcd", "issue": "COV-1", "class": "implement", "step": "prepare", "level": "INFO",
	} {
		if info[k] != want {
			t.Fatalf("merged record %q = %v; want %v (full: %v)", k, info[k], want, info)
		}
	}
	if info["attempt"] != float64(1) {
		t.Fatalf("VM attr not preserved: attempt = %v; want 1", info["attempt"])
	}
	if info["vm_time"] != "2026-07-15T00:00:00Z" {
		t.Fatalf("VM time not preserved as vm_time: %v", info["vm_time"])
	}

	errRec := findRec(t, recs, "git fetch failed")
	if errRec["level"] != "ERROR" || errRec["step"] != "prepare" || errRec["code"] != float64(128) {
		t.Fatalf("error record not faithfully re-emitted: %v", errRec)
	}

	raw := findRec(t, recs, "vm output (unparsed)")
	if raw["level"] != "DEBUG" || raw["step"] != "prepare" {
		t.Fatalf("unparseable line must be preserved at debug, tagged: %v", raw)
	}
	if raw["raw"] != "this line is not JSON at all" {
		t.Fatalf("raw line not preserved verbatim: %v", raw["raw"])
	}
}

// TestMergeVMRecordsScrubsRawLines asserts the redaction backstop (§6.4): a known
// secret value appearing in an UNPARSEABLE line is masked before it is logged.
func TestMergeVMRecordsScrubsRawLines(t *testing.T) {
	var buf bytes.Buffer
	lg, ctx := bufLogger(t, &buf, "run_x", "COV-2", "implement")
	const token = "ghp-super-secret-token-xyz"

	mergeVMRecords(ctx, lg, "complete", []byte("boom: leaked "+token+" here"), []string{token})

	if strings.Contains(buf.String(), token) {
		t.Fatalf("secret token leaked into a merged raw line:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "«redacted»") {
		t.Fatalf("expected the scrubbed line to carry the redaction marker:\n%s", buf.String())
	}
}

// captureRunner writes canned bytes to a bracket step's injected stderr so a
// hermetic test can exercise the host capture → merge / classify wiring without
// a live VM. It matches by a substring of the remote command in the ssh argv.
type captureRunner struct {
	*runner.Fake
	match string // remote-command substring identifying the step (e.g. "claude -p")
	emit  string // bytes to write to that step's injected stderr
}

func (c *captureRunner) RunIO(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	err := c.Fake.RunIO(stdin, stdout, stderr, name, args...) // record + honor Fake semantics
	if stderr != nil && c.match != "" && strings.Contains(strings.Join(args, " "), c.match) {
		_, _ = io.WriteString(stderr, c.emit)
	}
	return err
}

// TestDispatchMergesCapturedPrepareStderr wires the capture seam end to end: the
// prepare step's stderr (a structured record) is captured and merged into the
// host stream with the run/issue/class/step context grafted on.
func TestDispatchMergesCapturedPrepareStderr(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"issue":{"key":"COV-9"},"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	r := &captureRunner{
		Fake:  &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"status":{"ok":{}}}`}}},
		match: "at-task prepare",
		emit:  `{"time":"2026-07-15T00:00:00Z","level":"INFO","msg":"prepared branch","branch":"implement/COV-9"}` + "\n",
	}
	var buf bytes.Buffer
	_, ctx := bufLogger(t, &buf, "run_COV-9_beef", "COV-9", "implement")

	err := Dispatch(ctx, Options{
		Ops: &fakeOps{}, R: r,
		Cfg: kit.Config{
			Name:          "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
			Workers:       map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		Image: "at-cove-for-w", Name: "disp-merge",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	rec := findRec(t, records(t, &buf), "prepared branch")
	for k, want := range map[string]any{
		"run": "run_COV-9_beef", "issue": "COV-9", "class": "implement", "step": "prepare", "branch": "implement/COV-9",
	} {
		if rec[k] != want {
			t.Fatalf("merged prepare record %q = %v; want %v (full: %v)", k, rec[k], want, rec)
		}
	}
}

// TestDispatchEmitsAuthFailedOutcome asserts the agent-outcome record (§6.3):
// when the agent's captured output shows a 401 and the run ends in error, the
// structured record classifies it error + auth_failed at error level — turning a
// bare 401 into a self-attributing record. The agent's raw output is scanned in
// memory and never emitted.
func TestDispatchEmitsAuthFailedOutcome(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"issue":{"key":"COV-9"},"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	r := &captureRunner{
		Fake:  &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"status":{"error":{"message":"Agent did not respond"}}}`}}},
		match: "claude -p",
		emit:  "API Error: 401 Invalid authentication credentials\n",
	}
	var buf bytes.Buffer
	_, ctx := bufLogger(t, &buf, "run_COV-9_beef", "COV-9", "implement")

	err := Dispatch(ctx, Options{
		Ops: &fakeOps{}, R: r,
		Cfg: kit.Config{
			Name:          "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
			Workers:       map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		Image: "at-cove-for-w", Name: "disp-401",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	rec := findRec(t, records(t, &buf), "agent outcome")
	if rec["outcome"] != "error" || rec["auth_failed"] != true || rec["level"] != "ERROR" || rec["step"] != "agent" {
		t.Fatalf("auth-failed outcome not classified: %v", rec)
	}
	// The agent's raw output is scanned in memory, never shipped to the structured
	// sink (§6.3) — only the self-constructed outcome record is emitted.
	if strings.Contains(buf.String(), "Invalid authentication credentials") {
		t.Fatalf("raw agent output leaked into the structured stream:\n%s", buf.String())
	}
}

// TestDispatchOkOutcome asserts the happy path emits an info-level ok outcome and
// runs no classification hooks.
func TestDispatchOkOutcome(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"issue":{"key":"COV-9"},"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"status":{"ok":{"message":"opened PR"}}}`}}}
	var buf bytes.Buffer
	_, ctx := bufLogger(t, &buf, "run_ok", "COV-9", "implement")

	err := Dispatch(ctx, Options{
		Ops: &fakeOps{}, R: r,
		Cfg: kit.Config{
			Name:          "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
			Workers:       map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		Image: "at-cove-for-w", Name: "disp-ok",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	rec := findRec(t, records(t, &buf), "agent outcome")
	if rec["outcome"] != "ok" || rec["level"] != "INFO" {
		t.Fatalf("ok outcome not classified: %v", rec)
	}
	if _, hasAuth := rec["auth_failed"]; hasAuth {
		t.Fatalf("ok outcome must not run the auth_failed hook: %v", rec)
	}
}

// TestDispatchSecretNeverAppearsInLog is the §10 secret-safety test: a known
// resolved token fed through the resolution + dispatch path must NEVER appear in
// any emitted record. It is a root secret (so it reaches every step's env) and
// the captured prepare output echoes it in an unparseable line — the §6.4
// redaction backstop must mask it. Injection paths themselves are never logged.
func TestDispatchSecretNeverAppearsInLog(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"issue":{"key":"COV-9"},"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	const token = "sk-ant-SUPER-SECRET-do-not-log-me"
	// The prepare step's captured output echoes the token in an unparseable line
	// (as a leaky tool might); the merge's scrubber must mask it before logging.
	r := &captureRunner{
		Fake:  &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"status":{"ok":{}}}`}}},
		match: "at-task prepare",
		emit:  "connecting with bearer " + token + " ...\n",
	}
	var buf bytes.Buffer
	_, ctx := bufLogger(t, &buf, "run_sec", "COV-9", "implement")

	err := Dispatch(ctx, Options{
		Ops: &fakeOps{}, R: r,
		Cfg: kit.Config{
			Name:          "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
			Workers:       map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		// A resolved root secret — reaches prepare's env, so the merge knows to mask it.
		Secrets: []secret.Spec{{Name: "SOME_TOKEN", Value: token, Literal: true}},
		Image:   "at-cove-for-w", Name: "disp-sec",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if strings.Contains(buf.String(), token) {
		t.Fatalf("SECRET LEAK: the resolved token appeared in an emitted record:\n%s", buf.String())
	}
	// Sanity: the scrubber did run (proving the token was present pre-scrub, not
	// merely absent because nothing was logged).
	if !strings.Contains(buf.String(), "«redacted»") {
		t.Fatalf("expected the captured line to be scrubbed:\n%s", buf.String())
	}
}
