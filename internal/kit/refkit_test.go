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
	spec, ok := cfg.GitTokenSpec()
	if !ok || len(spec.Command) == 0 {
		t.Errorf("expected source-control AT_TASK_GIT_TOKEN with a resolver command; source-control=%+v", cfg.SourceControl)
	}
	if strings.TrimSpace(cfg.Workers["implement"].Prompt) == "" {
		t.Errorf("expected a non-empty workers[implement].prompt; workers=%v", cfg.Workers)
	}
	if cfg.SourceControl == nil || cfg.SourceControl.GitHub == nil || strings.TrimSpace(cfg.SourceControl.GitHub.Project) == "" {
		t.Errorf("expected a non-empty source-control.github.project; source-control=%+v", cfg.SourceControl)
	}
}
