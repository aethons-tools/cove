package kit

import "testing"

func TestProtectedEnvKey(t *testing.T) {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "NO_PROXY", "CLAUDE_CONFIG_DIR", "GOOGLE_APPLICATION_CREDENTIALS", "PATH"} {
		if !ProtectedEnvKey(k) {
			t.Errorf("ProtectedEnvKey(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"ANTHROPIC_VERTEX_PROJECT_ID", "CLOUD_ML_REGION", "", "path"} {
		if ProtectedEnvKey(k) {
			t.Errorf("ProtectedEnvKey(%q) = true, want false", k)
		}
	}
}
