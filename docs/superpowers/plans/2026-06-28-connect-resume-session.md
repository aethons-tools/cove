# Connect Resume-Session Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `at-cove connect` returns the user to their last Claude Code session by default,
with a `--fresh` flag to start a new one.

**Architecture:** The only behavioral change is the remote launch command the connect transport exec's.
The `claude` launch becomes resume-aware:
when resuming and a prior session transcript exists, it runs `claude --continue`, else a fresh `claude`.
`main` decides resume vs fresh from `--fresh`/`--raw` and sets a `Resume` field on the transport.
`connect.Connect`, `recreate`, and the backend are untouched.

**Tech Stack:** Go 1.22, standard library only.
No new dependencies.
Shell snippet runs inside the sandbox VM over ssh.

## Global Constraints

- Go version floor `go 1.22`; standard library only — no new dependencies.
- The feature lives entirely in `connect`'s launch command: no exit hook, no session-id capture, no state file, no `recreate` changes, no backend/volume changes.
- Resume is the default; `--fresh` opts out. `--raw` (debug bash) never resumes.
- Resume detection must test for **any** session under `"${CLAUDE_CONFIG_DIR:-/agent-data}"/projects/*/*.jsonl` — do NOT replicate Claude Code's per-directory folder-name hash.
- Detection is best-effort: missing/empty/unreadable projects dir ⇒ fall through to a fresh `exec claude`; never block or fail the connect.
- `claude --continue` is the resume mechanism (no `--resume <id>`).
- Follow existing conventions: transports build remote command strings via `remoteExec`; collaborators passed as params; tests assert the generated remote string.

---

### Task 1: Resume-aware launch in the transport

**Files:**
- Modify: `internal/connect/transport.go` (replace `launchCmd`/`remoteExec`; add `Resume` to both transport structs; pass it through both `Launch` methods)
- Test: `internal/connect/transport_test.go` (add four tests)

**Interfaces:**
- Consumes: nothing new.
- Produces (relied on by Task 2):
  - `StdinScript` and `SendEnv` each gain a field `Resume bool` (zero value `false` = fresh).
  - `func remoteExec(cmd string, resume bool) string`
  - `func launchProgram(cmd string, resume bool) string`

- [ ] **Step 1: Write the failing tests**

Append to `internal/connect/transport_test.go`:

```go
func TestStdinScriptResumesWhenEnabled(t *testing.T) {
	f := &runner.Fake{}
	if err := (StdinScript{R: f, Resume: true}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if !strings.Contains(remote, "exec claude --continue") {
		t.Fatalf("resume should pass --continue: %q", remote)
	}
	if !strings.Contains(remote, "else exec claude; fi") {
		t.Fatalf("resume must fall back to a fresh claude when no session exists: %q", remote)
	}
	if !strings.Contains(remote, "/projects/*/*.jsonl") {
		t.Fatalf("resume must guard on an existing session transcript: %q", remote)
	}
}

func TestStdinScriptFreshDoesNotResume(t *testing.T) {
	f := &runner.Fake{}
	if err := (StdinScript{R: f}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if strings.Contains(remote, "--continue") {
		t.Fatalf("fresh launch must not pass --continue: %q", remote)
	}
	if !strings.HasSuffix(remote, "cd /home/agent/workspace && exec claude") {
		t.Fatalf("fresh launch should exec plain claude: %q", remote)
	}
}

func TestSendEnvResumesWhenEnabled(t *testing.T) {
	f := &runner.Fake{}
	if err := (SendEnv{R: f, Resume: true}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	remote := f.Calls[0].Args[len(f.Calls[0].Args)-1]
	if !strings.Contains(remote, "exec claude --continue") || !strings.Contains(remote, "else exec claude; fi") {
		t.Fatalf("SendEnv resume should pass --continue with fallback: %q", remote)
	}
}

func TestRawNeverResumes(t *testing.T) {
	f := &runner.Fake{}
	if err := (StdinScript{R: f, Cmd: "bash", Resume: true}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if strings.Contains(remote, "--continue") {
		t.Fatalf("raw bash must never resume: %q", remote)
	}
	if !strings.HasSuffix(remote, "cd /home/agent/workspace && exec bash") {
		t.Fatalf("raw should exec bash: %q", remote)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/connect/ -run 'Resume|FreshDoesNotResume|RawNeverResumes'`
Expected: FAIL — build error, `unknown field 'Resume' in struct literal` (the field does not exist yet). A build failure is the expected failing state.

- [ ] **Step 3: Replace `launchCmd`/`remoteExec` with the resume-aware builder**

In `internal/connect/transport.go`, replace this block:

