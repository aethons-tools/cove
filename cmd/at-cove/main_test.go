package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/install"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/logging"
	"github.com/aethons-tools/cove/internal/mint"
	"github.com/aethons-tools/cove/internal/naming"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/state"
	"github.com/aethons-tools/cove/internal/usersecret"
)

// ptr returns a pointer to s — usersecret.Source.Value is *string so an
// explicit empty literal is distinct from "unset".
func ptr(s string) *string { return &s }

func TestProjectDirFlagResolves(t *testing.T) {
	dir := t.TempDir()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cove, "config.yml"), []byte("name: k\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	got, code := resolveProjectDir(dir, &errb)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	if got != cove {
		t.Fatalf("resolveProjectDir(%q) = %q; want the .at-cove under it (%q)", dir, got, cove)
	}
}

// An explicit --project-dir with no .at-cove/ at that root errors clearly.
func TestProjectDirFlagErrorsWithoutKit(t *testing.T) {
	dir := t.TempDir() // no .at-cove/ here
	var errb bytes.Buffer
	if _, code := resolveProjectDir(dir, &errb); code != 1 {
		t.Fatalf("code=%d, want 1 when the project root has no .at-cove/", code)
	}
	if !strings.Contains(errb.String(), "no .at-cove/ at project root") || !strings.Contains(errb.String(), dir) {
		t.Fatalf("error should name the missing .at-cove/ and the project root; got %q", errb.String())
	}
}

// The shared project-dir resolver reads the flag only — it no longer consumes a
// positional. Rejecting a stray positional is the caller's job, via noPositionals.
func TestNoPositionalsRejectsStray(t *testing.T) {
	var errb bytes.Buffer
	if noPositionals([]string{"stray"}, "build", &errb) {
		t.Fatal("noPositionals should reject a stray positional")
	}
	if !strings.Contains(errb.String(), "--project-dir") {
		t.Fatalf("error should mention --project-dir; got %q", errb.String())
	}
}

func TestNoPositionalsAllowsNone(t *testing.T) {
	var errb bytes.Buffer
	if !noPositionals(nil, "build", &errb) {
		t.Fatalf("noPositionals should allow no positional; stderr=%q", errb.String())
	}
}

// install/uninstall/work/dispatch take the project root only from --project-dir; a
// stray positional is a usage error (exit 2), matching the interactive verbs.
func TestFlagOnlyCommandsRejectPositional(t *testing.T) {
	for _, cmd := range []string{"install", "uninstall", "work", "dispatch"} {
		t.Run(cmd, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run([]string{cmd, "somekit"}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
			if code != 2 {
				t.Fatalf("exit = %d; want 2 (stray positional), stderr=%q", code, errOut.String())
			}
			if !strings.Contains(errOut.String(), "--project-dir") {
				t.Fatalf("stderr = %q; want mention of --project-dir", errOut.String())
			}
		})
	}
}

func TestPlanRequired(t *testing.T) {
	// resolved from the store, keyed by kit name.
	store := usersecret.Store{Kits: map[string]map[string]usersecret.Source{
		"k": {"T": {Value: ptr("v")}},
	}}
	sp, err := planRequired(store, nil, "k", "/kitpath", "T", "/p/secrets.yml")
	if err != nil || !sp.Literal || sp.Value != "v" {
		t.Fatalf("store value should supply a literal: %+v err=%v", sp, err)
	}
	// unresolved → error naming the secret + path.
	if _, err := planRequired(usersecret.Store{}, nil, "k", "/kitpath", "T", "/p/secrets.yml"); err == nil ||
		!strings.Contains(err.Error(), "T") || !strings.Contains(err.Error(), "/p/secrets.yml") {
		t.Fatalf("unresolved must error naming the secret and path; got %v", err)
	}
}

func TestPlanRequiredExpandsMint(t *testing.T) {
	appKey := "/etc/cove/gh.pem"
	store := usersecret.Store{
		Minters: map[string]usersecret.Minter{
			"gh": {GitHub: &usersecret.GitHubMinter{AppID: "1", InstallID: "2", AppKey: usersecret.Source{Value: &appKey}}},
		},
		Kits: map[string]map[string]usersecret.Source{
			"k": {"AT_TASK_GIT_TOKEN": {Mint: "gh"}},
		},
	}
	expand := mint.Expander(&runner.Fake{}, store.Global, "o/r")
	spec, err := planRequired(store, expand, "k", "/p", "AT_TASK_GIT_TOKEN", "/cfg/secrets.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Command) == 0 || spec.Command[0] != "at-mint" || spec.Command[1] != "github" {
		t.Fatalf("expected an at-mint github spec, got %v", spec.Command)
	}
}

// gitTokenStore supplies AT_TASK_GIT_TOKEN as a literal for the kit named box.
func gitTokenStore() usersecret.Store {
	return usersecret.Store{Kits: map[string]map[string]usersecret.Source{
		"box": {"AT_TASK_GIT_TOKEN": {Value: ptr("git-tok")}},
	}}
}

func sourceControlKit() kit.Config {
	return kit.Config{
		Name: "box",
		SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{
			Project:    "your-org/your-repo",
			MainBranch: "main",
			Secrets:    map[string]kit.SecretConfig{"AT_TASK_GIT_TOKEN": {}},
		}},
	}
}

// An isolated workspace with source-control + a supplied git token yields a
// clone plan with the derived URL/branch and the resolved token.
func TestWorkspaceClonePlanIsolated(t *testing.T) {
	st := state.State{Name: "box", WorkspaceMode: "isolated"}
	plan, err := workspaceClonePlan(sourceControlKit(), st, gitTokenStore(), nil, "/kitpath", "/secrets.yml")
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil {
		t.Fatal("expected a clone plan for an isolated workspace with source-control")
	}
	if plan.RepoURL != "https://github.com/your-org/your-repo.git" {
		t.Fatalf("RepoURL = %q", plan.RepoURL)
	}
	if plan.Branch != "main" {
		t.Fatalf("Branch = %q; want main", plan.Branch)
	}
	if !plan.Token.Literal || plan.Token.Value != "git-tok" {
		t.Fatalf("Token = %+v; want the resolved literal git-tok", plan.Token)
	}
}

// A shared workspace (share-repo-dir) is never cloned — the host checkout is shared live.
func TestWorkspaceClonePlanSharedSkips(t *testing.T) {
	st := state.State{Name: "box", WorkspaceMode: "shared", WorkspaceHostPath: "/host/repo"}
	plan, err := workspaceClonePlan(sourceControlKit(), st, gitTokenStore(), nil, "/kitpath", "/secrets.yml")
	if err != nil {
		t.Fatal(err)
	}
	if plan != nil {
		t.Fatalf("shared workspace must not clone; got %+v", plan)
	}
}

// No source-control (or no AT_TASK_GIT_TOKEN declared) → skip cloning, start empty.
func TestWorkspaceClonePlanUnconfiguredSkips(t *testing.T) {
	st := state.State{Name: "box", WorkspaceMode: "isolated"}
	plan, err := workspaceClonePlan(kit.Config{Name: "box"}, st, gitTokenStore(), nil, "/kitpath", "/secrets.yml")
	if err != nil {
		t.Fatal(err)
	}
	if plan != nil {
		t.Fatalf("unconfigured source-control must not clone; got %+v", plan)
	}
}

// Configured source-control whose git token has no supply is a hard error
// (fail closed), never a silent empty workspace.
func TestWorkspaceClonePlanUnsuppliedTokenErrors(t *testing.T) {
	st := state.State{Name: "box", WorkspaceMode: "isolated"}
	_, err := workspaceClonePlan(sourceControlKit(), st, usersecret.Store{}, nil, "/kitpath", "/secrets.yml")
	if err == nil {
		t.Fatal("a declared-but-unsupplied git token must be a hard error")
	}
	if !strings.Contains(err.Error(), "AT_TASK_GIT_TOKEN") {
		t.Fatalf("error should name the git token; got %v", err)
	}
}

func TestCanonicalKitPath(t *testing.T) {
	dir := t.TempDir()
	got := canonicalKitPath(dir)
	if !filepath.IsAbs(got) {
		t.Fatalf("canonicalKitPath(%q) = %q, want absolute", dir, got)
	}
}

