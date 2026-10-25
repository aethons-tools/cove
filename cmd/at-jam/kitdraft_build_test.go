package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
)

// cliDraftBuilder's build writes log, waits for release (or its ctx), and
// returns err or sha256:img.
type cliDraftBuilder struct {
	log     string
	err     error
	release chan struct{}
}

func (f *cliDraftBuilder) BuildKitDraft(ctx context.Context, _ jam.KitDefinition, _ string, log io.Writer) (string, error) {
	_, _ = io.WriteString(log, f.log)
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if f.err != nil {
		return "", f.err
	}
	return "sha256:img", nil
}
func (f *cliDraftBuilder) HasKitDraftImage(string) (bool, error) { return true, nil }

// draftBuildServer is a Jam holding kit app v1 with an open draft and test
// builds over fb (wired once, on the supervisor); it returns its URL.
func draftBuildServer(t *testing.T, fb *cliDraftBuilder) (string, jam.KitDraft) {
	t.Helper()
	store := jam.NewMemStore()
	if _, err := store.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	d, err := jam.OpenKitDraft(store, "app", 0, "op", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.DiscardHandler)
	sup := jam.NewSupervisor(store, nil, "holder-cli-build", time.Minute, time.Minute, time.Now, log)
	sup.SetKitDraftBuilds(jam.NewKitDraftBuilds(ctx, store, fb, log))
	ts := httptest.NewServer(jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil))
	t.Cleanup(ts.Close)
	return ts.URL, d
}

// draftBuildCLI runs `at-jam kit draft build …` against draftBuildServer,
// polling every millisecond.
func draftBuildCLI(t *testing.T, fb *cliDraftBuilder) (func(args ...string) (int, string, string), jam.KitDraft) {
	t.Helper()
	old := draftBuildPoll
	draftBuildPoll = time.Millisecond
	t.Cleanup(func() { draftBuildPoll = old })
	url, d := draftBuildServer(t, fb)
	return func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		argv := append([]string{"kit", "draft", "build", "--admin-url", url}, args...)
		code := run(argv, func(string) string { return "" }, &out, &errb)
		return code, out.String(), errb.String()
	}, d
}

// Review Focus 5: the log is followed to the end and printed escaped.
func TestKitDraftBuildFollowsToSuccess(t *testing.T) {
	kit, d := draftBuildCLI(t, &cliDraftBuilder{log: "#1 step\n#2 \x1b[31mred\x1b[0m\n"})
	code, out, errs := kit("app")
	tree := d.Tree[:12]
	for _, want := range []string{
		"building draft app @" + tree + " (build kdb_",
		"#1 step\n",
		`#2 \x1b[31mred\x1b[0m` + "\n",
		"built draft app @" + tree + ": cove-kit-draft:app-" + tree + " (digest sha256:img)\n",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("exit %d lacks %q:\n%s\nstderr: %s", code, want, out, errs)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Fatal("a raw escape reached the terminal")
	}
}

func TestKitDraftBuildFailureExits1(t *testing.T) {
	kit, d := draftBuildCLI(t, &cliDraftBuilder{err: errors.New("RUN make: exit 2")})
	code, _, errs := kit("app")
	if code != 1 || !strings.Contains(errs, "at-jam: draft build of app @"+d.Tree[:12]+" failed: RUN make: exit 2\n") {
		t.Fatalf("exit %d stderr %q", code, errs)
	}
}

func TestKitDraftBuildNoFollowShowCancel(t *testing.T) {
	fb := &cliDraftBuilder{release: make(chan struct{})}
	kit, _ := draftBuildCLI(t, fb)
	if code, out, _ := kit("app", "--no-follow"); code != 0 || !strings.HasPrefix(out, "building draft app @") {
		t.Fatalf("--no-follow: %d %q", code, out)
	}
	if code, _, errs := kit("app", "--no-follow"); code != 1 || !strings.Contains(errs, "kit app already has a draft build running") {
		t.Fatalf("second start: %d %q", code, errs)
	}
	if code, out, _ := kit("app", "--show"); code != 0 || !strings.Contains(out, "running") {
		t.Fatalf("--show while running: %d %q", code, out)
	}
	if code, out, _ := kit("app", "--cancel"); code != 0 || !strings.HasPrefix(out, "cancelled draft build kdb_") {
		t.Fatalf("--cancel: %d %q", code, out)
	}
	if code, _, errs := kit("app", "--show"); code != 1 || !strings.Contains(errs, "failed: cancelled") {
		t.Fatalf("--show after cancel: %d %q", code, errs)
	}
	if code, _, errs := kit("app", "--etag", strings.Repeat("0", 64)); code != 1 || !strings.Contains(errs, "draft app changed since you loaded it (reload)") {
		t.Fatalf("stale --etag: %d %q", code, errs)
	}
	if code, _, errs := kit("app", "--show", "--cancel"); code != 2 || !strings.Contains(errs, "--show and --cancel cannot be combined") {
		t.Fatalf("--show --cancel: %d %q", code, errs)
	}
}

