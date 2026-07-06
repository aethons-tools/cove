package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBuildEnv(t *testing.T) {
	task := Task{
		Issue: "AET-42", Class: "implement", Repo: "aethons-tools/cove",
		Timeout: "30m", BriefPath: "/tmp/brief.md", ResultPath: "/tmp/result.json",
	}
	got := BuildEnv(task, map[string]string{"B_TOKEN": "b", "A_TOKEN": "a"})
	want := []string{
		"DISPATCH_ISSUE=AET-42",
		"DISPATCH_CLASS=implement",
		"DISPATCH_REPO=aethons-tools/cove",
		"DISPATCH_TIMEOUT=30m",
		"DISPATCH_BRIEF=/tmp/brief.md",
		"DISPATCH_RESULT=/tmp/result.json",
		"A_TOKEN=a", // secrets sorted for determinism
		"B_TOKEN=b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildEnv =\n%v\nwant\n%v", got, want)
	}
}

func TestResolveSecrets(t *testing.T) {
	secrets := []Secret{{Name: "TOK", Command: []string{"echo", "x"}}}
	resolve := func(cmd []string) (string, error) {
		if len(cmd) == 2 && cmd[0] == "echo" {
			return "resolved", nil
		}
		return "", errors.New("unexpected cmd")
	}
	got, err := ResolveSecrets(secrets, resolve)
	if err != nil {
		t.Fatalf("ResolveSecrets: %v", err)
	}
	if got["TOK"] != "resolved" {
		t.Fatalf("TOK = %q; want resolved", got["TOK"])
	}
}

func TestResolveSecretsPropagatesError(t *testing.T) {
	secrets := []Secret{{Name: "TOK", Command: []string{"false"}}}
	resolve := func([]string) (string, error) { return "", errors.New("boom") }
	_, err := ResolveSecrets(secrets, resolve)
	if err == nil {
		t.Fatal("expected an error from ResolveSecrets")
	}
	if !strings.Contains(err.Error(), "TOK") {
		t.Fatalf("error %q should name the secret TOK", err.Error())
	}
}