func TestDispatchTrackerTokenFromSecretsYML(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	if err := os.MkdirAll(filepath.Join(cfgHome, "at-cove"), 0o755); err != nil {
		t.Fatal(err)
	}
	// secrets.yml supplies the tracker token as a literal, keyed by kit name.
	if err := os.WriteFile(filepath.Join(cfgHome, "at-cove", "secrets.yml"),
		[]byte("kits:\n  dispatch-kit:\n    AT_DISPATCH_TRACKER_TOKEN: { value: \"supplied-tok\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A valid dispatch kit (kits declare demand only — no resolver command).
	dir := writeDispatchKit(t, dispatchGoodConfig)
	writeInstall(t, filepath.Join(dir, ".at-cove")) // dispatch reads run-config from a current install
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	// It must get PAST token resolution (past "kit OK"); it then fails connecting to
	// Linear (no network), which is fine — the point is the token resolved from secrets.yml.
	if !strings.Contains(out.String(), "kit OK") {
		t.Fatalf("expected to reach token resolution + connect; stdout=%q stderr=%q", out.String(), errOut.String())
	}
	if strings.Contains(errOut.String(), "AT_DISPATCH_TRACKER_TOKEN has no supply entry") {
		t.Fatalf("token should have resolved from secrets.yml; stderr=%q", errOut.String())
	}
	_ = code
}

func writeKit(t *testing.T, dir string) string {
	t.Helper()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\n"
	if err := os.WriteFile(filepath.Join(cove, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	return cove
}

// writeInstall compiles the kit at kitDir into a current install.json — as
// `at-cove install` would, but without docker — so the run commands
// (create/recreate/chat and work/dispatch) find a manifest that passes the strict
// currency check (COV-38). It computes the real currency hash from the live kit
// source + at-cove's embedded identity (no docker, no keys), so the manifest stays
// current until the kit source changes (a later edit makes loadCurrentInstall
// report it stale). Returns the written manifest.
func writeInstall(t *testing.T, kitDir string) install.Manifest {
	t.Helper()
	cfg, err := kit.Load(kitDir)
	if err != nil {
		t.Fatalf("writeInstall: load kit: %v", err)
	}
	in, err := currencyInputs(kitDir, cfg)
	if err != nil {
		t.Fatalf("writeInstall: currency inputs: %v", err)
	}
	m := install.Compile(cfg, install.ResolvedBuild{
		Image:        naming.Image(cfg.Name),
		BaseRef:      in.BaseRef,
		BaseDigest:   "sha256:test",
		CurrencyHash: install.CurrencyHash(in),
		InstalledAt:  "2026-07-18T00:00:00Z",
	})
	if err := install.Save(kitDir, m); err != nil {
		t.Fatalf("writeInstall: save: %v", err)
	}
	return m
}

// writeState records a created instance so the state-driven commands have
// something to operate on.
func writeState(t *testing.T, kitDir, backendName, container string, secrets ...state.Secret) {
	t.Helper()
	if err := state.Save(kitDir, state.State{
		Name: container, Backend: backendName, Container: container,
		Image: naming.Image(container), WorkspaceMode: "isolated", Secrets: secrets,
	}); err != nil {
		t.Fatal(err)
	}
}

// writeStateFor records a created instance keyed by a collaborator class
// (state file <class>.json, container <kit>-<class>), so the instance-aware
// verbs (COV-71) find the per-class VM they now target.
func writeStateFor(t *testing.T, kitDir string, inst state.Instance, kitName, container string, secrets ...state.Secret) {
	t.Helper()
	if err := state.SaveFor(kitDir, inst, state.State{
		Name: kitName, Backend: "colima", Container: container,
		Image: naming.Image(kitName), WorkspaceMode: "isolated", Secrets: secrets,
	}); err != nil {
		t.Fatal(err)
	}
}

func dummyLookPath(string) (string, error) { return "/usr/bin/x", nil }

func dockerArg0Index(calls []runner.Call, arg0 string) int {
	for i, c := range calls {
		if c.Name != "docker" {
			continue
		}
		a := c.Args
		if len(a) >= 2 && a[0] == "--context" { // skip the pinned colima context
			a = a[2:]
		}
		if len(a) > 0 && a[0] == arg0 {
			return i
		}
	}
	return -1
}

// seedConfigDir points configDir() at a temp dir pre-loaded with a keypair, so
// keys.Ensure does not shell out to ssh-keygen during non-dry-run tests.
func seedConfigDir(t *testing.T) {
	t.Helper()
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	coveCfg := filepath.Join(cfgHome, "at-cove")
	if err := os.MkdirAll(coveCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coveCfg, "id_ed25519"), []byte("PRIV"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coveCfg, "id_ed25519.pub"), []byte("ssh-ed25519 AAAA test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusDispatchesToBackend(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box")
	// preflight `docker info` is a Probe (no Output consumed); `inspect` is the Output.
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}}}
	var out, errOut bytes.Buffer
	code := run([]string{"status", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "running") {
		t.Fatalf("status output = %q", out.String())
	}
}

// TestColimaDownPrintsActionableError guards that a stopped colima surfaces the
// "colima start" guidance to the user — not swallowed by main's ExitError path.
func TestColimaDownPrintsActionableError(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box")
	// The preflight `docker info` Probe fails (colima unreachable).
	f := &runner.Fake{Err: &runner.ExitError{Code: 1}}
	var out, errOut bytes.Buffer
	code := run([]string{"status", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatal("status must fail when colima is unreachable")
	}
	if !strings.Contains(errOut.String(), "colima start") {
		t.Fatalf("must print actionable colima guidance; stderr=%q", errOut.String())
	}
}

func TestStatusAbsentWhenNoState(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	var out, errOut bytes.Buffer
	code := run([]string{"status", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "absent") {
		t.Fatalf("status with no state: code=%d out=%q", code, out.String())
	}
}

func TestUnknownBackendErrors(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "bogus", "box") // state names an unknown backend
	var out, errOut bytes.Buffer
	code := run([]string{"status", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "bogus") {
		t.Fatalf("expected unknown-backend error, code=%d stderr=%q", code, errOut.String())
	}
}

// TestInstallBuildsGatesTagsAndWritesManifest: `at-cove install` is the single
// build+gate path — it assembles the context, builds + tags the image via the
// backend (no container run), and freezes the result into install.json with a
// non-empty currency hash and the resolved base recorded.
func TestInstallBuildsGatesTagsAndWritesManifest(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	seedConfigDir(t)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"install", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("install exit=%d stderr=%s", code, errOut.String())
	}
	if dockerArg0Index(f.Calls, "build") == -1 {
		t.Fatalf("install must docker build; calls=%+v", f.Calls)
	}
	if dockerArg0Index(f.Calls, "run") != -1 {
		t.Fatalf("install must not run a container; calls=%+v", f.Calls)
	}
	m, err := install.Load(kitDir)
	if err != nil {
		t.Fatalf("install.json not written: %v", err)
	}
	if m.Image != "atcove-box" || m.Name != "box" {
		t.Fatalf("manifest = %+v", m)
	}
	if m.CurrencyHash == "" || m.BaseRef == "" || m.BaseDigest == "" {
		t.Fatalf("manifest must record currency + base: %+v", m)
	}
}

// TestInstallRecordsBuiltImageDigest: install captures the built image's own
// sha256 (via a post-build `docker inspect` of the tag) and freezes it into
// install.json as the built-image digest — distinct from the FROM-base digest —
// so runs pin the exact image install gated (COV-78).
func TestInstallRecordsBuiltImageDigest(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	seedConfigDir(t)
	// The queued Output feeds the post-build `docker inspect --format {{.Id}}`.
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "sha256:cafef00d\n"}}}
	var out, errOut bytes.Buffer
	if code := run([]string{"install", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("install exit=%d stderr=%s", code, errOut.String())
	}
	m, err := install.Load(kitDir)
	if err != nil {
		t.Fatalf("install.json not written: %v", err)
	}
	if m.ImageDigest != "sha256:cafef00d" {
		t.Fatalf("manifest must record the built-image digest; got %q (base=%q)", m.ImageDigest, m.BaseDigest)
	}
	if m.ImageDigest == m.BaseDigest {
		t.Fatalf("built-image digest must be distinct from the FROM-base digest: %+v", m)
	}
}

// TestInstallOwnsAllowUnverifiedFlag: --allow-unverified-base is accepted only by
// `install` (the one command that builds a base + runs the gate).
func TestInstallOwnsAllowUnverifiedFlag(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	seedConfigDir(t)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"install", "--allow-unverified-base", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("install --allow-unverified-base should be accepted; code=%d stderr=%s", code, errOut.String())
	}
}

