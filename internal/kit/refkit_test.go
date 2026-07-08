package kit

import (
	"os"
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
	if len(cfg.Dispatch.Command) != 1 || cfg.Dispatch.Command[0] != "run-worker.sh" {
		t.Errorf("dispatch.command = %v; want [run-worker.sh]", cfg.Dispatch.Command)
	}
	if cfg.Image.Env["AT_WORK_AGENT_COMMAND"] != "run-agent.sh" {
		t.Errorf("AT_WORK_AGENT_COMMAND = %q; want run-agent.sh", cfg.Image.Env["AT_WORK_AGENT_COMMAND"])
	}
	var found bool
	for _, s := range cfg.Secrets {
		if s.Name == "AT_WORK_GIT_TOKEN" && len(s.Command) > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an AT_WORK_GIT_TOKEN secret with a resolver command; secrets=%v", cfg.Secrets)
	}
}