```go
// launchCmd is the remote program a transport exec's. Empty means the agent
// (claude); `connect --raw` sets it to "bash" to drop into a debug shell with
// the same injected environment the agent would see.
func launchCmd(cmd string) string {
	if cmd == "" {
		return "claude"
	}
	return cmd
}

// remoteExec is the tail of a transport's remote command: cd into the workspace,
// then exec the launch program. Using && (not ;) fails loudly if the workspace
// mount is missing rather than silently dropping the session in the home dir.
func remoteExec(cmd string) string {
	return "cd " + workspaceDir + " && exec " + launchCmd(cmd)
}
```

with:

```go
// resumeProbe succeeds when the persistent config dir holds any prior Claude
// Code session transcript. CLAUDE_CONFIG_DIR is baked into the sandbox image
// (=/agent-data); the :- fallback keeps detection working if it is absent from
// this shell. We test for any session under projects/*/*.jsonl rather than
// replicating Claude Code's internal per-directory folder hash: the sandbox's
// only project dir is the workspace (sessions always start there), so "any
// session exists" is equivalent and robust to that hash changing.
const resumeProbe = `ls "${CLAUDE_CONFIG_DIR:-/agent-data}"/projects/*/*.jsonl >/dev/null 2>&1`

// launchProgram returns the remote shell tail that exec's the session program.
// Empty cmd means the agent (claude); a non-empty cmd (e.g. "bash" from --raw)
// is a whole-program replacement that never resumes. When resume is set, claude
// reopens the most-recent session for the workspace dir if one exists, else
// starts fresh — making a first-ever connect deterministic.
func launchProgram(cmd string, resume bool) string {
	if cmd != "" {
		return "exec " + cmd
	}
	if !resume {
		return "exec claude"
	}
	return "if " + resumeProbe + "; then exec claude --continue; else exec claude; fi"
}

// remoteExec is the tail of a transport's remote command: cd into the workspace,
// then exec the launch program. Using && (not ;) fails loudly if the workspace
// mount is missing rather than silently dropping the session in the home dir.
func remoteExec(cmd string, resume bool) string {
	return "cd " + workspaceDir + " && " + launchProgram(cmd, resume)
}
```

- [ ] **Step 4: Add the `Resume` field to `SendEnv` and pass it through**

In `internal/connect/transport.go`, change the `SendEnv` struct:

```go
type SendEnv struct {
	R      runner.Runner
	Cmd    string // remote program to exec; "" => claude
	Resume bool   // when launching claude, resume the most-recent session if one exists
}
```

and in `SendEnv.Launch`, change the `remoteExec` call:

```go
	args := sshargs.InteractiveSendEnv(t, names, remoteExec(s.Cmd, s.Resume))
```

- [ ] **Step 5: Add the `Resume` field to `StdinScript` and pass it through**

In `internal/connect/transport.go`, change the `StdinScript` struct:

```go
type StdinScript struct {
	R      runner.Runner
	Cmd    string // remote program to exec; "" => claude
	Resume bool   // when launching claude, resume the most-recent session if one exists
}
```

and in `StdinScript.Launch`, change the line that builds `remote`:

```go
	remote := "set -a; . " + file + "; rm -f " + file + "; " + remoteExec(s.Cmd, s.Resume)
```

- [ ] **Step 6: Run the connect package tests to verify they pass**

Run: `go test ./internal/connect/`
Expected: PASS — the four new tests plus all existing transport tests (the existing tests use `Resume` unset = fresh, so their `exec claude` / `exec bash` assertions still hold).

- [ ] **Step 7: Build, vet, and commit**

Run: `go build ./... && go vet ./internal/connect/`
Expected: success (main.go still compiles — it constructs `StdinScript{R: r, Cmd: cmd}` with `Resume` defaulting to false), no vet output.

```bash
git add internal/connect/transport.go internal/connect/transport_test.go
git commit -m "feat(connect): resume-aware launch command in the transport"
```

---

### Task 2: Wire `--fresh` and default-resume into `main`

**Files:**
- Modify: `main.go` (parse `--fresh`; `doConnect` signature + body; dry-run message; help text)
- Test: `main_test.go` (add two dry-run tests)

**Interfaces:**
- Consumes: `StdinScript.Resume bool` from Task 1.
- Produces: `at-cove connect [--fresh]`; `doConnect(kitDir string, r runner.Runner, dryRun, raw, noAuth, fresh bool, stdout, stderr io.Writer) error`.

- [ ] **Step 1: Write the failing tests**

Append to `main_test.go`:

```go
func TestDryRunConnectResumesByDefault(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN", Command: []string{"op", "x"}})
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "connect", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "resuming") {
		t.Fatalf("default connect should resume; msg=%q", out.String())
	}
}

func TestDryRunConnectFresh(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN", Command: []string{"op", "x"}})
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "--fresh", "connect", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "fresh") || strings.Contains(s, "resuming") {
		t.Fatalf("--fresh connect should be fresh; msg=%q", s)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test . -run 'TestDryRunConnectResumesByDefault|TestDryRunConnectFresh'`
Expected: FAIL — the dry-run message does not yet contain `resuming`/`fresh`, and `--fresh` is parsed as a kit-dir arg (so `TestDryRunConnectFresh` likely errors). Failing output confirms the wiring is absent.

- [ ] **Step 3: Parse the `--fresh` flag**

In `main.go`, in `run(...)`, add the flag variable next to the others (after `noAuth := false`):

```go
	fresh := false
```

and add a case in the argument-parsing `switch` (after the `--no-auth` case):

```go
		case a == "--fresh":
			fresh = true
```

- [ ] **Step 4: Thread `fresh` into the `doConnect` call**

In `main.go`, change the `connect` dispatch line:

```go
	case "connect":
		err = doConnect(kitDir, r, dryRun, raw, noAuth, fresh, stdout, stderr)
```

- [ ] **Step 5: Update `doConnect` signature and add the resume decision**

In `main.go`, change the `doConnect` signature:

```go
func doConnect(kitDir string, r runner.Runner, dryRun, raw, noAuth, fresh bool, stdout, stderr io.Writer) error {
```

Immediately after the existing `launch` block (the lines that set `launch := "claude"` / `if raw { launch = "bash" }`), add:

```go
	resume := !raw && !fresh
```

Replace the existing dry-run block:

```go
	if dryRun {
		auth := "with auth"
		if noAuth {
			auth = "no auth"
		}
		fmt.Fprintf(stdout, "would resolve %d secrets and connect to %s, launching %s (%s)\n",
			len(specs), st.Container, launch, auth)
		return nil
	}
```

with:

```go
	if dryRun {
		auth := "with auth"
		if noAuth {
			auth = "no auth"
		}
		session := "resuming"
		if !resume {
			session = "fresh"
		}
		fmt.Fprintf(stdout, "would resolve %d secrets and connect to %s, launching %s (%s, %s)\n",
			len(specs), st.Container, launch, auth, session)
		return nil
	}
```

- [ ] **Step 6: Set `Resume` on the transport**

In `main.go`, change the `connect.Connect(...)` call's transport argument to set `Resume`:

```go
	return connect.Connect(b, r, connect.StdinScript{R: r, Cmd: cmd, Resume: resume}, awake.New(), connect.Options{
		Container:     st.Container,
		Secrets:       specs,
		IdentityFile:  priv,
		KnownHostsDir: filepath.Join(configDir(), "known_hosts.d"),
		SkipAuth:      noAuth,
		Stderr:        stderr,
	})
```

- [ ] **Step 7: Update the help text**

In `main.go`, in the `usage` const, change the connect usage line:

```
  at-cove connect  [kit-dir] [--raw] [--no-auth] [--fresh]
```

and add to the `connect flags:` block (after the `--no-auth` line):

```
  --fresh     start a new session instead of resuming the last one
```

- [ ] **Step 8: Run the full suite to verify everything passes**

Run: `go test ./...`
Expected: PASS across all packages, including the two new tests and the existing `TestDryRunConnectRawNoAuth` (its message is now `launching bash (no auth, fresh)`, which still contains `bash` and `no auth`).

- [ ] **Step 9: Build, vet, gofmt, and commit**

Run: `go build ./... && go vet ./... && gofmt -l main.go`
Expected: success, no vet output, `gofmt -l` prints nothing.

```bash
git add main.go main_test.go
git commit -m "feat(connect): resume last session by default, add --fresh"
```

---

## Self-Review

**Spec coverage:**
- `claude --continue` resume mechanism → Task 1, `launchProgram`.
- `if … fi` guard for deterministic first-run → Task 1, `launchProgram` + `TestStdinScriptResumesWhenEnabled` (asserts `else exec claude; fi`).
- "any session under `projects/*/*.jsonl`", not the per-dir hash → Task 1, `resumeProbe` + test asserts `/projects/*/*.jsonl`.
- `${CLAUDE_CONFIG_DIR:-/agent-data}` fallback → Task 1, `resumeProbe`.
- Resume default-on, `--fresh` opt-out, `--raw` never resumes → Task 2 (`resume := !raw && !fresh`) + Task 1 `TestRawNeverResumes`.
- Best-effort (never blocks) → `ls … >/dev/null 2>&1` then `else exec claude`; no Go error path added.
- `connect.Connect`, `recreate`, backend unchanged → not touched by any task.
- Help text + dry-run observability → Task 2, Steps 5 & 7.
- Tests (transport string cases; main flag mapping) → Tasks 1 & 2.

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `Resume bool` field name and the `remoteExec(cmd, resume)` / `launchProgram(cmd, resume)` signatures are identical across Task 1 (definition) and Task 2 (use). `doConnect(..., fresh bool, ...)` matches between the call site (Task 2 Step 4) and the definition (Step 5).