// TestAllowUnverifiedFlagRelocatedOffRunCommands: create/recreate/work reject the
// flag now that it lives only on install (a flag-parse error → exit 2).
func TestAllowUnverifiedFlagRelocatedOffRunCommands(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	for _, cmd := range []string{"create", "recreate", "work"} {
		var out, errOut bytes.Buffer
		code := run([]string{cmd, "--allow-unverified-base", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
		if code != 2 {
			t.Fatalf("%s must reject --allow-unverified-base (exit 2); code=%d stderr=%s", cmd, code, errOut.String())
		}
	}
}

// TestDryRunInstallIsSideEffectFree (COV-82): `--dry-run install` is a pure
// preview — it assembles nothing (no .build), touches no docker/keys, and writes
// no manifest. The assemble+inspect use now lives behind `--assemble-only`.
func TestDryRunInstallIsSideEffectFree(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	seedConfigDir(t)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"--dry-run", "install", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("dry-run install exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run install executed commands: %+v", f.Calls)
	}
	if install.Exists(kitDir) {
		t.Fatalf("dry-run install must not write install.json")
	}
	if _, err := os.Stat(filepath.Join(kitDir, ".build")); !os.IsNotExist(err) {
		t.Fatalf("dry-run install must NOT assemble .build (side effect); stat err=%v", err)
	}
	if !strings.Contains(out.String(), "would assemble") {
		t.Fatalf("dry-run install should describe the planned assemble+build: %q", out.String())
	}
}

// TestAssembleOnlyInstall (COV-82): `install --assemble-only` materializes the
// .build context for inspection, then stops — touching no docker and writing no
// manifest (the old `--dry-run` assemble+inspect behavior, now explicit).
func TestAssembleOnlyInstall(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	seedConfigDir(t)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"install", "--assemble-only", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("assemble-only install exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("assemble-only install executed commands: %+v", f.Calls)
	}
	if install.Exists(kitDir) {
		t.Fatalf("assemble-only install must not write install.json")
	}
	if _, err := os.Stat(filepath.Join(kitDir, ".build")); err != nil {
		t.Fatalf("assemble-only install should assemble the .build context: %v", err)
	}
	if !strings.Contains(out.String(), "assembled") {
		t.Fatalf("assemble-only install should report what it assembled: %q", out.String())
	}
}

// TestBuildCommandRetired: the `build` command is gone (COV-38) — `install`
// subsumes it (with --dry-run for assemble+inspect).
func TestBuildCommandRetired(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	var out, errOut bytes.Buffer
	code := run([]string{"build", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("build must be an unknown command; code=%d stderr=%q", code, errOut.String())
	}
}

func TestDryRunCreatePrintsNoExec(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeInstall(t, kitDir) // create consumes the installed image (COV-38)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "create", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed commands: %+v", f.Calls)
	}
	if !strings.Contains(out.String(), "would") {
		t.Fatalf("dry-run should describe planned actions: %q", out.String())
	}
}

func TestCreateWritesStateAndRejectsSecond(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeInstall(t, kitDir) // create consumes the pre-built image (COV-38)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"create", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("create exit=%d stderr=%s", code, errOut.String())
	}
	// Create is run-only now — it runs the installed image and never builds.
	if dockerArg0Index(f.Calls, "build") != -1 {
		t.Fatalf("create must not build (run-only); calls=%+v", f.Calls)
	}
	if dockerArg0Index(f.Calls, "run") == -1 {
		t.Fatalf("create must run the installed image; calls=%+v", f.Calls)
	}
	st, err := state.Load(kitDir)
	if err != nil {
		t.Fatalf("state not written: %v", err)
	}
	if st.Container != "atcove-box" || st.Image != "atcove-box" || st.Backend != "colima" {
		t.Fatalf("state = %+v", st)
	}
	var o2, e2 bytes.Buffer
	code := run([]string{"create", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &o2, &e2)
	if code == 0 || !strings.Contains(e2.String(), "already created") {
		t.Fatalf("second create should refuse; code=%d stderr=%q", code, e2.String())
	}
}

// TestCreateRequiresInstall: with no install.json, create (and recreate) fail
// closed with a clear "run `at-cove install`" pointer and touch no docker —
// strict currency, no auto-build (COV-38).
func TestCreateRequiresInstall(t *testing.T) {
	for _, cmd := range []string{"create", "recreate"} {
		dir := t.TempDir()
		writeKit(t, dir) // kit, but no install.json
		f := &runner.Fake{}
		var out, errOut bytes.Buffer
		code := run([]string{cmd, "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
		if code == 0 {
			t.Fatalf("%s must fail with no install; stdout=%q", cmd, out.String())
		}
		if !strings.Contains(errOut.String(), "at-cove install") {
			t.Fatalf("%s error must point at `at-cove install`; stderr=%q", cmd, errOut.String())
		}
		if len(f.Calls) != 0 {
			t.Fatalf("%s must not touch docker with no install; calls=%+v", cmd, f.Calls)
		}
	}
}

// TestCreateRejectsStaleInstall: an install whose kit source has since changed is
// stale — create (and recreate) refuse with a "stale … run `at-cove install`"
// error and build nothing, rather than silently running an out-of-date image.
func TestCreateRejectsStaleInstall(t *testing.T) {
	for _, cmd := range []string{"create", "recreate"} {
		dir := t.TempDir()
		kitDir := writeKit(t, dir)
		writeInstall(t, kitDir)
		// Edit config.yml after install → the currency hash no longer matches.
		if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte("name: box\n# changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f := &runner.Fake{}
		var out, errOut bytes.Buffer
		code := run([]string{cmd, "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
		if code == 0 {
			t.Fatalf("%s must fail on a stale install; stdout=%q", cmd, out.String())
		}
		if !strings.Contains(errOut.String(), "stale") || !strings.Contains(errOut.String(), "at-cove install") {
			t.Fatalf("%s error must name the stale install and `at-cove install`; stderr=%q", cmd, errOut.String())
		}
		if len(f.Calls) != 0 {
			t.Fatalf("%s must not touch docker on a stale install; calls=%+v", cmd, f.Calls)
		}
	}
}

// TestChatRequiresCurrentInstall: chat reads its run-config from install.json and
// verifies currency, so a missing install fails with the `at-cove install`
// pointer before dialing the backend.
func TestChatRequiresCurrentInstall(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeState(t, kitDir, "colima", "box") // created, but never installed
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"chat", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatalf("chat must fail with no install; stdout=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "at-cove install") {
		t.Fatalf("chat error must point at `at-cove install`; stderr=%q", errOut.String())
	}
}

func TestDestroyRemovesContainerAndState(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box")
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"destroy", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("destroy exit=%d stderr=%s", code, errOut.String())
	}
	if dockerArg0Index(f.Calls, "rm") == -1 {
		t.Fatalf("destroy must force-remove the container; calls=%+v", f.Calls)
	}
	// The image is an install artifact (COV-63): destroy tears down the instance
	// but must NOT rmi the image, so install.json stays consistent.
	if dockerArg0Index(f.Calls, "rmi") != -1 {
		t.Fatalf("destroy must NOT rmi the image; calls=%+v", f.Calls)
	}
	// A real destroy purges the instance's named volumes (no orphaned -state/-workspace).
	vol := dockerArg0Index(f.Calls, "volume")
	if vol == -1 {
		t.Fatalf("destroy must remove the instance volumes; calls=%+v", f.Calls)
	}
	gotState := false
	for _, a := range f.Calls[vol].Args {
		if a == "box-state" {
			gotState = true
		}
	}
	if !gotState {
		t.Fatalf("destroy must remove the box-state (/agent-data) volume; calls=%+v", f.Calls[vol].Args)
	}
	if state.Exists(kitDir) {
		t.Fatal("destroy must delete the state file")
	}
}

// Create records the backend's actual volume names in the state file, so a later
// destroy removes exactly those instead of re-deriving them (COV-76).
func TestCreateRecordsVolumesInState(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"create", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("create exit=%d stderr=%s", code, errOut.String())
	}
	st, err := state.Load(kitDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Volumes == nil || st.Volumes.State != "atcove-box-agent-data" || st.Volumes.Workspace != "atcove-box-workspace" {
		t.Fatalf("create must record the volume names in state; got %+v", st.Volumes)
	}
}

// Destroy tears down the volume names recorded in the state file, not names
// re-derived from the container (COV-76): a state file recording custom volume
// names removes exactly those.
func TestDestroyRemovesRecordedVolumes(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	if err := state.Save(kitDir, state.State{
		Name: "box", Backend: "colima", Container: "box",
		Image: "atcove-box", WorkspaceMode: "isolated",
		Volumes: &state.Volumes{State: "recorded-state", Workspace: "recorded-workspace"},
	}); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"destroy", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("destroy exit=%d stderr=%s", code, errOut.String())
	}
	vol := dockerArg0Index(f.Calls, "volume")
	if vol == -1 {
		t.Fatalf("destroy must remove volumes; calls=%+v", f.Calls)
	}
	gotRecorded, gotDerived := false, false
	for _, a := range f.Calls[vol].Args {
		switch a {
		case "recorded-state", "recorded-workspace":
			gotRecorded = true
		case "box-state", "box-workspace":
			gotDerived = true
		}
	}
	if !gotRecorded {
		t.Fatalf("destroy must remove the recorded volume names; calls=%+v", f.Calls[vol].Args)
	}
	if gotDerived {
		t.Fatalf("destroy must not re-derive volume names from the container; calls=%+v", f.Calls[vol].Args)
	}
}

func TestDestroyBlockedByActiveConnection(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box")
	lock, err := state.AcquireShared(kitDir) // simulate an open connection
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	var out, errOut bytes.Buffer
	code := run([]string{"destroy", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "active connection") {
		t.Fatalf("destroy should refuse with an active connection; code=%d stderr=%q", code, errOut.String())
	}
	if !state.Exists(kitDir) {
		t.Fatal("a blocked destroy must not delete the state file")
	}
}

// TestUninstallRemovesImageAndManifest: uninstall is the inverse of install — it
// `docker rmi`s the compiled image (via the backend) and deletes install.json,
// returning the kit to "not installed". No container/instance exists here.
func TestUninstallRemovesImageAndManifest(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeInstall(t, kitDir) // installed, but never created
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"uninstall", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("uninstall exit=%d stderr=%s", code, errOut.String())
	}
	rmi := dockerArg0Index(f.Calls, "rmi")
	if rmi == -1 {
		t.Fatalf("uninstall must docker rmi the image; calls=%+v", f.Calls)
	}
	gotImage := false
	for _, a := range f.Calls[rmi].Args {
		if a == "atcove-box" {
			gotImage = true
		}
	}
	if !gotImage {
		t.Fatalf("uninstall must rmi atcove-box; calls=%+v", f.Calls[rmi].Args)
	}
	if install.Exists(kitDir) {
		t.Fatal("uninstall must delete install.json")
	}
}

// TestUninstallRefusesWhileCreated: uninstall removes the build artifact, so it
// refuses while a created instance still exists (state.json present), pointing the
// user at `at-cove destroy` — and touches neither the image nor install.json.
func TestUninstallRefusesWhileCreated(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeInstall(t, kitDir)
	writeState(t, kitDir, "colima", "box") // an instance is still created
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"uninstall", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "at-cove destroy") {
		t.Fatalf("uninstall must refuse while created and point at `at-cove destroy`; code=%d stderr=%q", code, errOut.String())
	}
	if dockerArg0Index(f.Calls, "rmi") != -1 {
		t.Fatalf("a refused uninstall must not rmi the image; calls=%+v", f.Calls)
	}
	if !install.Exists(kitDir) {
		t.Fatal("a refused uninstall must not delete install.json")
	}
}

// TestUninstallIdempotentWhenImageGone: when install.json is present but the image
// is already gone (a machine left broken by the pre-COV-63 bug), uninstall still
// removes install.json (best-effort rmi) and succeeds.
func TestUninstallIdempotentWhenImageGone(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeInstall(t, kitDir)
	// A runner whose docker rmi (and preflight) fails, standing in for a
	// vanished image — uninstall must proceed regardless.
	f := &runner.Fake{Err: &runner.ExitError{Code: 1}}
	var out, errOut bytes.Buffer
	if code := run([]string{"uninstall", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("uninstall must succeed when the image is already gone; code=%d stderr=%q", code, errOut.String())
	}
	if install.Exists(kitDir) {
		t.Fatal("uninstall must delete install.json even when rmi fails")
	}
}

// TestUninstallNoOpWhenNotInstalled: a kit that was never installed has nothing to
// tear down — uninstall prints a friendly no-op and touches no docker.
func TestUninstallNoOpWhenNotInstalled(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir) // kit, but no install.json
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"uninstall", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("uninstall of a not-installed kit exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("no-op uninstall must not touch docker; calls=%+v", f.Calls)
	}
	if !strings.Contains(out.String(), "not installed") {
		t.Fatalf("no-op uninstall should report nothing to do; stdout=%q", out.String())
	}
}

// TestDryRunUninstall: `--dry-run uninstall` reports the image + manifest it would
// remove and touches nothing (no docker, install.json intact).
func TestDryRunUninstall(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"--dry-run", "uninstall", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("dry-run uninstall exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run uninstall executed commands: %+v", f.Calls)
	}
	if !install.Exists(kitDir) {
		t.Fatal("dry-run uninstall must not delete install.json")
	}
	if !strings.Contains(out.String(), "would remove") || !strings.Contains(out.String(), "atcove-box") {
		t.Fatalf("dry-run uninstall should describe the image + manifest it would remove; stdout=%q", out.String())
	}
}

func TestDryRunChatRawNoAuth(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // hermetic: no real ~/.config/at-cove/secrets.yml
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN"})
	writeInstall(t, kitDir) // chat reads run-config from install.json (COV-38)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "chat", "--raw", "--no-auth", "--project-dir", dir},
		f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed commands: %+v", f.Calls)
	}
	s := out.String()
	if !strings.Contains(s, "bash") || !strings.Contains(s, "no collaborator") {
		t.Fatalf("dry-run chat --raw --no-auth message = %q", s)
	}
}

func TestVersionSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"version"}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "at-cove "+version) {
		t.Fatalf("version output=%q want to contain %q", out.String(), "at-cove "+version)
	}
}

func TestVersionFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"--version"}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "at-cove "+version) {
		t.Fatalf("--version: code=%d out=%q", code, out.String())
	}
}

func TestDryRunRecreatePrintsNoExec(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "recreate", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed commands: %+v", f.Calls)
	}
	if !strings.Contains(out.String(), "would") || !strings.Contains(out.String(), "keeping volumes") {
		t.Fatalf("dry-run should describe a volume-keeping recreate: %q", out.String())
	}
}

func TestRecreateDestroysThenCreatesKeepingVolumes(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box") // already created -> recreate must destroy first
	writeInstall(t, kitDir)                // recreate re-runs the installed image (no rebuild)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"recreate", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	rmIdx := dockerArg0Index(f.Calls, "rm")
	runIdx := dockerArg0Index(f.Calls, "run")
	if rmIdx == -1 {
		t.Fatalf("recreate must destroy the existing container; calls=%+v", f.Calls)
	}
	// recreate re-runs the installed image — it never rebuilds (COV-38).
	if dockerArg0Index(f.Calls, "build") != -1 {
		t.Fatalf("recreate must not build (run-only); calls=%+v", f.Calls)
	}
	if runIdx == -1 {
		t.Fatalf("recreate must re-run the container; calls=%+v", f.Calls)
	}
	if rmIdx > runIdx {
		t.Fatalf("destroy must precede the re-run; calls=%+v", f.Calls)
	}
	for _, a := range f.Calls[rmIdx].Args {
		if a == "-v" || a == "--volumes" {
			t.Fatalf("recreate must keep volumes: %v", f.Calls[rmIdx].Args)
		}
	}
	// recreate must NOT purge volumes (the saved login on /agent-data survives).
	if dockerArg0Index(f.Calls, "volume") != -1 {
		t.Fatalf("recreate must keep volumes (no `docker volume rm`): %+v", f.Calls)
	}
}

// writeSharedState records a previously created instance whose workspace was a
// shared bind-mount (i.e. a share-repo-dir collaborator).
func writeSharedState(t *testing.T, kitDir, container, hostPath string) {
	t.Helper()
	if err := state.Save(kitDir, state.State{
		Name: container, Backend: "colima", Container: container,
		Image: naming.Image(container), WorkspaceMode: "shared", WorkspaceHostPath: hostPath,
	}); err != nil {
		t.Fatal(err)
	}
}

// dockerRunHasArg reports whether the `docker run` call carries the given arg.
func dockerRunHasArg(t *testing.T, calls []runner.Call, want string) bool {
	t.Helper()
	i := dockerArg0Index(calls, "run")
	if i == -1 {
		t.Fatalf("no docker run call; calls=%+v", calls)
	}
	for _, a := range calls[i].Args {
		if a == want {
			return true
		}
	}
	return false
}

