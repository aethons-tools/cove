package kit

import (
	"os"
	"strings"
	"testing"
)

// The reference worker kit must be a valid, dispatch-ready kit.
func TestReferenceWorkerKitConfig(t *testing.T) {
	data, err := os.ReadFile("../../kits/reference-worker/config.yml")
	if err != nil {
		t.Fatalf("read reference kit config: %v", err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Image.Env["AT_WORK_AGENT_COMMAND"] != "run-agent.sh" {
		t.Errorf("AT_WORK_AGENT_COMMAND = %q; want run-agent.sh", cfg.Image.Env["AT_WORK_AGENT_COMMAND"])
	}
	if s, ok := cfg.Secrets["AT_WORK_GIT_TOKEN"]; !ok || len(s.Command) == 0 {
		t.Errorf("expected an AT_WORK_GIT_TOKEN secret with a resolver command; secrets=%v", cfg.Secrets)
	}
	if strings.TrimSpace(cfg.Workers["implement"].Prompt) == "" {
		t.Errorf("expected a non-empty workers[implement].prompt; workers=%v", cfg.Workers)
	}
}
