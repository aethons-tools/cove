package colima

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

// A logged builder builds exactly as the plain one (same pinned argv, kit
// build args, --progress=plain) and copies the build's output to the log.
func TestWithBuildLogStreamsTheBuildAndKeepsItsArgv(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{}, // docker info (preflight)
		{Stdout: "#1 [1/2] FROM base\n#2 DONE\n"}, // docker build --progress=plain …
		{Stdout: "sha256:img\n"},                  // docker inspect --format {{.Id}} <tag>
	}}
	var log bytes.Buffer
	b := NewWithContext(f, "colima").WithBuildLog(context.Background(), &log)
	id, err := b.BuildKitImage(t.TempDir(), "jam-kit-draft:app-0123456789ab", "base@sha256:x", map[string]string{"A": "1"}, false)
	if err != nil || id != "sha256:img" {
		t.Fatalf("BuildKitImage = %q, %v", id, err)
	}
	if !strings.Contains(log.String(), "#1 [1/2] FROM base\n#2 DONE\n") {
		t.Fatalf("log = %q", log.String())
	}
	build := dockerCall(f.Calls, "build")
	if !contains(build, "--progress=plain") || !contains(build, "A=1") || !contains(build, "BASE=base@sha256:x") || !contains(build, "jam-kit-draft:app-0123456789ab") {
		t.Fatalf("build argv = %v", build)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
	}
}

// A cancelled context stops the build before docker build runs.
func TestWithBuildLogCancelledStopsBeforeBuilding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &runner.Fake{}
	if _, err := NewWithContext(f, "").WithBuildLog(ctx, io.Discard).BuildKitImage(t.TempDir(), "t", "b", nil, false); err == nil {
		t.Fatal("a cancelled build must fail")
	}
	if dockerCall(f.Calls, "build") != nil {
		t.Fatalf("a cancelled build ran docker build: %+v", f.Calls)
	}
}

// A cache-miss check of a logged builder keeps docker's "No such image"
// out of the build log (F12), and still answers a miss.
func TestWithBuildLogCacheMissIsNotLogged(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stderr: "Error: No such image: jam-kit-base:x\n", Err: &runner.ExitError{Code: 1}},  // docker image inspect (HasKitBase)
		{Stderr: "Error: No such image: jam-kit-draft:x\n", Err: &runner.ExitError{Code: 1}}, // docker image inspect (HasKitImage)
	}}
	var log bytes.Buffer
	b := NewWithContext(f, "").WithBuildLog(context.Background(), &log).(*Colima)
	if ok, err := b.HasKitBase("jam-kit-base:x"); ok || err != nil {
		t.Fatalf("HasKitBase = %v, %v", ok, err)
	}
	if ok, err := b.HasKitImage("jam-kit-draft:x"); ok || err != nil {
		t.Fatalf("HasKitImage = %v, %v", ok, err)
	}
	if log.Len() != 0 {
		t.Fatalf("a cache miss reached the build log: %q", log.String())
	}
	if dockerCall(f.Calls, "image") == nil || !allPinned(f.Calls) {
		t.Fatalf("calls = %+v", f.Calls)
	}
}

// A logged builder's cache-miss check runs under the build's context too.
func TestWithBuildLogCacheCheckRunsUnderTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	held := false
	f := &runner.Fake{OnRun: func(ctx context.Context, c runner.Call) error {
		held = true
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}}
	if ok, err := NewWithContext(f, "").WithBuildLog(ctx, io.Discard).HasKitImage("t"); ok || err != nil || !held {
		t.Fatalf("HasKitImage = %v, %v (held under ctx: %v)", ok, err, held)
	}
}

// The base's local inspect before a pull is a cache-miss check too: its
// "No such object" stays out of the log, the pull and the retry do not.
func TestWithBuildLogBaseInspectMissIsNotLogged(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stderr: "Error: No such object: base:1\n", Err: &runner.ExitError{Code: 1}}, // docker inspect (miss)
		{Stdout: "pulled\n"}, // docker pull
		{Stdout: `["sha256:l1"]`, Stderr: "retry-stderr\n"}, // docker inspect (retry)
	}}
	var log bytes.Buffer
	b := NewWithContext(f, "").WithBuildLog(context.Background(), &log).(*Colima)
	layers, err := dockerImg{c: b}.Layers("base:1")
	if err != nil || len(layers) != 1 {
		t.Fatalf("Layers = %v, %v", layers, err)
	}
	if got := log.String(); got != "pulled\nretry-stderr\n" {
		t.Fatalf("log = %q", got)
	}
}
