package config

import "testing"

// validYAML is a complete, valid config used across tests.
const validYAML = `
tracker:
  provider: linear
  team: AET
  token:          { command: ["op","read","op://work/linear-token"] }
  webhook-secret: { command: ["op","read","op://work/linear-webhook"] }
  poll-interval: 60s
  states:
    ready: Todo
    in-progress: In Progress
    in-review: In Review
    done: Done
    needs-input: Needs Input
    blocked: Backlog
repo:
  slug: aethons-tools/cove
secrets:
  - name: SOME_TOKEN
    command: ["op","read","op://work/x"]
classes:
  implement: { mode: autonomous, command: ["./dispatch/implement.sh"], timeout: 30m, concurrency: 2 }
  spec:      { mode: interactive }
concurrency: 4
reaper-timeout: 45m
`

func TestParseConfigValid(t *testing.T) {
	cfg, err := ParseConfig([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseConfig: unexpected error: %v", err)
	}
	if cfg.Tracker.Team != "AET" {
		t.Errorf("Team = %q; want AET", cfg.Tracker.Team)
	}
	if cfg.Tracker.States.Ready != "Todo" {
		t.Errorf("States.Ready = %q; want Todo", cfg.Tracker.States.Ready)
	}
	if cfg.Repo.Slug != "aethons-tools/cove" {
		t.Errorf("Repo.Slug = %q", cfg.Repo.Slug)
	}
	impl := cfg.Classes["implement"]
	if impl.Mode != "autonomous" || len(impl.Command) != 1 || impl.Timeout != "30m" || impl.Concurrency != 2 {
		t.Errorf("implement class parsed wrong: %+v", impl)
	}
	if len(cfg.Secrets) != 1 || cfg.Secrets[0].Name != "SOME_TOKEN" {
		t.Errorf("secrets parsed wrong: %+v", cfg.Secrets)
	}
	if cfg.Concurrency != 4 {
		t.Errorf("Concurrency = %d; want 4", cfg.Concurrency)
	}
}

func TestParseConfigDefaultsClassLabelPrefix(t *testing.T) {
	cfg, err := ParseConfig([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Tracker.ClassLabelPrefix != "class:" {
		t.Errorf("ClassLabelPrefix = %q; want default class:", cfg.Tracker.ClassLabelPrefix)
	}
}

func TestParseConfigRejectsUnknownKey(t *testing.T) {
	_, err := ParseConfig([]byte("repo:\n  slug: a/b\nbogus: 1\n"))
	if err == nil {
		t.Fatal("ParseConfig: expected error for unknown key, got nil")
	}
}