// Ctrl-C while following stops the follow, not the build: the build is
// still running afterwards, and the CLI says how to cancel it.
func TestKitDraftBuildInterruptLeavesBuildRunning(t *testing.T) {
	fb := &cliDraftBuilder{log: "#1 step\n", release: make(chan struct{})}
	url, _ := draftBuildServer(t, fb)
	c := adminclient.New(url, "")
	ctx, interrupt := context.WithCancel(context.Background())
	var out, errb bytes.Buffer
	b, err := c.StartKitDraftBuild("app", mustETag(t, c))
	if err != nil {
		t.Fatal(err)
	}
	// The first poll prints the log, then the user hits Ctrl-C.
	polls := 0
	code := followDraftBuild(ctx, c, "app", "app", b.ID, &out, &errb, func(time.Duration) <-chan time.Time {
		if polls++; polls == 2 {
			interrupt()
		}
		return time.After(time.Millisecond)
	})
	if code != 130 || !strings.Contains(errb.String(), "stopped following build kdb_") || !strings.Contains(errb.String(), "still running") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	if b, err := c.KitDraftBuildStatus("app", 0); err != nil || b.State != jam.DraftBuildRunning {
		t.Fatalf("build after interrupt = %+v %v", b, err)
	}
}

// A server string is escaped wherever it is printed, the error included.
func TestBuildResultEscapes(t *testing.T) {
	var out, errb bytes.Buffer
	b := jam.KitDraftBuild{State: jam.DraftBuildFailed, Tree: "abc", Error: "boom \x1b[31m"}
	if code := buildResult(&out, &errb, "app", b); code != 1 || strings.Contains(errb.String(), "\x1b") || !strings.Contains(errb.String(), `\x1b[31m`) {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	out.Reset()
	b = jam.KitDraftBuild{State: jam.DraftBuildSucceeded, Tree: "abc", Tag: "t\x1b", Digest: "d\x1b", DraftETag: "e\x1b"}
	if buildResult(&out, &errb, "app", b); strings.Contains(out.String(), "\x1b") {
		t.Fatalf("stdout %q", out.String())
	}
}

func mustETag(t *testing.T, c *adminclient.Client) string {
	t.Helper()
	st, err := c.KitDraftStatus("app")
	if err != nil {
		t.Fatal(err)
	}
	return st.ETag
}

func TestStudioRaiseKitDraft(t *testing.T) {
	store := jam.NewMemStore()
	if err := store.PutRole("default", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	d, err := jam.OpenKitDraft(store, "app", 0, "op", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, log)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sup.SetKitDraftBuilds(jam.NewKitDraftBuilds(ctx, store, &cliDraftBuilder{}, log))
	ts := httptest.NewServer(jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil))
	t.Cleanup(ts.Close)
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	if code := run([]string{"studio", "raise", "--admin-url", ts.URL, "--id", "try", "--role", "guest", "--kit-draft", "app"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("raise: %d %s", code, errb.String())
	}
	want := regexp.MustCompile(`^raised try as session ses_[0-9a-z]{26} \(phase=live\) from the draft of app @` + d.Tree[:12] + `\n$`)
	if !want.MatchString(out.String()) {
		t.Fatalf("raise output = %q", out.String())
	}
	out.Reset()
	if code := run([]string{"studio", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 || !strings.Contains(out.String(), "\timage=draft\n") {
		t.Fatalf("list: %d %q", code, out.String())
	}
}

// A raise's server error is escaped: draft problems quote kit.yml values.
func TestStudioRaiseEscapesTheServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "raise: draft studio: kit.yml: base \"a\x1b[2Jb\" unknown", http.StatusBadRequest)
	}))
	t.Cleanup(ts.Close)
	var out, errb bytes.Buffer
	code := run([]string{"studio", "raise", "--admin-url", ts.URL, "--id", "try", "--role", "guest", "--kit-draft", "app"}, func(string) string { return "" }, &out, &errb)
	if code != 1 || strings.Contains(errb.String(), "\x1b") || !strings.Contains(errb.String(), `a\x1b[2Jb`) {
		t.Fatalf("raise: %d %q", code, errb.String())
	}
}