// Recreate keeps volumes, but a shared bind-mount is not a volume — it must be
// re-specified at `docker run`. recreate must recover the shared workspace from
// state instead of silently falling back to an isolated volume.
func TestRecreatePreservesSharedWorkspaceFromState(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	hostPath := filepath.Join(dir, "myrepo")
	if err := os.MkdirAll(hostPath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSharedState(t, kitDir, "box", hostPath)
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"recreate", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	wantMount := hostPath + ":/home/agent/workspace"
	if !dockerRunHasArg(t, f.Calls, wantMount) {
		t.Fatalf("recreate dropped the shared workspace; want mount %q in run args", wantMount)
	}
	st, err := state.Load(kitDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.WorkspaceMode != "shared" || st.WorkspaceHostPath != hostPath {
		t.Fatalf("recreate must persist the shared workspace; state=%+v", st)
	}
}

// The arbitrary-host-path bind-mount flag is gone (COV-72): --ws/--workspace are
// no longer accepted by create or recreate (a flag-parse error → exit 2), and
// touch no docker. Only a share-repo-dir collaborator can now share the kit repo.
func TestWorkspaceFlagRemoved(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeInstall(t, kitDir)
	for _, cmd := range []string{"create", "recreate"} {
		for _, flag := range []string{"--ws", "--workspace"} {
			f := &runner.Fake{}
			var out, errOut bytes.Buffer
			code := run([]string{cmd, flag, filepath.Join(dir, "x"), "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
			if code != 2 {
				t.Fatalf("%s %s must be rejected (exit 2); code=%d stderr=%s", cmd, flag, code, errOut.String())
			}
			if len(f.Calls) != 0 {
				t.Fatalf("%s %s must not touch docker; calls=%+v", cmd, flag, f.Calls)
			}
		}
	}
}

// A share-repo-dir collaborator's create shares the kit's repo dir (the .at-cove
// parent) as a live-.git bind-mount, and records the shared mode in state.
func TestCreateShareRepoDirSharesKitRepo(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeShareRepoKit(t, dir, "steward")
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"create", "steward", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("create exit=%d stderr=%s", code, errOut.String())
	}
	want := dir + ":/home/agent/workspace" // the repo dir is the .at-cove parent
	if !dockerRunHasArg(t, f.Calls, want) {
		t.Fatalf("share-repo-dir create must bind-mount the kit repo dir; want %q in run args %+v", want, f.Calls)
	}
	// It must NOT also mount an isolated workspace volume for this instance.
	if dockerRunHasArg(t, f.Calls, "atcove-box-steward-workspace:/home/agent/workspace") {
		t.Fatalf("share-repo-dir create must not use an isolated workspace volume; calls=%+v", f.Calls)
	}
	st, err := state.LoadFor(kitDir, state.Instance("steward"))
	if err != nil {
		t.Fatalf("class-keyed state not written: %v", err)
	}
	if st.WorkspaceMode != "shared" || st.WorkspaceHostPath != dir {
		t.Fatalf("state must record the shared repo mount; state=%+v (want host %q)", st, dir)
	}
}

// A collaborator without share-repo-dir gets an isolated workspace volume (so
// COV-25's clone-on-first-session populates it), never a bind-mount.
func TestCreateWithoutShareRepoDirIsIsolated(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeCollabKit(t, dir, "steward")
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"create", "steward", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("create exit=%d stderr=%s", code, errOut.String())
	}
	if !dockerRunHasArg(t, f.Calls, "atcove-box-steward-workspace:/home/agent/workspace") {
		t.Fatalf("a non-share-repo-dir collaborator must use an isolated volume; calls=%+v", f.Calls)
	}
	if dockerRunHasArg(t, f.Calls, dir+":/home/agent/workspace") {
		t.Fatalf("a non-share-repo-dir collaborator must not bind-mount the repo dir; calls=%+v", f.Calls)
	}
	st, err := state.LoadFor(kitDir, state.Instance("steward"))
	if err != nil {
		t.Fatalf("state not written: %v", err)
	}
	if st.WorkspaceMode != "isolated" {
		t.Fatalf("state must record isolated mode; state=%+v", st)
	}
}

// The share-repo-dir dry-run intent line names the shared repo path.
func TestDryRunCreateShareRepoDirIntent(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeShareRepoKit(t, dir, "steward")
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "create", "steward", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed commands: %+v", f.Calls)
	}
	if !strings.Contains(out.String(), "sharing repo dir "+dir) {
		t.Fatalf("dry-run must name the shared repo dir; got %q", out.String())
	}
}

func TestRecreateSkipsDestroyWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeInstall(t, kitDir)
	f := &runner.Fake{} // no state -> nothing to destroy
	var out, errOut bytes.Buffer
	code := run([]string{"recreate", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if dockerArg0Index(f.Calls, "rm") != -1 {
		t.Fatalf("must not destroy when nothing is created; calls=%+v", f.Calls)
	}
	// recreate re-runs the installed image; it never builds (COV-38).
	if dockerArg0Index(f.Calls, "build") != -1 {
		t.Fatalf("recreate must not build (run-only); calls=%+v", f.Calls)
	}
	if dockerArg0Index(f.Calls, "run") == -1 {
		t.Fatalf("recreate must still run the container; calls=%+v", f.Calls)
	}
}

// TestDryRunChatCountsDemandsWithoutResolving (COV-82): `--dry-run chat` reports
// the number of *demanded* secrets and resolves none — the secret plan/mint
// expansion (which can run host lookups) now happens only after the dry-run
// return, so no resolver runs and no unresolved-supply warning is emitted.
func TestDryRunChatCountsDemandsWithoutResolving(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN"}) // demanded, no command
	writeInstall(t, kitDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // empty config dir -> no secrets.yml
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "chat", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run chat must resolve nothing; executed commands: %+v", f.Calls)
	}
	if strings.Contains(errOut.String(), "will not be set") {
		t.Fatalf("dry-run must not resolve supply, so it must emit no unresolved warning; got %q", errOut.String())
	}
	if !strings.Contains(out.String(), "would resolve 1 secrets") {
		t.Fatalf("demanded count should be 1; got %q", out.String())
	}
}

// TestDryRunDispatchIsSideEffectFree (COV-82): `--dry-run dispatch` previews the
// kit, worker classes, and poll interval, and returns before resolving the
// tracker token, connecting to Linear, or starting the scheduler. It needs no
// install and no secrets.yml supply.
func TestDryRunDispatchIsSideEffectFree(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no secrets.yml at all
	dir := writeDispatchKit(t, dispatchGoodConfig)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "dispatch", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("dry-run dispatch exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run dispatch executed commands: %+v", f.Calls)
	}
	s := out.String()
	if !strings.Contains(s, "would dispatch") || !strings.Contains(s, "implement") || !strings.Contains(s, "60s") {
		t.Fatalf("dry-run dispatch should name the plan, classes, and poll interval; got %q", s)
	}
	if strings.Contains(s, "kit OK") || strings.Contains(errOut.String(), "Linear") {
		t.Fatalf("dry-run dispatch must not connect to the tracker; stdout=%q stderr=%q", s, errOut.String())
	}
}

// A kit with no collaborators declared resolves to "no collaborator" in the
// dry-run message, and --fresh is accepted without error (fresh/resume is a
// launch-time detail no longer surfaced in the dry-run summary).
func TestDryRunChatNoCollaboratorFresh(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN"})
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "chat", "--fresh", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "as no collaborator") {
		t.Fatalf("kit with no collaborators should report 'no collaborator'; msg=%q", s)
	}
}

// A kit declaring a single collaborator class auto-selects it (SelectCollaborator's
// "sole class" default), and the dry-run message names it.
func TestDryRunChatResolvesDefaultCollaborator(t *testing.T) {
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\ncollaborators:\n  steward:\n    prompt: \"you are the steward\"\n"
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// The sole collaborator keys its own instance: state file steward.json,
	// container box-steward (COV-71).
	writeStateFor(t, kitDir, state.Instance("steward"), "box", "box-steward", state.Secret{Name: "GITHUB_TOKEN"})
	writeInstall(t, kitDir) // the collaborator is read from install.json's run-config
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "chat", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "as collaborator steward") {
		t.Fatalf("expected the sole collaborator to be auto-selected; msg=%q", out.String())
	}
}

