package config

import (
	"strings"
	"testing"
)

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

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string // substring expected in the error
	}{
		{"bad provider", strings.Replace(validYAML, "provider: linear", "provider: jira", 1), "provider"},
		{"missing team", strings.Replace(validYAML, "  team: AET\n", "", 1), "team"},
		{"missing state role", strings.Replace(validYAML, "    blocked: Backlog\n", "", 1), "states.blocked"},
		{"bad poll duration", strings.Replace(validYAML, "poll-interval: 60s", "poll-interval: soon", 1), "poll-interval"},
		{"bad repo slug", strings.Replace(validYAML, "slug: aethons-tools/cove", "slug: cove", 1), "repo.slug"},
		{"autonomous without command", strings.Replace(validYAML,
			`implement: { mode: autonomous, command: ["./dispatch/implement.sh"], timeout: 30m, concurrency: 2 }`,
			`implement: { mode: autonomous, timeout: 30m }`, 1), "command"},
		{"interactive with command", strings.Replace(validYAML,
			`spec:      { mode: interactive }`,
			`spec:      { mode: interactive, command: ["x"] }`, 1), "command"},
		{"bad mode", strings.Replace(validYAML,
			`spec:      { mode: interactive }`, `spec:      { mode: sideways }`, 1), "mode"},
		{"reserved secret name", strings.Replace(validYAML, "name: SOME_TOKEN", "name: DISPATCH_ISSUE", 1), "reserved"},
		{"global concurrency zero", strings.Replace(validYAML, "concurrency: 4", "concurrency: 0", 1), "concurrency"},
		{"multi-segment slug", strings.Replace(validYAML, "slug: aethons-tools/cove", "slug: a/b/c", 1), "repo.slug"},
		{"empty token command", strings.Replace(validYAML, `token:          { command: ["op","read","op://work/linear-token"] }`, `token:          { command: [] }`, 1), "token"},
		{"empty webhook-secret command", strings.Replace(validYAML, `webhook-secret: { command: ["op","read","op://work/linear-webhook"] }`, `webhook-secret: { command: [] }`, 1), "webhook-secret"},
		{"bad reaper-timeout", strings.Replace(validYAML, "reaper-timeout: 45m", "reaper-timeout: soon", 1), "reaper-timeout"},
		{"bad class timeout", strings.Replace(validYAML, "timeout: 30m", "timeout: soon", 1), "timeout"},
		{"negative class concurrency", strings.Replace(validYAML, "concurrency: 2", "concurrency: -1", 1), "concurrency"},
		{"empty secret name", strings.Replace(validYAML, "name: SOME_TOKEN", `name: ""`, 1), "name"},
		{"empty secret command", strings.Replace(validYAML, "- name: SOME_TOKEN\n    command: [\"op\",\"read\",\"op://work/x\"]", "- name: SOME_TOKEN\n    command: []", 1), "command"},
		{"duplicate secret name", strings.Replace(validYAML, "secrets:\n  - name: SOME_TOKEN\n    command: [\"op\",\"read\",\"op://work/x\"]", "secrets:\n  - name: DUP\n    command: [\"x\"]\n  - name: DUP\n    command: [\"y\"]", 1), "duplicated"},
		{"classes empty", strings.Replace(validYAML, "classes:\n  implement: { mode: autonomous, command: [\"./dispatch/implement.sh\"], timeout: 30m, concurrency: 2 }\n  spec:      { mode: interactive }", "classes: {}", 1), "class"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}
