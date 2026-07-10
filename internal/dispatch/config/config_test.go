package config

import (
	"os"
	"path/filepath"
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
classes:
  implement: { mode: autonomous, kit: ./kits/implement, timeout: 30m, concurrency: 2 }
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
	impl := cfg.Classes["implement"]
	if impl.Mode != "autonomous" || impl.Kit != "./kits/implement" || impl.Timeout != "30m" || impl.Concurrency != 2 {
		t.Errorf("implement class parsed wrong: %+v", impl)
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
	_, err := ParseConfig([]byte("bogus: 1\n"))
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
		{"autonomous without kit", strings.Replace(validYAML,
			`implement: { mode: autonomous, kit: ./kits/implement, timeout: 30m, concurrency: 2 }`,
			`implement: { mode: autonomous, timeout: 30m }`, 1), "kit"},
		{"interactive with kit", strings.Replace(validYAML,
			`spec:      { mode: interactive }`,
			`spec:      { mode: interactive, kit: ./kits/spec }`, 1), "kit"},
		{"bad mode", strings.Replace(validYAML,
			`spec:      { mode: interactive }`, `spec:      { mode: sideways }`, 1), "mode"},
		{"global concurrency zero", strings.Replace(validYAML, "concurrency: 4", "concurrency: 0", 1), "concurrency"},
		{"empty token command", strings.Replace(validYAML, `token:          { command: ["op","read","op://work/linear-token"] }`, `token:          { command: [] }`, 1), "token"},
		{"empty webhook-secret command", strings.Replace(validYAML, `webhook-secret: { command: ["op","read","op://work/linear-webhook"] }`, `webhook-secret: { command: [] }`, 1), "webhook-secret"},
		{"bad reaper-timeout", strings.Replace(validYAML, "reaper-timeout: 45m", "reaper-timeout: soon", 1), "reaper-timeout"},
		{"bad class timeout", strings.Replace(validYAML, "timeout: 30m", "timeout: soon", 1), "timeout"},
		{"negative class concurrency", strings.Replace(validYAML, "concurrency: 2", "concurrency: -1", 1), "concurrency"},
		{"classes empty", strings.Replace(validYAML, "classes:\n  implement: { mode: autonomous, kit: ./kits/implement, timeout: 30m, concurrency: 2 }\n  spec:      { mode: interactive }", "classes: {}", 1), "class"},
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

func TestLoadConfigResolvesKitAndDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "at-dispatch.yml")
	yaml := `
tracker:
  provider: linear
  team: T
  token: {command: ["echo","t"]}
  webhook-secret: {command: ["echo","w"]}
  poll-interval: 30s
  states: {ready: R, in-progress: IP, in-review: IR, done: D, needs-input: NI, blocked: B}
concurrency: 2
reaper-timeout: 1h
classes:
  implement:
    mode: autonomous
    kit: ./kits/worker
    timeout: 30m
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DispatchOverhead != "15m" {
		t.Errorf("DispatchOverhead default = %q; want 15m", cfg.DispatchOverhead)
	}
	want := filepath.Join(dir, "kits/worker")
	if got := cfg.Classes["implement"].Kit; got != want {
		t.Errorf("Kit = %q; want absolute %q", got, want)
	}
}