// sessionEgressCalls returns the recorded `docker exec … apply-session-domains.sh`
// calls (the per-session egress deliveries, COV-39 §5), in order, so tests can
// assert apply-on-start and clear-on-exit distinctly.
func sessionEgressCalls(calls []runner.Call) []runner.Call {
	var out []runner.Call
	for _, c := range calls {
		if c.Name != "docker" {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, "apply-session-domains.sh") {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// launchIndex returns the index of the interactive launch ssh call (`-tt`), the
// point at which the session is handed to the agent.
func launchIndex(calls []runner.Call) int {
	for i, c := range calls {
		if c.Name != "ssh" {
			continue
		}
		for _, a := range c.Args {
			if a == "-tt" {
				return i
			}
		}
	}
	return -1
}

// egressCallIndices returns the f.Calls indices of the session-egress deliveries.
func egressCallIndices(calls []runner.Call) []int {
	var idx []int
	for i, c := range calls {
		if c.Name != "docker" {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, "apply-session-domains.sh") {
				idx = append(idx, i)
				break
			}
		}
	}
	return idx
}

// TestChatAppliesCollaboratorEgressAndClearsOnExit asserts the persistent
// (interactive) path scopes egress to the selected collaborator class on start
// and reverts it on exit (COV-39 §5): before connect hands the session to the
// agent, chat applies the class's resolved <common> ∪ class delta (from the
// install.json RunConfig) via ApplySessionEgress; on exit it clears the session
// file so the idle container reverts to root-only.
func TestChatAppliesCollaboratorEgressAndClearsOnExit(t *testing.T) {
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\ncollaborators:\n  <common>:\n    allowed-domains: [docs.internal]\n  planner:\n    prompt: \"plan it\"\n    allowed-domains: [linear.app]\n"
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	seedConfigDir(t) // keypair present so keys.Ensure doesn't shell out
	// The planner collaborator keys its own instance: state planner.json,
	// container box-planner (COV-71).
	writeStateFor(t, kitDir, state.Instance("planner"), "box", "box-planner")
	writeInstall(t, kitDir) // collaborator domains are sourced from install.json
	// GetStatus -> running; Dial -> host:port. --no-auth skips the ssh auth probe.
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}, {Stdout: "127.0.0.1:49153\n"}}}
	var out, errOut bytes.Buffer
	code := run([]string{"chat", "planner", "--no-auth", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	egr := sessionEgressCalls(f.Calls)
	if len(egr) != 2 {
		t.Fatalf("want exactly 2 session-egress calls (apply-on-start + clear-on-exit); got %d: %+v", len(egr), egr)
	}
	// apply-on-start carries the resolved, deduped, sorted <common> ∪ class delta
	// on stdin (one host per line), never on argv.
	if want := "docs.internal\nlinear.app\n"; egr[0].Stdin != want {
		t.Fatalf("apply-on-start stdin = %q; want %q", egr[0].Stdin, want)
	}
	// clear-on-exit carries an empty delta so the container reverts to root-only.
	if egr[1].Stdin != "" {
		t.Fatalf("clear-on-exit stdin = %q; want empty", egr[1].Stdin)
	}
	// Ordering: apply BEFORE the agent handoff (the `-tt` launch), clear AFTER it.
	idx := egressCallIndices(f.Calls)
	li := launchIndex(f.Calls)
	if li < 0 {
		t.Fatalf("expected an interactive launch ssh call; calls=%+v", f.Calls)
	}
	if !(idx[0] < li && li < idx[1]) {
		t.Fatalf("egress order wrong: apply@%d launch@%d clear@%d (want apply<launch<clear)", idx[0], li, idx[1])
	}
}

// TestChatPlainSessionAppliesRootOnlyEgress asserts a no-collaborator plain
// session applies an empty delta on start (root only — no widened egress) and
// still clears on exit (COV-39 §5).
func TestChatPlainSessionAppliesRootOnlyEgress(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir) // no collaborators -> plain session
	seedConfigDir(t)           // keypair present so keys.Ensure doesn't shell out
	writeState(t, kitDir, "colima", "box")
	writeInstall(t, kitDir)
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}, {Stdout: "127.0.0.1:49153\n"}}}
	var out, errOut bytes.Buffer
	code := run([]string{"chat", "--no-auth", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	egr := sessionEgressCalls(f.Calls)
	if len(egr) != 2 {
		t.Fatalf("want exactly 2 session-egress calls (apply empty + clear); got %d: %+v", len(egr), egr)
	}
	for i, c := range egr {
		if c.Stdin != "" {
			t.Fatalf("call %d stdin = %q; a plain session applies root-only (empty delta)", i, c.Stdin)
		}
	}
}

// launchRemote returns the remote command string of the interactive launch ssh
// call (the last arg of the `-tt` invocation), where the claude `-n <name>`
// session-name flag lives.
func launchRemote(t *testing.T, calls []runner.Call) string {
	t.Helper()
	li := launchIndex(calls)
	if li < 0 {
		t.Fatalf("expected an interactive launch ssh call; calls=%+v", calls)
	}
	args := calls[li].Args
	if len(args) == 0 {
		t.Fatalf("launch ssh call has no args; call=%+v", calls[li])
	}
	return args[len(args)-1]
}

// TestChatCollaboratorSessionName asserts a collaborator session launches claude
// under "{kit} {collaborator} cove" so per-collaborator instances don't collide
// on session identity (COV-75).
func TestChatCollaboratorSessionName(t *testing.T) {
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\ncollaborators:\n  planner:\n    prompt: \"plan it\"\n"
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	seedConfigDir(t)
	writeStateFor(t, kitDir, state.Instance("planner"), "box", "box-planner")
	writeInstall(t, kitDir)
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}, {Stdout: "127.0.0.1:49153\n"}}}
	var out, errOut bytes.Buffer
	code := run([]string{"chat", "planner", "--no-auth", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if remote := launchRemote(t, f.Calls); !strings.Contains(remote, `-n 'box planner cove'`) {
		t.Fatalf("collaborator session should be named '<kit> <collaborator> cove': %q", remote)
	}
}

// TestChatPlainSessionName asserts a no-collaborator session keeps the "{kit} cove"
// label exactly — no stray collaborator segment or double spaces (COV-75).
func TestChatPlainSessionName(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir) // no collaborators -> plain session
	seedConfigDir(t)
	writeState(t, kitDir, "colima", "box")
	writeInstall(t, kitDir)
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}, {Stdout: "127.0.0.1:49153\n"}}}
	var out, errOut bytes.Buffer
	code := run([]string{"chat", "--no-auth", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if remote := launchRemote(t, f.Calls); !strings.Contains(remote, `-n 'box cove'`) {
		t.Fatalf("plain session should be named exactly '<kit> cove': %q", remote)
	}
}

func TestChatMalformedSecretsFileAborts(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN"})
	writeInstall(t, kitDir)
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	coveCfg := filepath.Join(cfgHome, "at-cove")
	if err := os.MkdirAll(coveCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	// Malformed: a supply must set exactly one of value/command/global/mint —
	// this one sets both value and command.
	badYAML := "kits:\n  box:\n    GITHUB_TOKEN: { value: \"x\", command: [\"true\"] }\n"
	if err := os.WriteFile(filepath.Join(coveCfg, "secrets.yml"), []byte(badYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "chat", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatalf("malformed secrets.yml should abort; out=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "secrets.yml") {
		t.Fatalf("error should name the offending file; stderr=%q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "exactly one of value/command") {
		t.Fatalf("error should explain the malformed source; stderr=%q", errOut.String())
	}
}

func TestSaveStateSnapshot(t *testing.T) {
	dir := t.TempDir()
	cfg := kit.Config{Name: "box", Secrets: map[string]kit.SecretConfig{
		"GITHUB_TOKEN": {Description: "code host token"},
	}}
	inst := backend.Instance{Backend: "colima", Container: "box", Image: "img", ImageDigest: "sha256:cafe",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated}}
	if err := saveState(dir, state.Interactive, cfg, inst); err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Name != "box" || st.Backend != "colima" || st.Container != "box" || st.Image != "img" || st.ImageDigest != "sha256:cafe" {
		t.Fatalf("state = %+v", st)
	}
	if len(st.Secrets) != 1 || st.Secrets[0].Name != "GITHUB_TOKEN" {
		t.Fatalf("secrets not snapshotted: %+v", st.Secrets)
	}
}

// TestEffectiveWorkTimeout (COV-88): an unset --timeout adopts the resolved
// workers.<class>.timeout so a manual `work` matches `dispatch`; an explicit flag
// always wins; a class with no timeout keeps the flag default.
func TestEffectiveWorkTimeout(t *testing.T) {
	const flagDefault = 30 * time.Minute
	cases := []struct {
		name         string
		flagValue    time.Duration
		flagSet      bool
		classTimeout string
		want         time.Duration
	}{
		{"unset flag adopts class timeout", flagDefault, false, "2h", 2 * time.Hour},
		{"explicit flag wins over class", 45 * time.Minute, true, "2h", 45 * time.Minute},
		{"explicit flag wins even matching default", flagDefault, true, "2h", flagDefault},
		{"no class timeout keeps flag default", flagDefault, false, "", flagDefault},
		{"unparseable class timeout keeps flag default", flagDefault, false, "nonsense", flagDefault},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveWorkTimeout(tc.flagValue, tc.flagSet, tc.classTimeout); got != tc.want {
				t.Fatalf("effectiveWorkTimeout(%v, %v, %q) = %v, want %v", tc.flagValue, tc.flagSet, tc.classTimeout, got, tc.want)
			}
		})
	}
}

func TestWorkRequiresInAndOut(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d; want 2 (missing --in/--out)", code)
	}
	// Note: a bare "--in" substring check would also match "--interval" in the
	// generic unknown-command usage fallback, so this pins the exact message.
	if !strings.Contains(errOut.String(), "--in and --out are required") {
		t.Fatalf("stderr = %q; want mention of --in/--out being required", errOut.String())
	}
}

func TestWorkRejectsPositionalProjectDir(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"work", "somekit"}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d; want 2 (stray positional)", code)
	}
	if !strings.Contains(errOut.String(), "--project-dir") {
		t.Fatalf("stderr = %q; want mention of --project-dir", errOut.String())
	}
}

// --reap must not require --in/--out: it only scavenges labeled orphans.
func TestWorkReapDoesNotRequireInOut(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	// preflight `docker info` is a Probe (no Output consumed); `docker ps` is the
	// one Output call ScavengeLabeled makes (empty => no orphans to remove).
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: ""}}}
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--project-dir", dir, "--reap"}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d; want 0, stderr=%s", code, errOut.String())
	}
}

func TestWorkRequiresWorkers(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir) // no workers declared
	writeInstall(t, kitDir)    // work reads run-config from a current install
	inFile := filepath.Join(dir, "in.json")
	outFile := filepath.Join(dir, "out.json")
	if err := os.WriteFile(inFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--project-dir", dir, "--in", inFile, "--out", outFile}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (no workers)", code)
	}
	if !strings.Contains(errOut.String(), "declares no workers") {
		t.Fatalf("stderr = %q; want mention of declares no workers", errOut.String())
	}
}

