package sshargs

import (
	"reflect"
	"testing"
)

func target() Target {
	return Target{Host: "127.0.0.1", User: "agent", Port: 49153,
		IdentityFile: "/k/id", KnownHostsFile: "/k/kh"}
}

func TestBase(t *testing.T) {
	got := Base(target())
	want := []string{
		"-i", "/k/id",
		"-p", "49153",
		"-o", "UserKnownHostsFile=/k/kh",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "IdentitiesOnly=yes",
		"agent@127.0.0.1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Base = %v\nwant %v", got, want)
	}
}

func TestInteractive(t *testing.T) {
	got := Interactive(target(), "claude auth login")
	want := []string{
		"-tt",
		"-i", "/k/id",
		"-p", "49153",
		"-o", "UserKnownHostsFile=/k/kh",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "IdentitiesOnly=yes",
		"agent@127.0.0.1",
		"claude auth login",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Interactive = %v\nwant %v", got, want)
	}
}

func TestInteractiveSendEnv(t *testing.T) {
	got := InteractiveSendEnv(target(), []string{"GITHUB_TOKEN", "X"}, "exec claude")
	want := []string{
		"-tt",
		"-o", "SendEnv=GITHUB_TOKEN",
		"-o", "SendEnv=X",
		"-i", "/k/id",
		"-p", "49153",
		"-o", "UserKnownHostsFile=/k/kh",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "IdentitiesOnly=yes",
		"agent@127.0.0.1",
		"exec claude",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InteractiveSendEnv = %v\nwant %v", got, want)
	}
}
