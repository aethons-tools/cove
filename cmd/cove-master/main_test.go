package main

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
)

func TestBuildConfigRequiresEnv(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if _, err := buildConfig(get); err == nil {
		t.Fatal("expected error when AT_HARBOR_RUNTIME_ADDR is missing")
	}
	env["AT_HARBOR_RUNTIME_ADDR"] = "harbor:9090"
	if _, err := buildConfig(get); err == nil {
		t.Fatal("expected error when AT_HARBOR_IDENTITY_TOKEN is missing")
	}
	env["AT_HARBOR_IDENTITY_TOKEN"] = "tok"
	if _, err := buildConfig(get); err == nil {
		t.Fatal("expected error when AT_HARBOR_LAUNCH_SECRET is missing")
	}
	env["AT_HARBOR_LAUNCH_SECRET"] = "sec"
	cfg, err := buildConfig(get)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "harbor:9090" || cfg.Token != "tok" || cfg.LaunchSecret != "sec" {
		t.Fatalf("config = %+v", cfg)
	}
	if len(cfg.DialOptions) == 0 {
		t.Fatal("expected transport credentials in DialOptions")
	}
}

func TestStubWorkloadRunReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := stubWorkload{}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, noopHandle{}) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stub Run did not return on ctx cancel")
	}
	w.Control(covemaster.Control{Kind: covemaster.Teardown}) // must not panic
}

type noopHandle struct{}

func (noopHandle) Report(covemaster.Activity) {}