// TestWorkFailsFastWhenNotInstalled: with no install.json, `work` must fail fast
// telling the operator to run `at-cove install` — it never builds and never
// touches the backend (COV-38 §6). A dispatch-ready kit with resolvable secrets
// isolates the missing-install gate as the sole cause.
func TestWorkFailsFastWhenNotInstalled(t *testing.T) {
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(workerBearerKitConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	seedConfigDir(t) // note: no writeInstall — the kit is not installed
	inFile := filepath.Join(dir, "in.json")
	if err := os.WriteFile(inFile, []byte(implementTaskJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out.json")
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--project-dir", dir, "--in", inFile, "--out", outFile}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatal("work must fail fast when the kit is not installed")
	}
	if !strings.Contains(errOut.String(), "at-cove install") {
		t.Fatalf("stderr must tell the operator to run `at-cove install`; got %q", errOut.String())
	}
	if dockerArg0Index(f.Calls, "build") != -1 || dockerArg0Index(f.Calls, "run") != -1 {
		t.Fatalf("work must not build or run anything without an install; calls=%+v", f.Calls)
	}
}

// TestWorkFailsFastWhenStale: an install that no longer matches the kit source
// (here config.yml is edited after install) is stale, and `work` must fail fast
// with `at-cove install` rather than run a mismatched image.
func TestWorkFailsFastWhenStale(t *testing.T) {
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(workerBearerKitConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	seedConfigDir(t)
	writeInstall(t, kitDir) // install against the current source...
	// ...then edit config.yml so the frozen currency hash no longer matches.
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(workerBearerKitConfig+"\n# drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inFile := filepath.Join(dir, "in.json")
	if err := os.WriteFile(inFile, []byte(implementTaskJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out.json")
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--project-dir", dir, "--in", inFile, "--out", outFile}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatal("work must fail fast when the install is stale")
	}
	if !strings.Contains(errOut.String(), "stale") || !strings.Contains(errOut.String(), "at-cove install") {
		t.Fatalf("stderr must report a stale install and point at `at-cove install`; got %q", errOut.String())
	}
	if dockerArg0Index(f.Calls, "run") != -1 {
		t.Fatalf("work must not run a stale image; calls=%+v", f.Calls)
	}
}

// TestDispatchFailsFastWhenNotInstalled: `dispatch` reads its run-config from
// install.json, so a missing install fails fast with `at-cove install` — before
// it ever validates the scheduler surface or resolves a tracker token.
func TestDispatchFailsFastWhenNotInstalled(t *testing.T) {
	dir := writeDispatchKit(t, dispatchGoodConfig) // valid kit, but never installed
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (not installed)", code)
	}
	if !strings.Contains(errOut.String(), "at-cove install") {
		t.Fatalf("stderr must tell the operator to run `at-cove install`; got %q", errOut.String())
	}
}

// workerBearerKitConfig is a complete, dispatch-ready worker kit whose
// `implement` worker class declares ANTHROPIC_AUTH_TOKEN (the agent's
// Anthropic bearer) under workers.implement.secrets — not at the root, which
// kit.Load now rejects (see validateSecretNames) — alongside the required
// source-control.github AT_TASK_GIT_TOKEN demand. It deliberately supplies no
// secrets.yml entry for ANTHROPIC_AUTH_TOKEN so the bearer stays unresolved.
// AT_TASK_GIT_TOKEN, by contrast, is resolved cleanly by the test (see
// TestWorkFailsClosedWhenWorkerBearerUnresolved) so the bearer is the ONLY
// unresolved secret — isolating the new gate from the pre-existing
// planRequired fail-closed check on the git token.
const workerBearerKitConfig = `name: box
source-control:
  github:
    project: your-org/your-repo
    secrets:
      AT_TASK_GIT_TOKEN: {}
workers:
  implement:
    prompt: "impl"
    timeout: 30m
    secrets:
      ANTHROPIC_AUTH_TOKEN: {}
`

// implementTaskJSON dispatches to the "implement" worker class — the class
// workerBearerKitConfig declares — so doWork resolves the matching worker
// secret bucket (Config.ResolvedWorker reads worker.class from the task).
const implementTaskJSON = `{"worker":{"class":"implement"}}`

// TestWorkFailsClosedWhenWorkerBearerUnresolved guards the motivating
// production bug: a dispatched worker with no ANTHROPIC_AUTH_TOKEN is a
// guaranteed 401 once it reaches the agent inside the VM. doWork must fail
// closed — naming the unresolved secret and the kit — before it ever
// assembles/dispatches a VM, not warn-and-continue like a general secret. The
// bearer is now demanded under workers.<class>.secrets rather than the kit
// root, so the gate must source from the resolved worker bucket.
func TestWorkFailsClosedWhenWorkerBearerUnresolved(t *testing.T) {
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(workerBearerKitConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	seedConfigDir(t)        // hermetic XDG_CONFIG_HOME: fresh temp dir, pre-seeded keypair
	writeInstall(t, kitDir) // work consumes a current installed image
	// Resolve AT_TASK_GIT_TOKEN cleanly so the bearer gate is the ONLY unresolved
	// secret: without this, the pre-existing planRequired fail-closed check on
	// the git token would ALSO abort the VM, and the "no ssh/no docker run"
	// assertions below would pass even if the bearer gate were deleted.
	if err := os.WriteFile(filepath.Join(configDir(), "secrets.yml"),
		[]byte("kits:\n  box:\n    AT_TASK_GIT_TOKEN: { value: \"git-tok\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inFile := filepath.Join(dir, "in.json")
	if err := os.WriteFile(inFile, []byte(implementTaskJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out.json")
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--project-dir", dir, "--in", inFile, "--out", outFile}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatalf("expected non-zero exit when the worker bearer is unresolved")
	}
	if !strings.Contains(errOut.String(), "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("stderr must name the unresolved bearer secret; got %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "fail") {
		t.Fatalf("stderr must read as a fail-closed message; got %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "box") {
		t.Fatalf("stderr must name the kit; got %q", errOut.String())
	}
	for _, c := range f.Calls {
		if c.Name == "ssh" {
			t.Fatalf("must abort before any SSH step; calls=%+v", f.Calls)
		}
	}
	if dockerArg0Index(f.Calls, "run") != -1 {
		t.Fatalf("must abort before running the work VM; calls=%+v", f.Calls)
	}
}

// TestWorkResolvesWorkerBearerFromWorkerBucket is the positive counterpart:
// when ANTHROPIC_AUTH_TOKEN is supplied for the dispatched class's worker
// bucket (and AT_TASK_GIT_TOKEN besides), doWork must NOT fail closed — it
// should clear the gate and proceed into dispatchrun.Dispatch (which then
// runs the real ssh/docker steps against the fake runner).
func TestWorkResolvesWorkerBearerFromWorkerBucket(t *testing.T) {
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(workerBearerKitConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	seedConfigDir(t)
	writeInstall(t, kitDir) // work consumes a current installed image
	if err := os.WriteFile(filepath.Join(configDir(), "secrets.yml"),
		[]byte("kits:\n  box:\n    AT_TASK_GIT_TOKEN: { value: \"git-tok\" }\n    ANTHROPIC_AUTH_TOKEN: { value: \"sk-ant-test\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inFile := filepath.Join(dir, "in.json")
	if err := os.WriteFile(inFile, []byte(implementTaskJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out.json")
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--project-dir", dir, "--in", inFile, "--out", outFile}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if strings.Contains(errOut.String(), "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("resolved worker bearer must not trip the fail-closed gate; stderr=%q code=%d", errOut.String(), code)
	}
	if dockerArg0Index(f.Calls, "run") == -1 {
		t.Fatalf("expected the gate to clear and dispatch to reach the VM; calls=%+v stderr=%q", f.Calls, errOut.String())
	}
	// The run path consumes the installed image — it must never `docker build`.
	if dockerArg0Index(f.Calls, "build") != -1 {
		t.Fatalf("work must consume the installed image, not build; calls=%+v", f.Calls)
	}
}

// workerAPIKeyBearerKitConfig is a dispatch-ready worker kit whose worker
// bucket declares ONLY ANTHROPIC_API_KEY — the alternate well-known bearer
// name (config validation accepts either it or ANTHROPIC_AUTH_TOKEN as the
// worker-bucket bearer, and rejects both at the kit root). It is otherwise
// identical to workerBearerKitConfig so the gate is exercised on the API-key
// name alone.
const workerAPIKeyBearerKitConfig = `name: box
source-control:
  github:
    project: your-org/your-repo
    secrets:
      AT_TASK_GIT_TOKEN: {}
workers:
  implement:
    prompt: "impl"
    timeout: 30m
    secrets:
      ANTHROPIC_API_KEY: {}
`

// TestWorkAcceptsAnthropicAPIKeyAsBearer guards COV-28: a worker kit that
// declares ONLY ANTHROPIC_API_KEY (with a supply) must pass the fail-closed
// bearer gate and proceed to dispatch, rather than being hard-failed pre-VM for
// a missing ANTHROPIC_AUTH_TOKEN.
func TestWorkAcceptsAnthropicAPIKeyAsBearer(t *testing.T) {
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(workerAPIKeyBearerKitConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	seedConfigDir(t)
	writeInstall(t, kitDir) // work consumes a current installed image
	// Supply BOTH the API-key bearer and the git token so neither gate aborts:
	// the run must clear the gate and reach dispatch.
	if err := os.WriteFile(filepath.Join(configDir(), "secrets.yml"),
		[]byte("kits:\n  box:\n    ANTHROPIC_API_KEY: { value: \"sk-ant-api-test\" }\n    AT_TASK_GIT_TOKEN: { value: \"git-tok\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inFile := filepath.Join(dir, "in.json")
	if err := os.WriteFile(inFile, []byte(implementTaskJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out.json")
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--project-dir", dir, "--in", inFile, "--out", outFile}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	// The bearer gate must NOT have fired: its message names the 401 it prevents.
	if strings.Contains(errOut.String(), "401") {
		t.Fatalf("bearer gate must accept ANTHROPIC_API_KEY, not fail closed; got %q", errOut.String())
	}
	// Proof we cleared the gate: dispatch reached the VM.
	if dockerArg0Index(f.Calls, "run") == -1 {
		t.Fatalf("expected the gate to clear and dispatch to reach the VM; code=%d calls=%+v stderr=%q", code, f.Calls, errOut.String())
	}
}

// workerNoBearerKitConfig is a dispatch-ready worker kit whose worker bucket
// declares NEITHER well-known bearer name — only the required git token lives
// on the kit. The fail-closed gate must still abort pre-VM.
const workerNoBearerKitConfig = `name: box
source-control:
  github:
    project: your-org/your-repo
    secrets:
      AT_TASK_GIT_TOKEN: {}
workers:
  implement: { prompt: "impl", timeout: 30m }
`

// TestWorkFailsClosedWhenNoBearerDeclared guards COV-28's other half: a worker
// that declares neither ANTHROPIC_AUTH_TOKEN nor ANTHROPIC_API_KEY must still
// fail closed before any VM, and the attribution must name BOTH bearer names it
// looked for so the operator knows either would satisfy the gate.
func TestWorkFailsClosedWhenNoBearerDeclared(t *testing.T) {
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(workerNoBearerKitConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	seedConfigDir(t)
	writeInstall(t, kitDir) // work consumes a current installed image
	if err := os.WriteFile(filepath.Join(configDir(), "secrets.yml"),
		[]byte("kits:\n  box:\n    AT_TASK_GIT_TOKEN: { value: \"git-tok\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inFile := filepath.Join(dir, "in.json")
	if err := os.WriteFile(inFile, []byte(implementTaskJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out.json")
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--project-dir", dir, "--in", inFile, "--out", outFile}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatalf("expected non-zero exit when no agent bearer is declared")
	}
	if !strings.Contains(errOut.String(), "ANTHROPIC_AUTH_TOKEN") || !strings.Contains(errOut.String(), "ANTHROPIC_API_KEY") {
		t.Fatalf("stderr must name both bearer names the gate looked for; got %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "fail") {
		t.Fatalf("stderr must read as a fail-closed message; got %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "box") {
		t.Fatalf("stderr must name the kit; got %q", errOut.String())
	}
	for _, c := range f.Calls {
		if c.Name == "ssh" {
			t.Fatalf("must abort before any SSH step; calls=%+v", f.Calls)
		}
	}
	if dockerArg0Index(f.Calls, "run") != -1 {
		t.Fatalf("must abort before running the work VM; calls=%+v", f.Calls)
	}
}

// TestDryRunWorkPrintsNoExec guards Fix A: --dry-run work must print
// the plan and exit 0 without touching the backend, assembling, or resolving
// secrets (no calls recorded on the fake runner at all).
func TestDryRunWorkPrintsNoExec(t *testing.T) {
	dir := t.TempDir()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\nworkers:\n  implement:\n    prompt: do the thing\n"
	if err := os.WriteFile(filepath.Join(cove, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	inFile := filepath.Join(dir, "in.json")
	if err := os.WriteFile(inFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out.json")
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "work", "--project-dir", dir, "--in", inFile, "--out", outFile},
		f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run work executed commands: %+v", f.Calls)
	}
	if _, err := os.Stat(outFile); err == nil {
		t.Fatal("dry-run work must not write the output file")
	}
	s := out.String()
	if !strings.Contains(s, "would") || !strings.Contains(s, inFile) || !strings.Contains(s, outFile) {
		t.Fatalf("dry-run work should describe the planned actions incl. --in/--out: %q", s)
	}
}

// TestDryRunWorkReapPrintsNoExec guards --dry-run work --reap: it must
// not call Reap (no ScavengeLabeled == no Output call on the fake).
func TestDryRunWorkReapPrintsNoExec(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "work", "--project-dir", dir, "--reap"}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run work --reap executed commands: %+v", f.Calls)
	}
	if !strings.Contains(out.String(), "would") {
		t.Fatalf("dry-run work --reap should describe the planned scavenge: %q", out.String())
	}
}

// dispatchGoodConfig is a complete dispatch-capable kit config.yml (name +
// source-control + tracker.linear + dispatch + workers).
const dispatchGoodConfig = `name: dispatch-kit
source-control:
  github:
    project: your-org/your-repo
    secrets:
      AT_TASK_GIT_TOKEN: {}
tracker:
  linear:
    team: AET
    poll-interval: 60s
    states: { ready: Todo, in-progress: In Progress, in-review: In Review, done: Done, needs-input: Needs Input, blocked: Backlog }
    secrets:
      AT_DISPATCH_TRACKER_TOKEN:  {}
      AT_DISPATCH_WEBHOOK_SECRET: {}
dispatch:
  concurrency: 1
  reaper-timeout: 45m
workers:
  implement: { prompt: "impl", timeout: 30m }
`

// writeDispatchKit writes body as a kit config.yml under a temp project root's
// .at-cove/ and returns the project root (suitable as `dispatch --project-dir`).
func writeDispatchKit(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cove, "config.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestDispatchTokenResolveFailure: valid kit, but the tracker token resolver
// command fails → dispatch exits 1 before constructing the tracker client.
func TestDispatchTokenResolveFailure(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	if err := os.MkdirAll(filepath.Join(cfgHome, "at-cove"), 0o755); err != nil {
		t.Fatal(err)
	}
	// secrets.yml supplies the tracker token via a resolver command that fails.
	if err := os.WriteFile(filepath.Join(cfgHome, "at-cove", "secrets.yml"),
		[]byte("kits:\n  dispatch-kit:\n    AT_DISPATCH_TRACKER_TOKEN: { command: [\"false\"] }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := writeDispatchKit(t, dispatchGoodConfig)
	writeInstall(t, filepath.Join(dir, ".at-cove")) // dispatch reads run-config from a current install
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "token") {
		t.Fatalf("stderr = %q; want a token-resolution error", errOut.String())
	}
}

// TestDispatchRejectsBadConfig: kit with no tracker section → the missing-surface
// check rejects it, exit 1.
func TestDispatchRejectsBadConfig(t *testing.T) {
	dir := writeDispatchKit(t, "name: dispatch-kit\nworkers:\n  implement: { prompt: \"impl\", timeout: 30m }\n")
	writeInstall(t, filepath.Join(dir, ".at-cove")) // dispatch reads run-config from a current install
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "must declare") {
		t.Fatalf("stderr = %q; want the missing-surface error", errOut.String())
	}
}

// TestDispatchRejectsIncompleteKit: a kit missing tracker/dispatch/workers exits 1
// with the missing-surface message.
func TestDispatchRejectsIncompleteKit(t *testing.T) {
	dir := writeDispatchKit(t, "name: dispatch-kit\n")
	writeInstall(t, filepath.Join(dir, ".at-cove")) // dispatch reads run-config from a current install
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "must declare") {
		t.Fatalf("stderr = %q; want the missing-surface error", errOut.String())
	}
}

// TestLogLevelEnvFallbackOnDispatchPath guards the AT_LOG_LEVEL env fallback
// used by doDispatch (and mirrored by doWork's bearer-gate logger): the
// --log-level global flag must default to "" so envOr(g.LogLevel,
// "AT_LOG_LEVEL") actually falls through to the environment. It exercises
// the real flag-parsing path (cli.App.Run with a probe command), not
// logLevelFrom in isolation, so it fails if the flag's zero value regresses
// back to a non-empty "info".
func TestLogLevelEnvFallbackOnDispatchPath(t *testing.T) {
	var got cli.Globals
	app := cli.App{
		Name: "at-cove", Version: "test",
		Commands: []cli.Command{
			{Name: "probe", Brief: "capture globals", Run: func(args []string, g cli.Globals, stdout, stderr io.Writer) int {
				got = g
				return 0
			}},
		},
	}

	t.Run("AT_LOG_LEVEL set, flag omitted", func(t *testing.T) {
		t.Setenv("AT_LOG_LEVEL", "debug")
		var out, errOut bytes.Buffer
		if code := app.Run([]string{"probe"}, &out, &errOut); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errOut.String())
		}
		if got.LogLevel != "" {
			t.Fatalf("g.LogLevel = %q, want %q (flag omitted) — --log-level flag default is no longer empty, so envOr can never see AT_LOG_LEVEL", got.LogLevel, "")
		}
		if lvl := logLevelFrom(envOr(got.LogLevel, "AT_LOG_LEVEL")); lvl != slog.LevelDebug {
			t.Fatalf("logLevelFrom(envOr(g.LogLevel, \"AT_LOG_LEVEL\")) = %v, want %v (AT_LOG_LEVEL=debug ignored)", lvl, slog.LevelDebug)
		}
	})

	t.Run("neither flag nor env set", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := app.Run([]string{"probe"}, &out, &errOut); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errOut.String())
		}
		if lvl := logLevelFrom(envOr(got.LogLevel, "AT_LOG_LEVEL")); lvl != slog.LevelInfo {
			t.Fatalf("logLevelFrom(envOr(g.LogLevel, \"AT_LOG_LEVEL\")) = %v, want %v (effective default)", lvl, slog.LevelInfo)
		}
	})
}

// TestLogModeFrom covers the --log-mode/AT_LOG_MODE value mapper directly: the
// two recognized modes map to their logging.Mode, and everything else (empty,
// garbage) falls back to Auto (TTY-detected).
func TestLogModeFrom(t *testing.T) {
	cases := map[string]logging.Mode{
		"attended":   logging.Attended,
		"unattended": logging.Unattended,
		"":           logging.Auto,
		"nonsense":   logging.Auto,
	}
	for in, want := range cases {
		if got := logModeFrom(in); got != want {
			t.Errorf("logModeFrom(%q) = %v; want %v", in, got, want)
		}
	}
}

// TestLogLevelFrom covers the --log-level/AT_LOG_LEVEL value mapper directly:
// the four recognized levels map through, and everything else (empty, "info",
// garbage) falls back to Info.
func TestLogLevelFrom(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":    slog.LevelDebug,
		"warn":     slog.LevelWarn,
		"error":    slog.LevelError,
		"info":     slog.LevelInfo,
		"":         slog.LevelInfo,
		"nonsense": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := logLevelFrom(in); got != want {
			t.Errorf("logLevelFrom(%q) = %v; want %v", in, got, want)
		}
	}
}

// TestEnvOr covers the flag-else-env fallback: a non-empty flag wins verbatim
// (env ignored), and an empty flag falls through to os.Getenv(key).
func TestEnvOr(t *testing.T) {
	t.Setenv("AT_TEST_ENVOR", "from-env")
	if got := envOr("from-flag", "AT_TEST_ENVOR"); got != "from-flag" {
		t.Errorf("envOr with non-empty flag = %q; want %q", got, "from-flag")
	}
	if got := envOr("", "AT_TEST_ENVOR"); got != "from-env" {
		t.Errorf("envOr with empty flag = %q; want %q (env fallback)", got, "from-env")
	}
	if got := envOr("", "AT_TEST_ENVOR_UNSET"); got != "" {
		t.Errorf("envOr with empty flag and unset env = %q; want %q", got, "")
	}
}

// TestWorkEmitsStructuredDiagnostic proves the operational work path routes its
// diagnostics through the structured logger (logging.UserError) rather than a
// bare Fprintf: in the test's non-TTY (unattended) mode the not-installed error
// must be a JSON record on stderr, carrying a step attr and the actionable
// message.
func TestWorkEmitsStructuredDiagnostic(t *testing.T) {
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(workerBearerKitConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	seedConfigDir(t) // no writeInstall — the kit is not installed
	inFile := filepath.Join(dir, "in.json")
	if err := os.WriteFile(inFile, []byte(implementTaskJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out.json")
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--project-dir", dir, "--in", inFile, "--out", outFile}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatal("work must fail fast when not installed")
	}
	s := errOut.String()
	if !strings.Contains(s, `"level":"ERROR"`) || !strings.Contains(s, `"step":`) {
		t.Fatalf("work diagnostic must be a structured record with a step attr; got %q", s)
	}
	if !strings.Contains(s, "at-cove install") {
		t.Fatalf("structured record must carry the actionable message; got %q", s)
	}
}

// TestDispatchEmitsStructuredDiagnostic is the doDispatch counterpart: the
// missing-scheduler-surface rejection must be a structured record (JSON on
// stderr in unattended mode) with a step attr, not a bare Fprintln.
func TestDispatchEmitsStructuredDiagnostic(t *testing.T) {
	dir := writeDispatchKit(t, "name: dispatch-kit\n")
	writeInstall(t, filepath.Join(dir, ".at-cove"))
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "--project-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1", code)
	}
	s := errOut.String()
	if !strings.Contains(s, `"level":"ERROR"`) || !strings.Contains(s, `"step":`) {
		t.Fatalf("dispatch diagnostic must be a structured record with a step attr; got %q", s)
	}
	if !strings.Contains(s, "must declare") {
		t.Fatalf("structured record must carry the missing-surface message; got %q", s)
	}
}

// TestDispatchStartHintMentionsCtrlC guards the restored scheduler-start hint:
// the "scheduler started" line must tell the operator how to stop it (Ctrl-C),
// a UX affordance dropped when the plain logger became structured.
func TestDispatchStartHintMentionsCtrlC(t *testing.T) {
	var errb bytes.Buffer
	lg, err := logging.New(logging.Options{Mode: logging.Unattended, Stderr: &errb, Level: slog.LevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	lg.Info(schedulerStartMsg)
	if !strings.Contains(errb.String(), "Ctrl-C") {
		t.Fatalf("scheduler-start message must mention Ctrl-C to stop; got %q", errb.String())
	}
}

// --- COV-71: per-collaborator interactive instances ---

// writeCollabKit writes a kit whose config.yml declares the given collaborator
// classes (a bare "prompt" each). The first entry is marked default:true when
// more than one is given, so SelectCollaborator has an unambiguous default.
func writeCollabKit(t *testing.T, dir string, classes ...string) string {
	t.Helper()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("name: box\ncollaborators:\n")
	for i, c := range classes {
		b.WriteString("  " + c + ":\n    prompt: \"be " + c + "\"\n")
		if i == 0 && len(classes) > 1 {
			b.WriteString("    default: true\n")
		}
	}
	if err := os.WriteFile(filepath.Join(cove, "config.yml"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return cove
}

// writeShareRepoKit writes a kit whose sole collaborator opts into share-repo-dir
// (COV-72): that class's VM shares the kit's repo dir instead of an isolated volume.
func writeShareRepoKit(t *testing.T, dir, class string) string {
	t.Helper()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\ncollaborators:\n  " + class + ":\n    prompt: \"be " + class + "\"\n    share-repo-dir: true\n"
	if err := os.WriteFile(filepath.Join(cove, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	return cove
}

// TestCreateKeysInstanceByCollaborator: `create <class>` runs a container and
// volumes keyed by atcove-<kit>-<class>, and records them in <class>.json —
// while the interactive state.json stays untouched.
func TestCreateKeysInstanceByCollaborator(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeCollabKit(t, dir, "steward", "planner")
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"create", "steward", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("create exit=%d stderr=%s", code, errOut.String())
	}
	for _, want := range []string{"atcove-box-steward", "atcove-box-steward-agent-data:/agent-data", "atcove-box-steward-workspace:/home/agent/workspace"} {
		if !dockerRunHasArg(t, f.Calls, want) {
			t.Fatalf("create must key the container/volumes by class; missing %q in run args %+v", want, f.Calls)
		}
	}
	st, err := state.LoadFor(kitDir, state.Instance("steward"))
	if err != nil {
		t.Fatalf("class-keyed state not written: %v", err)
	}
	if st.Container != "atcove-box-steward" || st.Name != "box" {
		t.Fatalf("state = %+v (want container atcove-box-steward, kit name box)", st)
	}
	if state.ExistsFor(kitDir, state.Interactive) {
		t.Fatal("a collaborator create must not write the interactive state.json")
	}
}

// TestCreateAmbiguousCollaboratorErrors: with several classes and no positional,
// resolution is ambiguous (no default) and create refuses, touching no docker.
func TestCreateAmbiguousCollaboratorErrors(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeCollabKit(t, dir, "steward")
	// Add a second class with no default so resolution is ambiguous.
	yml := "name: box\ncollaborators:\n  steward:\n    prompt: \"be steward\"\n  planner:\n    prompt: \"be planner\"\n"
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"create", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatalf("ambiguous create should refuse; stdout=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "multiple collaborators") {
		t.Fatalf("error must name the ambiguity; stderr=%q", errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("ambiguous create must not touch docker; calls=%+v", f.Calls)
	}
}

// TestCreateUnknownCollaboratorErrors: an explicit class the kit does not declare
// is rejected before any docker call.
func TestCreateUnknownCollaboratorErrors(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeCollabKit(t, dir, "steward")
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"create", "nope", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "no collaborator") {
		t.Fatalf("unknown collaborator should error; code=%d stderr=%q", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("unknown collaborator must not touch docker; calls=%+v", f.Calls)
	}
}

// TestCreateNoCollaboratorKeepsInteractiveInstance: a kit that defines no
// collaborators still uses the plain Interactive instance (state.json, container
// atcove-<kit>, atcove-<kit> volume names) — the plain path is preserved exactly.
func TestCreateNoCollaboratorKeepsInteractiveInstance(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"create", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("create exit=%d stderr=%s", code, errOut.String())
	}
	for _, want := range []string{"atcove-box", "atcove-box-agent-data:/agent-data", "atcove-box-workspace:/home/agent/workspace"} {
		if !dockerRunHasArg(t, f.Calls, want) {
			t.Fatalf("plain create must use the atcove-<kit> names; missing %q in run args", want)
		}
	}
	if !state.ExistsFor(kitDir, state.Interactive) {
		t.Fatal("plain create must write the interactive state.json")
	}
}

// TestCreateEachCollaboratorIsIndependentVM: two classes create two independent
// instances (own state files, own containers/volumes) side by side.
func TestCreateEachCollaboratorIsIndependentVM(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeCollabKit(t, dir, "steward", "planner")
	writeInstall(t, kitDir)
	for _, c := range []string{"steward", "planner"} {
		f := &runner.Fake{}
		var out, errOut bytes.Buffer
		if code := run([]string{"create", c, "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
			t.Fatalf("create %s exit=%d stderr=%s", c, code, errOut.String())
		}
	}
	for _, c := range []string{"steward", "planner"} {
		st, err := state.LoadFor(kitDir, state.Instance(c))
		if err != nil {
			t.Fatalf("instance %s state missing: %v", c, err)
		}
		if st.Container != "atcove-box-"+c {
			t.Fatalf("instance %s container = %q, want atcove-box-%s", c, st.Container, c)
		}
	}
	// Creating steward again is refused (that instance exists), independently of planner.
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"create", "steward", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "already created") {
		t.Fatalf("second create of steward should refuse; code=%d stderr=%q", code, errOut.String())
	}
}

// TestRecreateKeyedByCollaborator: recreate <class> destroys and re-runs that
// class's container, keeping volumes — leaving other instances alone.
func TestRecreateKeyedByCollaborator(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeCollabKit(t, dir, "steward", "planner")
	writeStateFor(t, kitDir, state.Instance("steward"), "box", "atcove-box-steward")
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"recreate", "steward", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("recreate exit=%d stderr=%s", code, errOut.String())
	}
	rmIdx := dockerArg0Index(f.Calls, "rm")
	if rmIdx == -1 || !contains(f.Calls[rmIdx].Args, "atcove-box-steward") {
		t.Fatalf("recreate must rm the class-keyed container; calls=%+v", f.Calls)
	}
	if !dockerRunHasArg(t, f.Calls, "atcove-box-steward") {
		t.Fatalf("recreate must re-run the class-keyed container; calls=%+v", f.Calls)
	}
	// Volumes kept (saved login survives).
	if dockerArg0Index(f.Calls, "volume") != -1 {
		t.Fatalf("recreate must keep volumes; calls=%+v", f.Calls)
	}
}

// TestDestroyKeyedByCollaborator: destroy <class> tears down only that instance's
// container + volumes and deletes only that class's state file.
func TestDestroyKeyedByCollaborator(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeCollabKit(t, dir, "steward", "planner")
	writeStateFor(t, kitDir, state.Instance("steward"), "box", "box-steward")
	writeStateFor(t, kitDir, state.Instance("planner"), "box", "box-planner")
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"destroy", "steward", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("destroy exit=%d stderr=%s", code, errOut.String())
	}
	rmIdx := dockerArg0Index(f.Calls, "rm")
	if rmIdx == -1 || !contains(f.Calls[rmIdx].Args, "box-steward") {
		t.Fatalf("destroy must rm box-steward; calls=%+v", f.Calls)
	}
	volIdx := dockerArg0Index(f.Calls, "volume")
	if volIdx == -1 || !contains(f.Calls[volIdx].Args, "box-steward-state") {
		t.Fatalf("destroy must purge box-steward volumes; calls=%+v", f.Calls)
	}
	if state.ExistsFor(kitDir, state.Instance("steward")) {
		t.Fatal("destroy must delete the steward state file")
	}
	if !state.ExistsFor(kitDir, state.Instance("planner")) {
		t.Fatal("destroy of one instance must not touch the other")
	}
}

// TestDestroyAllRemovesEveryInstance: destroy --all tears down every instance of
// the kit (enumerated from state files), leaving none behind.
func TestDestroyAllRemovesEveryInstance(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeCollabKit(t, dir, "steward", "planner")
	writeStateFor(t, kitDir, state.Instance("steward"), "box", "box-steward")
	writeStateFor(t, kitDir, state.Instance("planner"), "box", "box-planner")
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"destroy", "--all", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("destroy --all exit=%d stderr=%s", code, errOut.String())
	}
	// Both containers removed.
	var rmed []string
	for _, c := range f.Calls {
		if c.Name == "docker" && contains(c.Args, "rm") {
			for _, a := range c.Args {
				if a == "box-steward" || a == "box-planner" {
					rmed = append(rmed, a)
				}
			}
		}
	}
	if len(rmed) != 2 {
		t.Fatalf("destroy --all must rm both containers; rmed=%v calls=%+v", rmed, f.Calls)
	}
	if got, _ := state.List(kitDir); len(got) != 0 {
		t.Fatalf("destroy --all must delete every state file; remaining=%v", got)
	}
}

// TestStatusListsAllInstances: status with no positional lists every instance of
// the kit, naming each class, its running state, and its container.
func TestStatusListsAllInstances(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeCollabKit(t, dir, "steward", "planner")
	writeStateFor(t, kitDir, state.Instance("steward"), "box", "box-steward")
	writeStateFor(t, kitDir, state.Instance("planner"), "box", "box-planner")
	writeInstall(t, kitDir)
	// Two instances -> two GetStatus Output calls (preflight is a Probe).
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}, {Stdout: "false\n"}}}
	var out, errOut bytes.Buffer
	if code := run([]string{"status", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("status exit=%d stderr=%s", code, errOut.String())
	}
	s := out.String()
	for _, want := range []string{"planner", "steward", "box-planner", "box-steward"} {
		if !strings.Contains(s, want) {
			t.Fatalf("status list must mention %q; got %q", want, s)
		}
	}
	// planner sorts first (true) -> running; steward -> stopped.
	if !strings.Contains(s, "running") || !strings.Contains(s, "stopped") {
		t.Fatalf("status list must report each instance's run state; got %q", s)
	}
}

// TestStatusSingleCollaborator: status <class> shows just that instance.
func TestStatusSingleCollaborator(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeCollabKit(t, dir, "steward", "planner")
	writeStateFor(t, kitDir, state.Instance("steward"), "box", "box-steward")
	writeInstall(t, kitDir)
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}}}
	var out, errOut bytes.Buffer
	if code := run([]string{"status", "steward", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("status steward exit=%d stderr=%s", code, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "steward") || !strings.Contains(s, "running") {
		t.Fatalf("status steward must show that one instance running; got %q", s)
	}
	if strings.Contains(s, "planner") {
		t.Fatalf("status steward must not list other instances; got %q", s)
	}
}

// TestDestroyToleratesStaleInstall: a stale install must not block tearing down
// an instance — destroy resolves the collaborator leniently and still removes it.
func TestDestroyToleratesStaleInstall(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeCollabKit(t, dir, "steward")
	writeStateFor(t, kitDir, state.Instance("steward"), "box", "box-steward")
	writeInstall(t, kitDir)
	// Drift the kit source so the install is now stale.
	yml := "name: box\ncollaborators:\n  steward:\n    prompt: \"be steward\"\n# changed\n"
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"destroy", "steward", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("destroy on stale install should still work; exit=%d stderr=%s", code, errOut.String())
	}
	if state.ExistsFor(kitDir, state.Instance("steward")) {
		t.Fatal("destroy must delete the state file even with a stale install")
	}
}

// TestUninstallRefusesWhileCollaboratorInstanceExists: a per-collaborator
// instance holds the image too, so uninstall must refuse while any <class>.json
// remains — not just the interactive state.json (COV-71).
func TestUninstallRefusesWhileCollaboratorInstanceExists(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeCollabKit(t, dir, "steward")
	writeInstall(t, kitDir)
	writeStateFor(t, kitDir, state.Instance("steward"), "box", "box-steward") // a class instance, no state.json
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"uninstall", "--project-dir", dir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "at-cove destroy") {
		t.Fatalf("uninstall must refuse while a collaborator instance exists; code=%d stderr=%q", code, errOut.String())
	}
	if dockerArg0Index(f.Calls, "rmi") != -1 {
		t.Fatalf("uninstall must not rmi while an instance exists; calls=%+v", f.Calls)
	}
	if !install.Exists(kitDir) {
		t.Fatal("uninstall must keep install.json while an instance exists")
	}
}

// contains reports whether s is in ss.
func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestVertexPlan_ResolvesADCAndEnv(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.yml")
	localPath := filepath.Join(dir, "secrets.local.yml")
	if err := os.WriteFile(secretsPath, []byte(`
kits:
  vkit:
    GOOGLE_APPLICATION_CREDENTIALS_JSON:
      value: '{"type":"authorized_user","refresh_token":"r"}'
`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		t.Fatalf("usersecret.Load: %v", err)
	}
	cfg, err := kit.ParseConfig([]byte(`
name: vkit
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: p
      CLOUD_ML_REGION: us-east5
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	r := &runner.Fake{}
	expand := mint.Expander(r, store.Global, "")
	va, env, err := vertexPlan(cfg, store, expand, "vkit", "/canon/vkit", secretsPath, r)
	if err != nil {
		t.Fatalf("vertexPlan: %v", err)
	}
	if !strings.Contains(string(va.ADC), "authorized_user") {
		t.Fatalf("ADC not resolved: %q", va.ADC)
	}
	if env["CLAUDE_CODE_USE_VERTEX"] != "1" || env["ANTHROPIC_VERTEX_PROJECT_ID"] != "p" {
		t.Fatalf("vertex env wrong: %v", env)
	}
}

func TestVertexPlan_FailsClosedWhenUnsupplied(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.yml")
	localPath := filepath.Join(dir, "secrets.local.yml")
	if err := os.WriteFile(secretsPath, []byte("kits: {}\n"), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		t.Fatalf("usersecret.Load: %v", err)
	}
	cfg, _ := kit.ParseConfig([]byte("name: vkit\nmodel-provider:\n  vertex:\n    env:\n      ANTHROPIC_VERTEX_PROJECT_ID: p\n      CLOUD_ML_REGION: us\n"))
	r := &runner.Fake{}
	expand := mint.Expander(r, store.Global, "")
	if _, _, err := vertexPlan(cfg, store, expand, "vkit", "/canon/vkit", secretsPath, r); err == nil {
		t.Fatalf("want a fail-closed error when the ADC is unsupplied")
	}
}
